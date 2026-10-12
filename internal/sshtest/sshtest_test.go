package sshtest

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) { os.Exit(Main(m)) }

// listeners returns the inode set of TCP and Unix-agnostic LISTEN sockets.
func listeners(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		fh, err := os.Open(f)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(fh)
		sc.Scan()
		for sc.Scan() {
			fs := strings.Fields(sc.Text())
			if len(fs) > 9 && fs[3] == "0A" { // TCP_LISTEN
				out[f+" "+fs[1]+" "+fs[9]] = true
			}
		}
		fh.Close()
	}
	return out
}

func TestConnectStrict(t *testing.T) {
	before := listeners(t)
	l := New(t)
	r := l.Default()
	var total time.Duration
	const n = 5
	for i := 0; i < n; i++ {
		start := time.Now()
		res := r.Run("true")
		total += time.Since(start)
		if res.Code != 0 || res.Stderr != "" {
			t.Fatalf("ssh true: code %d stderr %q\nsshd log:\n%s", res.Code, res.Stderr, r.SSHDLog())
		}
	}
	t.Logf("connect+true through private sshd (BatchMode, StrictHostKeyChecking=yes): mean %v over %d cold connections", (total / n).Round(time.Millisecond), n)

	for k := range listeners(t) {
		if !before[k] {
			t.Errorf("new LISTEN socket appeared while the lab ran: %s", k)
		}
	}
}

func TestHostKeyMismatchRefused(t *testing.T) {
	l := New(t)
	r := l.Default()
	r.SwapHostKey()
	start := time.Now()
	res := r.Run("true")
	if res.Code != 255 || !strings.Contains(res.Stderr, "Host key verification failed") {
		t.Fatalf("changed key: code %d stderr %q", res.Code, res.Stderr)
	}
	if strings.Contains(res.Stderr, "(yes/no") {
		t.Errorf("ssh prompted: %q", res.Stderr)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("refusal took %v: a prompt may have waited", time.Since(start))
	}
	// known_hosts must not have been modified by the refusal.
	if b, _ := os.ReadFile(r.KnownHosts); strings.Count(string(b), "\n") != 1 {
		t.Errorf("known_hosts changed: %q", b)
	}

	r.RestoreHostKey()
	if res := r.Run("true"); res.Code != 0 {
		t.Fatalf("after RestoreHostKey: code %d stderr %q", res.Code, res.Stderr)
	}

	// An unknown host (empty known_hosts) is refused too, never accepted.
	r.ForgetHostKey()
	res = r.Run("true")
	if res.Code != 255 || !strings.Contains(res.Stderr, "Host key verification failed") {
		t.Fatalf("unknown host: code %d stderr %q", res.Code, res.Stderr)
	}
	if b, _ := os.ReadFile(r.KnownHosts); len(b) != 0 {
		t.Errorf("known_hosts written by a refused connection: %q", b)
	}
	r.TrustHostKey()
	if res := r.Run("true"); res.Code != 0 {
		t.Fatalf("after TrustHostKey: code %d stderr %q", res.Code, res.Stderr)
	}
}

func TestRemoteEnvIsIsolated(t *testing.T) {
	l := New(t)
	r := l.Default()
	real, _ := os.UserHomeDir()

	res := r.Run("env; echo PWD_IS=$(pwd)")
	if res.Code != 0 {
		t.Fatalf("env: %d %s", res.Code, res.Stderr)
	}
	t.Logf("remote environment:\n%s", res.Stdout)
	env := map[string]string{}
	for _, line := range strings.Split(res.Stdout, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			env[k] = v
		}
	}
	want := r.envPaths()
	for _, k := range []string{"HOME", "CLAUDE_CONFIG_DIR", "XDG_STATE_HOME", "XDG_CONFIG_HOME", "XDG_RUNTIME_DIR", "DEN_SOCKET"} {
		if env[k] != want[k] {
			t.Errorf("%s = %q, want %q", k, env[k], want[k])
		}
		if !within(env[k], l.Dir) || (real != "" && within(env[k], real) && !within(l.Dir, real)) {
			t.Errorf("%s = %q is outside the scratch dir or under the real home", k, env[k])
		}
	}
	if pwd := env["PWD_IS"]; pwd != r.Home {
		t.Errorf("remote cwd = %q, want the scratch home %q", pwd, r.Home)
	}
	for k, v := range env {
		if real != "" && strings.Contains(v, real) && k != "SSH_CONNECTION" {
			t.Errorf("remote %s mentions the real home: %q", k, v)
		}
	}
	if first := strings.SplitN(env["PATH"], ":", 2)[0]; first != l.Bin {
		t.Errorf("remote PATH starts with %q, want the test-built den dir %q", first, l.Bin)
	}
	st, err := os.Stat(r.RuntimeDir)
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Errorf("XDG_RUNTIME_DIR mode: %v %v", st, err)
	}
	if st, err := os.Stat(filepath.Join(r.ClaudeDir, "sessions")); err != nil || !st.IsDir() {
		t.Errorf("remote ~/.claude/sessions missing: %v", err)
	}

	// Files written on the remote land in the scratch home, including via scp.
	r.Run("echo x > touched.txt; mkdir -p .cache; echo y > .cache/y")
	for _, f := range []string{"touched.txt", ".cache/y"} {
		if _, err := os.Stat(filepath.Join(r.Home, f)); err != nil {
			t.Errorf("remote file %s not in the scratch home: %v", f, err)
		}
		if real != "" {
			if _, err := os.Stat(filepath.Join(real, f)); err == nil && f == "touched.txt" {
				t.Errorf("%s landed in the REAL home", f)
			}
		}
	}
}

func TestScpLandsInScratchHome(t *testing.T) {
	l := New(t)
	r := l.Default()
	src := filepath.Join(l.Dir, "payload")
	os.WriteFile(src, []byte("payload\n"), 0o644)
	name := "den-sshtest-" + filepath.Base(l.Dir) + ".txt"
	c := exec.Command(l.SCP, append(StrictOptions(), src, r.Label+":"+name)...)
	c.Env = l.Local.Env()
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("scp: %v: %s\nsshd log:\n%s", err, out, r.SSHDLog())
	}
	if b, err := os.ReadFile(filepath.Join(r.Home, name)); err != nil || string(b) != "payload\n" {
		t.Fatalf("scp target in scratch home: %q %v", b, err)
	}
	if real, _ := os.UserHomeDir(); real != "" {
		if _, err := os.Stat(filepath.Join(real, name)); err == nil {
			os.Remove(filepath.Join(real, name))
			t.Fatalf("scp wrote into the REAL home")
		}
	}
}

func TestRemoteRunsScratchBuiltDen(t *testing.T) {
	l := New(t)
	r := l.Default()
	res := r.Run("den version")
	if res.Code != 0 || strings.TrimSpace(res.Stdout) != Version() {
		t.Fatalf("den version = %q (code %d, %s), want %q", res.Stdout, res.Code, res.Stderr, Version())
	}
	if res := r.Run("command -v den"); strings.TrimSpace(res.Stdout) != filepath.Join(l.Bin, "den") {
		t.Errorf("den resolves to %q", res.Stdout)
	}
	if res := r.Run(r.DenPath + " version"); strings.TrimSpace(res.Stdout) != Version() {
		t.Errorf("installed copy: %q", res.Stdout)
	}
}

func TestControlMasterWorksWithConfig(t *testing.T) {
	l := New(t)
	r := l.Default()
	cp := filepath.Join(l.ControlDir, "%C")
	opts := append(StrictOptions(), "-o", "ControlMaster=auto", "-o", "ControlPath="+cp, "-o", "ControlPersist=10")
	var times []time.Duration
	for i := 0; i < 3; i++ {
		start := time.Now()
		c := l.Cmd(context.Background(), append(opts, "--", r.Label, "echo hi")...)
		out, err := c.CombinedOutput()
		times = append(times, time.Since(start))
		if err != nil || strings.TrimSpace(string(out)) != "hi" {
			t.Fatalf("mux run %d: %v %q", i, err, out)
		}
	}
	t.Logf("ControlMaster: first %v, then %v, %v", times[0].Round(time.Millisecond), times[1].Round(time.Millisecond), times[2].Round(time.Millisecond))
	// Stop the persistent master the test started.
	exit := l.Cmd(context.Background(), append(opts, "-O", "exit", "--", r.Label)...)
	exit.Run()
}

func TestSeedSessionAndHook(t *testing.T) {
	l := New(t)
	r := l.Default()
	s := r.SeedSession(SessionOpts{Role: "reviewer", Task: "check", Name: "alpha", Prompt: "p", Title: "t"})
	if s.ID != SessionA {
		t.Fatal(s.ID)
	}
	res := r.Run("ls .claude/sessions; ls .claude/plugins/store; ls .claude/projects/*")
	if res.Code != 0 || !strings.Contains(res.Stdout, ".json") || !strings.Contains(res.Stdout, SessionA+".jsonl") || !strings.Contains(res.Stdout, "agent-badge_test.json") {
		t.Fatalf("seeded files: %q %q", res.Stdout, res.Stderr)
	}
	// The dummy process is alive; after Stop it is not.
	if res := r.Run("kill -0 " + itoa(s.PID) + " && echo alive"); strings.TrimSpace(res.Stdout) != "alive" {
		t.Errorf("seeded pid not alive: %q", res.Stdout)
	}
	if res := r.Hook("PermissionRequest", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}}); res.Code != 0 || res.Stdout != "" {
		t.Errorf("hook with nothing listening: code %d stdout %q", res.Code, res.Stdout)
	}
	ev := r.Events()
	if len(ev) != 1 || ev[0]["session"] != SessionA {
		t.Errorf("remote events.jsonl after one PermissionRequest hook = %v (%s)", ev, r.EventsPath())
	}
	s.Stop()
	if res := r.Run("kill -0 " + itoa(s.PID) + " && echo alive"); strings.TrimSpace(res.Stdout) == "alive" {
		t.Errorf("stopped pid still alive")
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestKillAndDown(t *testing.T) {
	l := New(t)
	r := l.Default()

	// A long-running remote command, cut from the client side: it sees EOF.
	c := r.Cmd(context.Background(), "touch started.txt; cat >/dev/null; echo done > client_cut.txt")
	hold, _ := c.StdinPipe() // keeps remote stdin open until the client dies
	defer hold.Close()
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	if !WaitFor(5*time.Second, func() bool { _, err := os.Stat(filepath.Join(r.Home, "started.txt")); return err == nil }) {
		t.Fatal("remote command never started")
	}
	start := time.Now()
	if n := l.KillSSH(); n == 0 {
		t.Fatal("KillSSH found nothing")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ssh still running after KillSSH")
	}
	if !WaitFor(5*time.Second, func() bool { _, err := os.Stat(filepath.Join(r.Home, "client_cut.txt")); return err == nil }) {
		t.Error("remote command did not see stdin EOF after the client was killed")
	}
	t.Logf("client kill noticed remotely after %v", time.Since(start).Round(time.Millisecond))

	// Server-side drop.
	c = r.Cmd(context.Background(), "sleep 30")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { done <- c.Wait() }()
	time.Sleep(300 * time.Millisecond)
	r.KillSSHD()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ssh still running after KillSSHD")
	}

	// Down: new connections fail with 255 until Up.
	r.Down()
	if res := r.Run("true"); res.Code != 255 {
		t.Errorf("host down: code %d stderr %q", res.Code, res.Stderr)
	}
	r.Up()
	if res := r.Run("true"); res.Code != 0 {
		t.Errorf("host up again: code %d stderr %q", res.Code, res.Stderr)
	}
}

func TestPipeCapture(t *testing.T) {
	l := New(t)
	r := l.Default()
	r.CapturePipe(true)
	res := r.RunStdin("cat; echo out-marker; exit 7", "in-marker\n")
	if res.Code != 7 || !strings.Contains(res.Stdout, "out-marker") {
		t.Fatalf("captured run changed behaviour: %d %q", res.Code, res.Stdout)
	}
	if !strings.Contains(string(r.PipeOut()), "out-marker") || !strings.Contains(string(r.PipeIn()), "in-marker") {
		t.Errorf("capture: out %q in %q", r.PipeOut(), r.PipeIn())
	}
	r.CapturePipe(false)
}

func TestLocalIsolation(t *testing.T) {
	l := New(t)
	lo := l.Local
	c := exec.Command("sh", "-c", "env; echo PWD_IS=$HOME")
	c.Dir = lo.Home
	c.Env = lo.Env()
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("local env:\n%s", out)
	real, _ := os.UserHomeDir()
	for _, line := range strings.Split(string(out), "\n") {
		if real != "" && strings.Contains(line, real) && !strings.HasPrefix(line, "PWD=") { // PWD is the shell's own cwd
			t.Errorf("local env mentions the real home: %s", line)
		}
	}
	for k, v := range lo.paths() {
		if !within(v, l.Dir) {
			t.Errorf("%s = %s outside the lab", k, v)
		}
	}
	// The den binary run with the local env reads only local state.
	d := exec.Command(l.Den, "version")
	d.Dir = lo.Home
	d.Env = lo.Env()
	if o, err := d.Output(); err != nil || strings.TrimSpace(string(o)) != Version() {
		t.Errorf("local den version: %q %v", o, err)
	}
	// The notify-send stub records.
	n := exec.Command(filepath.Join(lo.Stubs, "notify-send"), "a b", "c")
	n.Dir = lo.Home
	n.Env = lo.Env()
	if err := n.Run(); err != nil {
		t.Fatal(err)
	}
	if got := lo.Notifications(); len(got) != 1 || got[0] != "a b c" {
		t.Errorf("notifications = %q", got)
	}
}

func TestTwoRemotes(t *testing.T) {
	l := New(t, "a", "b")
	for _, name := range []string{"a", "b"} {
		res := l.Remote(name).Run("echo $HOME")
		if strings.TrimSpace(res.Stdout) != l.Remote(name).Home {
			t.Errorf("%s HOME = %q", name, res.Stdout)
		}
	}
	// A key swap on one host leaves the other usable.
	l.Remote("a").SwapHostKey()
	if res := l.Remote("a").Run("true"); res.Code != 255 {
		t.Errorf("a: %d", res.Code)
	}
	if res := l.Remote("b").Run("true"); res.Code != 0 {
		t.Errorf("b: %d", res.Code)
	}
	if !strings.Contains(l.RemotesTOML("observe"), "[hosts.b]") {
		t.Error("RemotesTOML")
	}
}

// The helper must never reach the production binary.
func TestNotInProductionBinary(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "github.com/GlacierHubAB/den/cmd/den").Output()
	if err != nil {
		t.Skipf("go list: %v", err)
	}
	if strings.Contains(string(out), "internal/sshtest") {
		t.Fatal("cmd/den depends on internal/sshtest")
	}
}
