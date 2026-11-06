package desktop

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// scopedDesk is taskDesk scoped, with a second monitor holding a picked
// window, an outside one with a launched floating window over it, and an
// X window of a slice process.
func scopedDesk() (Mission, Snapshot) {
	m, s := taskDesk()
	s.Monitors = append(s.Monitors, Monitor{Name: "DP-2", X: 2000, Y: 0, W: 2000, H: 1000, Workspace: 2})
	s.Windows = append(s.Windows,
		Window{Address: "0xpick", Class: "org.gnome.Nautilus", PID: 950, Workspace: 2, At: [2]int{2000, 0}, Size: [2]int{500, 1000}, Mapped: true},
		Window{Address: "0xother", Class: "signal", PID: 960, Workspace: 2, At: [2]int{2500, 0}, Size: [2]int{500, 1000}, Mapped: true},
		Window{Address: "0xpop", Class: "zen", PID: 900, Workspace: 2, At: [2]int{2600, 100}, Size: [2]int{100, 100}, Floating: true, Mapped: true},
		Window{Address: "0xx", Class: "xterm", PID: 901, Workspace: 2, At: [2]int{3000, 0}, Size: [2]int{500, 1000}, Mapped: true, XWayland: true},
	)
	s.Active = "0xapp"
	m.Scoped = true
	m.InSlice = func(pid int) bool { return pid == 900 || pid == 901 }
	m.Picked = map[string]Pick{"0xpick": {PID: 950}}
	return m, s
}

func TestScopedSet(t *testing.T) {
	m, s := scopedDesk()
	want := map[string]bool{"0xden": false, "0xshell": false, "0xview": false, "0xapp": true, "0xpick": true, "0xother": false, "0xpop": true, "0xx": false}
	for _, w := range s.Windows {
		if got := m.InSet(w); got != want[w.Address] {
			t.Errorf("InSet(%s) = %v, want %v", w.Address, got, want[w.Address])
		}
	}
	// A picked address back on another process is another window.
	if m.InSet(Window{Address: "0xpick", PID: 951}) {
		t.Error("a picked address on a new pid is in the set")
	}
	// A pick of Den's own is never honoured, nor is a slice with no reader.
	m.Picked["0xden"] = Pick{PID: 500}
	if w, _ := s.Window("0xden"); m.InSet(w) {
		t.Error("Den's window, picked, is in the set")
	}
	m.InSlice = nil
	if w, _ := s.Window("0xapp"); m.InSet(w) {
		t.Error("a launched window is in the set with no slice reader")
	}
}

// Every input tool is refused outside the set and allowed inside it.
func TestScopedGate(t *testing.T) {
	cases := []struct {
		tool string
		args map[string]any
		ok   bool
	}{
		{"keyboard", map[string]any{"action": "type", "text": "hi", "window": "0xapp"}, true},
		{"keyboard", map[string]any{"action": "type", "text": "hi", "window": "0xpick"}, true},
		{"keyboard", map[string]any{"action": "type", "text": "hi", "window": "0xother"}, false},
		{"keyboard", map[string]any{"action": "type", "text": "hi", "window": "0xshell"}, false}, // an agent's terminal
		{"keyboard", map[string]any{"action": "type", "text": "hi", "window": "0xx"}, false},
		{"keyboard", map[string]any{"action": "key", "keys": "ctrl+c", "window": "0xother"}, false},
		{"keyboard", map[string]any{"action": "key", "keys": "ctrl+c", "window": "0xapp"}, true},
		{"keyboard", map[string]any{"action": "type", "text": "hi"}, true}, // the active window, 0xapp
		{"pointer", map[string]any{"action": "click", "x": 1500, "y": 500}, true},
		{"pointer", map[string]any{"action": "click", "x": 2200, "y": 500}, true},
		{"pointer", map[string]any{"action": "click", "x": 2800, "y": 500}, false},
		{"pointer", map[string]any{"action": "click", "x": 2650, "y": 150}, false}, // the popup over an outside window
		{"pointer", map[string]any{"action": "click", "x": 3200, "y": 500}, false},
		{"pointer", map[string]any{"action": "click", "x": 3800, "y": 500}, false}, // no window there
		{"pointer", map[string]any{"action": "scroll", "x": 2800, "y": 500, "scroll_dy": 1}, false},
		{"pointer", map[string]any{"action": "drag", "x": 1500, "y": 500, "to_x": 2800, "to_y": 500}, false},
		{"pointer", map[string]any{"action": "drag", "x": 1500, "y": 500, "to_x": 2200, "to_y": 500}, true},
		{"pointer", map[string]any{"action": "move", "x": 2800, "y": 500}, true},
		{"click_ui", map[string]any{"name": "OK", "window": "0xapp"}, true},
		{"click_ui", map[string]any{"name": "OK", "window": "0xother"}, false},
		{"click_ui", map[string]any{"name": "OK", "window": "0xx"}, false},
		{"hypr", map[string]any{"action": "focus_window", "target": "0xpick"}, true},
		{"hypr", map[string]any{"action": "focus_window", "target": "0xother"}, false},
		{"hypr", map[string]any{"action": "close_window", "target": "0xshell"}, false},
		{"hypr", map[string]any{"action": "workspace", "workspace": "2"}, true},
		{"use_bind", map[string]any{"combo": "super+w"}, true},
		{"screenshot", map[string]any{}, true},
		{"launch", map[string]any{"command": "zen"}, true},
	}
	for _, c := range cases {
		m, s := scopedDesk()
		v := taskCall(t, c.tool, c.args, m, s)
		if (v.Kind == Allow) != c.ok {
			t.Errorf("%s %v: %v (%s), want allowed=%v", c.tool, c.args, v.Kind, v.Reason, c.ok)
		}
	}
}

// A bind runs only with a set window focused, and never names an outside
// window.
func TestScopedBinds(t *testing.T) {
	m, s := scopedDesk()
	s.Active = "0xother"
	if v := taskCall(t, "use_bind", map[string]any{"combo": "super+w"}, m, s); v.Kind != Deny {
		t.Error("use_bind allowed with an outside window focused")
	}
	s.Active = "0xx"
	if v := taskCall(t, "use_bind", map[string]any{"combo": "super+w"}, m, s); v.Kind != Deny {
		t.Error("use_bind allowed with an X window focused")
	}
	s.Active = "0xapp"
	for _, c := range []struct {
		b  Bind
		ok bool
	}{
		{Bind{Mods: modSuper, Key: "k", Dispatcher: "focuswindow", Arg: "address:0xother"}, false},
		{Bind{Mods: modSuper, Key: "k", Dispatcher: "focuswindow", Arg: "address:0xpick"}, true},
		{Bind{Mods: modSuper, Key: "k", Dispatcher: "movetoworkspace", Arg: "2,address:0xother"}, false},
		{Bind{Mods: modSuper, Key: "k", Dispatcher: "swapwindow", Arg: "address:0xother"}, false},
		{Bind{Mods: modSuper, Key: "k", Dispatcher: "swapwindow", Arg: "address:0xpop"}, true},
		{Bind{Mods: modSuper, Key: "k", Dispatcher: "swapwindow", Arg: "l"}, true},
		{Bind{Mods: modSuper, Key: "k", Dispatcher: "workspace", Arg: "2"}, true},
	} {
		s.Binds = []Bind{c.b}
		v := taskCall(t, "use_bind", map[string]any{"combo": "super+k"}, m, s)
		if (v.Kind == Allow) != c.ok {
			t.Errorf("%s %s: %v (%s), want allowed=%v", c.b.Dispatcher, c.b.Arg, v.Kind, v.Reason, c.ok)
		}
	}
	// A keyboard key whose bind names an outside window is refused, even
	// into a set window.
	s.Binds = []Bind{{Mods: modCtrl, Key: "k", Dispatcher: "closewindow", Arg: "address:0xother"}}
	if v := taskCall(t, "keyboard", map[string]any{"action": "key", "keys": "ctrl+k", "window": "0xapp"}, m, s); v.Kind != Deny {
		t.Error("a key whose bind closes an outside window allowed")
	}
}

// Scoping only narrows: whatever a scoped task may do, the same task
// unscoped may do too.
func TestScopedNeverWidens(t *testing.T) {
	tools := []struct {
		tool string
		args map[string]any
	}{
		{"keyboard", map[string]any{"action": "type", "text": "hi", "window": "0xden"}},
		{"keyboard", map[string]any{"action": "key", "keys": "super+return", "window": "0xapp"}},
		{"pointer", map[string]any{"action": "click", "x": 100, "y": 100}},
		{"click_ui", map[string]any{"name": "OK", "window": "0xview"}},
		{"use_bind", map[string]any{"combo": "super+return"}},
		{"launch", map[string]any{"command": "hyprctl reload"}},
	}
	for _, c := range tools {
		m, s := scopedDesk()
		m.Picked["0xden"] = Pick{PID: 500}
		m.InSlice = func(int) bool { return true }
		if v := taskCall(t, c.tool, c.args, m, s); v.Kind == Allow {
			t.Errorf("%s %v allowed when scoped", c.tool, c.args)
		}
	}
}

// A pointer verdict names the window it lands on, scoped or not.
func TestTaskPointerCarriesTarget(t *testing.T) {
	for _, scoped := range []bool{false, true} {
		m, s := scopedDesk()
		m.Scoped = scoped
		v := taskCall(t, "pointer", map[string]any{"action": "click", "x": 1500, "y": 500}, m, s)
		if v.Kind != Allow || v.Target == nil || v.Target.Address != "0xapp" {
			t.Errorf("scoped=%v: %v %s target %v", scoped, v.Kind, v.Reason, v.Target)
		}
		if !scoped {
			v = taskCall(t, "pointer", map[string]any{"action": "click", "x": 2650, "y": 150}, m, s)
			if v.Target == nil || v.Target.Address != "0xpop" {
				t.Errorf("a click on a floating window names %v", v.Target)
			}
		}
	}
}

// Hyprland not saying a window is Wayland makes it an X window.
func TestUnstatedXWaylandIsX(t *testing.T) {
	old := hyprctl
	t.Cleanup(func() { hyprctl = old })
	hyprctl = fakeHyprctl(map[string]string{
		"clients":      `[{"address":"0xa","class":"a","pid":900,"workspace":{"id":1},"xwayland":false},{"address":"0xb","class":"b","pid":900,"workspace":{"id":1}}]`,
		"monitors":     `[]`,
		"activewindow": `{}`,
		"layers":       `{}`,
		"binds":        `[]`,
	})
	s, err := ReadSnapshot(t.Context())
	if err != nil && !strings.Contains(err.Error(), "monitor") {
		t.Fatal(err)
	}
	for _, w := range s.Windows {
		if w.XWayland != (w.Address == "0xb") {
			t.Errorf("%s: XWayland = %v", w.Address, w.XWayland)
		}
	}
}

// fakeHyprctl answers hyprctl by its first argument.
func fakeHyprctl(out map[string]string) func(context.Context, ...string) ([]byte, error) {
	return func(_ context.Context, args ...string) ([]byte, error) {
		o, ok := out[args[0]]
		if !ok {
			return nil, errors.New("no " + args[0])
		}
		return []byte(o), nil
	}
}
