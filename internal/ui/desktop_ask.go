package ui

import (
	"strconv"
	"time"

	"github.com/GlacierHubAB/den/internal/api"
	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/notify"
)

// deskCooldown is how long Den takes no new desktop request after the
// human declined one, or one expired unanswered: an asker cannot bring the
// card back the moment it is gone and wear the human down.
const deskCooldown = 2 * time.Minute

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
// path of its own to remember. A card whose asker stopped waiting is
// settled as expired, which starts the cooldown as a decline does.
func (m *Model) deskAskSync() {
	for _, it := range m.store.Items() {
		if it.Kind == inbox.DesktopMission && it.State == inbox.Waiting && it.Resolved == "" && m.desk.installing != it.ID && it.AskerGone() {
			delete(m.desk.offers, it.ID)
			m.store.Settle(it, false, "the asker stopped waiting; nothing started")
			m.desk.declinedAt = time.Now()
		}
	}
	deskSetAsking(m, m.deskAsking())
}

// deskOrigin is where a desktop request came from, as Den saw it rather
// than as the asker says: the orchestrator's own Den server (in an app's
// or the console's turn, or not), or another program.
func (m Model) deskOrigin(r *bridge.Request) string {
	if m.orch.proc == nil || r.PeerPID <= 0 || !m.orch.proc.Spawned(r.PeerPID) {
		return "Asked by another program (pid " + strconv.Itoa(r.PeerPID) + "), not your orchestrator, as Den checked"
	}
	if y := m.ctl.turn; y != nil && m.ctl.sent && !m.ctl.ended {
		if y.Console != "" {
			return "Asked by your orchestrator, in a turn from " + api.ConsoleLabel + ", as Den checked"
		}
		return "Asked by your orchestrator, in a turn from " + appName(y.Exe) + ", as Den checked"
	}
	return "Asked by your orchestrator, as Den checked"
}
