package ui

import (
	"encoding/json"
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

// /always on a plan says plans go one at a time and answers nothing.
func TestAlwaysRefusedForAPlan(t *testing.T) {
	t.Setenv("HYPRLAND_INSTANCE_SIGNATURE", "")
	m := testModel(140, 40)
	m.tab = tabInbox
	sid := m.agents[0].Session.SessionID
	p, _ := json.Marshal(map[string]any{"tool_name": "ExitPlanMode", "tool_input": map[string]any{"plan": "# Plan\n1. Write p.txt"}, "session_id": sid, "hook_event_name": "PermissionRequest"})
	r := bridge.TestRequest(bridge.Envelope{Event: "PermissionRequest", SessionID: sid, Payload: p})
	it := m.store.Apply(r)
	m.refresh()
	m.selectItem(it)
	mm, _ := m.command("/always")
	m = mm.(Model)
	if m.flash != inbox.PlansOneAtATime || decided(r) || !it.Answerable() {
		t.Errorf("flash %q, decided %v, item %+v", m.flash, decided(r), it)
	}
}
