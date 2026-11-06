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
	// Programs start only through launch, never through a bind.
	if v := taskCall(t, "use_bind", map[string]any{"combo": "super+return"}, m, s); v.Kind != Deny {
		t.Error("use_bind super+return (exec) allowed")
	}
	if v := taskCall(t, "keyboard", map[string]any{"action": "key", "keys": "super+return"}, m, s); v.Kind != Deny {
		t.Error("keyboard key super+return (exec) allowed")
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

// Hyprland runs every bind on a combo: a harmless one first does not let a
// refused one after it through (T3). Keys are one combo (T2); the
// system's combos are refused (T1).
func TestTaskEveryBindOnACombo(t *testing.T) {
	m, s := taskDesk()
	s.Binds = append(s.Binds,
		Bind{Mods: modSuper, Key: "m", Dispatcher: "exec", Arg: "notify-send bye"},
		Bind{Mods: modSuper, Key: "m", Dispatcher: "exit"},
	)
	if v := taskCall(t, "use_bind", map[string]any{"combo": "super+m"}, m, s); v.Kind != Deny {
		t.Error("use_bind super+m ran exit behind a harmless bind")
	}
	if v := taskCall(t, "keyboard", map[string]any{"action": "key", "keys": "super+m"}, m, s); v.Kind != Deny {
		t.Error("key super+m ran exit behind a harmless bind")
	}
	for _, keys := range []string{"ctrl+a super+shift+e", "ctrl+a,super+shift+e", "super", "ctrl+", "foo+e", "ctrl+alt+delete", "ctrl+alt+del", "ctrl+alt+f2", "ctrl+alt+backspace"} {
		if v := taskCall(t, "keyboard", map[string]any{"action": "key", "keys": keys}, m, s); v.Kind != Deny {
			t.Errorf("keys %q allowed", keys)
		}
	}
	for _, keys := range []string{"ctrl+shift+t", "escape", "ctrl + c", "alt+tab", "f5"} {
		if v := taskCall(t, "keyboard", map[string]any{"action": "key", "keys": keys}, m, s); v.Kind != Allow {
			t.Errorf("keys %q refused: %s", keys, v.Reason)
		}
	}
	if v := taskCall(t, "launch", map[string]any{"command": `bash -c "x;hyprctl dispatch exit"`}, m, s); v.Kind != Deny {
		t.Error("launch through bash -c with hyprctl allowed")
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

// A task's pointer glides there, kept to the monitor that holds the
// point: its steps never leave the screen, whichever monitor it starts on.
func TestTaskPointerGlidesOnTheTargetsMonitor(t *testing.T) {
	m, s := taskDesk()
	s.Monitors = append(s.Monitors, Monitor{Name: "HDMI-A-1", X: 2000, Y: 0, W: 1280, H: 720, Workspace: 2})
	for _, c := range []struct {
		args map[string]any
		want Rect
	}{
		{map[string]any{"action": "move", "x": 100, "y": 100}, Rect{X: 0, Y: 0, W: 2000, H: 1000}},
		{map[string]any{"action": "click", "x": 1500, "y": 800}, Rect{X: 0, Y: 0, W: 2000, H: 1000}},
		{map[string]any{"action": "scroll", "x": 2100, "y": 100, "scroll_dy": 3}, Rect{X: 2000, Y: 0, W: 1280, H: 720}},
		{map[string]any{"action": "drag", "x": 2100, "y": 100, "to_x": 1500, "to_y": 800}, Rect{X: 2000, Y: 0, W: 1280, H: 720}},
	} {
		v := taskCall(t, "pointer", c.args, m, s)
		if v.Kind != Allow || v.Bounds == nil || *v.Bounds != c.want {
			t.Errorf("%v: %v %s, bounds %+v", c.args, v.Kind, v.Reason, v.Bounds)
		}
	}
}

// Task mode runs a bind only when its dispatcher is on Den's short list;
// anything else, a Lua function's or a plugin's included, is refused on
// every route a call can reach it by.
func TestTaskBindsRunOnlyListedDispatchers(t *testing.T) {
	m, s := taskDesk()
	s.Binds = append(s.Binds,
		Bind{Mods: modSuper, Key: "1", Dispatcher: "workspace", Arg: "1"},
		Bind{Mods: modSuper, Key: "g", Dispatcher: "global", Arg: "app:action"},
		Bind{Mods: modSuper, Key: "e", Dispatcher: "execr", Arg: "nautilus"},
		Bind{Mods: modSuper, Key: "y", Dispatcher: "hy3:makegroup", Arg: "h"},
		Bind{Mods: modSuper, Key: "s", Dispatcher: "submap", Arg: "resize"},
		Bind{Mods: modSuper, Key: "o", Dispatcher: "focuswindow", Arg: "class:ghostty"},
		Bind{Mods: modSuper, Key: "i", Dispatcher: "focuswindow", Arg: "address:0xshell"},
		Bind{Mods: modSuper, Key: "u", Dispatcher: "movetoworkspace", Arg: "3,class:ghostty"},
	)
	for _, combo := range []string{"super+g", "super+e", "super+y", "super+s", "super+o", "super+u"} {
		if v := taskCall(t, "use_bind", map[string]any{"combo": combo}, m, s); v.Kind != Deny {
			t.Errorf("use_bind %s allowed", combo)
		}
		if v := taskCall(t, "keyboard", map[string]any{"action": "key", "keys": combo}, m, s); v.Kind != Deny {
			t.Errorf("keyboard key %s allowed", combo)
		}
	}
	for _, combo := range []string{"super+1", "super+i", "super+w"} {
		if v := taskCall(t, "use_bind", map[string]any{"combo": combo}, m, s); v.Kind != Allow {
			t.Errorf("use_bind %s refused: %s", combo, v.Reason)
		}
	}
	// A combo that hits none of the human's binds is an app's: it passes.
	if v := taskCall(t, "keyboard", map[string]any{"action": "key", "keys": "ctrl+c"}, m, s); v.Kind != Allow {
		t.Errorf("ctrl+c refused: %s", v.Reason)
	}
}

// Hyprland's Lua config lists every bind as "__lua" with a function id:
// Den can't tell what it runs, so a task never runs one.
func TestTaskLuaBindsAreNotRun(t *testing.T) {
	m, s := taskDesk()
	s.Binds = []Bind{
		{Mods: modSuper, Key: "return", Dispatcher: "__lua", Arg: "5"},
		{Mods: 0, Key: "x", Dispatcher: "__lua", Arg: "9"},
		{Mods: 0, Key: "mouse:274", Dispatcher: "__lua", Arg: "11"},
		{Mods: 0, Key: "mouse_down", Dispatcher: "__lua", Arg: "12"},
	}
	if v := taskCall(t, "use_bind", map[string]any{"combo": "super+return"}, m, s); v.Kind != Deny {
		t.Error("use_bind on a Lua bind allowed")
	}
	if v := taskCall(t, "keyboard", map[string]any{"action": "key", "keys": "super+return"}, m, s); v.Kind != Deny {
		t.Error("keyboard key on a Lua bind allowed")
	}
	if v := taskCall(t, "keyboard", map[string]any{"action": "type", "text": "fix", "window": "0xshell"}, m, s); v.Kind != Deny {
		t.Error("typed text hitting an unmodified Lua bind allowed")
	}
	if v := taskCall(t, "keyboard", map[string]any{"action": "type", "text": "hello", "window": "0xshell"}, m, s); v.Kind != Allow {
		t.Errorf("typed text hitting no bind refused: %s", v.Reason)
	}
	if v := taskCall(t, "pointer", map[string]any{"action": "click", "x": 1500, "y": 800, "button": "middle"}, m, s); v.Kind != Deny {
		t.Error("a middle click on an unmodified Lua mouse bind allowed")
	}
	if v := taskCall(t, "pointer", map[string]any{"action": "click", "x": 1500, "y": 800}, m, s); v.Kind != Allow {
		t.Errorf("a left click with no bind on it refused: %s", v.Reason)
	}
	if v := taskCall(t, "pointer", map[string]any{"action": "scroll", "x": 1500, "y": 800, "scroll_dy": 2}, m, s); v.Kind != Deny {
		t.Error("a scroll on an unmodified Lua wheel bind allowed")
	}
	if v := taskCall(t, "click_ui", map[string]any{"name": "OK", "window": "0xshell", "button": "middle"}, m, s); v.Kind != Deny {
		t.Error("click_ui with a middle click on a Lua mouse bind allowed")
	}
}

// Typed Return and Tab are keys too: an unmodified bind on them is checked.
func TestTaskTypedReturnAndTabAreChecked(t *testing.T) {
	m, s := taskDesk()
	s.Binds = append(s.Binds, Bind{Mods: 0, Key: "return", Dispatcher: "exec", Arg: "x"}, Bind{Mods: 0, Key: "tab", Dispatcher: "exec", Arg: "y"})
	for _, text := range []string{"ls\n", "a\tb"} {
		if v := taskCall(t, "keyboard", map[string]any{"action": "type", "text": text, "window": "0xshell"}, m, s); v.Kind != Deny {
			t.Errorf("typing %q allowed", text)
		}
	}
}

// With no binds read, Den can't check a combo, so none runs.
func TestTaskCombosNeedTheBinds(t *testing.T) {
	m, s := taskDesk()
	s.Binds = nil
	if v := taskCall(t, "use_bind", map[string]any{"combo": "super+w"}, m, s); v.Kind != Deny {
		t.Error("use_bind allowed with no binds read")
	}
	if v := taskCall(t, "keyboard", map[string]any{"action": "key", "keys": "ctrl+c"}, m, s); v.Kind != Deny {
		t.Error("keyboard key allowed with no binds read")
	}
}

// The dispatcher list is small and literal; a change to it is a review.
func TestTaskDispatchListIsPinned(t *testing.T) {
	want := map[string]dispatchArg{
		"workspace": argView, "togglespecialworkspace": argView, "focusmonitor": argView,
		"movefocus": argView, "cyclenext": argView, "focusurgentorlast": argView, "focuscurrentorlast": argView,
		"killactive": argActive, "forcekillactive": argActive, "fullscreen": argActive, "fullscreenstate": argActive,
		"centerwindow": argActive, "resizeactive": argActive, "moveactive": argActive, "swapnext": argActive,
		"movewindow": argActive, "swapwindow": argActive, "moveintogroup": argActive, "layoutmsg": argActive,
		"togglesplit": argActive, "swapsplit": argActive, "splitratio": argActive, "togglegroup": argActive,
		"changegroupactive": argActive, "lockactivegroup": argActive,
		"togglefloating": argWindow, "setfloating": argWindow, "settiled": argWindow, "pin": argWindow,
		"pseudo": argWindow, "moveoutofgroup": argWindow,
		"movetoworkspace": argWorkspaceWindow, "movetoworkspacesilent": argWorkspaceWindow,
		"focuswindow": argAddress, "closewindow": argAddress, "killwindow": argAddress,
	}
	if len(taskDispatch) != len(want) {
		t.Fatalf("taskDispatch has %d entries, want %d", len(taskDispatch), len(want))
	}
	for d, k := range want {
		if got, ok := taskDispatch[d]; !ok || got != k {
			t.Errorf("taskDispatch[%q] = %v, %v; want %v", d, got, ok, k)
		}
	}
	for _, d := range []string{"exec", "execr", "global", "__lua", "pass", "sendshortcut", "sendkeystate", "exit", "submap", "dpms", "signal", "forcerendererreload"} {
		if _, ok := taskDispatch[d]; ok {
			t.Errorf("%s is on the list", d)
		}
	}
}

// A window in a bind's argument is taken only as address:0x… of a window
// that exists and isn't Den's: a bare word is a class regex to Hyprland.
func TestTaskBindWindowArgsTakeOnlyAnAddress(t *testing.T) {
	m, s := taskDesk()
	other := s.Windows[len(s.Windows)-1].Address
	cases := []struct {
		d, arg string
		ok     bool
	}{
		{"movetoworkspacesilent", "special,den", false},
		{"togglefloating", "den", false},
		{"togglefloating", "floating", false},
		{"pin", "activewindow", false},
		{"movetoworkspace", "3,class:ghostty", false},
		{"movetoworkspace", "3,address:0xden", false},
		{"movetoworkspace", "3,address:0xdead", false},
		{"focuswindow", "title:Den", false},
		{"closewindow", "address:0xview", false},
		{"movetoworkspace", "3", true},
		{"movetoworkspace", "3,address:" + other, true},
		{"togglefloating", "", true},
		{"togglefloating", "address:" + other, true},
		{"focuswindow", "address:" + other, true},
	}
	for _, c := range cases {
		s.Binds = []Bind{{Mods: modSuper, Key: "k", Dispatcher: c.d, Arg: c.arg}}
		v := taskCall(t, "use_bind", map[string]any{"combo": "super+k"}, m, s)
		if (v.Kind == Allow) != c.ok {
			t.Errorf("%s %q: %v (%s), want allowed=%v", c.d, c.arg, v.Kind, v.Reason, c.ok)
		}
	}
	// With Den's window focused, a bind on the focused window is refused.
	s.Active = "0xden"
	s.Binds = []Bind{{Mods: modSuper, Key: "k", Dispatcher: "togglesplit"}}
	if v := taskCall(t, "use_bind", map[string]any{"combo": "super+k"}, m, s); v.Kind != Deny {
		t.Error("togglesplit allowed with Den's window focused")
	}
}

// Typing never runs a bind, even one the list would allow, and keys are
// matched by their keysym names.
func TestTaskTypingNeverRunsABind(t *testing.T) {
	m, s := taskDesk()
	cases := []struct {
		bind Bind
		text string
	}{
		{Bind{Key: "n", Dispatcher: "cyclenext"}, "pwn"},
		{Bind{Key: "minus", Dispatcher: "workspace", Arg: "-1"}, "a-b"},
		{Bind{Key: "period", Dispatcher: "exec", Arg: "x"}, "end."},
		{Bind{Key: "slash", Dispatcher: "exec", Arg: "x"}, "a/b"},
		{Bind{Mods: modShift, Key: "1", Dispatcher: "exec", Arg: "x"}, "hi!"},
		{Bind{Key: "odiaeresis", Dispatcher: "exec", Arg: "x"}, "höst"},
		{Bind{Key: "odiaeresis", Dispatcher: "exec", Arg: "x"}, "a,b"},
		{Bind{Key: "code:38", Dispatcher: "exec", Arg: "x"}, "x"},
	}
	for _, c := range cases {
		s.Binds = append([]Bind(nil), c.bind)
		if v := taskCall(t, "keyboard", map[string]any{"action": "type", "text": c.text, "window": "0xshell"}, m, s); v.Kind != Deny {
			t.Errorf("typing %q with a bind on %q allowed", c.text, c.bind.Key)
		}
	}
	// A bind on a key no text reaches, or with a modifier, leaves typing alone.
	s.Binds = []Bind{{Key: "xf86audioraisevolume", Dispatcher: "__lua"}, {Key: "super_l", Dispatcher: "__lua"}, {Mods: modSuper, Key: "minus", Dispatcher: "exec"}}
	if v := taskCall(t, "keyboard", map[string]any{"action": "type", "text": "a-b, c./d!", "window": "0xshell"}, m, s); v.Kind != Allow {
		t.Errorf("typing refused with no unmodified bind on its keys: %s", v.Reason)
	}
	// A combo spelled with the symbol matches a bind on its keysym name.
	s.Binds = []Bind{{Mods: modCtrl, Key: "minus", Dispatcher: "exec", Arg: "x"}}
	if v := taskCall(t, "keyboard", map[string]any{"action": "key", "keys": "ctrl+-"}, m, s); v.Kind != Deny {
		t.Error("ctrl+- allowed with an exec bind on ctrl+minus")
	}
}

// Layer surfaces aren't a task's: no click, drag or scroll lands on one,
// and keys wait while a surface that opened during the task is up.
func TestTaskLayerSurfacesAreNotATasks(t *testing.T) {
	m, s := taskDesk()
	s.Layers = []Layer{
		{Address: "0xbg", Monitor: "DP-1", Level: 0, X: 0, Y: 0, W: 2000, H: 1200, Namespace: "wallpaper", PID: 70},
		{Address: "0xbar", Monitor: "DP-1", Level: 2, X: 0, Y: 0, W: 2000, H: 26, Namespace: "omarchy-bar", PID: 71},
		{Address: "0xnote", Monitor: "DP-1", Level: 3, X: 1400, Y: 700, W: 300, H: 120, Namespace: "notifications", PID: 72},
	}
	m.LayersAtStart = map[string]bool{"0xbg": true, "0xbar": true, "0xnote": true}
	for _, c := range []map[string]any{
		{"action": "click", "x": 700, "y": 10},                    // the bar, over the shell
		{"action": "scroll", "x": 1500, "y": 800, "scroll_dy": 2}, // a notification
		{"action": "drag", "x": 1500, "y": 500, "to_x": 700, "to_y": 10},
	} {
		if v := taskCall(t, "pointer", c, m, s); v.Kind != Deny {
			t.Errorf("pointer %v on a layer surface allowed", c)
		}
	}
	if v := taskCall(t, "click_ui", map[string]any{"name": "Open", "window": "0xbar"}, m, s); v.Kind != Deny {
		t.Error("click_ui on a layer surface allowed")
	}
	// The wallpaper lies under every window: a click on a window is the
	// window's, a click where no window is lands on the wallpaper.
	if v := taskCall(t, "pointer", map[string]any{"action": "click", "x": 1500, "y": 500}, m, s); v.Kind != Allow {
		t.Errorf("a click on a window above the wallpaper refused: %s", v.Reason)
	}
	s.Windows = s.Windows[:3] // the app's window closes: the wallpaper is bare there
	if v := taskCall(t, "pointer", map[string]any{"action": "click", "x": 1500, "y": 500}, m, s); v.Kind != Deny || !strings.Contains(v.Reason, "wallpaper") {
		t.Errorf("a click on the bare wallpaper: %v %s", v.Kind, v.Reason)
	}

	// Keys wait while a launcher that opened during the task is up.
	m, s = taskDesk()
	s.Layers = []Layer{{Address: "0xbar", Level: 2, W: 2000, H: 26, Namespace: "omarchy-bar", PID: 71}}
	m.LayersAtStart = map[string]bool{"0xbar": true}
	if v := taskCall(t, "keyboard", map[string]any{"action": "type", "text": "ls", "window": "0xshell"}, m, s); v.Kind != Allow {
		t.Errorf("typing with only the start's bar up refused: %s", v.Reason)
	}
	s.Layers = append(s.Layers, Layer{Address: "0xlaunch", Level: 3, X: 600, Y: 300, W: 600, H: 400, Namespace: "walker", PID: 73})
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"keyboard", map[string]any{"action": "type", "text": "ls", "window": "0xshell"}},
		{"keyboard", map[string]any{"action": "key", "keys": "ctrl+c", "window": "0xshell"}},
		{"use_bind", map[string]any{"combo": "super+w"}},
	} {
		if v := taskCall(t, c.tool, c.args, m, s); v.Kind != Deny {
			t.Errorf("%s %v allowed with a launcher up", c.tool, c.args)
		}
	}
	// With no start list, every top surface counts as new: keys wait.
	m.LayersAtStart = nil
	s.Layers = s.Layers[:1]
	if v := taskCall(t, "keyboard", map[string]any{"action": "type", "text": "ls", "window": "0xshell"}, m, s); v.Kind != Deny {
		t.Error("typing allowed with no start list of surfaces")
	}
}
