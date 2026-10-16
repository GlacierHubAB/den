package desktop

import (
	"encoding/json"
	"strings"
	"testing"
)

// taskDesk is a desktop where one ghostty process (pid 500) runs Den's
// window, the human's shell and an agent terminal, and Den's agent view
// is a fourth window: only the two protected addresses are off limits.
func taskDesk() (Mission, Snapshot) {
	s := Snapshot{
		Monitors: []Monitor{{Name: "DP-1", X: 0, Y: 0, W: 2000, H: 1000, Workspace: 1}},
		Windows: []Window{
			{Address: "0xden", Class: "com.mitchellh.ghostty", PID: 500, Workspace: 1, At: [2]int{0, 0}, Size: [2]int{500, 1000}, Mapped: true},
			{Address: "0xshell", Class: "com.mitchellh.ghostty", PID: 500, Workspace: 1, At: [2]int{500, 0}, Size: [2]int{500, 1000}, Mapped: true},
			{Address: "0xview", Class: "io.glacierhub.den.desktop", PID: 700, Workspace: 1, At: [2]int{1000, 0}, Size: [2]int{400, 300}, Floating: true, Mapped: true},
			{Address: "0xapp", Class: "zen", PID: 900, Workspace: 1, At: [2]int{1000, 300}, Size: [2]int{1000, 700}, Mapped: true},
		},
		Active: "0xshell",
		Binds: []Bind{
			{Mods: modSuper, Key: "return", Dispatcher: "exec", Arg: "ghostty"},
			{Mods: modSuper | modShift, Key: "e", Dispatcher: "exit"},
			{Mods: modSuper | modShift, Key: "r", Dispatcher: "exec", Arg: "hyprctl reload"},
			{Mods: modSuper | modCtrl, Key: "x", Dispatcher: "exec", Arg: "/home/u/.local/bin/den desktop stop"},
			{Mods: modSuper, Key: "w", Dispatcher: "killactive"},
			{Mods: modSuper, Key: "q", Dispatcher: "closewindow", Arg: "class:ghostty"},
			{Mods: modSuper, Key: "p", Dispatcher: "pass", Arg: "class:obs"},
			{Mods: modSuper, Key: "f", Dispatcher: "forcerendererreload"},
			{Mods: 0, Key: "f9", Dispatcher: "exec", Arg: "sh -c 'hyprctl keyword unbind SUPER,X'"},
		},
	}
	m := Mission{ID: "t1", Task: true, Protected: map[string]bool{"0xden": true, "0xview": true}}
	return m, s
}

func taskCall(t *testing.T, tool string, args map[string]any, m Mission, s Snapshot) Verdict {
	t.Helper()
	raw, _ := json.Marshal(args)
	return Decide(tool, raw, m, s)
}

func TestTaskInputOnDensWindowsIsRefused(t *testing.T) {
	m, s := taskDesk()
	refused := []struct {
		tool string
		args map[string]any
	}{
		{"pointer", map[string]any{"action": "click", "x": 100, "y": 100}},  // Den's window
		{"pointer", map[string]any{"action": "click", "x": 1100, "y": 100}}, // the agent view
		{"pointer", map[string]any{"action": "scroll", "x": 100, "y": 100, "scroll_dy": 3}},
		{"pointer", map[string]any{"action": "drag", "x": 1500, "y": 500, "to_x": 100, "to_y": 100}}, // ends on Den's
		{"keyboard", map[string]any{"action": "type", "text": "hi", "window": "0xden"}},
		{"click_ui", map[string]any{"name": "OK", "window": "0xview"}},
		{"hypr", map[string]any{"action": "close_window", "target": "0xden"}},
		{"hypr", map[string]any{"action": "move_window", "target": "0xview"}},
		{"hypr", map[string]any{"action": "focus_window", "target": "0xden"}},
		{"pointer", map[string]any{"action": "move", "x": 5000, "y": 100}}, // off every monitor
	}
	for _, c := range refused {
		if v := taskCall(t, c.tool, c.args, m, s); v.Kind != Deny {
			t.Errorf("%s %v allowed", c.tool, c.args)
		}
	}
	// One ghostty process runs Den's window and the human's shell: only
	// Den's address is off limits, the shell (same pid) is the agent's.
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"pointer", map[string]any{"action": "click", "x": 700, "y": 100}},
		{"keyboard", map[string]any{"action": "type", "text": "ls", "window": "0xshell"}},
		{"hypr", map[string]any{"action": "close_window", "target": "0xshell"}},
		{"pointer", map[string]any{"action": "move", "x": 100, "y": 100}}, // moving over Den's window does nothing
		{"pointer", map[string]any{"action": "click", "x": 1500, "y": 800}},
	} {
		if v := taskCall(t, c.tool, c.args, m, s); v.Kind != Allow {
			t.Errorf("%s %v refused: %s", c.tool, c.args, v.Reason)
		}
	}
	// With Den's window focused, no key, typing or bind at all.
	s.Active = "0xden"
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"keyboard", map[string]any{"action": "type", "text": "y"}},
		{"keyboard", map[string]any{"action": "key", "keys": "ctrl+y"}},
		{"keyboard", map[string]any{"action": "type", "text": "y", "window": "0xshell"}},
		{"use_bind", map[string]any{"combo": "super+return"}},
	} {
		if v := taskCall(t, c.tool, c.args, m, s); v.Kind != Deny {
			t.Errorf("%s %v allowed with Den's window focused", c.tool, c.args)
		}
	}
}

func TestTaskFixedDispatchListIsRefused(t *testing.T) {
	m, s := taskDesk()
	for _, combo := range []string{"super+shift+e", "super+shift+r", "super+ctrl+x", "super+q", "super+p", "super+f", "f9"} {
		if v := taskCall(t, "use_bind", map[string]any{"combo": combo}, m, s); v.Kind != Deny {
			t.Errorf("use_bind %s allowed", combo)
		}
		if v := taskCall(t, "keyboard", map[string]any{"action": "key", "keys": combo}, m, s); v.Kind != Deny {
			t.Errorf("keyboard key %s allowed", combo)
		}
	}
	// killactive is fine on another window, refused on Den's.
	if v := taskCall(t, "use_bind", map[string]any{"combo": "super+w"}, m, s); v.Kind != Allow {
		t.Errorf("killactive on the human's shell refused: %s", v.Reason)
	}
	// Launching apps through the human's binds is the agent's.
	if v := taskCall(t, "use_bind", map[string]any{"combo": "super+return"}, m, s); v.Kind != Allow {
		t.Errorf("super+return refused: %s", v.Reason)
	}
	if v := taskCall(t, "use_bind", map[string]any{"combo": "super+z"}, m, s); v.Kind != Deny {
		t.Error("a combo the human never bound was run as a bind")
	}
	// A bound key typed as text runs its bind: it is checked as one.
	if v := taskCall(t, "keyboard", map[string]any{"action": "key", "keys": "f9"}, m, s); v.Kind != Deny {
		t.Error("f9 (an exec of hyprctl) allowed as a key")
	}
	// Binds are read as they are at the call: a new one is checked too.
	s.Binds = append(s.Binds, Bind{Mods: modAlt, Key: "k", Dispatcher: "exec", Arg: "hyprctl dispatch exit"})
	if v := taskCall(t, "keyboard", map[string]any{"action": "key", "keys": "alt+k"}, m, s); v.Kind != Deny {
		t.Error("a bind added during the task was not checked")
	}
}

func TestTaskLaunch(t *testing.T) {
	m, s := taskDesk()
	for _, cmd := range []string{"hyprctl reload", "/usr/bin/hyprctl keyword unbind SUPER,X", "den desktop stop", "  /home/u/.local/bin/den  hook", "env FOO=1 hyprctl"} {
		if v := taskCall(t, "launch", map[string]any{"command": cmd}, m, s); v.Kind != Deny {
			t.Errorf("launch %q allowed", cmd)
		}
	}
	for _, cmd := range []string{"ghostty", "zen-browser --new-window", "nvim notes.md"} {
		if v := taskCall(t, "launch", map[string]any{"command": cmd}, m, s); v.Kind != Allow {
			t.Errorf("launch %q refused: %s", cmd, v.Reason)
		}
	}
	// An app-scoped mission still never launches.
	m.Task = false
	if v := taskCall(t, "launch", map[string]any{"command": "ghostty"}, m, s); v.Kind != Deny {
		t.Error("an app mission launched")
	}
}

func TestTaskEndsWhenDensWindowGoes(t *testing.T) {
	m, s := taskDesk()
	s.Windows = s.Windows[1:] // Den's window closed
	if v := taskCall(t, "desktop", map[string]any{}, m, s); v.Kind != Deny || !strings.Contains(v.Reason, "gone") {
		t.Errorf("Den's window gone: %+v", v)
	}
	m2, s2 := taskDesk()
	m2.Protected = nil
	if v := taskCall(t, "desktop", map[string]any{}, m2, s2); v.Kind != Deny {
		t.Error("a task with no placed Den windows ran")
	}
}

// A task mission's hypruse has no confinement; an app mission's keeps its
// workspace.
func TestTaskHypruseEnv(t *testing.T) {
	if env := strings.Join(HypruseEnv(0, "/j"), " "); strings.Contains(env, "HYPRUSE_CONFINE") || !strings.Contains(env, "HYPRUSE_STRICT=1") {
		t.Errorf("task env: %s", env)
	}
	if env := strings.Join(HypruseEnv(9, "/j"), " "); !strings.Contains(env, "HYPRUSE_CONFINE=workspace:9") {
		t.Errorf("app env: %s", env)
	}
}

func TestTaskToolLists(t *testing.T) {
	open := strings.Join(AllowedToolNames(true), ",")
	closed := strings.Join(ClosedToolNames(true), ",")
	for _, t2 := range TaskTools {
		if !strings.Contains(open, ToolName(t2)) || strings.Contains(closed, ToolName(t2)) {
			t.Errorf("%s: open %v closed %v", t2, strings.Contains(open, ToolName(t2)), strings.Contains(closed, ToolName(t2)))
		}
	}
	for _, still := range []string{"sequence", "clipboard", "wait_for"} {
		if !strings.Contains(closed, ToolName(still)) || strings.Contains(open, ToolName(still)) {
			t.Errorf("%s is not closed in task mode", still)
		}
	}
	if strings.Contains(strings.Join(AllowedToolNames(false), ","), ToolName("launch")) {
		t.Error("an app mission has launch")
	}
	for _, d := range DeniedTools(true) {
		if d == ToolName("launch") {
			t.Error("launch is denied in task mode's settings")
		}
	}
}
