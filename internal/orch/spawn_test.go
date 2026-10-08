package orch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPConfigAndDescribe(t *testing.T) {
	cfg := MCPConfig("/usr/local/bin/den")
	if !strings.Contains(cfg, `"den":{"args":["mcp"],"command":"/usr/local/bin/den"}`) {
		t.Errorf("mcp config = %s", cfg)
	}
	home, _ := os.UserHomeDir()
	in, _ := json.Marshal(map[string]string{"dir": home + "/code/site", "brief": "Fix the login redirect.\nThen run the tests."})
	if got := Describe(SpawnAgentTool, in); got != "▶ starting an agent in ~/code/site · Fix the login redirect. …" {
		t.Errorf("spawn = %q", got)
	}
	if got := Describe(OpenTerminalTool, in); got != "▶ opening a terminal in ~/code/site" {
		t.Errorf("terminal = %q", got)
	}
	if got := Describe("ListAgents", nil); got != "· ListAgents" {
		t.Errorf("other = %q", got)
	}
}

func TestStartOffersMCPOnlyWhenSpawning(t *testing.T) {
	dir := t.TempDir()
	// The fake records its arguments, then behaves as before.
	script := filepath.Join(dir, "claude")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"$@\" > \"$0.args\"\n"+strings.TrimPrefix(fakeClaude, "#!/bin/sh\n")), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, spawn := range []bool{false, true} {
		o, err := Start(context.Background(), Options{Command: []string{script}, Dir: dir, Spawn: spawn, Executable: "/opt/den"})
		if err != nil {
			t.Fatal(err)
		}
		o.Close()
		args, _ := os.ReadFile(script + ".args")
		hasMCP := strings.Contains(string(args), "--mcp-config") && strings.Contains(string(args), "--strict-mcp-config") && strings.Contains(string(args), SpawnAgentTool)
		if hasMCP != spawn {
			t.Errorf("spawn=%v: args = %s", spawn, args)
		}
		if !strings.Contains(string(args), "--tools ListAgents,SendMessage ") {
			t.Errorf("built-in tools must stay restricted: %s", args)
		}
	}
}
