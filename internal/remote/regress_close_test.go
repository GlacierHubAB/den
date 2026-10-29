package remote_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/badge"
	"github.com/GlacierHubAB/den/internal/sshtest"
)

// Regression tests for what a real host showed about closing and installing.

// /protect holds for a remote agent however it is named: the bare name, name@host,
// and /close idle. A protected agent is never proposed for closing and the
// refusal says how to allow it.
func TestRegressProtectHoldsForRemoteNames(t *testing.T) {
	if testing.Short() || os.Getenv("DEN_SLOW_TESTS") != "1" {
		t.Skip("types into the TUI in a private tmux: set DEN_SLOW_TESTS=1")
	}
	l := sshtest.New(t, "b")
	l.Local.Apply()
	l.Local.WriteConfig("[orchestrator]\nspawn = false\n")
	l.Local.WriteRemotes(l.RemotesTOML("approve"))
	r := l.Remote("b")
	sa := r.SeedSession(sshtest.SessionOpts{ID: idA, Status: "idle", StartedAgo: 3 * time.Hour})
	sb := r.SeedSession(sshtest.SessionOpts{ID: idB, Status: "idle", StartedAgo: 3 * time.Hour})
	const idC = "11111111-2222-4333-8444-777777777777"
	r.SeedSession(sshtest.SessionOpts{ID: idC, Status: "idle", StartedAgo: 3 * time.Hour})
	named, bare, open := badge.Name(idA), badge.Name(idB), badge.Name(idC)

	tm := l.Local.StartTUI(200, 56, []string{"DEN_ORCHESTRATOR_SPAWN=off", "DEN_RESTORE=off"})
	// Den's own setup card waits from the start (Claude Code in the lab has
	// no hooks), and nothing else: any close question makes it 2.
	if f := sshtest.Squeeze(tm.WaitFor(30*time.Second, "3 agents")); !strings.Contains(f, "inbox 1 waiting") {
		t.Fatalf("before any command the inbox is not exactly Den's setup card:\n%s", f)
	}
	say := func(cmd string) string {
		tm.Type(cmd)
		tm.Keys("Enter")
		time.Sleep(1500 * time.Millisecond)
		return sshtest.Squeeze(tm.Capture())
	}
	noQuestion := func(what, f string) {
		t.Helper()
		if strings.Contains(f, "Close "+named+" on b?") || strings.Contains(f, "Close "+bare+" on b?") || strings.Contains(f, "Close 2 agents") || !strings.Contains(f, "inbox 1 waiting") {
			t.Errorf("%s: a close was proposed or is waiting:\n%s", what, f)
		}
	}
	alive := func(what string) {
		t.Helper()
		for _, s := range []*sshtest.Session{sa, sb} {
			if s.Exited() {
				t.Errorf("%s: the session %s was closed", what, s.ID)
			}
		}
	}

	f := say("/protect " + named + "@b")
	t.Logf("--- protect name@host ---\n%s", f)
	if !strings.Contains(f, "protected") {
		t.Errorf("/protect %s@b: not acknowledged", named)
	}
	f = say("/close " + named + "@b")
	t.Logf("--- close name@host ---\n%s", f)
	noQuestion("/close "+named+"@b", f)
	if !strings.Contains(f, "/unprotect") {
		t.Errorf("the refusal does not say how to allow it:\n%s", f)
	}

	say("/protect " + bare)
	f = say("/close " + bare + "@b")
	t.Logf("--- close after a bare protect ---\n%s", f)
	noQuestion("/close "+bare+"@b after /protect "+bare, f)
	f = say("/close " + bare)
	noQuestion("/close "+bare, f)

	// Control: /close idle does propose an agent nobody protected, and only that one.
	f = say("/close idle")
	t.Logf("--- close idle ---\n%s", f)
	f = say("/inbox")
	t.Logf("--- inbox ---\n%s", f)
	if !strings.Contains(f, "Close "+open+" on b?") {
		t.Errorf("/close idle did not propose the unprotected agent %s:\n%s", open, f)
	}
	if strings.Contains(f, "Close "+named) || strings.Contains(f, "Close "+bare) || strings.Contains(f, " and ") && strings.Contains(f, "Close 2 ") {
		t.Errorf("/close idle proposed a protected agent:\n%s", f)
	}
	alive("before anything was approved")
}

// A host that waits for den (not installed) is connected again once an
// install approved in the inbox has finished, without /remote connect.
func TestRegressInstallReconnectsTheHost(t *testing.T) {
	if testing.Short() || os.Getenv("DEN_SLOW_TESTS") != "1" {
		t.Skip("types into the TUI in a private tmux: set DEN_SLOW_TESTS=1")
	}
	g := newRig(t)
	l := g.l
	l.Local.WriteConfig("[orchestrator]\nspawn = false\n")
	g.r.WithoutDen()

	tm := l.Local.StartTUI(200, 56, []string{"DEN_ORCHESTRATOR_SPAWN=off", "DEN_RESTORE=off"})
	time.Sleep(3 * time.Second)
	tm.Type("/remote")
	tm.Keys("Enter")
	f := tm.WaitFor(30*time.Second, "mismatch")
	t.Logf("--- before ---\n%s", sshtest.Squeeze(f))

	type result struct {
		out  string
		code int
	}
	done := make(chan result, 1)
	go func() {
		out, code := g.den("remote", "install", "b")
		done <- result{out, code}
	}()
	f = tm.WaitFor(30*time.Second, "Install den")
	t.Logf("--- install question ---\n%s", sshtest.Squeeze(f))
	tm.Keys("C-y") // a request that just came up asks to be answered again
	time.Sleep(1500 * time.Millisecond)
	tm.Keys("C-y")
	select {
	case r := <-done:
		t.Logf("den remote install -> exit %d: %s", r.code, strings.TrimSpace(r.out))
		if r.code != 0 || !strings.Contains(r.out, "installed") {
			t.Fatalf("the install did not finish: exit %d: %s", r.code, r.out)
		}
	case <-time.After(90 * time.Second):
		t.Fatal("den remote install never finished")
	}
	// The host comes up by itself: Den starts a serve on it, which it
	// could not before den was there, and the host reads live.
	if !sshtest.WaitFor(40*time.Second, func() bool { return len(g.r.ServePIDs()) > 0 }) {
		t.Fatalf("no serve ever ran on the host after the install: it stayed in version mismatch")
	}
	tm.Keys("Escape")
	tm.Keys("C-o") // back from the orchestrator pane, if the keys opened it
	tm.Type("/remote")
	tm.Keys("Enter")
	f = tm.WaitFor(20*time.Second, "Remote hosts")
	sshtest.WaitFor(30*time.Second, func() bool {
		f = sshtest.Squeeze(tm.Capture())
		return strings.Contains(f, "  live")
	})
	t.Logf("--- after ---\n%s", f)
	if strings.Contains(f, "version mismatch") || !strings.Contains(f, "  live") {
		t.Errorf("the host is not live after the install:\n%s", f)
	}
}

// A `den send name@host` from this machine is one inbox question with the
// host, the agent, who asked and the full text. Nothing is sent before ^Y; ^N
// sends nothing and the command says so.
func TestRegressCLISendAsksFirst(t *testing.T) {
	if testing.Short() || os.Getenv("DEN_SLOW_TESTS") != "1" {
		t.Skip("types into the TUI in a private tmux: set DEN_SLOW_TESTS=1")
	}
	l := sshtest.New(t, "b")
	l.Local.Apply()
	l.Local.WriteConfig("[orchestrator]\nspawn = false\n")
	l.Local.WriteRemotes(l.RemotesTOML("approve"))
	s := l.Remote("b").SeedSession(sshtest.SessionOpts{ID: idA, Name: "alpha"})
	in := listenInbox(t, s.SocketPath())
	g := &rig{t: t, l: l}
	tm := l.Local.StartTUI(200, 56, []string{"DEN_ORCHESTRATOR_SPAWN=off", "DEN_RESTORE=off"})
	tm.WaitFor(30*time.Second, "1 agent")

	type result struct {
		out  string
		code int
	}
	send := func(text string) <-chan result {
		ch := make(chan result, 1)
		go func() { o, c := g.den("send", "alpha@b", text); ch <- result{o, c} }()
		return ch
	}
	// Declined.
	ch := send("SECRET-ask-first declined")
	tm.WaitFor(20*time.Second, "Send a message to")
	tm.Type("/inbox")
	tm.Keys("Enter")
	f := tm.WaitFor(20*time.Second, "SECRET-ask-first declined")
	t.Logf("--- question ---\n%s", sshtest.Squeeze(f))
	if !strings.Contains(f, "on b") {
		t.Errorf("the question does not name the host")
	}
	if txt, ok := in.text(1500 * time.Millisecond); ok {
		t.Fatalf("delivered before the human answered: %q", txt)
	}
	tm.Keys("C-n")
	time.Sleep(1200 * time.Millisecond)
	tm.Keys("C-n")
	select {
	case r := <-ch:
		t.Logf("declined -> exit %d: %s", r.code, strings.TrimSpace(r.out))
		if r.code == 0 {
			t.Errorf("a declined send exited 0: %s", r.out)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the declined send never ended")
	}
	if txt, ok := in.text(1500 * time.Millisecond); ok {
		t.Errorf("a declined send was delivered: %q", txt)
	}
	// Allowed. ^Y on the overview answers the question waiting, not the
	// declined one the inbox view still has selected.
	tm.Type("/overview")
	tm.Keys("Enter")
	time.Sleep(500 * time.Millisecond)
	ch = send("allowed text")
	time.Sleep(time.Second)
	for deadline := time.After(30 * time.Second); ; {
		select {
		case r := <-ch:
			t.Logf("allowed -> exit %d: %s", r.code, strings.TrimSpace(r.out))
			if r.code != 0 {
				t.Errorf("an allowed send failed: %s", r.out)
			}
		case <-deadline:
			t.Fatal("the allowed send never ended")
		case <-time.After(1200 * time.Millisecond):
			tm.Keys("C-y") // a request that just came up asks for it again
			continue
		}
		break
	}
	if txt, ok := in.text(5 * time.Second); !ok || !strings.Contains(txt, "allowed text") {
		t.Errorf("the allowed send did not arrive: %q %v", txt, ok)
	}
}
