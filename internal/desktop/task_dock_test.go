package desktop

import "testing"

// Drey's dock (the console, pid 1500) is off limits to a task like Den's
// own windows: its window, and its bar on the top layer. Its pointer
// outline, which takes no input, blocks nothing, and neither does a
// stranger's surface of the outline's name.
func TestTaskConsoleDockIsOffLimits(t *testing.T) {
	m, s := taskDesk()
	console := map[int]bool{1500: true}
	s.Windows = append(s.Windows, Window{Address: "0xdock", Class: "drey", PID: 1500, Workspace: 1, At: [2]int{1500, 0}, Size: [2]int{500, 300}, Mapped: true})
	s.Layers = []Layer{
		{Monitor: "DP-1", Level: 2, X: 0, Y: 950, W: 2000, H: 50, Namespace: "drey-dock", PID: 1500},
		{Monitor: "DP-1", Level: OverlayLevel, X: 0, Y: 0, W: 2000, H: 1000, Namespace: PointerNamespace, PID: 1500},
		{Monitor: "DP-1", Level: 1, X: 0, Y: 840, W: 2000, H: 160, Namespace: "agent-glow", PID: 1500},
	}
	s.ExemptConsole(console) // as Den does before every decision
	m.Protected["0xdock"] = true
	m.Console = console

	refused := []struct {
		tool string
		args map[string]any
	}{
		{"pointer", map[string]any{"action": "click", "x": 1600, "y": 100}},  // the dock's window
		{"pointer", map[string]any{"action": "click", "x": 300, "y": 975}},   // the dock bar
		{"pointer", map[string]any{"action": "scroll", "x": 300, "y": 975}},  // the dock bar
		{"keyboard", map[string]any{"action": "type", "text": "hi", "window": "0xdock"}},
		{"hypr", map[string]any{"action": "focus_window", "target": "0xdock"}},
	}
	for _, c := range refused {
		if v := taskCall(t, c.tool, c.args, m, s); v.Kind != Deny {
			t.Errorf("%s %v: %+v", c.tool, c.args, v)
		}
	}
	// Elsewhere, under the outline and above the glow: allowed.
	if v := taskCall(t, "pointer", map[string]any{"action": "click", "x": 1200, "y": 600}, m, s); v.Kind != Allow {
		t.Errorf("a click on an app under the outline: %+v", v)
	}
	// A key while the dock's window is focused: refused.
	s.Active = "0xdock"
	if v := taskCall(t, "keyboard", map[string]any{"action": "key", "keys": "Return"}, m, s); v.Kind != Deny {
		t.Errorf("a key into the focused dock: %+v", v)
	}
}
