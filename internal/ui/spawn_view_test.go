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
	m.agents[2].Session.Name = "site-1a2b" // Fuzzy Sprite is the session the spawn starts
	m.input = newInput(st)
	m.orch.chat = append(m.orch.chat, chatEntry{role: "you", text: "start an agent on the site", at: time.Now()})
	in, _ := json.Marshal(map[string]string{"dir": "/srv/site", "brief": "Fix the login redirect.", "model": "sonnet"})
	m.handleOrchEvent(orch.Event{Kind: orch.ToolUse, Tool: orch.SpawnAgentTool, ToolUseID: "s1", Input: in})
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "▶ starting an agent in /srv/site on sonnet · Fix the login redirect.") {
		t.Errorf("spawn note:\n%s", plain)
	}
	m.handleOrchEvent(orch.Event{Kind: orch.ToolResult, ToolUseID: "s1", Text: "Started Fuzzy Sprite (peer \"site-1a2b\") in /srv/site on workspace 2 on sonnet."})
	if c := m.orch.chat[1]; c.role != "card" || c.card == nil || c.card.Agent != "Fuzzy Sprite" || c.card.Brief != "Fix the login redirect." {
		t.Fatalf("the spawn did not become Fuzzy's card: %+v", c)
	}
	m.handleOrchEvent(orch.Event{Kind: orch.ToolUse, Tool: orch.OpenTerminalTool, ToolUseID: "s2", Input: in})
	m.handleOrchEvent(orch.Event{Kind: orch.ToolResult, ToolUseID: "s2", IsError: true, Text: "no such directory: /srv/site"})
	plain := ansi.Strip(m.View().Content)
	for _, want := range []string{"Fuzzy Sprite", "▸ Fix the login redirect.", "▶ opening a terminal in /srv/site", "failed: no such directory"} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q in:\n%s", want, plain)
		}
	}
}
