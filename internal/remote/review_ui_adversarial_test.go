package remote_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/sshtest"
)

// Review of a remote worktree in the real TUI: /merge and /cleanup are refused
// (they work on this machine only), a comment goes to the agent through the
// host, and no diff line, comment quote or title lands in the local state.
func TestAdvReviewUIRemoteWork(t *testing.T) {
	if testing.Short() || os.Getenv("DEN_SLOW_TESTS") != "1" {
		t.Skip("types into the TUI in a private tmux: set DEN_SLOW_TESTS=1")
	}
	g := fullRig(t)
	l := g.l
	l.Local.WriteConfig("[orchestrator]\nspawn = false\n")
	main := mkRepo(t, g, "main-checkout")
	gitIn(t, g, main, "checkout", "-q", "main")
	wt := filepath.Join(g.r.Home, "wt")
	gitIn(t, g, main, "worktree", "add", "-q", "-b", "feature-x", wt)
	g.r.WriteFile("wt/a.txt", "one\nSECRET-diff-line two\n")
	s := g.r.SeedSession(sshtest.SessionOpts{ID: idA, Name: "alpha", CWD: wt, Role: "coder", Title: "SECRET-title", Prompt: "SECRET-prompt"})
	wrote(g, idA, filepath.Join(wt, "a.txt"))
	in := listenInbox(t, s.SocketPath())

	tm := l.Local.StartTUI(200, 56, []string{"DEN_ORCHESTRATOR_SPAWN=off", "DEN_RESTORE=off"})
	tm.WaitFor(30*time.Second, "1 agent")
	tm.Type("/review")
	tm.Keys("Enter")
	f := tm.WaitFor(30*time.Second, "SECRET-diff-line")
	t.Logf("--- review ---\n%s", sshtest.Squeeze(f))
	if !strings.Contains(f, "@b") && !strings.Contains(f, " b") {
		t.Errorf("the host chip is missing")
	}

	// A comment on a diff line, then /send. Right moves into the diff pane.
	tm.Keys("Right")
	time.Sleep(200 * time.Millisecond)
	tm.Keys("Right")
	time.Sleep(200 * time.Millisecond)
	tm.Type("please explain this")
	tm.Keys("Enter")
	time.Sleep(500 * time.Millisecond)
	f = tm.Capture()
	t.Logf("--- after a comment ---\n%s", sshtest.Squeeze(f))
	tm.Type("/send")
	tm.Keys("Enter")
	got, ok := in.text(10 * time.Second)
	t.Logf("delivered to the agent on b: %q (%v)", got, ok)
	f = tm.Capture()
	t.Logf("--- after /send ---\n%s", sshtest.Squeeze(f))
	if !ok || !strings.Contains(got, "please explain this") {
		t.Errorf("the comment did not reach the agent through the host")
	}

	for _, cmd := range []string{"/merge", "/cleanup"} {
		tm.Type(cmd)
		tm.Keys("Enter")
		f = tm.WaitFor(10*time.Second, "works on this machine only")
		t.Logf("--- %s ---\n%s", cmd, sshtest.Squeeze(f))
		if !strings.Contains(f, "on b") {
			t.Errorf("%s: the refusal does not name the host", cmd)
		}
	}

	if b, err := os.ReadFile(filepath.Join(l.Local.StateDir(), "reviews.json")); err == nil {
		t.Logf("reviews.json: %s", b)
	} else {
		t.Logf("reviews.json: %v", err)
	}
	// Nothing remote is written down: the comment's quote of a remote line,
	// the title, the prompt.
	time.Sleep(500 * time.Millisecond)
	for _, secret := range []string{"SECRET-diff-line", "SECRET-title", "SECRET-prompt"} {
		if hits := l.Local.Grep(secret); len(hits) > 0 {
			t.Errorf("%s was written to local state: %v", secret, hits)
		}
	}
}
