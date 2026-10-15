package inbox

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/events"
)

const remoteSID = "11111111-2222-4333-8444-555555555555"

func remoteEvent(kind, request, tool string) events.Event {
	ev := events.New(kind, remoteSID)
	ev.Request, ev.Tool, ev.Host = request, tool, "devbox"
	return ev
}

func TestApplyRemoteIsShownNeverAnswerable(t *testing.T) {
	s := New()
	it := s.ApplyRemote(remoteEvent(events.NeedsYou, "permission", "Bash"))
	if it == nil {
		t.Fatal("no item")
	}
	if it.Answerable() || it.Request() != nil || it.State != InTerminal {
		t.Errorf("answerable %v, request %v, state %v", it.Answerable(), it.Request(), it.State)
	}
	if it.Host != "devbox" || it.SessionID != remoteSID || it.Title != "Wants to use Bash on devbox" {
		t.Errorf("item = %+v", it)
	}
	if s.Answer(it, "allow") {
		t.Errorf("Answer took a remote item")
	}
	// Keyed by host: the same session id on this machine is not waiting.
	if s.SessionNeedsYou(remoteSID) || !s.NeedsYouOn("devbox", remoteSID) {
		t.Errorf("needs-you keyed wrongly")
	}
	if again := s.ApplyRemote(remoteEvent(events.NeedsYou, "permission", "Bash")); again != nil {
		t.Errorf("the same wait made a second item")
	}
	if !s.ReleaseHost("devbox", "released to the terminal on devbox (link lost)") || it.State != Resolved {
		t.Errorf("not released: %v", it.State)
	}
	if s.Waiting() != 0 {
		t.Errorf("waiting = %d", s.Waiting())
	}
}

func TestApplyRemoteTurnEvents(t *testing.T) {
	s := New()
	s.ApplyRemote(remoteEvent(events.NeedsYou, "question", ""))
	s.ApplyRemote(remoteEvent(events.ToolFailed, "", "Bash"))
	for _, it := range s.Items() {
		if it.Kind == Question && it.State != Resolved {
			t.Errorf("a tool ran but the question is still open")
		}
	}
	done := s.ApplyRemote(remoteEvent(events.Done, "", ""))
	if done == nil || done.Kind != Done || done.Title != "Turn finished on devbox." {
		t.Errorf("done = %+v", done)
	}
	if s.ApplyRemote(events.New(events.NeedsYou, remoteSID)) != nil {
		t.Errorf("an event with no host was taken as remote")
	}
	st := s.AddStalledOn("devbox", remoteSID, 0)
	if st == nil || st.Host != "devbox" || s.AddStalledOn("devbox", remoteSID, 0) != nil {
		t.Errorf("stalled = %+v", st)
	}
}

// A host's events fill at most MaxRemoteOpen open items, all hosts
// together MaxRemoteOpenAll, and the drops are counted per host.
func TestApplyRemoteCaps(t *testing.T) {
	s := New()
	for h := range 6 {
		host := fmt.Sprintf("h%d", h)
		for i := range 40 {
			ev := events.New(events.NeedsYou, fmt.Sprintf("11111111-2222-4333-8444-%012d", i))
			ev.Request, ev.Host = "permission", host
			s.ApplyRemote(ev)
		}
	}
	open := map[string]int{}
	all := 0
	for _, it := range s.Items() {
		if it.State != Resolved {
			open[it.Host]++
			all++
		}
	}
	if all != MaxRemoteOpenAll || open["h0"] != MaxRemoteOpen || s.RemoteDropped("h0") != 40-MaxRemoteOpen {
		t.Errorf("open %v (all %d), h0 dropped %d", open, all, s.RemoteDropped("h0"))
	}
	if s.RemoteDropped("h5") != 40 {
		t.Errorf("h5 dropped %d, want all 40 past the total cap", s.RemoteDropped("h5"))
	}
	// Once the host's items are let go, it has room again.
	s.ReleaseHost("h0", "link lost")
	ev := events.New(events.Idle, "11111111-2222-4333-8444-000000000001")
	ev.Host = "h0"
	if s.ApplyRemote(ev) == nil {
		t.Errorf("no room after the host was released")
	}
}

// TestRemoteHeldQuestionStandsAlone: a question a host holds for Den is
// the only item for it, whichever comes first, the held request or the
// host's own "needs you" event, and an event that arrives after the
// question was answered adds nothing.
func TestRemoteHeldQuestionStandsAlone(t *testing.T) {
	held := func(s *Store) *Item {
		in, _ := json.Marshal(map[string]any{"session_id": remoteSID, "hook_event_name": "PermissionRequest", "tool_name": "AskUserQuestion", "tool_input": askInput})
		r := bridge.NewRemoteRequest("devbox", "0123456789abcdef0123456789abcdef", bridge.Envelope{Event: "PermissionRequest", SessionID: remoteSID, Payload: in}, func(bridge.Decision) {})
		return s.Apply(r)
	}
	soft := func(s *Store) { s.ApplyRemote(remoteEvent(events.NeedsYou, "question", "")) }
	open := func(s *Store) (n int, titles []string) {
		for _, it := range s.Items() {
			if it.Host == "devbox" && it.State != Resolved {
				n++
				titles = append(titles, it.Title)
			}
		}
		return n, titles
	}
	for _, c := range []struct {
		name  string
		steps func(s *Store) *Item
	}{
		{"event first", func(s *Store) *Item { soft(s); return held(s) }},
		{"held first", func(s *Store) *Item { it := held(s); soft(s); return it }},
	} {
		s := New()
		s.AnswerQuestions(true)
		it := c.steps(s)
		if it == nil || !it.AsksInDen() {
			t.Fatalf("%s: no held question: %+v", c.name, it)
		}
		if n, titles := open(s); n != 1 {
			t.Errorf("%s: %d open items, want only the held question: %q", c.name, n, titles)
		}
	}
	// Answered in Den before the host's event arrives.
	s := New()
	s.AnswerQuestions(true)
	it := held(s)
	s.AnswerQuestion(it, "2")
	s.AnswerQuestion(it, "1 3")
	soft(s)
	if n, titles := open(s); n != 0 {
		t.Errorf("answered, then the late event: %d open items: %q", n, titles)
	}
	// A new turn starts clean: a question the host does not hold for Den
	// (it went to its terminal) shows again.
	s.ApplyRemote(remoteEvent(events.TurnStart, "", ""))
	soft(s)
	if n, _ := open(s); n != 1 {
		t.Errorf("a question in the next turn: %d open items, want 1", n)
	}
	// Later in the same turn, a question the host could not hold shows:
	// only a notice soon after a held one is taken to be the same.
	old := heldQuiet
	t.Cleanup(func() { heldQuiet = old })
	s = New()
	s.AnswerQuestions(true)
	it = held(s)
	s.AnswerQuestion(it, "2")
	s.AnswerQuestion(it, "1 3")
	heldQuiet = 0
	soft(s)
	if n, _ := open(s); n != 1 {
		t.Errorf("a later question in the same turn: %d open items, want 1", n)
	}
	// A host whose link went down forgets its held questions.
	heldQuiet = time.Hour
	s = New()
	s.AnswerQuestions(true)
	held(s)
	s.ReleaseHost("devbox", "the link to devbox was lost")
	if len(s.heldQ) != 0 {
		t.Errorf("held questions kept after the host was released: %v", s.heldQ)
	}
}
