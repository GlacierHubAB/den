package inbox

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
)

func TestPermissionFlowThroughRealSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "den.sock")
	srv, err := bridge.Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	store := New()

	payload, _ := json.Marshal(map[string]any{"session_id": "s1", "hook_event_name": "PermissionRequest", "tool_name": "Bash", "tool_input": map[string]string{"command": "gh pr create --fill"}})
	results := make(chan bridge.Decision, 2)
	for i := 0; i < 2; i++ {
		go func() {
			d, _ := bridge.Ask(path, bridge.Envelope{Event: "PermissionRequest", SessionID: "s1", Payload: payload}, 3*time.Second)
			results <- d
		}()
		req := <-srv.Requests()
		it := store.Apply(req)
		if i == 0 {
			if it == nil || !it.Answerable() || it.Kind != Permission || it.Body[0] != "$ gh pr create --fill" {
				t.Fatalf("first item = %+v", it)
			}
			if store.Waiting() != 1 || !store.SessionNeedsYou("s1") {
				t.Errorf("waiting = %d", store.Waiting())
			}
			if !store.Answer(it, "always") {
				t.Fatal("answer failed")
			}
		} else if it != nil {
			t.Errorf("second request should be auto-allowed by the always rule, got item %+v", it)
		}
		if d := <-results; d.Behavior != "allow" {
			t.Errorf("decision %d = %+v", i, d)
		}
	}
	if store.Waiting() != 0 {
		t.Errorf("waiting after answers = %d", store.Waiting())
	}
}

func TestNoticesAndResolution(t *testing.T) {
	store := New()
	mk := func(event string, in map[string]any) *bridge.Request {
		in["session_id"] = "s2"
		in["hook_event_name"] = event
		p, _ := json.Marshal(in)
		r := &bridge.Request{Envelope: bridge.Envelope{Event: event, SessionID: "s2", Payload: p}}
		json.Unmarshal(p, &r.Input)
		return r
	}
	if it := store.Apply(mk("Notification", map[string]any{"notification_type": "permission_prompt", "message": "Claude needs your permission to use Bash"})); it == nil || it.State != InTerminal {
		t.Fatalf("soft permission = %+v", it)
	}
	if store.Apply(mk("Notification", map[string]any{"notification_type": "permission_prompt"})) != nil {
		t.Error("duplicate permission notice should be ignored")
	}
	q := store.Apply(mk("PreToolUse", map[string]any{"tool_name": "AskUserQuestion", "tool_input": map[string]any{"questions": []map[string]any{{"question": "Which key?", "options": []map[string]string{{"label": "Super+M"}, {"label": "Alt+Space"}}}}}}))
	if q == nil || q.Kind != Question || q.Title != "Which key?" || len(q.Options) != 2 {
		t.Fatalf("question = %+v", q)
	}
	if store.Waiting() != 2 {
		t.Errorf("waiting = %d", store.Waiting())
	}
	store.Apply(mk("PostToolUse", map[string]any{"tool_name": "AskUserQuestion"}))
	if q.State != Resolved {
		t.Error("question should resolve on PostToolUse")
	}
	done := store.Apply(mk("Stop", map[string]any{"last_assistant_message": "Pushed the repo.\n\nOpened issue #10463."}))
	if done == nil || done.Kind != Done || done.Title != "Pushed the repo." || len(done.Body) != 1 {
		t.Fatalf("done = %+v", done)
	}
	if store.Waiting() != 0 {
		t.Errorf("waiting after stop = %d", store.Waiting())
	}
	store.Dismiss(done)
	for _, it := range store.Items() {
		if it == done {
			t.Error("dismiss did not remove the item")
		}
	}
}
