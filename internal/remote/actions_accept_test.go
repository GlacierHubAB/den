package remote_test

// The acceptance tests for send and close, run for real over a private sshd:
// the Manager under test sends to and closes fake sessions on host b. A Unix
// socket in host b's scratch runtime dir stands in for Claude Code's
// messagingSocketPath, and a dummy `sleep` process stands in for the session's
// process, so a real SIGTERM crosses the real pipe.

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/peer"
	"github.com/GlacierHubAB/den/internal/remote"
	"github.com/GlacierHubAB/den/internal/sshtest"
)

// listenAt stands in for a session's inbox socket and reports each line.
func listenAt(t *testing.T, path string) <-chan string {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o700)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	lines := make(chan string, 8)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(c).ReadString('\n')
			c.Close()
			lines <- line
		}
	}()
	return lines
}

func TestAcceptS3SendReachesTheSessionsSocket(t *testing.T) {
	g := approveRig(t)
	s := g.r.SeedSession(sshtest.SessionOpts{ID: idA})
	lines := listenAt(t, s.Socket())
	g.start()
	g.live()

	marker := filepath.Join(g.l.Dir, "SHELL-RAN")
	text := "rebase on main `touch " + marker + "` $(touch " + marker + ") ; \"quoted\"\nline two"
	if err := g.m.Send("b", idA, text); err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": peer.Compose(text)}})
	select {
	case line := <-lines:
		if line != string(want)+"\n" {
			t.Errorf("the socket got\n%q\nwant\n%q", line, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing reached the session's socket on host b")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the message text was run by a shell")
	}
}

func TestAcceptS3SendRefusals(t *testing.T) {
	g := approveRig(t)
	live := g.r.SeedSession(sshtest.SessionOpts{ID: idA})
	lines := listenAt(t, live.Socket())
	dead := g.r.SeedSession(sshtest.SessionOpts{ID: idB})
	dead.Stop() // the file stays, the process is gone
	g.start()
	g.live()

	for _, c := range []struct{ what, session, want string }{
		{"an unknown session", "11111111-2222-4333-8444-999999999999", "no live session"},
		{"a session whose process is gone", idB, "no live session"},
		{"a prefix of a session id", idA[:8], "no live session"},
	} {
		err := g.m.Send("b", c.session, "hi")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.what, err)
		}
	}
	if err := g.m.Send("b", idA, strings.Repeat("x", remote.MaxSendText+1)); err == nil {
		t.Error("an oversize text was sent")
	}
	select {
	case line := <-lines:
		t.Errorf("a refused send reached the socket: %q", line)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestAcceptS3CloseSigtermsTheSessionProcess(t *testing.T) {
	g := approveRig(t)
	idle := g.r.SeedSession(sshtest.SessionOpts{ID: idA})
	busy := g.r.SeedSession(sshtest.SessionOpts{ID: idB, Status: "busy"})
	g.start()
	g.live()
	g.wait("both sessions", 10*time.Second, func() bool { return g.status().Agents == 2 })

	if _, err := g.m.CloseAgent("b", idB, false); err == nil || !strings.Contains(err.Error(), "working") {
		t.Errorf("a working agent without force: %v", err)
	}
	if busy.Exited() {
		t.Fatal("a refused close killed the process")
	}
	start := time.Now()
	killed, err := g.m.CloseAgent("b", idA, false)
	if err != nil || killed {
		t.Fatalf("closing the idle one: killed %v, err %v", killed, err)
	}
	if !idle.Exited() {
		t.Error("SIGTERM never ended the idle session's process on host b")
	}
	t.Logf("close across ssh took %v", time.Since(start).Round(time.Millisecond))
	if killed, err := g.m.CloseAgent("b", idB, true); err != nil || killed {
		t.Fatalf("forcing the working one: killed %v, err %v", killed, err)
	}
	if !busy.Exited() {
		t.Error("a forced close did not end the working session's process")
	}
	if _, err := g.m.CloseAgent("b", idA, true); err == nil {
		t.Error("closing an already closed session did not fail")
	}
}

func TestAcceptS3ObserveAndOfflineRefuseBeforeSending(t *testing.T) {
	g := newRig(t) // observe
	s := g.r.SeedSession(sshtest.SessionOpts{ID: idA})
	lines := listenAt(t, s.Socket())
	g.start()
	g.live()
	if err := g.m.Send("b", idA, "hi"); err == nil || !strings.Contains(err.Error(), "observe") {
		t.Errorf("send under observe: %v", err)
	}
	if _, err := g.m.CloseAgent("b", idA, true); err == nil || !strings.Contains(err.Error(), "observe") {
		t.Errorf("close under observe: %v", err)
	}
	if s.Exited() {
		t.Error("observe let a close through")
	}
	select {
	case line := <-lines:
		t.Errorf("observe let a message through: %q", line)
	case <-time.After(300 * time.Millisecond):
	}

	g.r.Down()
	g.l.KillSSH()
	g.wait("offline", 20*time.Second, func() bool { return g.status().State == remote.Offline })
	if err := g.m.Send("b", idA, "hi"); err == nil || err.Error() != "b is offline; nothing was sent" {
		t.Errorf("send while offline: %v", err)
	}
	if _, err := g.m.CloseAgent("b", idA, false); err == nil || err.Error() != "b is offline; nothing was closed" {
		t.Errorf("close while offline: %v", err)
	}
	g.r.Up()
}
