package sshtest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Remote is one fake host: a private sshd (inetd mode) with its own keys and a
// remote home that holds a fake ~/.claude.
type Remote struct {
	Label string
	lab   *Lab

	Dir        string // scratch dir of this host (keys, sshd_config, remote home)
	Home       string // remote $HOME
	ClaudeDir  string // remote CLAUDE_CONFIG_DIR (= Home/.claude)
	StateHome  string // remote XDG_STATE_HOME
	ConfigHome string // remote XDG_CONFIG_HOME
	RuntimeDir string // remote XDG_RUNTIME_DIR, mode 0700
	Socket     string // remote DEN_SOCKET (the local-to-remote Den socket)
	// RemoteSock is where den-remote.sock, for answering held requests, would
	// live.
	RemoteSock string
	// KnownHosts is the client's known_hosts file for this host.
	KnownHosts string
	// DenPath is a copy of the test-built den at the conventional install
	// location on the remote, $HOME/.local/bin/den.
	DenPath string

	hostKey  string
	sshdConf string
	sshdLog  string
	flagDown string
	flagCap  string
	capOut   string
	capIn    string
	seed     *seedDir
	keygen   string
}

func newRemote(l *Lab, name, keygen, clientKey, sftp string) *Remote {
	t := l.t
	if strings.ContainsAny(name, " \t/") || strings.HasPrefix(name, "-") || name == "" {
		t.Fatalf("sshtest: bad host label %q", name)
	}
	r := &Remote{Label: name, lab: l, keygen: keygen}
	r.Dir = filepath.Join(l.Dir, "h-"+name)
	r.Home = filepath.Join(r.Dir, "home")
	r.ClaudeDir = filepath.Join(r.Home, ".claude")
	r.StateHome = filepath.Join(r.Home, ".local", "state")
	r.ConfigHome = filepath.Join(r.Home, ".config")
	r.RuntimeDir = filepath.Join(r.Home, "run")
	r.Socket = filepath.Join(r.RuntimeDir, "den.sock")
	r.RemoteSock = filepath.Join(r.RuntimeDir, "den-remote.sock")
	r.KnownHosts = filepath.Join(r.Dir, "known_hosts")
	r.DenPath = filepath.Join(r.Home, ".local", "bin", "den")
	r.seed = &seedDir{lab: l, claudeDir: r.ClaudeDir, runtimeDir: r.RuntimeDir}
	r.hostKey = filepath.Join(r.Dir, "hk")
	r.sshdConf = filepath.Join(r.Dir, "sshd_config")
	r.sshdLog = filepath.Join(r.Dir, "sshd.log")
	r.flagDown = filepath.Join(r.Dir, "down")
	r.flagCap = filepath.Join(r.Dir, "capture")
	r.capOut = filepath.Join(r.Dir, "pipe.out")
	r.capIn = filepath.Join(r.Dir, "pipe.in")

	for _, d := range []string{r.Dir, r.Home, filepath.Join(r.ClaudeDir, "sessions"), r.StateHome, r.ConfigHome, filepath.Dir(r.DenPath)} {
		must(t, os.MkdirAll(d, 0o755))
	}
	must(t, os.MkdirAll(r.RuntimeDir, 0o700))
	must(t, os.Chmod(r.RuntimeDir, 0o700))
	must(t, linkOrCopy(l.Den, r.DenPath))

	must(t, run(keygen, "-q", "-t", "ed25519", "-N", "", "-C", "den-test-host-"+name, "-f", r.hostKey))
	must(t, os.WriteFile(filepath.Join(r.Dir, "authorized_keys"), mustRead(t, clientKey+".pub"), 0o600))
	r.writeKnownHosts()

	sub := ""
	if sftp != "" {
		sub = "Subsystem sftp " + sftp + "\n"
	}
	cfg := fmt.Sprintf(`HostKey %s
AuthorizedKeysFile %s
StrictModes no
UsePAM no
PidFile none
PubkeyAuthentication yes
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin no
PermitUserEnvironment no
AllowTcpForwarding no
AllowStreamLocalForwarding no
X11Forwarding no
PrintMotd no
UseDNS no
LogLevel INFO
ForceCommand %s
%s`, r.hostKey, filepath.Join(r.Dir, "authorized_keys"), r.writeForceCommand(), sub)
	must(t, os.WriteFile(r.sshdConf, []byte(cfg), 0o600))
	return r
}

// writeForceCommand writes the sshd ForceCommand wrapper. sshd sets HOME from
// the passwd entry and starts in that directory, so every remote command would
// otherwise touch the real home; the wrapper replaces the environment and
// changes into the scratch home before running $SSH_ORIGINAL_COMMAND.
//
// Capture (opt-in, Remote.CapturePipe) tees the command's stdin and stdout to
// files: those are exactly the bytes that cross the ssh pipe.
func (r *Remote) writeForceCommand() string {
	p := filepath.Join(r.Dir, "force.sh")
	body := fmt.Sprintf(`#!/bin/sh
HOME=%[1]s
CLAUDE_CONFIG_DIR=%[2]s
XDG_STATE_HOME=%[3]s
XDG_CONFIG_HOME=%[4]s
XDG_RUNTIME_DIR=%[5]s
DEN_SOCKET=%[6]s
PATH=%[7]s:/usr/local/bin:/usr/bin:/bin
DEN_SSHTEST=%[8]s
export HOME CLAUDE_CONFIG_DIR XDG_STATE_HOME XDG_CONFIG_HOME XDG_RUNTIME_DIR DEN_SOCKET PATH DEN_SSHTEST
unset XDG_DATA_HOME XDG_CACHE_HOME DEN_CONFIG TMUX HYPRLAND_INSTANCE_SIGNATURE
[ -f %[12]s ] && . %[12]s
[ -e %[13]s ] && PATH=/usr/local/bin:/usr/bin:/bin
cd "$HOME" || exit 1
unset OLDPWD
if [ -z "$SSH_ORIGINAL_COMMAND" ]; then exec sh; fi
if [ -e %[9]s ]; then
  rc=%[5]s/.rc.$$
  tee -a %[10]s | { sh -c "$SSH_ORIGINAL_COMMAND"; echo $? > "$rc"; } | tee -a %[11]s
  read -r st < "$rc"; rm -f "$rc"; exit "$st"
fi
exec sh -c "$SSH_ORIGINAL_COMMAND"
`, r.Home, r.ClaudeDir, r.StateHome, r.ConfigHome, r.RuntimeDir, r.Socket, r.lab.Bin, r.lab.Dir, r.flagCap, r.capIn, r.capOut, filepath.Join(r.Dir, "extra.env"), filepath.Join(r.Dir, "noden"))
	must(r.lab.t, os.WriteFile(p, []byte(body), 0o755))
	return p
}

// proxyCommand writes the script ssh runs as its ProxyCommand: the private
// sshd in inetd mode, one process on the pipe, no listener.
func (r *Remote) proxyCommand() string {
	p := filepath.Join(r.Dir, "proxy.sh")
	body := fmt.Sprintf(`#!/bin/sh
if [ -e %s ]; then echo "sshtest: host %s is down" >&2; exit 1; fi
DEN_SSHTEST=%s; export DEN_SSHTEST
%s >> %s/sshd-%s.$$
exec %s -i -f %s -E %s
`, shq(r.flagDown), r.Label, shq(r.lab.Dir), recordPID, r.lab.pids, r.Label, shq(r.lab.SSHD), shq(r.sshdConf), shq(r.sshdLog))
	must(r.lab.t, os.WriteFile(p, []byte(body), 0o755))
	return p
}

// clientStanza is this host's block of the scratch ssh client config. The
// host name doubles as the known_hosts name: with a ProxyCommand ssh checks
// the host key against the name it was asked to connect to.
func (r *Remote) clientStanza() string {
	u, _ := user.Current()
	name := "user"
	if u != nil {
		name = u.Username
	}
	return fmt.Sprintf(`Host %[1]s
  HostName %[1]s
  User %[2]s
  ProxyCommand %[3]s
  IdentityFile %[4]s
  IdentitiesOnly yes
  IdentityAgent none
  PreferredAuthentications publickey
  UserKnownHostsFile %[5]s
  GlobalKnownHostsFile /dev/null

`, r.Label, name, r.proxyCommand(), filepath.Join(r.lab.Dir, "ck"), r.KnownHosts)
}

// writeKnownHosts seeds known_hosts with the current host key under the label.
func (r *Remote) writeKnownHosts() {
	pub := strings.Fields(string(mustRead(r.lab.t, r.hostKey+".pub")))
	must(r.lab.t, os.WriteFile(r.KnownHosts, []byte(r.Label+" "+pub[0]+" "+pub[1]+"\n"), 0o600))
}

// envPaths are the remote paths the isolation proof checks.
func (r *Remote) envPaths() map[string]string {
	return map[string]string{"HOME": r.Home, "CLAUDE_CONFIG_DIR": r.ClaudeDir, "XDG_STATE_HOME": r.StateHome,
		"XDG_CONFIG_HOME": r.ConfigHome, "XDG_RUNTIME_DIR": r.RuntimeDir, "DEN_SOCKET": r.Socket, "known_hosts": r.KnownHosts}
}

// Target is what to hand Den as the ssh target for this host.
func (r *Remote) Target() string { return r.Label }

// --- link control ---------------------------------------------------------

// SwapHostKey replaces the host's key with a fresh one, leaving known_hosts as
// it was: the next connection sees a changed host key. RestoreHostKey undoes it.
func (r *Remote) SwapHostKey() {
	t := r.lab.t
	t.Helper()
	for _, ext := range []string{"", ".pub"} {
		if _, err := os.Stat(r.hostKey + ".orig" + ext); err != nil {
			must(t, os.Rename(r.hostKey+ext, r.hostKey+".orig"+ext))
		} else {
			os.Remove(r.hostKey + ext)
		}
	}
	must(t, run(r.keygen, "-q", "-t", "ed25519", "-N", "", "-C", "den-test-other", "-f", r.hostKey))
}

// RestoreHostKey puts the original host key back after SwapHostKey.
func (r *Remote) RestoreHostKey() {
	t := r.lab.t
	t.Helper()
	for _, ext := range []string{"", ".pub"} {
		if _, err := os.Stat(r.hostKey + ".orig" + ext); err == nil {
			must(t, os.Rename(r.hostKey+".orig"+ext, r.hostKey+ext))
		}
	}
}

// ForgetHostKey empties known_hosts: the host is now unknown to the client.
func (r *Remote) ForgetHostKey() {
	must(r.lab.t, os.WriteFile(r.KnownHosts, nil, 0o600))
}

// TrustHostKey seeds known_hosts with the host's current key again.
func (r *Remote) TrustHostKey() { r.writeKnownHosts() }

// Down makes every new connection to the host fail at once (the proxy exits),
// as an unreachable machine would. Existing connections are not touched; pair
// it with KillSSH. Up reverses it.
func (r *Remote) Down() { must(r.lab.t, os.WriteFile(r.flagDown, nil, 0o600)) }

// Up lets connections through again after Down.
func (r *Remote) Up() { os.Remove(r.flagDown) }

// KillSSHD SIGKILLs this host's private sshd processes and their children, a
// server-side drop: the client sees EOF. Children (including a running den
// remote serve) die without a stdin EOF, unlike a clean ssh exit.
func (r *Remote) KillSSHD() int {
	return killPidFiles(filepath.Join(r.lab.pids, "sshd-"+r.Label+".*"), true)
}

// KillSSH SIGKILLs every ssh/scp client started through the wrappers (and a
// ControlMaster among them), a client-side drop: sshd sees the pipe close and
// the remote command sees stdin EOF.
func (l *Lab) KillSSH() int {
	// Only the clients die; a client's ProxyCommand sshd survives to see the
	// pipe close, as it would when the network drops under a live ssh.
	return killPidFiles(filepath.Join(l.pids, "ssh.*"), false) + killPidFiles(filepath.Join(l.pids, "scp.*"), false)
}

// CapturePipe turns on (or off) capturing of the bytes of every remote command
// from now on: stdin (what Den sent) and stdout (what crossed back). They are
// the plaintext of the ssh pipe, for the SECRET grep test.
func (r *Remote) CapturePipe(on bool) {
	if on {
		must(r.lab.t, os.WriteFile(r.flagCap, nil, 0o600))
		return
	}
	os.Remove(r.flagCap)
}

// PipeOut returns the captured remote stdout so far.
func (r *Remote) PipeOut() []byte { b, _ := os.ReadFile(r.capOut); return b }

// PipeIn returns the captured remote stdin so far.
func (r *Remote) PipeIn() []byte { b, _ := os.ReadFile(r.capIn); return b }

// SSHDLog is the private sshd's own log, for diagnosing a failed connection.
func (r *Remote) SSHDLog() string { b, _ := os.ReadFile(r.sshdLog); return string(b) }

// --- remote files ---------------------------------------------------------

// WriteFile writes a file under the remote home (rel is relative to it).
func (r *Remote) WriteFile(rel, content string) string {
	t := r.lab.t
	p := filepath.Join(r.Home, rel)
	if !within(p, r.Home) {
		t.Fatalf("sshtest: %q escapes the remote home", rel)
	}
	must(t, os.MkdirAll(filepath.Dir(p), 0o755))
	must(t, os.WriteFile(p, []byte(content), 0o644))
	return p
}

// EventsPath is the remote events.jsonl.
func (r *Remote) EventsPath() string { return filepath.Join(r.StateHome, "den", "events.jsonl") }

// Events reads the remote events.jsonl as generic maps.
func (r *Remote) Events() []map[string]any {
	f, err := os.Open(r.EventsPath())
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

// Hook runs `den hook` on the remote host with a hook JSON on stdin, as Claude
// Code would. fields are merged over {session_id, hook_event_name}; for a
// PermissionRequest, pass tool_name and tool_input. The hook always exits 0.
// It returns once the hook does; with nothing listening on the remote socket
// that is immediately.
func (r *Remote) Hook(event string, fields map[string]any) Result {
	r.lab.t.Helper()
	m := map[string]any{"session_id": SessionA, "hook_event_name": event, "cwd": "/home/user/proj"}
	for k, v := range fields {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return r.RunStdin("den hook", string(b))
}

// WaitFor polls cond until it holds or the timeout passes.
func WaitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func mustRead(t interface{ Fatalf(string, ...any) }, p string) []byte {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("sshtest: %v", err)
	}
	return b
}

// pidsOf lists the live recorded pids of a kind ("sshd") for this host.
func (r *Remote) pidsOf(kind string) []int {
	files, _ := filepath.Glob(filepath.Join(r.lab.pids, kind+"-"+r.Label+".*"))
	var out []int
	for _, f := range files {
		out = append(out, recorded(f)...)
	}
	return out
}

// SetEnv exports k=v in the environment of every remote command from now on
// (for example DEN_TEST_REMOTE_NOW_OFFSET=10m to skew a test build's clock).
func (r *Remote) SetEnv(k, v string) {
	f, err := os.OpenFile(filepath.Join(r.Dir, "extra.env"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	must(r.lab.t, err)
	defer f.Close()
	fmt.Fprintf(f, "%s=%s; export %s\n", k, shq(v), k)
}

// WithoutDen makes den vanish from the host: the installed copy is deleted and
// the test-built directory leaves the remote PATH, so `den` is not found.
// Install tests start from here.
func (r *Remote) WithoutDen() {
	os.Remove(r.DenPath)
	must(r.lab.t, os.WriteFile(filepath.Join(r.Dir, "noden"), nil, 0o600))
}

// Env is the remote environment as a full env list, for running a command on
// the "remote" directly, without ssh.
func (r *Remote) Env() []string {
	return []string{"PATH=" + r.lab.Bin + ":/usr/local/bin:/usr/bin:/bin", "HOME=" + r.Home,
		"CLAUDE_CONFIG_DIR=" + r.ClaudeDir, "XDG_STATE_HOME=" + r.StateHome, "XDG_CONFIG_HOME=" + r.ConfigHome,
		"XDG_RUNTIME_DIR=" + r.RuntimeDir, "DEN_SOCKET=" + r.Socket, "DEN_SSHTEST=" + r.lab.Dir}
}

// HookLocal is Hook run directly on this machine in the remote's environment,
// not through ssh: it works while the host is Down, to make events happen
// "on the host" while the link is cut.
func (r *Remote) HookLocal(event string, fields map[string]any) {
	r.lab.t.Helper()
	m := map[string]any{"session_id": SessionA, "hook_event_name": event, "cwd": "/home/user/proj"}
	for k, v := range fields {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	c := exec.Command(r.lab.Den, "hook")
	c.Dir, c.Env, c.Stdin = r.Home, r.Env(), strings.NewReader(string(b))
	if out, err := c.CombinedOutput(); err != nil {
		r.lab.t.Fatalf("sshtest: den hook: %v: %s", err, out)
	}
}

// Connections is how many times a private sshd was started for this host,
// i.e. how many connection attempts reached the proxy (a refused host key
// still counts: the key check happens after the connection is made).
func (r *Remote) Connections() int {
	files, _ := filepath.Glob(filepath.Join(r.lab.pids, "sshd-"+r.Label+".*"))
	return len(files)
}

// Sessions lists the sessions seeded on this host so far.
func (r *Remote) Sessions() []*Session { return r.seed.sessions }

// Freeze stops this host's private sshd and everything under it (SIGSTOP):
// the link stays open but carries no bytes, as when a network path goes
// silent without a reset. The client only finds out through its keepalives.
// Thaw continues them. Cleanup kills frozen processes too.
func (r *Remote) Freeze() int { return r.signalSSHD(syscall.SIGSTOP) }

// Thaw continues what Freeze stopped.
func (r *Remote) Thaw() int { return r.signalSSHD(syscall.SIGCONT) }

// HalfOpen stops this host's private sshd processes (SIGSTOP) but leaves
// the commands they run, such as den remote serve, running: after KillSSH
// the remote end of the link is half-open. serve hears no EOF and keeps
// its held hooks, as on a network path that died without a reset. Thaw
// continues the sshd processes.
func (r *Remote) HalfOpen() int {
	n := 0
	files, _ := filepath.Glob(filepath.Join(r.lab.pids, "sshd-"+r.Label+".*"))
	for _, f := range files {
		for _, pid := range recorded(f) {
			signalTreeIf(pid, syscall.SIGSTOP, func(p int) bool {
				comm, _ := os.ReadFile(fmt.Sprintf("/proc/%d/comm", p))
				return strings.TrimSpace(string(comm)) == "den"
			})
			n++
		}
	}
	return n
}

func (r *Remote) signalSSHD(sig syscall.Signal) int {
	n := 0
	files, _ := filepath.Glob(filepath.Join(r.lab.pids, "sshd-"+r.Label+".*"))
	for _, f := range files {
		for _, pid := range recorded(f) {
			signalTree(pid, sig)
			n++
		}
	}
	return n
}
