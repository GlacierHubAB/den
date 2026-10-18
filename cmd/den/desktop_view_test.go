package main

import (
	"os/exec"
	"testing"
	"time"
)

func TestParseOwner(t *testing.T) {
	if pid, start, err := parseOwner("4242:987"); err != nil || pid != 4242 || start != "987" {
		t.Fatalf("got %d %q %v", pid, start, err)
	}
	if pid, _, err := parseOwner(""); err != nil || pid != 0 {
		t.Fatalf("no owner: %d %v", pid, err)
	}
	for _, bad := range []string{"4242", "x:1", "1:5", "0:5", "-3:5", "4242:"} {
		if _, _, err := parseOwner(bad); err == nil {
			t.Errorf("%q was taken", bad)
		}
	}
}

// When Den ends, the view stops the agent (P4): the terminal that runs
// the agent is not Den's child, so nothing else would.
func TestWatchDenStopsTheAgent(t *testing.T) {
	agent := exec.Command("sleep", "60")
	if err := agent.Start(); err != nil {
		t.Skip("no sleep:", err)
	}
	alive := make(chan bool, 1)
	alive <- true
	old, oldEvery := denAlive, denWatchEvery
	denAlive = func(int, string) bool {
		select {
		case v := <-alive:
			return v
		default:
			return false // Den has ended
		}
	}
	denWatchEvery = 10 * time.Millisecond
	t.Cleanup(func() { denAlive, denWatchEvery = old, oldEvery })
	go watchDen(4242, "987", agent.Process)
	done := make(chan error, 1)
	go func() { done <- agent.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("the agent ended on its own, not stopped")
		}
	case <-time.After(5 * time.Second):
		agent.Process.Kill()
		t.Fatal("the agent still runs after Den ended")
	}
}
