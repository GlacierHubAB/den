//go:build linux

package closer

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// child starts this test's own sleep under the given name (a symlink, so
// its comm is that name) and reaps it when it ends. Only these children are
// ever signalled.
func child(t *testing.T, name string) (*exec.Cmd, chan struct{}) {
	t.Helper()
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep")
	}
	bin := filepath.Join(t.TempDir(), name)
	if err := os.Symlink(sleep, bin); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	t.Cleanup(func() { cmd.Process.Kill(); <-done })
	return cmd, done
}

func running(done chan struct{}) bool {
	select {
	case <-done:
		return false
	case <-time.After(200 * time.Millisecond):
		return true
	}
}

func TestIsClaudeProc(t *testing.T) {
	for _, c := range []struct {
		comm, exe string
		argv      []string
		want      bool
	}{
		{"claude", "/home/user/.local/share/claude/versions/2.1.289", []string{"claude", "--name", "x"}, true},
		{"2.1.289", "/home/user/.local/bin/claude", []string{"claude"}, true},
		{"node", "/usr/bin/node", []string{"node", "/usr/lib/node_modules/@anthropic-ai/claude-code/cli.js"}, true},
		{"MainThread", "/usr/bin/node", []string{"/usr/bin/node", "/home/user/.npm-global/bin/claude", "-c"}, true},
		{"sleep", "/usr/bin/sleep", []string{"sleep", "30"}, false},
		{"tmux: server", "/usr/bin/tmux", []string{"tmux", "new", "claude"}, false},
		{"node", "/usr/bin/node", []string{"node", "server.js", "--name", "claude"}, false},
		{"bash", "/usr/bin/bash", []string{"bash", "-c", "claude"}, false},
	} {
		if got := isClaudeProc(c.comm, c.exe, c.argv); got != c.want {
			t.Errorf("%q %q %v = %v", c.comm, c.exe, c.argv, got)
		}
	}
}

// A sessions file whose pid now belongs to a live process that is not
// Claude Code (a reused pid) is refused, and the process is not signalled.
func TestCloseRefusesAPidThatIsNotClaude(t *testing.T) {
	cmd, done := child(t, "sleep")
	tg := Target{SessionID: "s1", Name: "Mossy Lemur", PID: cmd.Process.Pid, Status: "idle", Started: time.Now()}
	res := Run(context.Background(), Plan{Targets: []Target{tg}, Force: true}, Self{}, System{})
	if res[0].Err == nil || !strings.Contains(res[0].Err.Error(), "not Claude Code") || res[0].Ended {
		t.Errorf("result %+v", res[0])
	}
	if !running(done) {
		t.Fatal("a process that is not Claude Code was signalled")
	}
}

// A claude whose process started well after the session did is another
// process on the old pid: refused. One that started with its session is
// stopped, through the pidfd.
func TestCloseChecksTheStartTime(t *testing.T) {
	cmd, done := child(t, "claude")
	tg := Target{SessionID: "s1", Name: "Mossy Lemur", PID: cmd.Process.Pid, Status: "idle", Started: time.Now().Add(-time.Hour)}
	res := Run(context.Background(), Plan{Targets: []Target{tg}}, Self{}, System{})
	if res[0].Err == nil || !strings.Contains(res[0].Err.Error(), "after the session did") {
		t.Errorf("stale session: %+v", res[0])
	}
	if !running(done) {
		t.Fatal("a newer process on the session's pid was signalled")
	}
	tg.Started = time.Now()
	res = Run(context.Background(), Plan{Targets: []Target{tg}}, Self{}, System{})
	if res[0].Err != nil || !res[0].Ended || res[0].Killed {
		t.Errorf("its own claude: %+v", res[0])
	}
	if running(done) {
		t.Fatal("still running after the close")
	}
}

func TestCloseRefusesWithoutAKnownStart(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir()) // no sessions files
	cmd, done := child(t, "claude")
	res := Run(context.Background(), Plan{Targets: []Target{{SessionID: "s1", PID: cmd.Process.Pid, Status: "idle"}}}, Self{}, System{})
	if res[0].Err == nil || !strings.Contains(res[0].Err.Error(), "start is not known") || !running(done) {
		t.Errorf("result %+v", res[0])
	}
}

func TestCloseOfAnEndedProcessIsGone(t *testing.T) {
	cmd, done := child(t, "claude")
	pid := cmd.Process.Pid
	cmd.Process.Kill()
	<-done
	if _, err := (System{}).Hold(Target{PID: pid, Started: time.Now()}); !errors.Is(err, ErrGone) {
		t.Errorf("Hold of an ended process: %v", err)
	}
}

// When the wait is cut short (the link dropped, Den stopped), a forced
// close does not go on to SIGKILL: it says it does not know.
func TestNoKillAfterTheContextEnds(t *testing.T) {
	ops := &fakeOps{alive: map[int]bool{2: true}, stubborn: map[int]bool{2: true}}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	res := Run(ctx, Plan{Targets: []Target{{SessionID: "s2", PID: 2, Status: "idle"}}, Force: true}, Self{}, ops)
	if !errors.Is(res[0].Err, ErrNotKnown) || res[0].Killed || res[0].Ended {
		t.Errorf("result %+v", res[0])
	}
	for _, s := range ops.signals {
		if strings.Contains(s, "killed") || strings.Contains(s, syscall.SIGKILL.String()) {
			t.Errorf("signals = %v", ops.signals)
		}
	}
}
