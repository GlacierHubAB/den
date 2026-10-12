package inbox

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/GlacierHubAB/den/internal/bridge"
)

// hostHook is a hook request from session sid on host ("" for this machine).
func hostHook(host, event string, in map[string]any) *bridge.Request {
	in["session_id"] = sid
	in["hook_event_name"] = event
	p, _ := json.Marshal(in)
	env := bridge.Envelope{Event: event, SessionID: sid, Payload: p}
	var r *bridge.Request
	if event == "PermissionRequest" {
		r = bridge.TestRequest(env)
	} else {
		r = &bridge.Request{Envelope: env}
		json.Unmarshal(p, &r.Input)
	}
	r.Host = host
	return r
}

func bash(cmd string) map[string]any {
	return map[string]any{"tool_name": "Bash", "tool_input": map[string]string{"command": cmd}}
}

// The same session id on two hosts is two agents: their items, their
// "always" rules and their turn ends never touch each other.
func TestSessionsAreKeyedByHost(t *testing.T) {
	s := New()
	local := s.Apply(hostHook("", "PermissionRequest", bash("go test ./...")))
	remote := s.Apply(hostHook("devbox", "PermissionRequest", bash("go test ./...")))
	if local == nil || remote == nil || local.Host != "" || remote.Host != "devbox" {
		t.Fatalf("items = %+v, %+v", local, remote)
	}
	if !s.SessionNeedsYou(sid) || !s.NeedsYouOn("devbox", sid) || s.NeedsYouOn("gpu", sid) {
		t.Error("needs-you is not keyed by host")
	}

	// Always on the remote agent does not allow the local one's next call.
	if !s.Answer(remote, "always") {
		t.Fatal("answer failed")
	}
	again := hostHook("", "PermissionRequest", bash("go test ./internal"))
	if it := s.Apply(again); it == nil {
		t.Fatal("the remote host's always-allow answered a local request")
	}
	if _, ok := again.Decided(); ok {
		t.Error("the local request was answered without the human")
	}
	same := hostHook("devbox", "PermissionRequest", bash("go test ./cmd"))
	if it := s.Apply(same); it != nil {
		t.Fatalf("always-allow on devbox did not cover devbox: %+v", it)
	}
	if d, ok := same.Decided(); !ok || d.Behavior != "allow" {
		t.Errorf("devbox decision = %+v, %v", d, ok)
	}
	other := hostHook("gpu", "PermissionRequest", bash("go test ./cmd"))
	if it := s.Apply(other); it == nil {
		t.Fatal("always-allow on devbox answered the same session id on gpu")
	}

	// A turn ending on devbox resolves devbox's items only.
	s.Apply(hostHook("devbox", "Stop", map[string]any{"last_assistant_message": "Done."}))
	if local.State == Resolved {
		t.Error("devbox's Stop resolved the local item")
	}
	if !s.SessionNeedsYou(sid) || !s.NeedsYouOn("gpu", sid) {
		t.Error("devbox's Stop cleared another host's items")
	}
	if s.NeedsYouOn("devbox", sid) {
		t.Error("devbox's Stop left its own item waiting")
	}

	// Failure streaks are per host too.
	s.ToolFailureLimit(2)
	s.Apply(hostHook("devbox", "PostToolUseFailure", map[string]any{"tool_name": "Bash"}))
	if it := s.Apply(hostHook("", "PostToolUseFailure", map[string]any{"tool_name": "Bash"})); it != nil {
		t.Error("one failure on each host counted as a streak")
	}
}

func TestHistoryKeepsTheHost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	s := New()
	it := s.Apply(hostHook("devbox", "Stop", map[string]any{"last_assistant_message": "Shipped it."}))
	if err := AppendHistory(path, it); err != nil {
		t.Fatal(err)
	}
	local := s.Apply(hostHook("", "Stop", map[string]any{"last_assistant_message": "Local done."}))
	AppendHistory(path, local)
	got := LoadHistory(path, 10)
	if len(got) != 2 || got[0].Host != "devbox" || got[1].Host != "" {
		t.Fatalf("history = %+v", got)
	}
}
