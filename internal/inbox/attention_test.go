package inbox

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
)

func hook(session, event string, in map[string]any) *bridge.Request {
	in["session_id"] = session
	in["hook_event_name"] = event
	p, _ := json.Marshal(in)
	r := &bridge.Request{Envelope: bridge.Envelope{Event: event, SessionID: session, Payload: p}}
	json.Unmarshal(p, &r.Input)
	return r
}

func TestStopFailureIsFailedNotNeedsYou(t *testing.T) {
	store := New()
	store.Apply(hook("s1", "Notification", map[string]any{"notification_type": "permission_prompt"}))
	// The shape Claude Code 2.1.289 sends: "error", not the documented error_type.
	it := store.Apply(hook("s1", "StopFailure", map[string]any{"error": "rate_limit", "last_assistant_message": "API Error: secret detail"}))
	if it == nil || it.Kind != Failed || it.Title != "Turn ended: rate limited" {
		t.Fatalf("item = %+v", it)
	}
	if store.SessionNeedsYou("s1") || store.Waiting() != 0 {
		t.Error("a failure must never count as needs you, and it ends what was open")
	}
	if it := store.Apply(hook("s2", "StopFailure", map[string]any{"error_type": "server_error"})); it.Title != "Turn ended on an API error: server error" {
		t.Errorf("documented field: %q", it.Title)
	}
	if it := store.Apply(hook("s3", "StopFailure", map[string]any{"error": map[string]any{"odd": true}})); it.Title != "Turn ended on an API error" {
		t.Errorf("odd shape: %q", it.Title)
	}
	store.Apply(hook("s1", "UserPromptSubmit", map[string]any{}))
	if it.State != Resolved {
		t.Error("a new prompt resolves the failure")
	}
}

func TestToolFailureStreak(t *testing.T) {
	store := New()
	fail := func() *Item {
		return store.Apply(hook("s1", "PostToolUseFailure", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "cat secrets.txt"}, "tool_error": "exit 1"}))
	}
	if fail() != nil || fail() != nil {
		t.Fatal("two failures are normal work")
	}
	it := fail()
	if it == nil || it.Kind != Failed || it.Title != "3 tool calls failed in a row" || it.Tool != "Bash" || len(it.Body) != 0 {
		t.Fatalf("third = %+v", it)
	}
	if fail() != nil || it.Title != "4 tool calls failed in a row" {
		t.Errorf("the open item counts on: %q", it.Title)
	}
	if store.SessionNeedsYou("s1") {
		t.Error("failures are not needs you")
	}
	store.Apply(hook("s1", "PostToolUse", map[string]any{"tool_name": "Bash"}))
	if it.State != Resolved {
		t.Error("a working call resolves the streak")
	}
	if fail() != nil {
		t.Error("the streak starts over")
	}
	store.ToolFailureLimit(0)
	for i := 0; i < 5; i++ {
		if fail() != nil {
			t.Fatal("limit 0 turns it off")
		}
	}
}

func TestStalledResolvesOnAnyEvent(t *testing.T) {
	store := New()
	it := store.AddStalled("s1", 20*time.Minute)
	if it == nil || it.Kind != Stalled || it.Title != "Working with no sign of progress for 20 minutes" || store.AddStalled("s1", 25*time.Minute) != nil {
		t.Fatalf("stalled = %+v", it)
	}
	if store.SessionNeedsYou("s1") {
		t.Error("stalled is not needs you")
	}
	store.Apply(hook("s1", "PostToolUse", map[string]any{"tool_name": "Read"}))
	if it.State != Resolved {
		t.Error("a hook event ends the stall")
	}
	if one := store.AddStalled("s2", 90*time.Second); one.Title != "Working with no sign of progress for 1 minute" {
		t.Errorf("title = %q", one.Title)
	}
}

func TestKeepUnread(t *testing.T) {
	store := New()
	done := store.Apply(hook("s1", "Stop", map[string]any{"last_assistant_message": "Finished the demo."}))
	store.MarkSeen()
	if store.Unseen() != 0 {
		t.Fatal("seen")
	}
	if !store.KeepUnread(done) || store.Unseen() != 1 {
		t.Fatal("kept unread counts again")
	}
	store.MarkSeen()
	if store.Unseen() != 1 {
		t.Error("viewing the inbox does not clear a kept-unread item")
	}
	done.State, done.At = Resolved, time.Now().Add(-time.Hour)
	failed := store.Apply(hook("s2", "StopFailure", map[string]any{"error": "overloaded"}))
	failed.State, failed.At = Resolved, time.Now().Add(-time.Hour)
	store.KeepUnread(failed)
	store.Prune(time.Minute, 30)
	if len(store.Items()) != 2 {
		t.Error("pruning keeps kept-unread items")
	}
	store.MarkRead(done)
	if store.KeepUnread(failed) || store.Unseen() != 0 {
		t.Error("toggling and marking read clear it")
	}
}
