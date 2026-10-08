package ui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/theme"
	"github.com/GlacierHubAB/den/internal/transcript"
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

// codeBlock draws lines as a block: a bar down the left and the code in the
// code colour, wrapped to width.
func (m Model) codeBlock(text string, width int) []string {
	s := m.st
	inner := max(8, width-2)
	var out []string
	for _, raw := range strings.Split(text, "\n") {
		for _, l := range strings.Split(ansi.Wrap(raw, inner, ""), "\n") {
			out = append(out, s.muted.Render("┃ ")+s.code.Render(l))
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

// diffLines renders hunks the way Claude Code does: a line-number gutter,
// the sign, and removed or added lines on a tinted background filled to
// width. Context lines stay dim.
func (m Model) diffLines(hunks []transcript.Hunk, width int) []string {
	s := m.st
	p := s.pal
	del := lipgloss.NewStyle().Foreground(lipgloss.Color(p.Err)).Background(lipgloss.Color(theme.Tint(p.Err, p.Bg)))
	add := lipgloss.NewStyle().Foreground(lipgloss.Color(p.Ok)).Background(lipgloss.Color(theme.Tint(p.Ok, p.Bg)))
	total := 0
	for _, h := range hunks {
		total += len(h.Lines)
	}
	var out []string
	shown := 0
	for hi, h := range hunks {
		if hi > 0 {
			out = append(out, s.muted.Render("…"))
		}
		oldN, newN := h.OldStart, h.NewStart
		for _, l := range h.Lines {
			if l == "" {
				l = " "
			}
			sign, text := l[:1], l[1:]
			var num int
			style := s.dim
			switch sign {
			case "-":
				num, style = oldN, del
				oldN++
			case "+":
				num, style = newN, add
				newN++
			default:
				num = newN
				oldN++
				newN++
			}
			gutter := fmt.Sprintf("%4d %s ", num, sign)
			if sign == " " {
				gutter = fmt.Sprintf("%4d   ", num)
			}
			body := ansi.Truncate(strings.ReplaceAll(text, "\t", "    "), max(4, width-len(gutter)), "…")
			row := gutter + body
			if pad := width - ansi.StringWidth(row); pad > 0 && sign != " " {
				row += strings.Repeat(" ", pad)
			}
			out = append(out, style.Render(row))
			shown++
			if shown == maxDiffLines && total > maxDiffLines {
				out = append(out, s.muted.Render(fmt.Sprintf("… %d more lines", total-shown)))
				return out
			}
		}
	}
	return out
}

// maxDiffLines caps one file's diff in the pane; the terminal has the rest.
const maxDiffLines = 30
