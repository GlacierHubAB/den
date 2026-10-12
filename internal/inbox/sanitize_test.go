package inbox

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/bridge"
)

// evil would write the clipboard, clear the screen, retitle the window,
// overwrite the line and flip the reading order if printed raw.
const evil = "\x1b]52;c;SGVsbG8=\x07\x1b[2J\x1b[H\x1b]0;pwned\x07\r\u202e\u009b2J"

// injected reports a control character that reached s. An MCP call's
// arguments show as JSON, which spells a control character out as \u001b:
// that is visible text, not a sequence.
func injected(s string) bool {
	return strings.ContainsAny(s, "\x1b\x07\r\u202e\u009b")
}

func hookRequest(event string, in map[string]any) *bridge.Request {
	in["session_id"] = "s1"
	in["hook_event_name"] = event
	p, _ := json.Marshal(in)
	r := &bridge.Request{Envelope: bridge.Envelope{Event: event, SessionID: "s1", Payload: p}}
	json.Unmarshal(p, &r.Input)
	return r
}

func shown(it *Item) []string {
	out := []string{it.Tool, it.Title, it.Intent, it.Resolved, it.Role, it.Task}
	out = append(out, it.Body...)
	return append(out, it.Options...)
}

func TestHookTextIsSanitised(t *testing.T) {
	store := New()
	store.ToolFailureLimit(1)
	reqs := []*bridge.Request{
		hookRequest("PermissionRequest", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "echo hi " + evil + "\nsecond line", "description": "say hi" + evil}}),
		hookRequest("PermissionRequest", map[string]any{"tool_name": "Edit", "tool_input": map[string]any{"file_path": "/x/a" + evil + ".go", "old_string": "a" + evil, "new_string": "b" + evil}}),
		hookRequest("PermissionRequest", map[string]any{"tool_name": "Write", "tool_input": map[string]any{"file_path": "/x/b.go", "content": "one" + evil + "two"}}),
		hookRequest("PermissionRequest", map[string]any{"tool_name": "ExitPlanMode", "tool_input": map[string]any{"plan": "# Plan" + evil + "\n1. step"}}),
		hookRequest("PermissionRequest", map[string]any{"tool_name": "mcp__x" + evil + "__y", "tool_input": map[string]any{"arg": evil}}),
		hookRequest("PreToolUse", map[string]any{"tool_name": "AskUserQuestion", "tool_input": map[string]any{"questions": []any{map[string]any{"question": "Which?" + evil, "options": []any{map[string]any{"label": "one" + evil}, map[string]any{"label": "two"}}}}}}),
		hookRequest("Notification", map[string]any{"notification_type": "idle_prompt", "message": "idle" + evil}),
		hookRequest("PostToolUseFailure", map[string]any{"tool_name": "Ba" + evil + "sh"}),
		hookRequest("Stop", map[string]any{"last_assistant_message": "Done" + evil + " all of it. More."}),
	}
	for _, r := range reqs {
		it := store.Apply(r)
		if it == nil {
			t.Fatalf("%s made no item", r.Event)
		}
		for _, s := range shown(it) {
			if injected(s) {
				t.Errorf("%s: injected sequence survived in %q", r.Event, s)
			}
		}
	}
	var bash *Item
	for _, it := range store.Items() {
		if it.Tool == "Bash" && it.Kind == Permission {
			bash = it
		}
	}
	if bash.Body[0] != "$ echo hi " || bash.Body[1] != "  " || bash.Body[2] != "  second line" || bash.Intent != "say hi" {
		t.Errorf("bash item = %q intent %q", bash.Body, bash.Intent)
	}
	for _, it := range store.Items() {
		if it.Kind == Question && (it.Title != "Which?" || it.Options[0] != "one") {
			t.Errorf("question = %q %q", it.Title, it.Options)
		}
	}

	// Den's own items and trailers, and history read back from disk.
	it := store.AddClose("s1", "Close peer"+evil+"?", []string{"body" + evil}, nil)
	store.SetStatus(it, "sent"+evil, false)
	old := &Item{Title: "old" + evil, Body: []string{evil}}
	store.Restore([]*Item{old})
	for _, it := range []*Item{it, old} {
		for _, s := range shown(it) {
			if injected(s) {
				t.Errorf("injected sequence survived in %q", s)
			}
		}
	}
}

// TestLongCommand makes sure a megabyte-long single-line command is taken
// in quickly and whole.
func TestLongCommand(t *testing.T) {
	cmd := strings.Repeat("a", 1<<20) + evil
	it := New().Apply(hookRequest("PermissionRequest", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": cmd}}))
	if it == nil || len(it.Body) != 1 || len(it.Body[0]) != 2+1<<20 || injected(it.Body[0]) {
		t.Fatalf("body has %d lines", len(it.Body))
	}
}
