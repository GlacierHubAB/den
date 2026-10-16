package desktop

import (
	"path/filepath"
	"regexp"
	"strings"
)

// Task mode. A task mission has the whole desktop: any window, workspace,
// monitor or layer, and it may launch apps and press the human's binds,
// as the human could at the keyboard. Den keeps a short fixed list off
// limits, each refused silently, with no prompt (sec's F1):
//
//   - Den's own surfaces, by Hyprland address from Den's records (never a
//     title or a pid: one ghostty process runs every window): the window
//     Den runs in and the agent view. No click, drag or scroll lands on
//     them, no key goes to them, no window action targets them. When one
//     is focused, no key, bind or typing at all.
//   - Binds whose dispatcher ends the human's control: exit,
//     forcerendererreload, any hyprctl through exec (reload, keyword,
//     unbind…), the panic stop's bind, closing or killing while Den's
//     window is active or by naming it, and sending keys to another
//     window (pass, sendshortcut, sendkeystate).
//   - Launching hyprctl or den directly (sec's P3).
//
// Binds are checked as Hyprland has them at that call (the snapshot is
// read for each call), so a bind added from a launched terminal is checked
// too (P2). Typing is re-checked per piece by the proxy (P1).

// Protects reports whether a window is one of Den's own surfaces.
func (m Mission) Protects(addr string) bool { return addr != "" && m.Protected[addr] }

// decideTask is the gate for a task mission's calls.
func decideTask(tool string, args map[string]any, m Mission, s Snapshot) Verdict {
	if len(m.Protected) == 0 {
		return deny("Den has not placed its own windows; the task cannot run")
	}
	for addr := range m.Protected {
		if _, ok := s.Window(addr); !ok {
			return deny("Den's own window %s is gone; the task ends", addr)
		}
	}
	switch tool {
	case "request_approval", "your_turn":
		return Verdict{Kind: Ask, Args: args}
	case "desktop", "binds":
		return Verdict{Kind: Allow, Args: args}
	case "screenshot", "zoom", "ui", "marks":
		// Reading the screen is the agent's (F6); a window it names must
		// still exist.
		if addr := str(args, "window"); addr != "" && addr != "active" {
			if _, ok := s.Window(addr); !ok {
				return deny("%s: no window %s", tool, addr)
			}
		}
		return Verdict{Kind: Allow, Args: args}
	case "pointer":
		return taskPointer(args, m, s)
	case "keyboard":
		return taskKeyboard(args, m, s)
	case "click_ui":
		args["then"] = "none"
		addr := str(args, "window")
		if addr == "" || addr == "active" {
			addr = s.Active
		}
		if m.Protects(addr) {
			return deny("click_ui: that is one of Den's own windows")
		}
		if _, ok := s.Window(addr); !ok {
			return deny("click_ui: no window %s", addr)
		}
		args["window"] = addr
		return Verdict{Kind: Allow, Args: args}
	case "hypr":
		return taskHypr(args, m, s)
	case "launch":
		cmd := str(args, "command")
		if strings.TrimSpace(cmd) == "" {
			return deny("launch needs a command")
		}
		if why := refusedLaunch(cmd); why != "" {
			return deny("launch: %s", why)
		}
		return Verdict{Kind: Allow, Args: args}
	case "use_bind":
		args["then"] = "none"
		if m.Protects(s.Active) {
			return deny("use_bind: one of Den's own windows is focused; focus another window first")
		}
		combo := str(args, "combo")
		if why := oneCombo(combo); why != "" {
			return deny("use_bind: %s", why)
		}
		bs := findBinds(combo, s.Binds)
		if len(bs) == 0 {
			return deny("use_bind: the human has no bind %q", combo)
		}
		if why := refusedBinds(bs, m, s); why != "" {
			return deny("use_bind: %s", why)
		}
		return Verdict{Kind: Allow, Args: args}
	}
	return deny("%s is not a desktop agent's tool", tool)
}

// onScreen reports whether a point lies on a monitor.
func onScreen(x, y float64, s Snapshot) bool {
	for _, mon := range s.Monitors {
		if mon.Contains(x, y) {
			return true
		}
	}
	return false
}

// protectedAt says why a point is off limits: one of Den's windows is
// under it. Den cannot tell the stacking order from Hyprland's list, so
// any of its windows under the point refuses, even beneath another.
func protectedAt(x, y float64, m Mission, s Snapshot) string {
	if !onScreen(x, y, s) {
		return "the point is on no monitor"
	}
	for _, w := range s.At(x, y) {
		if m.Protects(w.Address) {
			return "the point is on one of Den's own windows"
		}
	}
	return ""
}

func taskPointer(args map[string]any, m Mission, s Snapshot) Verdict {
	args["then"] = "none"
	x, okx := num(args, "x")
	y, oky := num(args, "y")
	if !okx || !oky {
		return deny("pointer: give x and y; an action at wherever the cursor is cannot be checked")
	}
	switch act := str(args, "action"); act {
	case "move":
		if !onScreen(x, y, s) {
			return deny("pointer move: the point is on no monitor")
		}
		return Verdict{Kind: Allow, Args: args}
	case "click", "scroll", "drag":
		if why := protectedAt(x, y, m, s); why != "" {
			return deny("pointer %s: %s", act, why)
		}
		if act == "drag" {
			tx, okt := num(args, "to_x")
			ty, oku := num(args, "to_y")
			if !okt || !oku {
				return deny("pointer drag: give to_x and to_y")
			}
			if why := protectedAt(tx, ty, m, s); why != "" {
				return deny("pointer drag: the end %s", why)
			}
		}
		return Verdict{Kind: Allow, Args: args}
	}
	return deny("pointer: unknown action")
}

func taskKeyboard(args map[string]any, m Mission, s Snapshot) Verdict {
	args["then"] = "none"
	if m.Protects(s.Active) {
		return deny("keyboard: one of Den's own windows is focused; focus another window first")
	}
	addr := str(args, "window")
	if addr == "active" {
		addr = ""
	}
	if addr != "" {
		if m.Protects(addr) {
			return deny("keyboard: that is one of Den's own windows")
		}
		if _, ok := s.Window(addr); !ok {
			return deny("keyboard: no window %s", addr)
		}
	}
	switch str(args, "action") {
	case "type":
		text := str(args, "text")
		if text == "" {
			return deny("keyboard type needs text")
		}
		// A typed character the human bound runs that bind: check it as one.
		for _, b := range s.Binds {
			if b.Mods&^modShift != 0 {
				continue
			}
			if (len([]rune(b.Key)) == 1 && strings.Contains(strings.ToLower(text), b.Key)) || (b.Key == "space" && strings.ContainsRune(text, ' ')) {
				if why := refusedBind(b, m, s); why != "" {
					return deny("keyboard: typing %q runs a bind: %s", b.Key, why)
				}
			}
		}
	case "key":
		keys := str(args, "keys")
		if keys == "" {
			return deny("keyboard key needs keys")
		}
		// One combo only, as hypruse takes it (T2), and never one the
		// system takes for itself (T1).
		if why := oneCombo(keys); why != "" {
			return deny("keyboard: %s", why)
		}
		if why := systemCombo(keys); why != "" {
			return deny("keyboard: %s", why)
		}
		if why := refusedBinds(findBinds(keys, s.Binds), m, s); why != "" {
			return deny("keyboard: %s runs a bind: %s", keys, why)
		}
	default:
		return deny("keyboard: unknown action")
	}
	return Verdict{Kind: Allow, Args: args}
}

func taskHypr(args map[string]any, m Mission, s Snapshot) Verdict {
	args["then"] = "none"
	switch act := str(args, "action"); act {
	case "workspace":
		return Verdict{Kind: Allow, Args: args}
	case "focus_window", "fullscreen", "toggle_floating", "move_window", "close_window":
		addr := str(args, "target")
		if addr == "" || addr == "active" {
			return deny("hypr %s: target must be a window's address", act)
		}
		if m.Protects(addr) {
			return deny("hypr %s: that is one of Den's own windows", act)
		}
		if _, ok := s.Window(addr); !ok {
			return deny("hypr %s: no window %s", act, addr)
		}
		return Verdict{Kind: Allow, Args: args}
	}
	return deny("hypr: unknown action")
}

// findBinds are every one of the human's binds on a combo ("super+m"), as
// Hyprland has them now, whatever their submap: Hyprland runs all of them.
func findBinds(combo string, binds []Bind) []Bind {
	if strings.TrimSpace(combo) == "" {
		return nil
	}
	mods, key := comboMods(combo)
	var out []Bind
	for _, b := range binds {
		if b.Mods == mods && b.Key == key {
			out = append(out, b)
		}
	}
	return out
}

// refusedBinds says why one of the binds is refused, "" when none is.
func refusedBinds(bs []Bind, m Mission, s Snapshot) string {
	for _, b := range bs {
		if why := refusedBind(b, m, s); why != "" {
			return why
		}
	}
	return ""
}

// modifierNames are the modifiers a combo may hold before its key.
var modifierNames = map[string]bool{"ctrl": true, "control": true, "shift": true, "alt": true, "super": true, "logo": true, "mod4": true, "win": true, "meta": true, "hyper": true, "cmd": true}

// oneCombo says why keys is not exactly one combo as hypruse takes it
// (modifiers and one key joined by +): no sequence, no list, no bare
// modifier tap (which can run a release bind Den cannot see as a combo).
func oneCombo(keys string) string {
	parts := strings.Split(keys, "+")
	for i, p := range parts {
		p = strings.TrimSpace(p)
		switch {
		case p == "":
			return "keys must be one combo such as ctrl+shift+t"
		case strings.ContainsAny(p, " \t,;"):
			return "keys must be one combo such as ctrl+shift+t, not a sequence or a list"
		case i < len(parts)-1 && !modifierNames[strings.ToLower(p)]:
			return "unknown modifier " + p
		case i == len(parts)-1 && modifierNames[strings.ToLower(p)]:
			return "a modifier tapped alone is not allowed"
		}
	}
	return ""
}

// systemCombo says why a combo is the system's, not an app's or the
// human's binds: ctrl+alt+Delete, Backspace and F1 to F12 take the display
// or the session (T1).
func systemCombo(keys string) string {
	mods := map[string]bool{}
	for _, k := range strings.Split(strings.ToLower(keys), "+") {
		mods[keyName(k)] = true
	}
	if (mods["ctrl"] || mods["control"]) && mods["alt"] {
		for k := range mods {
			if k == "delete" || k == "backspace" || len(k) >= 2 && k[0] == 'f' && k[1] >= '1' && k[1] <= '9' {
				return "ctrl+alt+" + k + " is the system's"
			}
		}
	}
	return ""
}

// hyprctlWord and denWord find hyprctl or den as a command word.
var (
	hyprctlWord = regexp.MustCompile(`(^|[\s/;&|("'` + "`" + `])hyprctl([\s;&|)"'` + "`" + `]|$)`)
	denWord     = regexp.MustCompile(`(^|[\s/;&|("'` + "`" + `])den([\s;&|)"'` + "`" + `]|$)`)
)

// refusedBind says why a bind is one task mode never runs, "" when it may.
func refusedBind(b Bind, m Mission, s Snapshot) string {
	d, arg := strings.ToLower(b.Dispatcher), strings.TrimSpace(b.Arg)
	switch d {
	case "exit", "forcerendererreload":
		return "it ends the human's session or display (" + d + ")"
	case "exec", "execr":
		switch {
		case hyprctlWord.MatchString(arg):
			return "it runs hyprctl, which can reload, rebind or unbind the compositor"
		case denWord.MatchString(arg):
			return "it runs den, which holds the panic stop"
		}
	case "killactive", "forcekillactive":
		if m.Protects(s.Active) {
			return "it closes the focused window, which is one of Den's own"
		}
	case "closewindow", "killwindow", "movetoworkspace", "movetoworkspacesilent", "movewindow", "pin", "togglefloating", "fullscreen":
		for addr := range m.Protected {
			if strings.Contains(strings.ToLower(arg), strings.ToLower(addr)) || strings.Contains(strings.ToLower(arg), strings.TrimPrefix(strings.ToLower(addr), "0x")) {
				return "it acts on one of Den's own windows"
			}
		}
		if (d == "closewindow" || d == "killwindow") && !strings.Contains(strings.ToLower(arg), "address:") {
			return "it closes windows matched by class or title, which could be Den's"
		}
	case "pass", "sendshortcut", "sendkeystate":
		return "it sends keys to another window by name"
	}
	return ""
}

// refusedLaunch says why a launch is refused: hyprctl or den run
// directly (anything else is the agent's, as at the keyboard).
func refusedLaunch(cmd string) string {
	switch {
	case hyprctlWord.MatchString(cmd):
		return "hyprctl is never launched directly"
	case denWord.MatchString(cmd):
		return "den is never launched directly"
	}
	for _, f := range strings.Fields(cmd) {
		switch filepath.Base(strings.Trim(f, `"'`)) {
		case "hyprctl":
			return "hyprctl is never launched directly"
		case "den":
			return "den is never launched directly"
		}
	}
	return ""
}
