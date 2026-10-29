package inbox

import (
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
)

// A Started item takes no answer, and only its action's end settles it:
// not a dismiss, not a release to the terminal, not the agent's session
// ending. An action that never reports is settled as lost after its
// timeout and StartGrace on the store's clock, never answerable again.
func TestStartedItemSettlesOnlyByItsEnd(t *testing.T) {
	store := New()
	t0 := time.Date(2026, 10, 28, 19, 0, 0, 0, time.UTC)
	now := t0
	store.now = func() time.Time { return now }
	it := store.AddRestore("s1", "Reopen Comet Newt?", nil)
	if !it.Answerable() {
		t.Fatal("a waiting restore is not answerable")
	}
	store.Start(it, "reopening…", time.Minute)
	if it.Answerable() || it.Resolved != "reopening…" || it.State != Waiting || !it.StartedUntil.Equal(t0.Add(time.Minute+StartGrace)) {
		t.Fatalf("started: answerable %v, %q, %v, until %v", it.Answerable(), it.Resolved, it.State, it.StartedUntil)
	}
	store.Dismiss(it)
	store.Release(it, "you went to that terminal")
	store.SessionClosed("s1")
	if len(store.Items()) != 1 || it.State != Waiting || it.Resolved != "reopening…" {
		t.Fatalf("after a dismiss, a release and the session closing: %d items, %v %q", len(store.Items()), it.State, it.Resolved)
	}
	now = t0.Add(time.Minute + StartGrace)
	if lost := store.LoseStarted(); len(lost) != 0 || it.State != Waiting {
		t.Errorf("lost at its deadline: %v", lost)
	}
	now = t0.Add(time.Minute + StartGrace + time.Second)
	lost := store.LoseStarted()
	if len(lost) != 1 || it.State != Resolved || it.Resolved != "Den lost track of this; check the agents" || it.Answerable() {
		t.Errorf("after its deadline: %v, %v %q", lost, it.State, it.Resolved)
	}
	now = t0.Add(time.Hour)
	if again := store.LoseStarted(); len(again) != 0 {
		t.Errorf("lost twice: %v", again)
	}

	if it.Outcome != OutcomeLost {
		t.Errorf("a lost item's outcome: %q", it.Outcome)
	}
	// Its action reporting late changes nothing: it settled once, lost.
	store.Settle(it, true, "reopened Comet Newt")
	if it.Outcome != OutcomeLost || it.Resolved != "Den lost track of this; check the agents" {
		t.Errorf("a late report re-settled it: %q %q", it.Outcome, it.Resolved)
	}

	// A send that never reports may still have gone out: its asker is
	// told to check before sending again.
	send := store.add(&Item{SessionID: "s2", Kind: Message, Title: "Send to Mossy Lemur on devbox?"})
	store.Start(send, "sending…", time.Minute)
	now = now.Add(time.Minute + StartGrace + time.Second)
	if lost := store.LoseStarted(); len(lost) != 1 || send.Resolved != "Den lost track of this send; it may still have been sent, so check before sending again" {
		t.Errorf("a lost send: %v %q", lost, send.Resolved)
	}
}

// A Started item's end says how it went: done or failed, by its action's
// own report, through Settle or SettleWith; an item never Started has no
// outcome.
func TestStartedOutcome(t *testing.T) {
	store := New()
	done := store.AddRestore("s1", "Reopen Comet Newt?", nil)
	store.Start(done, "reopening…", time.Minute)
	store.Settle(done, true, "reopened Comet Newt")
	failed := store.AddClose("s2", "Close Mossy Lemur?", nil, nil)
	store.Start(failed, "closing…", time.Minute)
	store.SettleWith(failed, bridge.Decision{Behavior: "deny"}, "could not close Mossy Lemur")
	plain := store.AddClose("s3", "Close Quiet Panda?", nil, nil)
	store.Settle(plain, false, "declined in den; nothing closed")
	if done.Outcome != OutcomeDone || failed.Outcome != OutcomeFailed || plain.Outcome != "" {
		t.Errorf("outcomes %q %q %q", done.Outcome, failed.Outcome, plain.Outcome)
	}
}

// A Started send whose agent's session ends may still have gone out: the
// session's end does not cancel it as "nothing was sent", and no other
// clearing of the session settles it. Only its end, or LoseStarted, does.
func TestSessionEndLeavesAStartedSend(t *testing.T) {
	store := New()
	send := store.add(&Item{Host: "devbox", SessionID: "s2", Kind: Message, Title: "Send to Mossy Lemur on devbox?"})
	waiting := store.add(&Item{Host: "devbox", SessionID: "s2", Kind: Message, Title: "Send again?"})
	store.Start(send, "sending…", time.Minute)
	store.cancelMessages("devbox", "s2")
	store.resolveSession("devbox", "s2", "session ended", func(*Item) bool { return true })
	if send.State != Waiting || send.Resolved != "sending…" {
		t.Errorf("a started send after its session ended: %v %q", send.State, send.Resolved)
	}
	if waiting.State != Resolved {
		t.Errorf("a send not started stayed open: %v", waiting.State)
	}
}
