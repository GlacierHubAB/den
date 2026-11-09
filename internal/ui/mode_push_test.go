package ui

import "testing"

// Den's mode is published to den-api when it changes: at start, and by a
// lowering from /mode; a raise waiting on its card is not the mode yet.
func TestModeIsPublishedToTheAPI(t *testing.T) {
	m := consoleModel(t)
	if got := m.api.srv.ModeNow(); got != m.mode.global {
		t.Fatalf("at API start the server has %q, Den has %q", got, m.mode.global)
	}
	typeLine(&m, "/mode plan")
	if got := m.api.srv.ModeNow(); got != "plan" || m.mode.global != "plan" {
		t.Fatalf("after /mode plan the server has %q, Den has %q", got, m.mode.global)
	}
	typeLine(&m, "/mode default")
	if got := m.api.srv.ModeNow(); got != "default" {
		t.Fatalf("after /mode default the server has %q", got)
	}
	typeLine(&m, "/mode auto")
	if got := m.api.srv.ModeNow(); got != "default" || m.mode.global != "default" {
		t.Fatalf("a raise on its card changed the mode: server %q, Den %q", got, m.mode.global)
	}
}
