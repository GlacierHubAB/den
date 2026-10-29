package inbox

import (
	"testing"
	"time"
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

	// A send that never reports may still have gone out: its asker is
	// told to check before sending again.
	send := store.add(&Item{SessionID: "s2", Kind: Message, Title: "Send to Mossy Lemur on devbox?"})
	store.Start(send, "sending…", time.Minute)
	now = now.Add(time.Minute + StartGrace + time.Second)
	if lost := store.LoseStarted(); len(lost) != 1 || send.Resolved != "Den lost track of this send; it may still have been sent, so check before sending again" {
		t.Errorf("a lost send: %v %q", lost, send.Resolved)
	}
}
