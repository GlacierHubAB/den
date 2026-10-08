package ui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/orch"
	"github.com/GlacierHubAB/den/internal/theme"
)

func TestOrchestratorShowsSpawnCalls(t *testing.T) {
	st := newStyles(theme.Deepharness)
	m := Model{st: st, agents: sample(), store: inbox.New(), tab: tabOrch, width: 120, height: 30}
	m.input = newInput(st)
	m.orch.chat = append(m.orch.chat, chatEntry{role: "you", text: "start an agent on the site", at: time.Now()})
	in, _ := json.Marshal(map[string]string{"dir": "/srv/site", "brief": "Fix the login redirect."})
	m.handleOrchEvent(orch.Event{Kind: orch.ToolUse, Tool: orch.SpawnAgentTool, ToolUseID: "s1", Input: in})
	m.handleOrchEvent(orch.Event{Kind: orch.ToolResult, ToolUseID: "s1", Text: "Started Fuzzy Koala (peer \"site-1a2b\") in /srv/site on workspace 2."})
	m.handleOrchEvent(orch.Event{Kind: orch.ToolUse, Tool: orch.OpenTerminalTool, ToolUseID: "s2", Input: in})
	m.handleOrchEvent(orch.Event{Kind: orch.ToolResult, ToolUseID: "s2", IsError: true, Text: "no such directory: /srv/site"})
	plain := ansi.Strip(m.View().Content)
	for _, want := range []string{"▶ starting an agent in /srv/site · Fix the login redirect.", "Started Fuzzy Koala", "▶ opening a terminal in /srv/site", "failed: no such directory"} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q in:\n%s", want, plain)
		}
	}
}
