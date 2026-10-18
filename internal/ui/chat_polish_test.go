package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
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
