package inbox

import (
	"encoding/json"
	"path/filepath"
	"strings"
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
	again := hostHook("", "PermissionRequest", bash("go test ./..."))
	if it := s.Apply(again); it == nil {
		t.Fatal("the remote host's always-allow answered a local request")
	}
	if _, ok := again.Decided(); ok {
		t.Error("the local request was answered without the human")
	}
	same := hostHook("devbox", "PermissionRequest", bash("go test ./..."))
	if it := s.Apply(same); it != nil {
		t.Fatalf("always-allow on devbox did not cover devbox: %+v", it)
	}
	if d, ok := same.Decided(); !ok || d.Behavior != "allow" {
		t.Errorf("devbox decision = %+v, %v", d, ok)
	}
	other := hostHook("gpu", "PermissionRequest", bash("go test ./..."))
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

// A remote host's question, held for Den, is answered here; the answers go
// back through the request's own way, and the item says which host.
func TestRemoteQuestionAnsweredInDen(t *testing.T) {
	s := New()
	s.AnswerQuestions(true)
	var sent []bridge.Decision
	in, _ := json.Marshal(map[string]any{"session_id": sid, "hook_event_name": "PermissionRequest", "tool_name": "AskUserQuestion", "tool_input": askInput})
	r := bridge.NewRemoteRequest("devbox", "0123456789abcdef0123456789abcdef", bridge.Envelope{Event: "PermissionRequest", SessionID: sid, Payload: in},
		func(d bridge.Decision) { sent = append(sent, d) })
	it := s.Apply(r)
	if it == nil || !it.AsksInDen() || it.Host != "devbox" || !strings.Contains(it.Intent, "on devbox") {
		t.Fatalf("item = %+v", it)
	}
	s.AnswerQuestion(it, "2")
	if done, ok := s.AnswerQuestion(it, "1 3"); !done || !ok {
		t.Fatalf("answering: done %v ok %v", done, ok)
	}
	if len(sent) != 1 || sent[0].Behavior != "allow" || sent[0].Answers["Which colour scheme?"] != "Cool, calm" {
		t.Errorf("sent %+v", sent)
	}
	// Offered again after a reconnect under the same id: one item, not two.
	r2 := bridge.NewRemoteRequest("devbox", "fedcba9876543210fedcba9876543210", bridge.Envelope{Event: "PermissionRequest", SessionID: sid, Payload: in}, func(bridge.Decision) {})
	first := s.Apply(r2)
	r2.LetGo("released to the terminal on devbox (link lost)")
	s.LetGo(0)
	again := bridge.NewRemoteRequest("devbox", "fedcba9876543210fedcba9876543210", bridge.Envelope{Event: "PermissionRequest", SessionID: sid, Payload: in}, func(bridge.Decision) {})
	second := s.Apply(again)
	if first.Answerable() || !second.Answerable() || !second.At.Equal(first.At) {
		t.Errorf("re-offer: first answerable %v, second %v, at %v vs %v", first.Answerable(), second.Answerable(), first.At, second.At)
	}
	for _, it := range s.Items() {
		if it == first {
			t.Error("the item offered again is still listed twice")
		}
	}
}

// Flags that change what a call does are listed under its body; MultiEdit
// shows every edit.
func TestBodyListsWhatTheLayoutLeavesOut(t *testing.T) {
	s := New()
	it := s.Apply(hostHook("devbox", "PermissionRequest", map[string]any{"tool_name": "Bash",
		"tool_input": map[string]any{"command": "make", "dangerouslyDisableSandbox": true, "timeout": 5}}))
	body := strings.Join(it.Body, "\n")
	if !strings.Contains(body, "dangerouslyDisableSandbox: true") || strings.Contains(body, "timeout") {
		t.Errorf("bash body = %q", body)
	}
	it = s.Apply(hostHook("", "PermissionRequest", map[string]any{"tool_name": "MultiEdit",
		"tool_input": map[string]any{"file_path": "/p/a.go", "edits": []any{
			map[string]any{"old_string": "a", "new_string": "b"},
			map[string]any{"old_string": "c", "new_string": "d", "replace_all": true}}}}))
	body = strings.Join(it.Body, "\n")
	for _, want := range []string{"edit 1 of 2", "- a", "+ b", "edit 2 of 2 (replace_all: every occurrence)", "+ d"} {
		if !strings.Contains(body, want) {
			t.Errorf("multiedit body lacks %q:\n%s", want, body)
		}
	}
	it = s.Apply(hostHook("gpu", "PermissionRequest", map[string]any{"tool_name": "ExitPlanMode",
		"tool_input": map[string]any{"plan": "1. ship", "allowedPrompts": []any{map[string]any{"tool": "Bash", "prompt": "run tests"}}}}))
	if body = strings.Join(it.Body, "\n"); !strings.Contains(body, `allowedPrompts: [{"prompt":"run tests","tool":"Bash"}]`) {
		t.Errorf("plan body = %q", body)
	}
}
