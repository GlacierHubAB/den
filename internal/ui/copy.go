package ui

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Pane-scoped copy. A terminal's own selection runs across the whole row,
// so dragging over the mission pane also grabs the agents pane beside it
// and the box borders. On the Orchestrator and Overview views Den takes
// the mouse instead: a drag selects inside one pane only, and letting go
// copies it; a click on a command block copies the whole block; /copy
// copies the last block in view. The terminal's own selection stays one
// modifier away (Shift+drag in Ghostty).

// rect is a pane's inner area in screen cells.
type rect struct{ x0, y0, x1, y1 int } // inclusive

func (r rect) has(x, y int) bool { return x >= r.x0 && x <= r.x1 && y >= r.y0 && y <= r.y1 }
func (r rect) ok() bool          { return r.x1 >= r.x0 && r.y1 >= r.y0 }

// selState is the drag in progress or the selection it left.
type selState struct {
	pane     rect
	dragging bool
	active   bool   // something is selected and shown
	ax, ay   int    // anchor
	cx, cy   int    // current end
	copied   string // what the last copy sent, for tests
}

// copyPane is the pane that owns Den's selection on the current view.
func (m Model) copyPane() (rect, bool) {
	w, h := m.width, m.height
	if w <= 0 {
		w = 120
	}
	if h <= 0 {
		h = 40
	}
	top, paneH := m.paneArea()
	switch m.tab {
	case tabOrch:
		return rect{x0: 1, y0: top + 1, x1: w - 2, y1: top + paneH - 2}, true
	case tabOverview:
		y0 := top + stripRows
		bh := paneH - stripRows
		if bh < 4 {
			return rect{}, false
		}
		if w < 72 {
			return rect{x0: 1, y0: y0 + 1, x1: w - 2, y1: y0 + bh - 2}, true
		}
		lw := clamp(w*42/100, 36, 64)
		return rect{x0: lw + 1, y0: y0 + 1, x1: w - 2, y1: y0 + bh - 2}, true
	}
	return rect{}, false
}

// copyMouse handles the mouse on the views that own their selection.
func (m *Model) copyMouse(msg tea.Msg) tea.Cmd {
	pane, ok := m.copyPane()
	if !ok {
		return nil
	}
	switch e := msg.(type) {
	case tea.MouseWheelMsg:
		delta := 3
		if e.Button == tea.MouseWheelUp {
			delta = -3
		}
		m.scrollPane(delta)
		return nil
	case tea.MouseClickMsg:
		if e.Button != tea.MouseLeft || !pane.has(e.X, e.Y) {
			m.sel.active, m.sel.dragging = false, false
			return nil
		}
		m.sel = selState{pane: pane, dragging: true, ax: e.X, ay: e.Y, cx: e.X, cy: e.Y, copied: m.sel.copied}
		return nil
	case tea.MouseMotionMsg:
		if !m.sel.dragging {
			return nil
		}
		m.sel.cx, m.sel.cy = clamp(e.X, pane.x0, pane.x1), clamp(e.Y, pane.y0, pane.y1)
		m.sel.active = m.sel.cx != m.sel.ax || m.sel.cy != m.sel.ay
		return nil
	case tea.MouseReleaseMsg:
		if !m.sel.dragging {
			return nil
		}
		m.sel.dragging = false
		frame := m.frameLines()
		if m.sel.active {
			text := selectedText(frame, m.sel)
			return m.copyText(text, countLines(text)+" selected")
		}
		// A click: a command block under the pointer copies whole.
		if block := codeBlockAt(frame, pane, m.sel.ay); block != "" {
			return m.copyText(block, "command ("+countLines(block)+")")
		}
		// A click on an action shows or hides its full text.
		m.clickAction(m.sel.ay)
		return nil
	}
	return nil
}

// scrollPane moves the chat or the open mission by delta lines.
func (m *Model) scrollPane(delta int) {
	switch m.tab {
	case tabOrch:
		m.orch.scroll = max(0, m.orch.scroll-delta)
	case tabOverview:
		m.ov.scroll = max(0, m.ov.scroll-delta)
	}
}

// copyLast is /copy: the last command block in the pane.
func (m *Model) copyLast() tea.Cmd {
	pane, ok := m.copyPane()
	if !ok {
		return nil
	}
	frame := m.frameLines()
	for y := pane.y1; y >= pane.y0; y-- {
		if block := codeBlockAt(frame, pane, y); block != "" {
			return m.copyText(block, "command ("+countLines(block)+")")
		}
	}
	return nil
}

// frameLines is the view as drawn, one string per screen row, without the
// selection painted over it.
func (m Model) frameLines() []string {
	mo := m
	mo.sel.active = false
	return strings.Split(mo.View().Content, "\n")
}

// ordered returns the selection's corners in reading order.
func (s selState) ordered() (x0, y0, x1, y1 int) {
	x0, y0, x1, y1 = s.ax, s.ay, s.cx, s.cy
	if y1 < y0 || (y1 == y0 && x1 < x0) {
		x0, y0, x1, y1 = x1, y1, x0, y0
	}
	return
}

// selectedText is the pane text under the selection: full rows between
// the ends, the pane's own columns only, the command bar stripped, blank
// trailing space dropped.
func selectedText(frame []string, s selState) string {
	x0, y0, x1, y1 := s.ordered()
	var out []string
	for y := y0; y <= y1 && y < len(frame); y++ {
		c0, c1 := s.pane.x0, s.pane.x1+1
		if y == y0 {
			c0 = max(c0, x0)
		}
		if y == y1 {
			c1 = min(c1, x1+1)
		}
		if c1 <= c0 {
			out = append(out, "")
			continue
		}
		out = append(out, plainCode(ansi.Cut(frame[y], c0, c1)))
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

// codeMark is what a command block line starts with once styles are gone.
const codeMark = "┃ "

// plainCode strips styles and the command bar from one row.
func plainCode(row string) string {
	t := strings.TrimRight(ansi.Strip(row), " ")
	if i := strings.Index(t, codeMark); i >= 0 && strings.TrimSpace(t[:i]) == "" {
		t = t[i+len(codeMark):]
	}
	return t
}

// isCode reports whether a screen row inside the pane is a command block
// line.
func isCode(frame []string, pane rect, y int) bool {
	if y < 0 || y >= len(frame) {
		return false
	}
	t := ansi.Strip(ansi.Cut(frame[y], pane.x0, pane.x1+1))
	t = strings.TrimLeft(t, " ")
	return strings.HasPrefix(t, codeMark)
}

// codeBlockAt is the whole command block around row y, "" when y is not
// in one.
func codeBlockAt(frame []string, pane rect, y int) string {
	if !isCode(frame, pane, y) {
		return ""
	}
	top, bottom := y, y
	for top-1 >= pane.y0 && isCode(frame, pane, top-1) {
		top--
	}
	for bottom+1 <= pane.y1 && isCode(frame, pane, bottom+1) {
		bottom++
	}
	var out []string
	for r := top; r <= bottom; r++ {
		out = append(out, plainCode(ansi.Cut(frame[r], pane.x0, pane.x1+1)))
	}
	return strings.Join(out, "\n")
}

func countLines(text string) string {
	n := strings.Count(text, "\n") + 1
	if n == 1 {
		return "1 line"
	}
	return fmt.Sprintf("%d lines", n)
}

// copyFlashFor is how long "Copied …" stays up; tests shorten it.
var copyFlashFor = 3 * time.Second

// copyText sends text to the clipboard: OSC 52 through the terminal, and
// wl-copy as well when it is there, since not every terminal honours
// OSC 52. The flash says what went.
func (m *Model) copyText(text, what string) tea.Cmd {
	if text == "" {
		return nil
	}
	m.sel.copied = text
	m.flash, m.flashErr = "Copied "+what, false
	cmds := []tea.Cmd{tea.SetClipboard(text), flashTick(copyFlashFor, m.flash)}
	if copyViaWlCopy != nil {
		cmds = append(cmds, func() tea.Msg { copyViaWlCopy(text); return nil })
	}
	return tea.Batch(cmds...)
}

// copyViaWlCopy is the Wayland fallback; nil when wl-copy is not
// installed. Tests set it to nil or to a recorder.
var copyViaWlCopy = func() func(string) {
	path, err := exec.LookPath("wl-copy")
	if err != nil {
		return nil
	}
	return func(text string) {
		cmd := exec.Command(path)
		cmd.Stdin = strings.NewReader(text)
		_ = cmd.Run()
	}
}()

// highlightSelection paints the selection over the drawn rows.
func (m Model) highlightSelection(frame []string) []string {
	if !m.sel.active {
		return frame
	}
	if _, ok := m.copyPane(); !ok {
		return frame
	}
	x0, y0, x1, y1 := m.sel.ordered()
	hl := lipgloss.NewStyle().Reverse(true)
	for y := y0; y <= y1 && y < len(frame); y++ {
		c0, c1 := m.sel.pane.x0, m.sel.pane.x1+1
		if y == y0 {
			c0 = max(c0, x0)
		}
		if y == y1 {
			c1 = min(c1, x1+1)
		}
		if c1 <= c0 {
			continue
		}
		row := frame[y]
		width := ansi.StringWidth(row)
		if width < c1 {
			row += strings.Repeat(" ", c1-width)
		}
		mid := ansi.Strip(ansi.Cut(row, c0, c1))
		frame[y] = ansi.Cut(row, 0, c0) + hl.Render(mid) + ansi.Cut(row, c1, ansi.StringWidth(row))
	}
	return frame
}
