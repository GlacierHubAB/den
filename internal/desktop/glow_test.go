package desktop

import "testing"

// The console's overlay counts only when a proved console process draws
// it, as drey-agent-pointer on the overlay level, on every monitor.
func TestConsoleGlow(t *testing.T) {
	console := map[int]bool{1500: true}
	mons := []Monitor{{Name: "DP-1"}, {Name: "DP-2"}}
	own := func(mon string) Layer {
		return Layer{Monitor: mon, Level: OverlayLevel, Namespace: PointerNamespace, PID: 1500}
	}
	for _, c := range []struct {
		why    string
		layers []Layer
		want   bool
	}{
		{"on both monitors", []Layer{own("DP-1"), own("DP-2")}, true},
		{"on one monitor only", []Layer{own("DP-1")}, false},
		{"another process", []Layer{own("DP-1"), {Monitor: "DP-2", Level: OverlayLevel, Namespace: PointerNamespace, PID: 900}}, false},
		{"a test instance", []Layer{own("DP-1"), {Monitor: "DP-2", Level: OverlayLevel, Namespace: "drey-test-agent-pointer", PID: 1500}}, false},
		{"the bottom level", []Layer{own("DP-1"), {Monitor: "DP-2", Level: 1, Namespace: PointerNamespace, PID: 1500}}, false},
		{"no real process", []Layer{{Monitor: "DP-1", Level: OverlayLevel, Namespace: PointerNamespace, PID: 1}, own("DP-2")}, false},
	} {
		if got := (Snapshot{Monitors: mons, Layers: c.layers}).ConsoleGlow(console); got != c.want {
			t.Errorf("%s: %v, want %v", c.why, got, c.want)
		}
	}
	if (Snapshot{Monitors: mons, Layers: []Layer{own("DP-1"), own("DP-2")}}).ConsoleGlow(nil) {
		t.Error("an overlay with no console proved")
	}
	if (Snapshot{Layers: []Layer{own("DP-1")}}).ConsoleGlow(console) {
		t.Error("no monitors read")
	}
}
