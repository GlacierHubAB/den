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

func TestOrchestratorTabRendersConversation(t *testing.T) {
	st := newStyles(theme.Deepharness)
	m := Model{st: st, agents: sample(), store: inbox.New(), tab: tabOrch, width: 120, height: 30}
	m.input = newInput(st)
	m.agents[0].Session.Name = "grvlle-45"
	m.orch.chat = append(m.orch.chat, chatEntry{role: "you", text: "what is everyone blocked on?", at: time.Now()})
	m.handleOrchEvent(orch.Event{Kind: orch.Text, Text: "Mossy Lemur is waiting on a permission for gh pr create."})
	in, _ := json.Marshal(map[string]string{"to": "grvlle-45", "message": "Rebase on main first.\nThen open the PR."})
	m.handleOrchEvent(orch.Event{Kind: orch.ToolUse, Tool: "SendMessage", ToolUseID: "t1", Input: in})
	m.handleOrchEvent(orch.Event{Kind: orch.ToolResult, ToolUseID: "t1", Text: "Sent."})
	m.handleOrchEvent(orch.Event{Kind: orch.Result, Text: "ignored duplicate", CostUSD: 0.05})
	out := m.View().Content
	plain := ansi.Strip(out)
	for _, want := range []string{"orchestrator ›", "Mossy Lemur is waiting", "→ Mossy Lemur", "delivered", "$0.05"} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q in:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "ignored duplicate") {
		t.Error("result text should not be added when a text block was already shown")
	}
	for i, l := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(l); w > 120 {
			t.Errorf("line %d too wide: %d", i, w)
		}
	}
	r := m.roster()
	if len(r) != 4 || r[0].Peer != "grvlle-45" {
		t.Errorf("roster = %+v", r)
	}
}

func TestOrchestratorStreamsAndFormats(t *testing.T) {
	st := newStyles(theme.Deepharness)
	m := Model{st: st, agents: sample(), store: inbox.New(), tab: tabOrch, width: 120, height: 30}
	m.input = newInput(st)
	m.orch.thinking = true
	m.handleOrchEvent(orch.Event{Kind: orch.Delta, Text: "## Plan\n\nRun `go te"})
	if m.orch.thinking {
		t.Error("first delta should end the thinking indicator")
	}
	m.handleOrchEvent(orch.Event{Kind: orch.Delta, Text: "st` now."})
	out := ansi.Strip(m.View().Content)
	if !strings.Contains(out, "Run go test now.▍") {
		t.Errorf("streamed text with cursor missing:\n%s", out)
	}
	if strings.Contains(out, "## Plan") || !strings.Contains(out, "Plan") {
		t.Errorf("heading should render without hashes:\n%s", out)
	}
	m.handleOrchEvent(orch.Event{Kind: orch.Text, Text: "## Plan\n\nRun `go test` now.\n\n```sh\ngo test ./...\n```"})
	if n := len(m.orch.chat); n != 1 || m.orch.chat[0].streaming {
		t.Errorf("final text should replace the streamed entry: %+v", m.orch.chat)
	}
	out = ansi.Strip(m.View().Content)
	if strings.Contains(out, "▍") || !strings.Contains(out, "┃ go test ./...") {
		t.Errorf("final render wrong:\n%s", out)
	}
}
