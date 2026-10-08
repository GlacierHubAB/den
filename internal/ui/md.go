package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// mdLine renders one line of lightweight markdown for the detail pane:
// headings lose their hashes and go bright, quotes get a bar, bullets a dot,
// rules a thin line, and inline code and bold are styled. A line inside a
// fence comes back unstyled with code set, for the caller to draw as a
// block. The last result is the fence state after the line.
func (m Model) mdLine(line string, inFence bool, width int) (out string, code, fence bool) {
	s := m.st
	t := strings.TrimSpace(line)
	if strings.HasPrefix(t, "```") {
		return "", false, !inFence // the fence itself draws nothing
	}
	if inFence {
		return line, true, true
	}
	switch {
	case t == "":
		return "", false, false
	case strings.HasPrefix(t, "#"):
		return s.bright.Bold(true).Render(strings.TrimSpace(strings.TrimLeft(t, "#"))), false, false
	case t == "---" || t == "***" || t == "___":
		return s.muted.Render(strings.Repeat("─", max(4, min(width, 24)))), false, false
	case strings.HasPrefix(t, "> "):
		return s.muted.Render("│ ") + m.inline(strings.TrimPrefix(t, "> "), s.dim), false, false
	case strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ") || strings.HasPrefix(t, "• "):
		return s.dim.Render("• ") + m.inline(t[2:], s.fg), false, false
	}
	return m.inline(line, s.fg), false, false
}

// codeBlock draws lines as a block the way Claude Code shows a call: a bar
// down the left and the code on the raised surface, filled to width.
func (m Model) codeBlock(text string, width int) []string {
	s := m.st
	inner := max(8, width-2)
	var out []string
	for _, raw := range strings.Split(text, "\n") {
		for _, l := range strings.Split(ansi.Wrap(raw, inner, ""), "\n") {
			pad := inner - ansi.StringWidth(l)
			if pad < 0 {
				pad = 0
			}
			out = append(out, s.muted.Render("┃ ")+s.code.Render(l+strings.Repeat(" ", pad)))
		}
	}
	return out
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
