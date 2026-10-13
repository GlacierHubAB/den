package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/orch"
	"github.com/GlacierHubAB/den/internal/theme"
)

// actionsProposal is a mission proposal block as the orchestrator writes
// it, with the team members named.
const actionsProposal = "Here is the plan.\n\n```json\n" + `{"mission": {
  "name": "Drey review pack",
  "mission": "Ship the review pack for the security re-review.",
  "done_when": "The human has the pack and the re-review passed.",
  "constraints": ["No AI attribution anywhere."],
  "shape": "once",
  "team": [
    {"name": "Quiet Panda", "role": "UI engineer", "owns": "the bar item"},
    {"name": "Velvet Quokka", "role": "Security reviewer", "owns": "the review"}
  ]
}}` + "\n```\n\nSay go and I start it."

// actionsModel is the Orchestrator tab after a turn with each kind of
// action: a proposal, a brief to a fleet agent, a message to a peer
// outside the fleet, a spawn, and another tool's message.
func actionsModel(w, h int, to, summary string) Model {
	st := newStyles(theme.Deepharness)
	m := Model{st: st, agents: sample(), store: inbox.New(), tab: tabOrch, width: w, height: h, orch: orchState{cardCur: -1}}
	m.input = newInput(st)
	m.colors = map[string]string{}
	m.orch.chat = append(m.orch.chat, chatEntry{role: "you", text: "plan the review pack", at: time.Date(2026, 10, 13, 5, 20, 0, 0, time.UTC)})
	m.handleOrchEvent(orch.Event{Kind: orch.Text, Text: actionsProposal})
	call := func(id, tool string, in any) {
		raw, _ := json.Marshal(in)
		m.handleOrchEvent(orch.Event{Kind: orch.ToolUse, Tool: tool, ToolUseID: id, Input: raw})
		m.handleOrchEvent(orch.Event{Kind: orch.ToolResult, ToolUseID: id, Text: "Sent."})
	}
	call("t1", "SendMessage", map[string]string{"to": "user-aa", "summary": "asked it to rebuild the bar item", "message": "## Bar item\n\nRebuild the bar item so it opens the **HTML drop-down**.\n\n- keep `anchor` geometry\n- run the gate"})
	call("t2", "SendMessage", map[string]string{"to": to, "summary": summary, "message": "Please re-review **host-fixes-2**:\n\n1. the input region\n2. the `drey doctor` rule"})
	call("t3", orch.SpawnAgentTool, map[string]string{"dir": "/srv/drey", "role": "coding agent", "brief": "Build the HTML pages.\n\nUse the dev markup and **escape** all dynamic text."})
	call("t4", "mcp__mail__message", map[string]string{"to": "ops", "message": "The pack is ready for review.\nSecond line stays hidden."})
	return m
}

var actionsVolatile = regexp.MustCompile(`\d{2}:\d{2}|(\b|m)\d+[smhd]\b`)

func actionsFrame(m Model) string {
	return actionsVolatile.ReplaceAllStringFunc(m.View().Content, func(v string) string {
		if strings.HasPrefix(v, "m") {
			return "m#"
		}
		return "#"
	})
}

// TestChatActionsGolden pins the actions collapsed and expanded at two
// widths, and with a hostile peer name and summary.
func TestChatActionsGolden(t *testing.T) {
	hostileTo := "drey\u202eevil\x1b[31mred\x1b]8;;https://x.test\x07" + strings.Repeat("long", 60)
	hostileSum := "rm\x1b[2J -rf \u2066hidden\u2069 " + strings.Repeat("overlong ", 60)
	for _, c := range []struct {
		name        string
		w, h        int
		expand      bool
		to, summary string
		hostile     bool
	}{
		{"collapsed_wide", 100, 36, false, "drey-hatch", "asked for a re-review of host-fixes-2", false},
		{"collapsed_narrow", 46, 36, false, "drey-hatch", "asked for a re-review of host-fixes-2", false},
		{"expanded_wide", 100, 66, true, "drey-hatch", "asked for a re-review of host-fixes-2", false},
		{"expanded_narrow", 46, 72, true, "drey-hatch", "asked for a re-review of host-fixes-2", false},
		{"hostile", 80, 36, false, hostileTo, hostileSum, true},
	} {
		m := actionsModel(c.w, c.h, c.to, c.summary)
		if c.expand {
			for i := range m.orch.chat {
				m.orch.chat[i].expanded = m.orch.chat[i].collapsible()
				if d := m.orch.chat[i].card; d != nil {
					d.Expanded, d.turnLoaded = true, true
				}
			}
		}
		got := actionsFrame(m)
		plain := ansi.Strip(got)
		for i, l := range strings.Split(got, "\n") {
			if w := ansi.StringWidth(l); w > c.w {
				t.Errorf("%s: line %d is %d wide: %q", c.name, i, w, ansi.Strip(l))
			}
		}
		if strings.Contains(plain, `"mission"`) && !c.expand {
			t.Errorf("%s: the proposal's JSON is shown collapsed:\n%s", c.name, plain)
		}
		if c.hostile {
			for _, bad := range []string{"\u202e", "\u2066", "\x1b[31m", "\x1b[2J", "\x1b]8"} {
				if strings.Contains(got, bad) {
					t.Errorf("hostile: %q reached the frame", bad)
				}
			}
			// The unknown name is plain: drawn in the text colour, not
			// in an agent's.
			fg := m.st.fg.Render("x")
			fg = fg[:strings.Index(fg, "x")]
			if !strings.Contains(got, fg+"dreyevilred") {
				t.Errorf("hostile name should be plain text:\n%q", got)
			}
		}
		path := filepath.Join("testdata", "chat_actions_"+c.name+".golden")
		if *updateGolden {
			if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if got != string(want) {
			t.Errorf("%s: the frame changed\n--- got\n%s\n--- want\n%s", c.name, plain, ansi.Strip(string(want)))
		}
	}
}

func TestChatActionsCollapse(t *testing.T) {
	m := actionsModel(100, 60, "drey-hatch", "")
	plain := ansi.Strip(m.View().Content)
	for _, want := range []string{
		"◇ mission proposal: Drey review pack · once",
		"seats: UI engineer (Quiet Panda), Security reviewer (Velvet Quokka)",
		"done when: The human has the pack and the re-review passed.",
		"Here is the plan.", "Say go and I start it.",
		"▸ asked it to rebuild the bar item",
		// No summary given: the first line of the message.
		"→ drey-hatch: Please re-review **host-fixes-2**: · delivered",
		"▶ starting an agent in /srv/drey as coding agent · Build the HTML pages. …",
		"→ ops: The pack is ready for review. · delivered",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q in:\n%s", want, plain)
		}
	}
	for _, hidden := range []string{"Second line stays hidden", "the input region", "escape all dynamic", `"team"`} {
		if strings.Contains(plain, hidden) {
			t.Errorf("collapsed frame shows %q", hidden)
		}
	}
	// Down from the tail steps up to the last action; Enter expands it in
	// place with the markdown drawn, and Enter again collapses it.
	m.moveCard(-1)
	if i := m.orch.cardCur; i < 0 || m.orch.chat[i].to != "ops" {
		t.Fatalf("focus = %d", m.orch.cardCur)
	}
	press := func() {
		nm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = nm.(Model)
	}
	press()
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "Second line stays hidden") {
		t.Errorf("Enter should expand the focused action:\n%s", plain)
	}
	press()
	if plain := ansi.Strip(m.View().Content); strings.Contains(plain, "Second line stays hidden") {
		t.Errorf("Enter again should collapse it:\n%s", plain)
	}
	// The peer's message expands with its markdown rendered.
	m.moveCard(-1)
	m.moveCard(-1)
	press()
	plain = ansi.Strip(m.View().Content)
	if !strings.Contains(plain, "│ Please re-review host-fixes-2:") || !strings.Contains(plain, "│ 2. the drey doctor rule") {
		t.Errorf("expanded message should be rendered markdown:\n%s", plain)
	}
	// The proposal expands to its JSON block.
	for m.orch.cardCur > 0 && !strings.Contains(m.orch.chat[m.orch.cardCur].text, "mission") {
		m.moveCard(-1)
	}
	press()
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, `"done_when"`) {
		t.Errorf("expanded proposal should show the block:\n%s", plain)
	}
}

func TestChatActionClick(t *testing.T) {
	m := actionsModel(100, 60, "drey-hatch", "")
	frame := strings.Split(ansi.Strip(m.View().Content), "\n")
	y := -1
	for i, l := range frame {
		if strings.Contains(l, "→ ops:") {
			y = i
		}
	}
	if y < 0 {
		t.Fatal("no ops line")
	}
	if !m.clickAction(y) {
		t.Fatal("a click on the action should toggle it")
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "Second line stays hidden") {
		t.Error("clicked action did not expand")
	}
	if m.clickAction(0) {
		t.Error("a click outside the chat toggled something")
	}
}

func TestMissionSummaryWhileStreaming(t *testing.T) {
	st := newStyles(theme.Deepharness)
	m := Model{st: st, agents: sample(), store: inbox.New(), tab: tabOrch, width: 100, height: 30, orch: orchState{cardCur: -1}}
	m.input = newInput(st)
	m.handleOrchEvent(orch.Event{Kind: orch.Delta, Text: "Drafting.\n\n```json\n{\"mission\": {\"name\": \"half"})
	plain := ansi.Strip(m.View().Content)
	if !strings.Contains(plain, "◇ drafting a mission proposal…") || strings.Contains(plain, `"name"`) {
		t.Errorf("a half-written proposal should show as being drafted:\n%s", plain)
	}
}

func TestActionSummary(t *testing.T) {
	for _, c := range []struct{ summary, text, want string }{
		{"asked it to rebuild", "long text", "asked it to rebuild"},
		{"", "\n\n  first line\nsecond", "first line"},
		{"  ", "x", "x"},
		{"a\x1b[31mb\u202ec\nd", "", "abc d"},
	} {
		if got := actionSummary(c.summary, c.text); got != c.want {
			t.Errorf("actionSummary(%q, %q) = %q, want %q", c.summary, c.text, got, c.want)
		}
	}
	if got := actionSummary(strings.Repeat("x", 500), ""); ansi.StringWidth(got) > summaryMax {
		t.Errorf("summary not capped: %d", ansi.StringWidth(got))
	}
	if _, ok := parseMessageCall(orch.SpawnAgentTool, json.RawMessage(`{"to":"a","message":"b"}`)); ok {
		t.Error("Den's own tools are not generic messages")
	}
	if _, ok := parseMessageCall("mcp__x__y", json.RawMessage(`{"to":"a"}`)); ok {
		t.Error("no message, no summary line")
	}
}
