package remote

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/bridge"
)

func bodyOf(t *testing.T, tool string, input any) (string, bool) {
	t.Helper()
	raw, _ := json.Marshal(input)
	b, _, ok := heldBody(bridge.HookInput{ToolName: tool, ToolInput: raw})
	if ok && !checkBody(tool, b) {
		t.Errorf("%s: Den refuses what the host made: %s", tool, b)
	}
	return string(b), ok
}

// The human never approves what they cannot see: a request is relayed
// only when everything that decides what it does is in its body.
func TestHeldBodyShowsAllThatDecides(t *testing.T) {
	for _, c := range []struct {
		why   string
		tool  string
		input any
		want  string // "" refused
	}{
		{"bash", "Bash", map[string]any{"command": "go test ./...", "description": "Run tests", "timeout": 60000}, `{"command":"go test ./...","description":"Run tests"}`},
		{"bash without a command", "Bash", map[string]any{"cmd": "rm -rf /"}, ""},
		{"bash command not a string", "Bash", map[string]any{"command": []string{"rm", "-rf", "/"}}, ""},
		{"bash sandbox off is shown", "Bash", map[string]any{"command": "ls", "dangerouslyDisableSandbox": true}, `{"command":"ls","dangerouslyDisableSandbox":true}`},
		{"bash unknown key", "Bash", map[string]any{"command": "ls", "shell": "/bin/evil"}, ""},
		{"bash over 16 KB", "Bash", map[string]any{"command": strings.Repeat("a", exactMax+1)}, ""},
		{"edit replace_all is shown", "Edit", map[string]any{"file_path": "/p/a.go", "old_string": "a", "new_string": "b", "replace_all": true}, `{"file_path":"/p/a.go","new_string":"b","old_string":"a","replace_all":true}`},
		{"edit without a path", "Edit", map[string]any{"old_string": "a", "new_string": "b"}, ""},
		{"multiedit edits are shown", "MultiEdit", map[string]any{"file_path": "/p/a.go", "edits": []any{map[string]any{"old_string": "a", "new_string": "b", "replace_all": true}}},
			`{"edits":[{"new_string":"b","old_string":"a","replace_all":true}],"file_path":"/p/a.go"}`},
		{"multiedit edit with an unknown key", "MultiEdit", map[string]any{"file_path": "/p/a.go", "edits": []any{map[string]any{"old_string": "a", "new_string": "b", "x": 1}}}, ""},
		{"notebook edit mode is shown", "NotebookEdit", map[string]any{"notebook_path": "/p/n.ipynb", "new_source": "x", "edit_mode": "delete", "cell_id": "c1"}, `{"cell_id":"c1","edit_mode":"delete","new_source":"x","notebook_path":"/p/n.ipynb"}`},
		{"agent type is shown", "Agent", map[string]any{"description": "d", "prompt": "p", "subagent_type": "general-purpose", "run_in_background": true}, `{"description":"d","prompt":"p","subagent_type":"general-purpose"}`},
		{"plan's allowed prompts are shown", "ExitPlanMode", map[string]any{"plan": "1. x", "allowedPrompts": []any{map[string]any{"tool": "Bash", "prompt": "run tests"}}},
			`{"allowedPrompts":[{"prompt":"run tests","tool":"Bash"}],"plan":"1. x"}`},
		{"read with quiet keys", "Read", map[string]any{"file_path": "/p/a", "offset": 10, "limit": 5}, `{"file_path":"/p/a"}`},
		{"mcp args in full", "mcp__srv__do", map[string]any{"a": 1, "b": "two"}, `{"a":1,"b":"two"}`},
		{"mcp args too many lines to show", "mcp__srv__do", map[string]any{"list": make([]any, 60)}, ""},
		{"escape in a path", "Read", map[string]any{"file_path": "/p/\x1b[2Ka"}, ""},
	} {
		got, ok := bodyOf(t, c.tool, c.input)
		if c.want == "" {
			if ok {
				t.Errorf("%s: relayed %s", c.why, got)
			}
			continue
		}
		if !ok || got != c.want {
			t.Errorf("%s: got %s (%v), want %s", c.why, got, ok, c.want)
		}
	}
}

// A content field is cut on the host and says so; Den accepts the cut
// text and nothing longer.
func TestContentIsCutAndCheckedAgainstItsCaps(t *testing.T) {
	long := strings.Repeat("line\n", contentLines+50)
	got, ok := bodyOf(t, "Write", map[string]any{"file_path": "/p/a", "content": long})
	if !ok || !strings.Contains(got, "cut on the host") {
		t.Fatalf("write: %v %.80s", ok, got)
	}
	for _, c := range []struct {
		why  string
		body map[string]any
	}{
		{"too many lines", map[string]any{"file_path": "/p/a", "content": long}},
		{"too many bytes", map[string]any{"file_path": "/p/a", "content": strings.Repeat("a", contentMax+len(cutMark)+1)}},
		{"a huge path", map[string]any{"file_path": strings.Repeat("a", 200<<10), "content": "x"}},
		{"no path", map[string]any{"content": "x"}},
		{"an unknown key", map[string]any{"file_path": "/p/a", "content": "x", "mode": "0777"}},
		{"an escape in content", map[string]any{"file_path": "/p/a", "content": "\x1b]52;c;aGk=\x07"}},
	} {
		raw, _ := json.Marshal(c.body)
		if checkBody("Write", raw) {
			t.Errorf("Den accepted a Write body with %s", c.why)
		}
	}
	huge, _ := json.Marshal(map[string]any{"command": strings.Repeat("a", 200<<10)})
	if checkBody("Bash", huge) {
		t.Error("Den accepted a 200 KB command")
	}
}
