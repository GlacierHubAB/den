package ui

import (
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/inbox"
)

// Answered in the terminal first: Claude Code ended the held hook. A ^Y
// before the next tick must not report an answer that went nowhere.
func TestAnswerAfterTheTerminalAnswered(t *testing.T) {
	t.Setenv("HYPRLAND_INSTANCE_SIGNATURE", "")
	m := testModel(140, 40)
	m.tab = tabInbox
	m.cfg.Hooks.PermissionWait = 580
	r := heldPerm(&m, m.agents[0].Session.SessionID, "touch check.txt")
	selectReq(t, &m, r)
	it := itemOf(m, r)
	bridge.TestHangUp(r)
	mm, _ := m.answer("allow")
	m = mm.(Model)
	if strings.Contains(m.flash, "Allowed") || it.State != inbox.Resolved || it.Resolved != inbox.ClosedInTerminal {
		t.Errorf("flash %q, item %+v", m.flash, it)
	}
}
