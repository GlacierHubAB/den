package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A desktop agent that ended (or that ran when Den stopped) is never
// offered for restore and /reopen refuses it: Den's ordinary resume would
// give a conversation that read the screen an ordinary agent's tools.
func TestDesktopAgentsAreNeverRestored(t *testing.T) {
	m, fake := restoreModel(t)
	old := deskRecorded
	t.Cleanup(func() { deskRecorded = old })
	deskRecorded = func() (map[string]bool, bool) {
		return map[string]bool{"maple-owl": true, "snug-capybara": true}, false
	}
	for _, a := range m.restorable() {
		if a.SessionID == "maple-owl" || a.SessionID == "snug-capybara" {
			t.Fatalf("desktop agent %s offered for restore", a.SessionID)
		}
	}
	mm, _ := m.reopenCommand([]string{"Maple", "Owl"})
	m = mm.(Model)
	if !strings.Contains(m.flash, "desktop agent") || len(restoreItems(m)) != 0 {
		t.Fatalf("/reopen of a desktop agent: %q, items %d", m.flash, len(restoreItems(m)))
	}
	mm, _ = m.reopenCommand([]string{"--fork", "Snug", "Capybara"})
	m = mm.(Model)
	if !strings.Contains(m.flash, "desktop agent") || len(restoreItems(m)) != 0 {
		t.Fatalf("/reopen --fork of a desktop agent: %q", m.flash)
	}
	if len(fake.reqs) != 0 {
		t.Fatalf("resumed %+v", fake.reqs)
	}
}

// The record survives Den: mission files name their agent's session.
func TestDesktopSessionsFromMissionFiles(t *testing.T) {
	dir := t.TempDir()
	old := deskStateDir
	t.Cleanup(func() { deskStateDir = old })
	deskStateDir = func() string { return dir }
	os.MkdirAll(filepath.Join(dir, "m1"), 0o700)
	os.WriteFile(filepath.Join(dir, "m1", "mission.json"), []byte(`{"mission":"m1","session":"sess-old","ended":true}`), 0o600)
	m := Model{}
	if !m.isDesktopSession("sess-old") || m.isDesktopSession("other") || m.isDesktopSession("") {
		t.Fatal("mission files not read right")
	}
	// A corrupt or missing mission file fails closed: no session is
	// restored while it is there.
	os.MkdirAll(filepath.Join(dir, "m2"), 0o700)
	os.WriteFile(filepath.Join(dir, "m2", "mission.json"), []byte(`{"mission":`), 0o600)
	if !m.isDesktopSession("other") {
		t.Fatal("a corrupt mission file must fail closed")
	}
	os.Remove(filepath.Join(dir, "m2", "mission.json"))
	if !m.isDesktopSession("other") {
		t.Fatal("a mission folder without its file must fail closed")
	}
}
