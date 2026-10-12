// Package sshtest gives a test a private "remote host" with no network and no
// listening port.
//
// The remote is a throwaway sshd run in inetd mode (sshd -i) as the ssh
// client's ProxyCommand: one process on a pipe, scratch host and client keys,
// a scratch authorized_keys and known_hosts, and a ForceCommand wrapper that
// gives every remote command an isolated HOME, CLAUDE_CONFIG_DIR, XDG_* and
// DEN_SOCKET. Nothing is read from or written to the real home, ssh config,
// keys or ~/.claude.
//
// Den is pointed at it through DEN_SSH and DEN_SCP: wrapper scripts that exec
// the real ssh and scp with -F <scratch config>, so Den's own options
// (BatchMode, StrictHostKeyChecking=yes, ControlMaster, ControlPath) still
// apply on top.
//
// This is test support. Only _test.go files import it, and cmd/den never does,
// so it stays out of the production binary.
package sshtest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Fixed names for fake data, neutral on purpose.
const (
	SessionA = "11111111-2222-4333-8444-555555555555"
	SessionB = "11111111-2222-4333-8444-666666666666"
)

// Lab is one test's scratch world: a set of remote hosts, the ssh/scp wrappers
// that reach them, and an isolated local side.
type Lab struct {
	t   *testing.T
	Dir string // short scratch directory under /tmp/den-test-<uid>/

	SSHBin string // real ssh
	SCPBin string // real scp
	SSHD   string // real sshd

	// SSH and SCP are the wrapper scripts to pass to Den as DEN_SSH and DEN_SCP.
	SSH string
	SCP string
	// Config is the scratch ssh client config the wrappers pass with -F.
	Config string
	// ControlDir is a scratch directory for ControlPath sockets (mode 0700).
	ControlDir string
	// Bin is the directory holding the test-built den binary; it comes first
	// on the remote PATH.
	Bin string
	// Den is the test-built den binary.
	Den string

	Local   *Local
	Remotes map[string]*Remote
	order   []string

	realHome string
	pids     string // pid files of processes the wrappers started
}

// New builds a lab with one remote host per label (default "devbox"). It skips
// the test when ssh, scp, ssh-keygen or sshd are missing, and fails it when
// they are present but a private sshd cannot serve a connection.
func New(t *testing.T, labels ...string) *Lab {
	t.Helper()
	if len(labels) == 0 {
		labels = []string{"devbox"}
	}
	l := &Lab{t: t, Remotes: map[string]*Remote{}}
	l.realHome, _ = os.UserHomeDir()
	for _, p := range []struct {
		dst  *string
		name string
	}{{&l.SSHBin, "ssh"}, {&l.SCPBin, "scp"}, {&l.SSHD, "sshd"}} {
		*p.dst = findBin(p.name)
		if *p.dst == "" {
			t.Skipf("sshtest: %s not found (CI: apt-get install openssh-client openssh-server)", p.name)
		}
	}
	keygen := findBin("ssh-keygen")
	if keygen == "" {
		t.Skip("sshtest: ssh-keygen not found")
	}
	sftp := findSFTPServer()

	base := filepath.Join("/tmp", fmt.Sprintf("den-test-%d", os.Getuid()))
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatalf("sshtest: %v", err)
	}
	dir, err := os.MkdirTemp(base, "st")
	if err != nil {
		t.Fatalf("sshtest: %v", err)
	}
	l.Dir = dir
	t.Cleanup(func() {
		l.killAll()
		for _, r := range l.Remotes {
			r.stopSessions()
		}
		os.RemoveAll(dir)
	})

	l.Den = BuildDen(t)
	l.Bin = filepath.Join(dir, "bin")
	l.ControlDir = filepath.Join(dir, "cm")
	l.pids = filepath.Join(dir, "pids")
	for _, d := range []string{l.Bin, l.ControlDir, l.pids} {
		must(t, os.MkdirAll(d, 0o700))
	}
	must(t, linkOrCopy(l.Den, filepath.Join(l.Bin, "den")))

	// The client key is shared by every remote; each remote has its own host
	// key, authorized_keys, sshd_config and remote home.
	clientKey := filepath.Join(dir, "ck")
	must(t, run(keygen, "-q", "-t", "ed25519", "-N", "", "-C", "den-test-client", "-f", clientKey))

	var cfg strings.Builder
	for _, name := range labels {
		r := newRemote(l, name, keygen, clientKey, sftp)
		l.Remotes[name] = r
		l.order = append(l.order, name)
		cfg.WriteString(r.clientStanza())
	}
	l.Config = filepath.Join(dir, "ssh_config")
	must(t, os.WriteFile(l.Config, []byte(cfg.String()), 0o600))

	l.SSH = l.wrapper("ssh", l.SSHBin)
	l.SCP = l.wrapper("scp", l.SCPBin)
	l.Local = newLocal(l)

	l.logEnvironment()
	l.AssertIsolated()
	start := time.Now()
	res := l.Remote(labels[0]).Run("true")
	if res.Code != 0 {
		t.Fatalf("sshtest: private sshd did not serve a connection (exit %d): %s\n"+
			"  sshd must be runnable without root (inetd mode) and /usr/sbin/sshd present", res.Code, res.Stderr)
	}
	t.Logf("sshtest: first connect through private sshd: %v", time.Since(start).Round(time.Millisecond))
	return l
}

// Remote returns a host by label ("" is the first one).
func (l *Lab) Remote(label string) *Remote {
	if label == "" {
		label = l.order[0]
	}
	r := l.Remotes[label]
	if r == nil {
		l.t.Fatalf("sshtest: no remote %q", label)
	}
	return r
}

// Default is the first remote.
func (l *Lab) Default() *Remote { return l.Remote("") }

// wrapper writes a script that records its pid (so a test can cut the link)
// and execs the real binary with -F <scratch config> and the caller's args.
func (l *Lab) wrapper(name, bin string) string {
	p := filepath.Join(l.Dir, name)
	body := fmt.Sprintf("#!/bin/sh\nDEN_SSHTEST=%s; export DEN_SSHTEST\n%s >> %s/%s.$$\nexec %s -F %s \"$@\"\n", shq(l.Dir), recordPID, l.pids, name, shq(bin), shq(l.Config))
	must(l.t, os.WriteFile(p, []byte(body), 0o755))
	return p
}

// Cmd builds an ssh command through the wrapper. Args are passed as they are;
// use Remote.Cmd for the common "run this on the host" case.
func (l *Lab) Cmd(ctx context.Context, args ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, l.SSH, args...)
	c.Env = l.Local.Env()
	return c
}

// StrictOptions are the options Den adds to every background connection
// (design §3.2), without ConnectTimeout so a test can choose its own.
func StrictOptions() []string {
	return []string{"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
		"-o", "ForwardAgent=no", "-o", "ClearAllForwardings=yes"}
}

// killAll ends everything the wrappers started.
func (l *Lab) killAll() {
	for _, kind := range []string{"ssh", "scp"} {
		killPidFiles(filepath.Join(l.pids, kind+".*"), true)
	}
	for _, r := range l.Remotes {
		killPidFiles(filepath.Join(l.pids, "sshd-"+r.Label+".*"), true)
	}
}

func (l *Lab) logEnvironment() {
	t := l.t
	t.Logf("sshtest: scratch dir %s", l.Dir)
	t.Logf("sshtest: ssh=%s scp=%s sshd=%s", l.SSHBin, l.SCPBin, l.SSHD)
	t.Logf("sshtest: DEN_SSH=%s DEN_SCP=%s config=%s", l.SSH, l.SCP, l.Config)
	for _, k := range l.order {
		r := l.Remotes[k]
		t.Logf("sshtest: remote %s HOME=%s CLAUDE_CONFIG_DIR=%s XDG_STATE_HOME=%s XDG_CONFIG_HOME=%s XDG_RUNTIME_DIR=%s DEN_SOCKET=%s",
			r.Label, r.Home, r.ClaudeDir, r.StateHome, r.ConfigHome, r.RuntimeDir, r.Socket)
	}
	for _, kv := range l.Local.Env() {
		if k, _, _ := strings.Cut(kv, "="); k != "PATH" {
			t.Logf("sshtest: local %s", kv)
		}
	}
	t.Logf("sshtest: real home (must stay untouched): %s", l.realHome)
}

// AssertIsolated fails the test when any path the lab uses is outside the
// scratch directory, or is the real home or under it. It is called before
// anything starts; a test may call it again.
func (l *Lab) AssertIsolated() {
	t := l.t
	t.Helper()
	paths := map[string]string{"lab": l.Dir, "config": l.Config, "controlDir": l.ControlDir, "bin": l.Bin}
	for _, r := range l.Remotes {
		for k, v := range r.envPaths() {
			paths[r.Label+"."+k] = v
		}
	}
	for k, v := range l.Local.paths() {
		paths["local."+k] = v
	}
	for k, v := range paths {
		l.checkPath(k, v)
	}
}

func (l *Lab) checkPath(name, p string) {
	t := l.t
	t.Helper()
	if p == "" || !filepath.IsAbs(p) {
		t.Fatalf("sshtest: isolation: %s = %q is not an absolute scratch path", name, p)
	}
	if !within(p, l.Dir) {
		t.Fatalf("sshtest: isolation: %s = %s is outside the scratch dir %s", name, p, l.Dir)
	}
	if l.realHome != "" && within(p, l.realHome) && !within(l.Dir, "/tmp") {
		t.Fatalf("sshtest: isolation: %s = %s is under the real home", name, p)
	}
}

func within(p, dir string) bool {
	p, dir = filepath.Clean(p), filepath.Clean(dir)
	return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
}

// Result is the outcome of a command run through ssh.
type Result struct {
	Stdout, Stderr string
	Code           int
}

// Run runs a command on the host through ssh with BatchMode and strict host
// key checking, the way Den does, and a 20 s limit.
func (r *Remote) Run(command string) Result { return r.RunStdin(command, "") }

// RunStdin is Run with stdin.
func (r *Remote) RunStdin(command, stdin string) Result {
	r.lab.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c := r.Cmd(ctx, command)
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	if stdin != "" {
		c.Stdin = strings.NewReader(stdin)
	}
	err := c.Run()
	return Result{Stdout: out.String(), Stderr: errb.String(), Code: exitCode(err)}
}

// Cmd builds `ssh <strict options> -- <label> <command>`; the caller starts it.
func (r *Remote) Cmd(ctx context.Context, command string) *exec.Cmd {
	args := append(StrictOptions(), "-o", "ConnectTimeout=10", "--", r.Label, command)
	return r.lab.Cmd(ctx, args...)
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

func findBin(name string) string {
	for _, d := range []string{"/usr/bin", "/usr/sbin", "/usr/local/bin", "/usr/local/sbin", "/bin", "/sbin"} {
		p := filepath.Join(d, name)
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
			return p
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

// findSFTPServer locates sftp-server for the Subsystem line, so scp (which
// speaks SFTP in OpenSSH 9+) works. "" when absent: scp -O is then needed.
func findSFTPServer() string {
	for _, p := range []string{"/usr/lib/ssh/sftp-server", "/usr/lib/openssh/sftp-server",
		"/usr/libexec/openssh/sftp-server", "/usr/libexec/sftp-server", "/usr/lib/sftp-server"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, out)
	}
	return nil
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("sshtest: %v", err)
	}
}

// shq single-quotes s for sh.
func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func linkOrCopy(src, dst string) error {
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o755)
}

// recordPID is the sh snippet that appends "<pid> <starttime>" of the running
// script (which then execs, keeping both).
const recordPID = `echo "$$ $(sed 's/.*) //' /proc/$$/stat | cut -d' ' -f20)"`

// killPidFiles SIGKILLs the process tree of every process recorded in the
// files matching the glob (just the process itself unless tree), then removes
// the files. A recorded pid may have
// been reused after its process exited, so the start time recorded with it
// must still match.
func killPidFiles(glob string, tree bool) int {
	n := 0
	files, _ := filepath.Glob(glob)
	for _, f := range files {
		for _, pid := range recorded(f) {
			if tree {
				killTree(pid)
			} else {
				syscall.Kill(pid, syscall.SIGKILL)
			}
			n++
		}
		os.Remove(f)
	}
	return n
}

// recorded returns the pids in a pid file whose process is still the one
// that was recorded.
func recorded(file string) []int {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	f := strings.Fields(string(b))
	var out []int
	for i := 0; i+1 < len(f); i += 2 {
		pid, err := strconv.Atoi(f[i])
		if err == nil && pid > 1 && startTime(pid) == f[i+1] {
			out = append(out, pid)
		}
	}
	return out
}

// startTime is field 22 of /proc/<pid>/stat, "" when the process is gone.
func startTime(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ""
	}
	s := string(b)
	f := strings.Fields(s[strings.LastIndexByte(s, ')')+1:])
	if len(f) < 20 {
		return ""
	}
	return f[19]
}

// killTree SIGKILLs pid and its descendants (found through /proc).
func killTree(pid int) {
	children := map[int][]int{}
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		p, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		// pid (comm) state ppid ...; comm may hold spaces and parens.
		s := string(b)
		i := strings.LastIndexByte(s, ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(s[i+1:])
		if len(f) < 2 {
			continue
		}
		pp, _ := strconv.Atoi(f[1])
		children[pp] = append(children[pp], p)
	}
	var kill func(int)
	kill = func(p int) {
		for _, c := range children[p] {
			kill(c)
		}
		syscall.Kill(p, syscall.SIGKILL)
	}
	kill(pid)
}
