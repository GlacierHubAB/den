package desktop

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func sleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	c := exec.Command("sleep", "30")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Process.Kill(); c.Wait() })
	return c
}

func writeBeacon(t *testing.T, pid int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	os.WriteFile(path, []byte(fmt.Sprintf(`{"pid": %d, "started": 1, "last_action": "pointer:move", "last_ts": %f}`, pid, float64(time.Now().UnixNano())/1e9)), 0o600)
	return path
}

func gone(c *exec.Cmd) bool {
	done := make(chan struct{})
	go func() { c.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(time.Second):
		return false
	}
}

func TestStopOurs(t *testing.T) {
	child, agent := sleeper(t), sleeper(t)
	tg := Targets{Child: RecordProc(child.Process.Pid), Agent: RecordProc(agent.Process.Pid)}
	b, active := ReadBeacon(writeBeacon(t, child.Process.Pid))
	if !active || !b.Acting(time.Now()) {
		t.Fatalf("beacon %+v active %v", b, active)
	}
	r := Stop(tg, writeBeacon(t, child.Process.Pid))
	if r.Foreign != "" || strings.Join(r.Stopped, ",") != "hypruse,the agent" {
		t.Fatalf("report %+v", r)
	}
	if r.Took > 680*time.Millisecond {
		t.Errorf("took %v, over the 0.68 s target", r.Took)
	}
	if !gone(child) || !gone(agent) {
		t.Fatal("a process survived the stop")
	}
}

// A beacon held by a process Den did not start is named and never
// signalled; Den's own agent still stops.
func TestStopForeignBeacon(t *testing.T) {
	stranger, agent := sleeper(t), sleeper(t)
	r := Stop(Targets{Agent: RecordProc(agent.Process.Pid)}, writeBeacon(t, stranger.Process.Pid))
	if !strings.Contains(r.Foreign, fmt.Sprintf("pid %d", stranger.Process.Pid)) {
		t.Fatalf("report %+v", r)
	}
	if err := stranger.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatal("the stranger was signalled")
	}
	if !gone(agent) {
		t.Fatal("our agent survived")
	}
}

// A recorded pid that now belongs to another process (another start, or
// another executable) is never signalled.
func TestStopChecksIdentity(t *testing.T) {
	victim := sleeper(t)
	p := RecordProc(victim.Process.Pid)
	stale := p
	stale.Start = "1"
	other := p
	other.Exe = "/usr/bin/claude"
	r := Stop(Targets{Child: stale, Agent: other}, filepath.Join(t.TempDir(), "none.json"))
	if len(r.Stopped) != 0 {
		t.Fatalf("signalled %v", r.Stopped)
	}
	if err := victim.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatal("the process was signalled")
	}
	// The pid with the recorded start still runs, but not what Den
	// recorded: the stop failed, and says so first, never "nothing ran".
	// The other, recorded with another start, is gone: nothing to say.
	if r.OK() || len(r.Failed) != 1 || !strings.HasPrefix(r.Failed[0], "the agent (pid ") || !strings.HasPrefix(r.Line(), "could not stop the agent") || strings.Contains(r.Line(), "nothing of Den's was running") {
		t.Errorf("report %+v: %s", r, r.Line())
	}
}

// Stop still ends a process whose binary was updated in place during the
// mission (Claude Code updates itself): it is the process recorded.
func TestStopAfterAnUpdateInPlace(t *testing.T) {
	src, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep")
	}
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(bin, raw, 0o755); err != nil {
		t.Fatal(err)
	}
	c := exec.Command(bin, "30")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Process.Kill(); c.Wait() })
	p := RecordProc(c.Process.Pid)
	os.Remove(bin) // the update writes a new file at the path
	os.WriteFile(bin, raw, 0o755)
	r := Stop(Targets{Agent: p}, filepath.Join(t.TempDir(), "none.json"))
	if len(r.Stopped) != 1 || !r.OK() || !gone(c) {
		t.Fatalf("not stopped after the update: %+v", r)
	}
}

// Stop still ends a process whose binary was updated in place during the
// mission (Claude Code updates itself): it is the process recorded.
func TestStopAfterAnUpdateInPlace(t *testing.T) {
	src, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep")
	}
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(bin, raw, 0o755); err != nil {
		t.Fatal(err)
	}
	c := exec.Command(bin, "30")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Process.Kill(); c.Wait() })
	p := RecordProc(c.Process.Pid)
	os.Remove(bin) // the update writes a new file at the path
	os.WriteFile(bin, raw, 0o755)
	r := Stop(Targets{Agent: p}, filepath.Join(t.TempDir(), "none.json"))
	if len(r.Stopped) != 1 || !gone(c) {
		t.Fatalf("not stopped after the update: %+v", r)
	}
}

func TestBeaconZombieAndStale(t *testing.T) {
	c := exec.Command("true")
	c.Start()
	pid := c.Process.Pid
	time.Sleep(100 * time.Millisecond) // it exits; unreaped, it is a zombie
	if _, active := ReadBeacon(writeBeacon(t, pid)); active {
		t.Fatal("a zombie counted as active")
	}
	c.Wait()
	if _, active := ReadBeacon(writeBeacon(t, pid)); active {
		t.Fatal("a dead pid counted as active")
	}
	if _, active := ReadBeacon(filepath.Join(t.TempDir(), "missing")); active {
		t.Fatal("no file counted as active")
	}
}
