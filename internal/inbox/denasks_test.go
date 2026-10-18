package inbox

import (
	"testing"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/events"
)

// An agent's hook events never answer Den's own question stored on its
// session: a close card waits through the target's turn ending, a new
// prompt, a failed turn and its session ending (as when Den closes it),
// and the asker hears only the close's own outcome.
func TestHooksNeverAnswerDensQuestions(t *testing.T) {
	store := New()
	req := bridge.TestRequest(bridge.Envelope{Event: bridge.CloseEvent})
	it := store.AddCloseOn("", "s1", "Close Lemur?", []string{"Lemur"}, req)
	for _, ev := range []string{"Stop", "UserPromptSubmit", "StopFailure", "SessionEnd"} {
		store.Apply(hook("s1", ev, map[string]any{}))
		if _, ok := req.Decided(); ok {
			t.Fatalf("%s answered the close", ev)
		}
		if it.State == Resolved {
			t.Fatalf("%s resolved the close card", ev)
		}
	}
	// A remote host's session ending does not answer one there either.
	rreq := bridge.TestRequest(bridge.Envelope{Event: bridge.CloseEvent})
	rit := store.AddCloseOn("build1", "s2", "Close Otter?", []string{"Otter"}, rreq)
	store.ApplyRemote(events.Event{Kind: events.SessionEnd, Host: "build1", Session: "s2"})
	if _, ok := rreq.Decided(); ok || rit.State == Resolved {
		t.Fatal("a remote session's end answered the close")
	}
	store.Settle(it, true, "closed Lemur")
	if d, _ := req.Decided(); d.Behavior != "allow" || d.Message != "closed Lemur" {
		t.Errorf("the close's outcome: %+v", d)
	}
}

// A desktop agent's own request ends with its session, as a typed cancel,
// never an empty answer.
func TestSessionEndCancelsItsOwnAsks(t *testing.T) {
	store := New()
	req := bridge.TestRequest(bridge.Envelope{Event: bridge.DesktopEvent})
	it := store.AddDesktopAct("s1", "click", "Click Save?", nil, req)
	store.Apply(hook("s1", "SessionEnd", map[string]any{}))
	d, ok := req.Decided()
	if !ok || d.Behavior != "deny" || d.Message != "cancelled: the session ended; nothing done" || it.State != Resolved {
		t.Errorf("answer %+v %v, state %v", d, ok, it.State)
	}
}
