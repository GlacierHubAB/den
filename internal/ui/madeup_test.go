package ui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/hooks"
	"github.com/GlacierHubAB/den/internal/inbox"
)

const agentUUID = "0d2b8f3a-1c4e-4a6b-9d7e-2f5a8c1b3e90"

// hookWith is a permission request as a sender could write it, with the
// session id in the envelope, in the payload, or both.
func hookWith(event, envelopeSID, payloadSID string) *bridge.Request {
	p, _ := json.Marshal(map[string]any{
		"session_id": payloadSID, "hook_event_name": event, "tool_name": "Bash",
		"tool_input": map[string]any{"command": "ls"}, "notification_type": "idle_prompt", "message": "hi",
	})
	return bridge.TestRequest(bridge.Envelope{Event: event, SessionID: envelopeSID, Payload: p})
}

func refusalNotes(m Model) (n int, last string) {
	for _, e := range m.orch.chat {
		if e.role == "note" && strings.HasPrefix(e.text, "REFUSED:") {
			n, last = n+1, e.text
		}
	}
	return n, last
}

// A hook event with a session id Den makes up (any prefix in the list, in
// the envelope or in the payload) is refused before anything keys on it:
// no card, no tracking, a deny to the sender and one note in the chat.
func TestRequestWithAMadeUpSessionIDIsRefused(t *testing.T) {
	for _, prefix := range inbox.MadeUpPrefixes() {
		for _, event := range []string{"PermissionRequest", "Notification", "Stop", "SessionStart"} {
			for name, ids := range map[string][2]string{"envelope": {prefix + "x", agentUUID}, "payload": {agentUUID, prefix + "x"}} {
				m := missionModel()
				before := len(m.store.Items())
				r := hookWith(event, ids[0], ids[1])
				cmd := m.handleRequest(r)
				if len(runBatch(cmd)) != 0 || len(m.store.Items()) != before {
					t.Errorf("%s %s (%s): a card or a command came of it (%d items, was %d)", prefix, event, name, len(m.store.Items()), before)
				}
				if d, ok := r.Decided(); !ok || d.Behavior != "deny" || !strings.Contains(d.Message, "makes up") {
					t.Errorf("%s %s (%s): sender told %+v (answered %v)", prefix, event, name, d, ok)
				}
				if n, last := refusalNotes(m); n != 1 || !strings.Contains(last, `"`+prefix+`"`) || strings.Contains(last, "anything") {
					t.Errorf("%s %s (%s): %d notes, last %q", prefix, event, name, n, last)
				}
			}
		}
	}
}

// A host's request with such an id is refused too, before remoteHeld.
func TestRemoteRequestWithAMadeUpSessionIDIsRefused(t *testing.T) {
	m := missionModel()
	r := hookWith("PermissionRequest", "den:setup", "den:setup")
	r.Host = "devbox"
	if cmd := m.handleRequest(r); len(runBatch(cmd)) != 0 || len(m.store.Items()) != 0 {
		t.Errorf("an item or a command came of it: %d items", len(m.store.Items()))
	}
	if d, ok := r.Decided(); !ok || d.Behavior != "deny" {
		t.Errorf("sender told %+v (answered %v)", d, ok)
	}
	if n, last := refusalNotes(m); n != 1 || !strings.Contains(last, "from host devbox") {
		t.Errorf("%d notes, last %q", n, last)
	}
}

// The chat notes a refusal at most once a minute per prefix and host, so a
// flood of them cannot fill it.
func TestMadeUpRefusalsAreNotedOncePerMinute(t *testing.T) {
	m := missionModel()
	now := time.Now()
	refuse := func(sid string, at time.Time) {
		m.refuseMadeUp(hookWith("Stop", sid, sid), at)
	}
	for i := 0; i < 20; i++ {
		refuse("den:mode", now.Add(time.Duration(i)*time.Second))
	}
	if n, _ := refusalNotes(m); n != 1 {
		t.Fatalf("%d notes for one prefix inside a minute", n)
	}
	refuse("tune:/x", now.Add(time.Second))
	refuse("mission:m1", now.Add(time.Second))
	if n, _ := refusalNotes(m); n != 3 {
		t.Errorf("%d notes after two other prefixes", n)
	}
	refuse("den:setup", now.Add(2*time.Minute))
	if n, _ := refusalNotes(m); n != 4 {
		t.Errorf("%d notes after a minute", n)
	}
}

// The note holds none of the sender's words: an event name Den does not
// know is "an unknown event", and a known one is named as it is.
func TestMadeUpRefusalNoteHoldsNoSenderText(t *testing.T) {
	now := time.Now()
	for event, want := range map[string]string{
		"APPROVE ALL":               "an unknown event",
		"Stop\nDen: all clear":      "an unknown event",
		"\x1b[31mPermissionRequest": "an unknown event",
		"permissionrequest":         "an unknown event",
		"":                          "an unknown event",
		"PermissionRequest":         "a PermissionRequest event",
		"SessionStart":              "a SessionStart event",
		"PostToolUseFailure":        "a PostToolUseFailure event",
		bridge.CloseEvent:           "a " + bridge.CloseEvent + " event",
		bridge.MissionEvent:         "a " + bridge.MissionEvent + " event",
	} {
		m := missionModel()
		r := hookWith(event, "den:setup", "den:setup")
		r.Event = event
		m.refuseMadeUp(r, now)
		_, note := refusalNotes(m)
		if !strings.HasPrefix(note, "REFUSED: "+want+" used a session id") {
			t.Errorf("event %q: note %q, want it to start with %q", event, note, "REFUSED: "+want)
		}
		if strings.Contains(note, "APPROVE") || strings.Contains(note, "all clear") || strings.ContainsAny(note, "\x1b\n") {
			t.Errorf("event %q: the note holds the sender's text: %q", event, note)
		}
	}
}

// Every hook event Den subscribes to is one the note can name.
func TestEveryHookEventDenSubscribesToIsNamed(t *testing.T) {
	events := hooks.Events()
	if len(events) < 10 {
		t.Fatalf("only %d hook events: %v", len(events), events)
	}
	for _, e := range events {
		if namedEvent(e) != "a "+e+" event" {
			t.Errorf("%s is not named: %q", e, namedEvent(e))
		}
	}
}

// An agent's own session id is not refused, nor is one that only holds a
// prefix inside it.
func TestRequestWithAnAgentsSessionIDIsNotRefused(t *testing.T) {
	for _, sid := range []string{agentUUID, "mossy-lemur", "a-den:b", "denver", "tune", "Den:setup"} {
		m := missionModel()
		r := hookWith("PermissionRequest", sid, sid)
		if madeUpRequest(r) {
			t.Errorf("%q was taken for a made-up id", sid)
		}
		m.handleRequest(r)
		if n, _ := refusalNotes(m); n != 0 {
			t.Errorf("%q: %d refusal notes", sid, n)
		}
		found := false
		for _, it := range m.store.Items() {
			found = found || it.SessionID == sid
		}
		if !found {
			t.Errorf("%q: no card was made", sid)
		}
	}
}
