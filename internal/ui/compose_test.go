package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/theme"
)

func TestComposeShowsCompletionsAndFits(t *testing.T) {
	st := newStyles(theme.Deepharness)
	m := Model{st: st, agents: sample(), store: inbox.New(), width: 120, height: 36, tab: tabFleet}
	m.input = newInput(st)
	m.input.Focus()
	mm, _ := m.Update(tea.KeyPressMsg{Code: '@', Text: "@"})
	m = mm.(Model)
	if m.input.Value() != "@" {
		t.Fatalf("typing should go to the always-open line; value=%q", m.input.Value())
	}
	mm, _ = m.Update(tea.KeyPressMsg{Code: 'S', Text: "S"})
	m = mm.(Model)
	out := ansi.Strip(m.View().Content)
	if !strings.Contains(out, "Snug Capybara") || !strings.Contains(out, "complete name") {
		t.Errorf("expected completion rows and compose hints:\n%s", out)
	}
	for i, l := range strings.Split(m.View().Content, "\n") {
		if w := ansi.StringWidth(l); w > 120 {
			t.Errorf("line %d too wide: %d", i, w)
		}
	}
	if n := len(strings.Split(m.View().Content, "\n")); n != 36 {
		t.Errorf("height = %d lines, want 36", n)
	}
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = mm.(Model)
	if m.input.Value() != "@Snug Capybara " {
		t.Errorf("tab completion = %q", m.input.Value())
	}
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = mm.(Model)
	if m.input.Value() != "" {
		t.Error("esc should clear the line")
	}
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = mm.(Model)
	if m.tab != tabInbox {
		t.Errorf("tab with an empty line should switch views, got %d", m.tab)
	}
	m.tab = tabFleet
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m = mm.(Model)
	if m.focus != paneDetail {
		t.Error("right arrow should focus the detail pane")
	}
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = mm.(Model)
	if m.detailScroll != 1 || m.cursor != 0 {
		t.Errorf("down in the detail pane should scroll it, not move the agent cursor: scroll=%d cursor=%d", m.detailScroll, m.cursor)
	}
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = mm.(Model)
	if m.focus != paneAgents || m.cursor != 1 || m.detailScroll != 0 {
		t.Errorf("left then down should select the next agent: focus=%d cursor=%d scroll=%d", m.focus, m.cursor, m.detailScroll)
	}
	mm, _ = m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = mm.(Model)
	if m.input.Value() != "y" {
		t.Errorf("letters must go to the line, not act as commands; value=%q", m.input.Value())
	}
}
