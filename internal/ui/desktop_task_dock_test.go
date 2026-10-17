package ui

import (
	"context"
	"testing"

	"github.com/GlacierHubAB/den/internal/desktop"
)

// A task never reaches Drey's dock: the console's proved process (pid
// 1500, started at 15000) owns a window and a bar on the top layer, both
// off limits on every call; its pointer outline still blocks nothing. A
// process that is no longer the one that connected protects nothing.
func TestDesktopTaskLeavesTheDockAlone(t *testing.T) {
	m, d, _ := taskMission(t)
	oldConsole, oldStart, oldSnap := deskConsole, deskStart, deskSnapshot
	t.Cleanup(func() { deskConsole, deskStart, deskSnapshot = oldConsole, oldStart, oldSnap })
	deskConsole = func(*Model) map[int]uint64 { return map[int]uint64{1500: 15000} }
	start := "15000"
	deskStart = func(pid int) string {
		if pid == 1500 {
			return start
		}
		return oldStart(pid)
	}
	deskSnapshot = func(context.Context) (desktop.Snapshot, error) {
		s := d.snap()
		s.Windows = append(s.Windows, desktop.Window{Address: "0xdock", Class: "drey", PID: 1500, Workspace: 1, At: [2]int{1000, 600}, Size: [2]int{400, 300}, Mapped: true})
		s.Layers = append(s.Layers,
			desktop.Layer{Monitor: "DP-1", Level: 2, X: 0, Y: 1040, W: 1920, H: 40, Namespace: "drey-dock", PID: 1500},
			desktop.Layer{Monitor: "DP-1", Level: desktop.OverlayLevel, W: 1920, H: 1080, Namespace: desktop.PointerNamespace, PID: 1500},
		)
		return s, nil
	}
	for _, c := range []struct{ tool, args string }{
		{"pointer", `{"action":"click","x":1100,"y":700}`}, // the dock's window
		{"pointer", `{"action":"click","x":200,"y":1060}`}, // the dock bar
		{"keyboard", `{"action":"type","text":"yes","window":"0xdock"}`},
	} {
		if dv, _ := deskCall(m, c.tool, c.args); dv.Behavior != "deny" {
			t.Errorf("%s %s reached the dock: %+v", c.tool, c.args, dv)
		}
	}
	// An app under the outline: allowed.
	if dv, _ := deskCall(m, "pointer", `{"action":"click","x":1200,"y":300}`); dv.Behavior != "allow" {
		t.Fatalf("a click on an app: %+v", dv)
	}
	if dm := m.deskMission("m1"); dm.Ended != "" || len(dm.Protected) != 2 {
		t.Fatalf("the dock leaked into the mission's own record: ended %q, protected %v", dm.Ended, dm.Protected)
	}
	// The pid is no longer the process that connected: nothing of it is
	// protected (and its outline is no longer exempt, so a click under it
	// is refused by nothing in a task, which checks no other layers).
	start = "99999"
	if dv, _ := deskCall(m, "pointer", `{"action":"click","x":1100,"y":700}`); dv.Behavior != "allow" {
		t.Fatalf("a stale console pid still protects: %+v", dv)
	}
}
