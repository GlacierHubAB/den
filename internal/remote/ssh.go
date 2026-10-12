package remote

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/GlacierHubAB/den/internal/sanitize"
)

// SSH runs the system ssh and scp with Den's fixed options. Den embeds no
// ssh implementation and passes no option that weakens host key checking;
// the human's ~/.ssh/config, agent, keys, ProxyJump and known_hosts apply.
//
// DEN_SSH and DEN_SCP replace the binaries (default "ssh" and "scp"). They
// exist for tests, which point them at wrappers that add a scratch -F
// config; Den itself never passes -F or any other config override.
type SSH struct {
	Bin        string
	SCP        string
	ControlDir string // where ControlMaster sockets go; "" runs without multiplexing
}

// DefaultSSH is the runner with the environment's binaries and the
// ControlMaster directory, when it can be made private.
func DefaultSSH() SSH {
	s := SSH{Bin: os.Getenv("DEN_SSH"), SCP: os.Getenv("DEN_SCP")}
	if s.Bin == "" {
		s.Bin = "ssh"
	}
	if s.SCP == "" {
		s.SCP = "scp"
	}
	if dir := ControlDir(); ensurePrivateDir(dir) == nil {
		s.ControlDir = dir
	}
	return s
}

// ControlDir is $XDG_RUNTIME_DIR/den/ssh, or a per-user directory under
// the temporary directory when there is no runtime directory. Paths in it
// are short: a Unix socket path is limited to about 108 bytes, and %C is 40.
func ControlDir() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "den", "ssh")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("den-%d", os.Getuid()), "ssh")
}

// ensurePrivateDir makes dir (mode 0700) and checks that it is a real
// directory owned by this user that nobody else can enter.
func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	os.Chmod(dir, 0o700)
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	switch {
	case !st.IsDir():
		return fmt.Errorf("%s is not a directory", dir)
	case st.Mode().Perm()&0o077 != 0:
		return fmt.Errorf("%s is open to other users", dir)
	case ok && int(sys.Uid) != os.Getuid():
		return fmt.Errorf("%s belongs to another user", dir)
	}
	return nil
}

// options are passed on every ssh and scp run (docs/REMOTE.md).
func (s SSH) options() []string {
	opts := []string{
		"-o", "BatchMode=yes", // never prompt: a background connection has nobody to ask
		"-o", "StrictHostKeyChecking=yes", // an unknown or changed host key stops it
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=15", // a dead link ends the session within 45 s
		"-o", "ServerAliveCountMax=3",
		"-o", "ForwardAgent=no", // a compromised host cannot use the local ssh-agent
		"-o", "ForwardX11=no",
		"-o", "ClearAllForwardings=yes",
		"-o", "PermitLocalCommand=no",
	}
	if s.ControlDir != "" {
		opts = append(opts, "-o", "ControlMaster=auto", "-o", "ControlPath="+filepath.Join(s.ControlDir, "%C"), "-o", "ControlPersist=no")
	} else {
		opts = append(opts, "-o", "ControlMaster=no", "-o", "ControlPath=none")
	}
	return opts
}

// Command is ssh running remote, a fixed command string, on target. The
// target is checked and goes after "--" as one argument, so it can never
// be read as an option.
func (s SSH) Command(ctx context.Context, target, remote string) (*exec.Cmd, error) {
	if err := CheckTarget(target); err != nil {
		return nil, err
	}
	args := append(s.options(), "-T", "--", target, remote)
	cmd := exec.CommandContext(ctx, s.Bin, args...)
	cmd.Env = os.Environ()
	return cmd, nil
}

// Hygiene removes a dead ControlMaster socket for target, so a master that
// was killed or hangs cannot stall the next connection. It asks
// ssh where the socket is (ssh -G), and when one is there, whether a master
// answers on it (ssh -O check) within five seconds.
func (s SSH) Hygiene(ctx context.Context, target string) {
	if s.ControlDir == "" || CheckTarget(target) != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, s.Bin, append(s.options(), "-G", "--", target)...).Output()
	if err != nil {
		return
	}
	path := ""
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "controlpath "); ok {
			path = strings.TrimSpace(v)
		}
	}
	if path == "" || filepath.Dir(path) != filepath.Clean(s.ControlDir) {
		return
	}
	if _, err := os.Lstat(path); err != nil {
		return
	}
	check := exec.CommandContext(ctx, s.Bin, append(s.options(), "-O", "check", "--", target)...)
	if check.Run() != nil {
		os.Remove(path)
	}
}

// Failure is an ssh run that did not succeed.
type Failure struct {
	Code   int    // exit status; -1 when ssh did not run or was killed
	Stderr string // what ssh and the remote command wrote to stderr, trimmed
}

func (f *Failure) Error() string {
	if f.Stderr != "" {
		return fmt.Sprintf("ssh exited %d: %s", f.Code, f.Stderr)
	}
	return fmt.Sprintf("ssh exited %d", f.Code)
}

// failure turns a finished command's error and stderr into a *Failure.
func failure(err error, stderr string) error {
	if err == nil {
		return nil
	}
	f := &Failure{Code: -1, Stderr: lastLines(stderr, 4)}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		f.Code = ee.ExitCode()
	} else if f.Stderr == "" {
		f.Stderr = err.Error()
	}
	return f
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// Run runs remote on target and returns its stdout. stdin is empty.
func (s SSH) Run(ctx context.Context, target, remote string) ([]byte, error) {
	cmd, err := s.Command(ctx, target, remote)
	if err != nil {
		return nil, err
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, &stdout, &limitWriter{w: &stderr, n: 64 << 10}
	err = cmd.Run()
	return stdout.Bytes(), failure(err, stderr.String())
}

// Copy copies the local file to remotePath (relative to the remote home)
// on target with scp, with the same options as ssh.
func (s SSH) Copy(ctx context.Context, local, target, remotePath string) error {
	if err := CheckTarget(target); err != nil {
		return err
	}
	host := target
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		user, h, ok := strings.Cut(host, "@")
		if ok {
			host = user + "@[" + h + "]"
		} else {
			host = "[" + host + "]"
		}
	}
	args := append(s.options(), "-q", "--", local, host+":"+remotePath)
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, s.SCP, args...)
	cmd.Stderr = &limitWriter{w: &stderr, n: 64 << 10}
	return failure(cmd.Run(), stderr.String())
}

// limitWriter keeps the first n bytes and discards the rest.
type limitWriter struct {
	w io.Writer
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		k := min(len(p), l.n)
		l.w.Write(p[:k])
		l.n -= k
	}
	return len(p), nil
}

// Classify reads why a session or check ended. An authentication or host
// key failure needs the human (run ssh by hand once) and is not retried; a
// den that is missing or does not know `remote serve` is a version
// mismatch; anything else is offline and retried with backoff.
func Classify(err error) (State, string) {
	st, why := classify(err)
	// The reason can quote the host's stderr: printed by den remote add
	// and status as well as shown in the TUI, it is one clean line.
	return st, sanitize.Label(why, 200)
}

func classify(err error) (State, string) {
	var f *Failure
	if !errors.As(err, &f) {
		if err == nil {
			return Offline, "the session ended"
		}
		return Offline, err.Error()
	}
	e := f.Stderr
	low := strings.ToLower(e)
	switch {
	case f.Code == 255 && (strings.Contains(e, "Host key verification failed") ||
		strings.Contains(e, "REMOTE HOST IDENTIFICATION HAS CHANGED") ||
		strings.Contains(low, "host key is known") ||
		strings.Contains(e, "Host key for") && strings.Contains(e, "has changed")):
		return NeedsAttention, "the host key is unknown or changed"
	case f.Code == 255 && (strings.Contains(e, "Permission denied") || strings.Contains(low, "too many authentication failures")):
		return NeedsAttention, "ssh could not log in without a prompt"
	case f.Code == 127 || strings.Contains(e, "den: not found") || strings.Contains(e, "den: command not found") || strings.Contains(low, "no such file or directory") && strings.Contains(e, "den"):
		return VersionMismatch, "den is not installed on the host"
	case strings.Contains(e, `unknown command "remote"`) || strings.Contains(e, "unknown remote command") || strings.Contains(e, "unsupported remote proto"):
		return VersionMismatch, "den on the host is too old for remote serve"
	case f.Code == 255:
		if l := lastLines(e, 1); l != "" {
			return Offline, l
		}
		return Offline, "ssh failed"
	}
	if f.Code < 0 {
		// ssh was killed (a signal, or Den ending it): nothing it wrote
		// says more than that.
		return Offline, "the ssh session ended"
	}
	if l := lastLines(e, 1); l != "" {
		return Offline, fmt.Sprintf("exited %d: %s", f.Code, l)
	}
	return Offline, fmt.Sprintf("exited %d", f.Code)
}

// AttentionHint is what to tell the human when a host needs them.
func AttentionHint(target string) string {
	return fmt.Sprintf("run `ssh %s` once in a terminal to check and record the host key and unlock the key, then retry", target)
}

// shellQuote quotes s for a POSIX shell (and fish) in single quotes. Only
// used for values already checked to hold no quote.
func shellQuote(s string) string { return "'" + s + "'" }

// denCommand is how the remote command names den: the host's den_path,
// quoted; else den found through a login shell's PATH plus ~/.local/bin
// (a non-login ssh exec's PATH usually lacks it).
func denCommand(h Host, args string) string {
	switch {
	case strings.HasPrefix(h.DenPath, "~/"):
		return "~/" + shellQuote(strings.TrimPrefix(h.DenPath, "~/")) + " " + args
	case h.DenPath != "":
		return shellQuote(h.DenPath) + " " + args
	}
	return `sh -lc 'PATH="$PATH:$HOME/.local/bin"; export PATH; exec den ` + args + `'`
}

// ServeCommand is the fixed remote command of a host's session.
func ServeCommand(h Host) string {
	return denCommand(h, fmt.Sprintf("remote serve --proto %d", Proto))
}
