package ui

import (
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/notify"
)

// A desktop start card is answered only in Den's own window, so the human
// is told one waits wherever they are looking: a desktop notification,
// and the console's desktop event in state asking (the dock shows a line;
// the card's text stays in Den). Den does not move the focus to its
// window: that would take the keyboard from what the human is typing.

// deskAsked says a start card was just added.
func (m *Model) deskAsked() {
	deskNotify(notify.Note{Summary: "Desktop control asked", Body: "Answer the card in Den's window: Ctrl+Y starts it, Ctrl+N declines."})
	m.deskAskSync()
}

// deskAsking reports whether a desktop start card waits for the human: a
// waiting mission card not yet answered (an approved one installing or
// starting no longer waits on them, nor one whose asker gave up).
func (m Model) deskAsking() bool {
	for _, it := range m.store.Items() {
		if it.Kind != inbox.DesktopMission || it.State != inbox.Waiting {
			continue
		}
		if m.desk.installing == it.ID || it.Resolved != "" || it.AskerGone() {
			continue
		}
		return true
	}
	return false
}

// deskAskSync brings the console's asking state in line with the inbox,
// on every tick: a card answered, dropped or expired clears it with no
// path of its own to remember.
func (m *Model) deskAskSync() {
	deskSetAsking(m, m.deskAsking())
}
