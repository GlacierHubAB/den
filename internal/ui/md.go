package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// mdLine renders one line of lightweight markdown for the detail pane:
// headings lose their hashes and go bright, quotes get a bar, bullets a dot,
// rules a thin line, and inline code and bold are styled. Lines inside a
// fence come back as code. The returned bool is the fence state after it.
func (m Model) mdLine(line string, inFence bool, width int) (string, bool) {
	s := m.st
	t := strings.TrimSpace(line)
	if strings.HasPrefix(t, "```") {
		return "", !inFence // the fence itself draws nothing
	}
	if inFence {
		return s.code.Render("  " + line), true
	}
	switch {
	case t == "":
		return "", false
	case strings.HasPrefix(t, "#"):
		return s.bright.Bold(true).Render(strings.TrimSpace(strings.TrimLeft(t, "#"))), false
	case t == "---" || t == "***" || t == "___":
		return s.muted.Render(strings.Repeat("─", max(4, min(width, 24)))), false
	case strings.HasPrefix(t, "> "):
		return s.muted.Render("│ ") + m.inline(strings.TrimPrefix(t, "> "), s.dim), false
	case strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ") || strings.HasPrefix(t, "• "):
		return s.dim.Render("• ") + m.inline(t[2:], s.fg), false
	}
	return m.inline(line, s.fg), false
}

// inline styles `code` spans and **bold** runs; everything else gets base.
func (m Model) inline(text string, base lipgloss.Style) string {
	s := m.st
	var b strings.Builder
	parts := strings.Split(text, "`")
	for i, p := range parts {
		if i%2 == 1 && i < len(parts)-1 {
			b.WriteString(s.code.Render(p))
			continue
		}
		if i%2 == 1 { // unmatched backtick: keep it literal
			p = "`" + p
		}
		bolds := strings.Split(p, "**")
		for j, q := range bolds {
			if j%2 == 1 && j < len(bolds)-1 {
				b.WriteString(base.Bold(true).Render(q))
				continue
			}
			if j%2 == 1 {
				q = "**" + q
			}
			b.WriteString(base.Render(q))
		}
	}
	return b.String()
}
