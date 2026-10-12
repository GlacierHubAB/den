package ui

import (
	"fmt"

	"github.com/GlacierHubAB/den/internal/inbox"
)

// A request is answered only once the human could see all of it. An item
// taller than the Inbox pane scrolls inside it (↑ ↓, PgUp PgDn) under a
// marker that says how much is left, and ^Y, /always, a typed answer and
// Den's own ^Y questions wait until it was scrolled to its end. ^N, which
// lets nothing run, never waits.

// inboxRoom is the width and the rows the Inbox pane gives its items, as
// View lays it out.
func (m Model) inboxRoom() (w, h int) {
	w = m.width
	if w <= 0 {
		w = 120
	}
	_, ph := m.paneArea()
	return w - 2, ph - 2
}

// bodyMaxOff is how far the item's block scrolls: 0 when it fits, else
// the rows past the pane less the marker row.
func (m Model) bodyMaxOff(it *inbox.Item) int {
	w, h := m.inboxRoom()
	total := len(m.inboxBlock(it, true, w))
	if total <= h {
		return 0
	}
	return total - (h - 1)
}

// unreadRows is how many rows of the item's block the human has not been
// shown in the Inbox: 0 when it fits there or was read to its end.
func (m Model) unreadRows(it *inbox.Item) int {
	if it == nil || m.bodyRead[it.ID] {
		return 0
	}
	n := m.bodyMaxOff(it)
	if it.ID == m.inboxSel {
		n -= m.bodyOff
	}
	return max(0, n)
}

// readGate is the whole-body rule for anything that answers an inbox item
// yes, or with words: "" when the human has been shown all of it, else why
// it waits. Every ^Y path and a typed answer call it; a new kind of inbox
// question needs nothing more than to go through them.
func (m Model) readGate(it *inbox.Item) string {
	if n := m.unreadRows(it); n > 0 {
		return m.unreadNote(it, n)
	}
	return ""
}

// unreadNote is why ^Y waits for an item with unread rows.
func (m Model) unreadNote(it *inbox.Item, n int) string {
	lines := "1 more line"
	if n != 1 {
		lines = fmt.Sprintf("%d more lines", n)
	}
	if m.tab == tabInbox && it.ID == m.inboxSel {
		return lines + " below: read them (↓) before ^Y"
	}
	return m.nameFor(it.SessionID) + "'s request has " + lines + " than the Inbox shows at once: select it there and read it to the end before ^Y"
}

// scrollBody scrolls the selected item by delta rows when it is taller
// than the pane and not already at that end. It reports whether it did.
func (m *Model) scrollBody(delta int) bool {
	it := m.inboxSelected()
	if it == nil {
		return false
	}
	maxOff := m.bodyMaxOff(it)
	next := clamp(m.bodyOff+delta, 0, maxOff)
	if maxOff == 0 || next == m.bodyOff {
		return false
	}
	m.bodyOff = next
	if next == maxOff {
		if m.bodyRead == nil {
			m.bodyRead = map[string]bool{}
		}
		m.bodyRead[it.ID] = true
	}
	return true
}

// bodyWindow is the part of the selected item's block the pane shows when
// it is taller than h rows: h-1 rows from the scroll offset and a marker.
func (m Model) bodyWindow(block []string, h, w int) []string {
	maxOff := len(block) - (h - 1)
	off := clamp(m.bodyOff, 0, maxOff)
	out := append([]string(nil), block[off:off+h-1]...)
	below := len(block) - (off + h - 1)
	note := fmt.Sprintf(" ↓ %d more lines below: read them before ^Y", below)
	if below == 1 {
		note = " ↓ 1 more line below: read it before ^Y"
	}
	if below == 0 {
		note = fmt.Sprintf(" ↑ %d lines above · read to the end", off)
	}
	return append(out, fill(m.st.attn.Render(note), w, m.st.fg))
}
