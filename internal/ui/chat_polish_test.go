package ui

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/mission"
	"github.com/GlacierHubAB/den/internal/orch"
)

func TestChatNameSitsAboveFullWidthText(t *testing.T) {
	m := testModel(100, 40)
	long := strings.Repeat("plenty of words to wrap ", 12)
	m.orch.chat = []chatEntry{
		{role: "you", text: long, at: time.Now()},
		{role: "orchestrator", text: long, at: time.Now()},
	}
	lines, _, _ := m.chatLines(100, func(chatEntry) bool { return true })
	plain := make([]string, len(lines))
	for i, l := range lines {
		plain[i] = ansi.Strip(l)
	}
	for _, name := range []string{"you", "orchestrator"} {
		at := -1
		for i, l := range plain {
			if strings.TrimSpace(l) == name {
				at = i
				break
			}
		}
		if at < 0 || at+1 >= len(plain) {
			t.Fatalf("no line with just %q:\n%s", name, strings.Join(plain, "\n"))
		}
		if body := plain[at+1]; !strings.HasPrefix(strings.TrimLeft(body, " "), "plenty") || ansi.StringWidth(body) < 90 {
			t.Errorf("%s: text under the name does not use the width: %q", name, body)
		}
	}
	for _, l := range plain {
		if strings.Contains(l, "›") {
			t.Errorf("name still sits left of the text: %q", l)
		}
		if ansi.StringWidth(l) > 100 {
			t.Errorf("line too wide: %q", l)
		}
	}
}

// A dispatch made while the human talks in a mission's thread shows in that
// thread, in order, as it does in the Orchestrator tab; also when the mission
// is done (no run to file it under) and after a restart.
func TestMissionThreadShowsWorkingAgentsInline(t *testing.T) {
	m := missionModel()
	m.chatLog = filepath.Join(t.TempDir(), "orchestrator.jsonl")
	m.orch.starting = true
	now := time.Now()
	ms := &mission.Mission{ID: "m1", Project: m.proj.cur, Name: "Site", Mission: "Ship the site.", State: mission.Done, Created: now, Updated: now}
	m.missions.Save(ms)
	m.base[1].Session.Name = "peer-snug"
	m.refresh()
	m.ov.open = ms.ID
	// Den's own message for the mission (a run): the dispatch it brings
	// joins the mission's thread.
	m.turn = missionTurn{id: ms.ID}
	m.tell(ms.ID, "ask Snug to look at the header", "ask Snug to look at the header")
	m.handleOrchEvent(orch.Event{Kind: orch.Text, Text: "Sending it to Snug Capybara."})
	in, _ := json.Marshal(map[string]string{"to": "peer-snug", "message": "Look at the header spacing."})
	m.handleOrchEvent(orch.Event{Kind: orch.ToolUse, Tool: "SendMessage", ToolUseID: "t1", Input: in})
	for _, e := range m.orch.chat {
		if e.card != nil {
			e.card.State, e.card.StartedAt, e.card.Ticker = cardWorking, now, "Read header.go"
			m.logCard(e.card)
		}
	}
	check := func(when string, m Model) {
		out := ansi.Strip(m.missionsPane(100, 60))
		ask := strings.Index(out, "ask Snug to look at the header")
		card := strings.Index(out, "Look at the header spacing.")
		if ask < 0 || card < ask || !strings.Contains(out, "● working") || !strings.Contains(out, "Read header.go") {
			t.Errorf("%s: working agent not inline after the ask:\n%s", when, out)
		}
		if n := strings.Count(out, "Look at the header spacing."); n != 1 {
			t.Errorf("%s: card drawn %d times:\n%s", when, n, out)
		}
	}
	check("live", m)

	chat, _ := loadChatLog(m.chatLog, maxChatLog)
	m.orch.chat = chat
	check("restored", m)
}

func typeText(m Model, text string) Model {
	for _, r := range text {
		mm, _ := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = mm.(Model)
	}
	return m
}

func TestLongPromptGrowsWithoutLosingItsTop(t *testing.T) {
	m := testModel(100, 40)
	text := "FIRST " + strings.Repeat("lorem ipsum dolor sitametconsectetur ", 9) + "LAST"
	m = typeText(m, text)
	out := ansi.Strip(m.View().Content)
	if !strings.Contains(out, "FIRST") || !strings.Contains(out, "LAST") {
		t.Errorf("typed prompt not fully visible (height %d):\n%s", m.input.Height(), out)
	}
	if got := m.input.Value(); got != text {
		t.Errorf("value lost text: %q", got)
	}
	// A paste lands the same way.
	m.clear()
	mm, _ := m.Update(tea.PasteMsg{Content: text})
	m = mm.(Model)
	out = ansi.Strip(m.View().Content)
	if !strings.Contains(out, "FIRST") || !strings.Contains(out, "LAST") {
		t.Errorf("pasted prompt not fully visible (height %d):\n%s", m.input.Height(), out)
	}
}

func TestWidePromptWrapsByDisplayWidth(t *testing.T) {
	m := testModel(60, 40)
	text := "始" + strings.Repeat("漢字かな", 20) + "終"
	m = typeText(m, text)
	out := ansi.Strip(m.View().Content)
	if !strings.Contains(out, "始") || !strings.Contains(out, "終") {
		t.Errorf("wide prompt not fully visible:\n%s", out)
	}
	for i, l := range strings.Split(m.View().Content, "\n") {
		if w := ansi.StringWidth(l); w > 60 {
			t.Errorf("line %d too wide: %d", i, w)
		}
	}
}

func TestHugePasteScrollsInsideTheCapAndKeepsAllText(t *testing.T) {
	m := testModel(100, 30)
	var b strings.Builder
	for i := 0; i < 40; i++ {
		b.WriteString("line of a long pasted prompt\n")
	}
	b.WriteString("TAIL")
	mm, _ := m.Update(tea.PasteMsg{Content: b.String()})
	m = mm.(Model)
	if got := m.input.Value(); got != b.String() {
		t.Errorf("paste lost text: %d of %d bytes", len(got), b.Len())
	}
	if h := m.input.Height(); h != m.inputMaxRows() {
		t.Errorf("height = %d, want the cap %d", h, m.inputMaxRows())
	}
	out := ansi.Strip(m.View().Content)
	if !strings.Contains(out, "TAIL") {
		t.Errorf("cursor line not visible:\n%s", out)
	}
	if n := len(strings.Split(m.View().Content, "\n")); n != 30 {
		t.Errorf("screen height = %d, want 30", n)
	}
	// Sending clears it back to one row.
	m.clear()
	if h := m.input.Height(); h != 1 {
		t.Errorf("height after clear = %d", h)
	}
}

// TestMissionActionsToggleOnOverview: an open mission's actions show their
// full text on ^E or a click on the line, while Enter stays with the
// mission; a mission block with a mistake says so plainly.
func TestMissionActionsToggleOnOverview(t *testing.T) {
	m := missionModel()
	now := time.Now()
	ms := &mission.Mission{ID: "m1", Project: m.proj.cur, Name: "Site", Mission: "Ship the site.", State: mission.Running, Created: now, Updated: now}
	m.missions.Save(ms)
	m.ov.open = ms.ID
	m.orch.chat = []chatEntry{
		{role: "you", text: "check the header", mission: "m1", at: now},
		{role: "fan", to: "Plucky", text: "Look at the header", full: "Look at the header.\nTHE-FULL-BRIEF", status: "sent", mission: "m1", at: now},
		{role: "orchestrator", text: "```json\n{\"mission\": oops}\n```", mission: "m1", at: now},
	}
	pane, ok := m.copyPane()
	if !ok {
		t.Fatal("no mission pane")
	}
	text := func(m Model) string { return ansi.Strip(m.missionsPane(pane.x1-pane.x0+1, pane.y1-pane.y0+1)) }
	if out := text(m); strings.Contains(out, "THE-FULL-BRIEF") || !strings.Contains(out, "this mission block has a mistake, so nothing changed") {
		t.Fatalf("collapsed pane:\n%s", out)
	}

	// A click on the action's line.
	_, owners := m.missionsPaneOwned(pane.x1-pane.x0+1, pane.y1-pane.y0+1)
	row := -1
	for r, o := range owners {
		if o == 1 {
			row = r
			break
		}
	}
	if row < 0 || !m.clickAction(pane.y0+row) || !strings.Contains(text(m), "THE-FULL-BRIEF") {
		t.Fatalf("a click on the action did not show it:\n%s", text(m))
	}
	m.clickAction(pane.y0 + row)
	if strings.Contains(text(m), "THE-FULL-BRIEF") {
		t.Error("a second click did not hide it")
	}
	if m.clickAction(pane.y0) {
		t.Error("a click off the actions toggled something")
	}

	// ^E opens every action in the thread, and again closes them.
	mm, _ := m.handleKey(tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	m = mm.(Model)
	if out := text(m); !strings.Contains(out, "THE-FULL-BRIEF") || !strings.Contains(out, "oops") {
		t.Fatalf("^E did not open the actions:\n%s", out)
	}
	mm, _ = m.handleKey(tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	m = mm.(Model)
	if strings.Contains(text(m), "THE-FULL-BRIEF") || m.ov.open != "m1" {
		t.Errorf("a second ^E did not close them, or left the mission:\n%s", text(m))
	}

	// Enter still closes the mission, as before.
	mm, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m = mm.(Model); m.ov.open != "" {
		t.Error("Enter no longer closes the open mission")
	}
}
