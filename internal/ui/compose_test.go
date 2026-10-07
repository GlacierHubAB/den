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
	m := Model{st: st, agents: sample(), store: inbox.New(), width: 120, height: 36}
	m.input = newInput(st)
	mm, _ := m.Update(tea.KeyPressMsg{Code: '@', Text: "@"})
	m = mm.(Model)
	if !m.input.Focused() || m.input.Value() != "@" {
		t.Fatalf("@ should start compose; focused=%v value=%q", m.input.Focused(), m.input.Value())
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
	if m.input.Focused() {
		t.Error("esc should leave compose mode")
	}
}
