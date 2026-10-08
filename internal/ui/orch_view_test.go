package ui

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/orch"
	"github.com/GlacierHubAB/den/internal/theme"
	"github.com/GlacierHubAB/den/internal/transcript"
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
	for _, want := range []string{"orchestrator ›", "Mossy Lemur is waiting", "Mossy Lemur  CODING AGENT", "delivered", "Rebase on main first. Then open the PR.", "$0.05"} {
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

func TestDispatchCardLifecycle(t *testing.T) {
	st := newStyles(theme.Deepharness)
	m := Model{st: st, base: sample(), store: inbox.New(), tab: tabOrch, width: 120, height: 40, orch: orchState{cardCur: -1}}
	m.input = newInput(st)
	m.base[0].Session.Name = "grvlle-45"
	m.base[1].Session.Name = "grvlle-46"
	m.base[1].Session.SessionID = "fedcba9876543210"
	m.refresh() // hook handling refreshes from base, as the real model does
	sid := m.base[0].Session.SessionID
	send := func(id, to, text string) {
		in, _ := json.Marshal(map[string]string{"to": to, "message": text})
		m.handleOrchEvent(orch.Event{Kind: orch.ToolUse, Tool: "SendMessage", ToolUseID: id, Input: in})
		m.handleOrchEvent(orch.Event{Kind: orch.ToolResult, ToolUseID: id, Text: "Sent."})
	}
	send("t1", "grvlle-45", "Review the release notes.")
	cards := m.cards()
	if len(cards) != 1 || cards[0].State != cardQueued || cards[0].Reason != "delivered" {
		t.Fatalf("after send: %+v", cards)
	}
	// The agent picks it up and works.
	m.handleRequest(req("UserPromptSubmit", sid, map[string]any{"prompt": "Operator message via Den: Review the release notes."}))
	m.handleRequest(req("PostToolUse", sid, map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "go test ./...", "description": "Run the tests"}}))
	d := cards[0]
	if d.State != cardWorking || d.Calls != 1 || d.Ticker != "Bash  Run the tests" {
		t.Errorf("working: %+v", d)
	}
	out := ansi.Strip(m.View().Content)
	if !strings.Contains(out, "working") || !strings.Contains(out, "Bash  Run the tests") || !strings.Contains(out, "1 calls") {
		t.Errorf("working card not rendered:\n%s", out)
	}
	// A permission turns it amber, and answering it turns it back.
	m.handleRequest(req("Notification", sid, map[string]any{"notification_type": "permission_prompt", "message": "Claude needs your permission to use Bash"}))
	if d.State != cardNeedsYou {
		t.Errorf("needs you: %+v", d)
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "needs you") {
		t.Error("needs-you state not rendered")
	}
	m.handleRequest(req("PostToolUse", sid, map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": "/x/notes.md"}}))
	if d.State != cardWorking || d.Calls != 2 {
		t.Errorf("back to working: %+v", d)
	}
	// Two live cards show the in-flight strip.
	send("t2", "grvlle-46", "Check the installer.")
	out = ansi.Strip(m.View().Content)
	if !strings.Contains(out, "in flight") || !strings.Contains(out, "Snug Capybara") {
		t.Errorf("strip missing with two live cards:\n%s", out)
	}
	// The turn ends with the agent's own words.
	m.handleRequest(req("Stop", sid, map[string]any{"last_assistant_message": "Fixed the install path.\n\nEverything else matches."}))
	if d.State != cardDone || d.Result == "" || d.EndedAt.IsZero() {
		t.Errorf("done: %+v", d)
	}
	out = ansi.Strip(m.View().Content)
	if !strings.Contains(out, "✓ done") || !strings.Contains(out, "Fixed the install path.") {
		t.Errorf("done card not rendered:\n%s", out)
	}
	// Keys: up focuses the newest card, Enter expands, → opens the agent, ← returns.
	mm, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyUp})
	m = mm.(Model)
	if m.focusedCard() != m.cards()[1] {
		t.Fatalf("up should focus the last card, got %+v", m.focusedCard())
	}
	mm, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyUp})
	m = mm.(Model)
	if m.focusedCard() != d {
		t.Fatalf("second up should focus the first card")
	}
	mm, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if !d.Expanded || !strings.Contains(ansi.Strip(m.View().Content), "Everything else matches.") {
		t.Errorf("expand should show the agent's last message in full")
	}
	mm, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyRight})
	m = mm.(Model)
	if m.tab != tabFleet || m.agents[m.cursor].Session.SessionID != sid || m.focus != paneDetail {
		t.Errorf("right should open the agent in the Fleet view: tab=%v cursor=%d", m.tab, m.cursor)
	}
	mm, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyLeft})
	m = mm.(Model)
	if m.tab != tabOrch || m.focusedCard() != d {
		t.Errorf("left should come back to the card")
	}
	// The session ending fails whatever is still live.
	m.handleRequest(req("SessionEnd", "fedcba9876543210", map[string]any{}))
	if c := m.cards()[1]; c.State != cardFailed {
		t.Errorf("session end: %+v", c)
	}
}

func TestRosterTellsWhatAnAgentInHomeWorksOn(t *testing.T) {
	st := newStyles(theme.Deepharness)
	m := Model{st: st, agents: sample(), store: inbox.New()}
	home, _ := os.UserHomeDir()
	a := &m.agents[0]
	a.Title = "TUI agent orchestration platform"
	a.Tail = append(a.Tail, transcript.Entry{Kind: "tool", Tool: "Edit", Changes: []transcript.FileChange{{Path: home + "/code/den/internal/ui/view.go"}}})
	a.Tail = append(a.Tail, transcript.Entry{Kind: "text", Text: "Pushed as abc123.\nMore."})
	r := m.roster()[0]
	if len(r.Files) != 1 || r.Files[0] != "~/code/den" {
		t.Errorf("files = %v", r.Files)
	}
	if len(r.Recent) != 2 || !strings.HasPrefix(r.Recent[0], "said: Pushed as abc123.") || !strings.HasPrefix(r.Recent[1], "ran: Edit") {
		t.Errorf("recent = %v", r.Recent)
	}
	text := orch.Compose(m.roster(), "status?")
	for _, want := range []string{"title: TUI agent orchestration platform", "recently changed files in: ~/code/den", "recent: said: Pushed as abc123."} {
		if !strings.Contains(text, want) {
			t.Errorf("roster text missing %q:\n%s", want, text)
		}
	}
}

func TestOrchestratorWatchdog(t *testing.T) {
	st := newStyles(theme.Deepharness)
	m := Model{st: st, agents: sample(), store: inbox.New(), tab: tabOrch, width: 120, height: 30, orch: orchState{cardCur: -1}}
	m.input = newInput(st)
	m.orch.proc = &orch.Orchestrator{}
	m.orch.chat = []chatEntry{{role: "you", text: "any work left?", at: time.Now()}}
	m.orch.thinking = true
	m.orch.unanswered = []string{"any work left?"}
	m.orch.lastEvent = time.Now().Add(-2 * time.Minute)
	if cmd := m.watchOrchestrator(); cmd != nil || !strings.Contains(m.orch.status, "no reply for 2m") {
		t.Errorf("after two minutes: cmd=%v status=%q", cmd != nil, m.orch.status)
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "thinking… 2m") {
		t.Error("the chat should say how long it has been thinking")
	}
	m.orch.lastEvent = time.Now().Add(-6 * time.Minute)
	cmd := m.watchOrchestrator()
	if cmd == nil || m.orch.restarts != 1 || m.orch.proc != nil || len(m.orch.pending) != 1 || m.orch.pending[0] != "any work left?" {
		t.Errorf("after six minutes: restart expected, pending=%v restarts=%d", m.orch.pending, m.orch.restarts)
	}
	if m.watchOrchestrator() != nil {
		t.Error("only one automatic restart per silence")
	}
}

func TestOrchestratorOpensWithCtrlO(t *testing.T) {
	st := newStyles(theme.Deepharness)
	m := Model{st: st, agents: sample(), store: inbox.New(), tab: tabInbox, width: 120, height: 30, orch: orchState{cardCur: -1}}
	m.input = newInput(st)
	if out := ansi.Strip(m.View().Content); strings.Contains(out, "  Orchestrator  ") || !strings.Contains(out, "^O orchestrator") {
		t.Errorf("tab row should offer ^O instead of a tab:\n%s", out)
	}
	mm, _ := m.handleKey(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	m = mm.(Model)
	if m.tab != tabOrch {
		t.Fatalf("ctrl+o should open the orchestrator, got %v", m.tab)
	}
	mm, _ = m.handleKey(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	m = mm.(Model)
	if m.tab != tabInbox {
		t.Errorf("ctrl+o again should return to the inbox, got %v", m.tab)
	}
	for i := 0; i < 6; i++ {
		mm, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyTab})
		m = mm.(Model)
		if m.tab == tabOrch {
			t.Fatal("Tab must not land on the orchestrator")
		}
	}
}
