package remote_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/sshtest"
)

// The real den TUI, in a private tmux server, against a real private sshd: the
// read-only fleet as the human sees it. Slow (and it types into the TUI), so
// it runs only with DEN_SLOW_TESTS=1.
func TestE2ETUI(t *testing.T) {
	if testing.Short() || os.Getenv("DEN_SLOW_TESTS") != "1" {
		t.Skip("end-to-end TUI test: set DEN_SLOW_TESTS=1")
	}
	g := newRig(t)
	l := g.l
	l.Local.WriteConfig("[orchestrator]\nspawn = false\n")
	g.r.SeedSession(sshtest.SessionOpts{ID: idA, Name: "alpha", Role: "reviewer", Task: "check the parser"})
	g.r.SeedSession(sshtest.SessionOpts{ID: idB, Name: "beta", Role: "builder", Task: "fix the build", Status: "busy"})
	l.Local.SeedSession(sshtest.SessionOpts{ID: "22222222-2222-4333-8444-555555555555", Name: "local-one", Role: "planner", Task: "plan it"})

	tm := l.Local.StartTUI(200, 56, []string{"DEN_ORCHESTRATOR_SPAWN=off", "DEN_RESTORE=off"})
	frames := map[string]string{}
	frame := func(name, f string) {
		f = sshtest.Squeeze(f)
		frames[name] = f
		t.Logf("--- frame %q ---\n%s", name, f)
	}
	// show types a view command and waits for the panel that proves it took
	// effect (the header text is on every view). Escape, sent alone, first
	// closes the /remote listing.
	show := func(cmd string, panel ...string) string {
		tm.Keys("Escape")
		time.Sleep(300 * time.Millisecond)
		tm.Type(cmd)
		tm.Keys("Enter")
		return tm.WaitFor(15*time.Second, panel...)
	}

	// 1. The remote agents show with @b, next to the local one.
	tm.WaitFor(30*time.Second, "3 agents")
	f := show("/fleet", "┌─ Agents", "@b", "REVIEWER", "BUILDER", "PLANNER")
	frame("fleet", f)
	if strings.Count(f, "@b") < 2 {
		t.Errorf("want both remote agents marked @b:\n%s", f)
	}

	// 2. A remote PermissionRequest: an inbox item "on b" and a notification.
	start := time.Now()
	g.r.HookLocal("PermissionRequest", map[string]any{"session_id": idA, "tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}})
	t.Logf("remote hook returned in %v", time.Since(start).Round(time.Millisecond))
	if time.Since(start) > 3*time.Second {
		t.Errorf("the remote hook waited")
	}
	if !sshtest.WaitFor(15*time.Second, func() bool {
		for _, n := range l.Local.Notifications() {
			if strings.Contains(n, "on b") {
				return true
			}
		}
		return false
	}) {
		t.Errorf("no notification naming \"on b\": %q", l.Local.Notifications())
	}
	t.Logf("notifications: %q", l.Local.Notifications())
	f = show("/inbox", "┌─ Inbox", "on b")
	frame("inbox", f)

	// Ctrl+Y does not answer a remote request (observe is read-only).
	tm.Keys("C-y")
	f = tm.WaitFor(10*time.Second, "answer it in the terminal there")
	frame("after Ctrl+Y", f)
	if !tm.Alive() {
		t.Fatal("the TUI died")
	}
	if !strings.Contains(f, "Wants to use Bash on b") {
		t.Errorf("the request is gone after Ctrl+Y; it must stay:\n%s", f)
	}

	// 3. The link drops: the host reads offline and its agents are last known.
	l.Remote("b").Down()
	if l.KillSSH() == 0 {
		t.Fatal("no ssh to kill")
	}
	tm.WaitFor(30*time.Second, "last known")
	f = show("/remote", "Remote hosts", "offline")
	frame("remote hosts, offline", f)
	f = show("/fleet", "┌─ Agents", "@b", "last known")
	frame("fleet, link lost", f)

	// 4. Back: /remote connect b typed into Den's own command line.
	l.Remote("b").Up()
	tm.Type("/remote connect b")
	tm.Keys("Enter")
	tm.WaitFor(30*time.Second, "3 agents")
	f = show("/remote", "Remote hosts", " live")
	frame("remote hosts, reconnected", f)
	if strings.Contains(f, "offline") {
		t.Errorf("host still reads offline:\n%s", f)
	}
	f = show("/fleet", "┌─ Agents", "@b", "REVIEWER")
	frame("fleet, reconnected", f)
	if strings.Contains(f, "last known") {
		t.Errorf("agents still last known:\n%s", f)
	}
}
