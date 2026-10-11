package inbox

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDescribeToolCarriesTheWholeRequest(t *testing.T) {
	long := `S=/tmp/claude-1000/-home-user/11111111/scratchpad/ser.py; python3 $S "y\r" 8 | grep -v '^\s*$' | head -8`
	raw, _ := json.Marshal(map[string]any{"command": long + "\necho done", "description": "Open the password reset option on the router menu"})
	title, intent, body := describeTool("Bash", raw)
	if title != "Wants to run" || intent != "Open the password reset option on the router menu" {
		t.Errorf("bash: title=%q intent=%q", title, intent)
	}
	if len(body) != 2 || body[0] != "$ "+long || body[1] != "  echo done" {
		t.Errorf("bash body = %q", body)
	}

	oldS := strings.Repeat("old line\n", 25)
	newS := strings.Repeat("new line\n", 5)
	raw, _ = json.Marshal(map[string]any{"file_path": "a.go", "old_string": oldS, "new_string": newS})
	title, _, body = describeTool("Edit", raw)
	if title != "Wants to edit a.go" || len(body) != 20+1+5 || body[20] != "- … 5 more lines" || body[21] != "+ new line" {
		t.Errorf("edit: title=%q body=%d %q", title, len(body), body[20])
	}

	raw, _ = json.Marshal(map[string]any{"name": "fleet-1", "brief": "ship it"})
	title, _, body = describeTool("mcp__den__spawn_agent", raw)
	if title != "Wants to call spawn_agent on den" || len(body) != 4 || body[1] != `  "brief": "ship it",` {
		t.Errorf("mcp: title=%q body=%q", title, body)
	}

	raw, _ = json.Marshal(map[string]any{"query": "octant unicode"})
	if title, _, body = describeTool("WebSearch", raw); title != "Wants to search the web" || body[0] != "octant unicode" {
		t.Errorf("search: %q %q", title, body)
	}
}
