package ui

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/desktop"
)

// overlayRig is deskRig with the console's pointer outline over the
// mission's monitor, drawn by pid 800 (started at 8000), and the console's
// proved processes as given.
func overlayRig(t *testing.T, console map[int]uint64) Model {
	t.Helper()
	m := deskRig(t)
	oldConsole, oldStart, oldSnap := deskConsole, deskStart, deskSnapshot
	t.Cleanup(func() { deskConsole, deskStart, deskSnapshot = oldConsole, oldStart, oldSnap })
	deskConsole = func(*Model) map[int]uint64 { return console }
	deskStart = func(pid int) string {
		if pid == 800 {
			return "8000"
		}
		return oldStart(pid)
	}
	deskSnapshot = func(ctx context.Context) (desktop.Snapshot, error) {
		s, err := oldSnap(ctx)
		s.Layers = append(s.Layers, desktop.Layer{Monitor: "DP-1", Level: desktop.OverlayLevel, W: 1920, H: 1080, Namespace: desktop.PointerNamespace, PID: 800})
		return s, err
	}
	deskStarted(t, &m)
	return m
}

// The console's pointer outline never blocks the agent's click, but only
// while its process is one of the console's proved connections and still
// the process that connected.
func TestConsolePointerOutlineDoesNotBlockClicks(t *testing.T) {
	click := `{"action":"click","x":1200,"y":300}`
	m := overlayRig(t, map[int]uint64{800: 8000})
	if d, _ := deskCall(&m, "pointer", click); d.Behavior != "allow" {
		t.Fatalf("click under the console's outline: %+v", d)
	}
	for name, console := range map[string]map[int]uint64{
		"no console":                      nil,
		"another process":                 {801: 8000},
		"a pid reused since it connected": {800: 7999},
	} {
		m := overlayRig(t, console)
		if d, _ := deskCall(&m, "pointer", click); d.Behavior != "deny" || !strings.Contains(d.Message, desktop.PointerNamespace) {
			t.Errorf("%s: %+v", name, d)
		}
	}
}

// The console hears a mission start and end, and the pointer file goes
// with the mission, whichever way it ends.
func TestDesktopStateAndPointerFollowTheMission(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	m := deskRig(t)
	var said []string
	old := deskSetDesktop
	t.Cleanup(func() { deskSetDesktop = old })
	deskSetDesktop = func(_ *Model, mission string) { said = append(said, mission) }
	os.WriteFile(desktop.PointerPath(), []byte("10 20\n"), 0o600)
	m.endDeskMission(m.deskMission("m1"), "it failed to start")
	if len(said) != 1 || said[0] != "" {
		t.Fatalf("told the console %q", said)
	}
	if _, err := os.Stat(desktop.PointerPath()); !os.IsNotExist(err) {
		t.Fatalf("the pointer file outlived the mission: %v", err)
	}
	m.endDeskMission(m.deskMission("m1"), "again")
	if len(said) != 1 {
		t.Fatalf("an ended mission ended again: %q", said)
	}
	m.addDeskMission(&deskMission{Mission: desktop.Mission{ID: "m2"}})
	if len(said) != 2 || said[1] != "m2" {
		t.Fatalf("start told the console %q", said)
	}
	m.publishDesktop()
	if said[len(said)-1] != "m2" {
		t.Fatalf("published %q", said)
	}
}
