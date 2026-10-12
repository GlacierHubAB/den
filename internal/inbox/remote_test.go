package inbox

import (
	"testing"

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
