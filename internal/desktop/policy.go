package desktop

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The tools a desktop agent is given, as the proxy lists them. The first
// group is hypruse's, forwarded under Den's rules; the second is Den's
// own, served by the proxy.
var (
	HypruseTools = []string{"desktop", "screenshot", "zoom", "ui", "marks", "pointer", "keyboard", "click_ui", "hypr"}
	DenTools     = []string{"request_approval", "your_turn"}
	// ClosedTools are hypruse's tools no desktop agent ever gets: arbitrary
	// commands, compositor binds, unjudgeable bundles, the clipboard, and
	// two observers that report the whole desktop.
	ClosedTools = []string{"launch", "use_bind", "sequence", "clipboard", "binds", "wait_for"}
)

// MCPServer is the name the desktop agent's MCP config gives the proxy, so
// its tools reach Claude Code as mcp__hypruse__<tool>.
const MCPServer = "hypruse"

// ToolName is a tool's name as Claude Code knows it.
func ToolName(tool string) string { return "mcp__" + MCPServer + "__" + tool }

// AllowedToolNames are the Claude Code names of every tool a desktop agent
// has: its --tools and --allowedTools.
func AllowedToolNames() []string {
	var out []string
	for _, t := range append(append([]string(nil), HypruseTools...), DenTools...) {
		out = append(out, ToolName(t))
	}
	return out
}

// ClosedToolNames are the Claude Code names of the closed tools, for
// --disallowedTools.
func ClosedToolNames() []string {
	var out []string
	for _, t := range ClosedTools {
		out = append(out, ToolName(t))
	}
	return out
}

// Kind is what the gate does with a call.
type Kind int

const (
	Deny Kind = iota
	Allow
	Ask // a card: the human allows or denies this one call
)

func (k Kind) String() string {
	switch k {
	case Allow:
		return "allow"
	case Ask:
		return "ask"
	}
	return "deny"
}

// Verdict is the gate's decision on one call.
type Verdict struct {
	Kind   Kind
	Reason string // why it is denied or why it asks, for the agent and the card
	// Args is the call's input as it is forwarded: the agent's, with Den's
	// rewrites (a keyboard call's window, then = "none").
	Args map[string]any
	// Target is the window the call acts on or reads, when there is one.
	Target *Window
}

func deny(format string, a ...any) Verdict {
	return Verdict{Kind: Deny, Reason: fmt.Sprintf(format, a...)}
}

// Decide is the gate: what happens to one tool call, given the mission's
// scope and the desktop as it is now. It never asks Hyprland itself and
// never allows what it cannot place.
func Decide(tool string, raw json.RawMessage, m Mission, s Snapshot) Verdict {
	args := map[string]any{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &args); err != nil {
			return deny("the call's input is not a JSON object")
		}
	}
	if m.Paused {
		return deny("the mission is paused: a step only the human does is in progress; wait for your_turn to return")
	}
	if b, ok := args["allow_auth"].(bool); ok && b {
		return deny("allow_auth is never allowed: credentials and authentication are the human's")
	}
	if _, ok := args["allow_auth"]; ok {
		delete(args, "allow_auth")
	}
	switch tool {
	case "request_approval", "your_turn":
		v := Verdict{Kind: Ask, Args: args}
		return v
	case "desktop":
		return Verdict{Kind: Allow, Args: args}
	case "screenshot":
		if str(args, "region") != "" {
			return deny("screenshot takes only window=<address> of a mission window; regions and the full screen are not allowed")
		}
		w, why := inScopeWindow(args, "window", m, s)
		if why != "" {
			return deny("screenshot: %s", why)
		}
		return Verdict{Kind: Allow, Args: args, Target: &w}
	case "zoom":
		w, why := inScopeWindow(args, "window", m, s)
		if why != "" {
			return deny("zoom: %s (zoom must name window=<address>, which clamps it to that window)", why)
		}
		if _, shown := s.Shown(m.Workspace); !shown {
			return deny("zoom: the mission workspace is not on screen, so a zoom would capture something else")
		}
		x, okx := num(args, "x")
		y, oky := num(args, "y")
		if !okx || !oky || !w.Contains(x, y) {
			return deny("zoom: the point must lie inside window %s", w.Address)
		}
		return Verdict{Kind: Allow, Args: args, Target: &w}
	case "ui", "marks":
		w, why := inScopeWindow(args, "window", m, s)
		if why != "" {
			return deny("%s: %s", tool, why)
		}
		return Verdict{Kind: Allow, Args: args, Target: &w}
	case "pointer":
		return decidePointer(args, m, s)
	case "keyboard":
		return decideKeyboard(args, m, s)
	case "click_ui":
		return decideClickUI(args, m, s)
	case "hypr":
		return decideHypr(args, m, s)
	}
	for _, t := range ClosedTools {
		if tool == t {
			return deny("%s is closed to desktop agents", tool)
		}
	}
	return deny("unknown tool %q", tool)
}

func decidePointer(args map[string]any, m Mission, s Snapshot) Verdict {
	args["then"] = "none"
	mon, shown := s.Shown(m.Workspace)
	if !shown {
		return deny("pointer: the mission workspace %d is not on screen", m.Workspace)
	}
	x, okx := num(args, "x")
	y, oky := num(args, "y")
	switch act := str(args, "action"); act {
	case "move":
		if !okx || !oky || !mon.Contains(x, y) {
			return deny("pointer move: the point must be on the monitor showing the mission workspace")
		}
		return Verdict{Kind: Allow, Args: args}
	case "click", "scroll", "drag":
		if !okx || !oky {
			return deny("pointer %s: give x and y; a click at wherever the cursor is cannot be checked", act)
		}
		under, why := windowsAtPoint(x, y, m, s)
		if why != "" {
			return deny("pointer %s: %s", act, why)
		}
		w := top(under)
		if act == "drag" {
			tx, okt := num(args, "to_x")
			ty, oku := num(args, "to_y")
			if !okt || !oku {
				return deny("pointer drag needs to_x and to_y")
			}
			end, why := windowsAtPoint(tx, ty, m, s)
			if why != "" {
				return deny("pointer drag end: %s", why)
			}
			under = append(under, end...)
		}
		v := Verdict{Kind: Allow, Args: args, Target: &w}
		if act != "scroll" {
			for _, u := range under {
				if m.Outward(u) == OutwardAsk {
					v.Kind, v.Reason = Ask, fmt.Sprintf("a %s in %s, which can send things", act, u.Class)
					v.Target = &u
					break
				}
			}
		}
		return v
	}
	return deny("pointer: unknown action")
}

func decideKeyboard(args map[string]any, m Mission, s Snapshot) Verdict {
	args["then"] = "none"
	addr := str(args, "window")
	if addr == "" {
		addr = s.Active
	}
	w, ok := s.Window(addr)
	if !ok || !m.InScope(w) {
		return deny("keyboard: the target window is not a mission window; pass window=<address> of one")
	}
	// hypruse focuses this window and checks it against its confinement
	// right before typing, rather than typing into whatever has focus.
	args["window"] = w.Address
	v := Verdict{Kind: Allow, Args: args, Target: &w}
	switch str(args, "action") {
	case "type":
		text := str(args, "text")
		if text == "" {
			return deny("keyboard type needs text")
		}
		switch m.Outward(w) {
		case OutwardAsk:
			v.Kind, v.Reason = Ask, "typing into "+w.Class+", where typed text can leave the machine"
		case OutwardWords:
			if strings.ContainsAny(text, "\r\n") {
				v.Kind, v.Reason = Ask, "text with Enter in "+w.Class
			}
		}
	case "key":
		keys := str(args, "keys")
		if keys == "" {
			return deny("keyboard key needs keys")
		}
		if why := compositorCombo(keys); why != "" {
			return deny("keyboard: %s", why)
		}
		switch m.Outward(w) {
		case OutwardAsk:
			v.Kind, v.Reason = Ask, "pressing "+keys+" in "+w.Class
		case OutwardWords:
			if hasEnter(keys) {
				v.Kind, v.Reason = Ask, "pressing "+keys+" in "+w.Class+" can send"
			}
		}
	default:
		return deny("keyboard: unknown action")
	}
	return v
}

// compositorCombo says why a combo is one the compositor or the system
// may take for itself (a Hyprland bind, a switch of virtual terminal),
// "" when it is an app's.
func compositorCombo(keys string) string {
	mods := map[string]bool{}
	for _, k := range strings.Split(strings.ToLower(keys), "+") {
		mods[strings.TrimSpace(k)] = true
	}
	for _, k := range []string{"super", "logo", "mod4", "win", "meta", "hyper", "cmd"} {
		if mods[k] {
			return "combos with " + k + " can run the human's Hyprland binds"
		}
	}
	if mods["ctrl"] && mods["alt"] || mods["control"] && mods["alt"] {
		for k := range mods {
			if k == "delete" || k == "backspace" || len(k) >= 2 && k[0] == 'f' && k[1] >= '1' && k[1] <= '9' {
				return "ctrl+alt+" + k + " is the system's"
			}
		}
	}
	return ""
}

// hasEnter reports whether a key combo presses Enter.
func hasEnter(keys string) bool {
	for _, k := range strings.Split(strings.ToLower(keys), "+") {
		switch strings.TrimSpace(k) {
		case "enter", "return", "kp_enter", "keypadenter", "linefeed":
			return true
		}
	}
	return false
}

func decideClickUI(args map[string]any, m Mission, s Snapshot) Verdict {
	args["then"] = "none"
	w, why := inScopeWindow(args, "window", m, s)
	if why != "" {
		return deny("click_ui: %s", why)
	}
	v := Verdict{Kind: Allow, Args: args, Target: &w}
	name := str(args, "name")
	if _, hasMark := args["mark"]; hasMark && name == "" {
		if m.MarksWindow != w.Address {
			return deny("click_ui by mark: the last marks call was not of window %s; call marks(window=%s) first", w.Address, w.Address)
		}
		if m.Outward(w) != "" {
			v.Kind, v.Reason = Ask, "a click on a numbered control in "+w.Class+", whose name Den cannot see"
		}
		return v
	}
	if word, hit := Sensitive(name); hit {
		v.Kind, v.Reason = Ask, fmt.Sprintf("the control %q looks like %q", name, word)
	} else if m.Outward(w) == OutwardAsk {
		v.Kind, v.Reason = Ask, "a click in "+w.Class+", which can send things"
	}
	return v
}

func decideHypr(args map[string]any, m Mission, s Snapshot) Verdict {
	args["then"] = "none"
	switch act := str(args, "action"); act {
	case "workspace":
		if fmt.Sprint(args["workspace"]) != fmt.Sprint(m.Workspace) {
			return deny("hypr workspace: only the mission workspace %d", m.Workspace)
		}
		return Verdict{Kind: Allow, Args: args}
	case "focus_window", "fullscreen", "toggle_floating":
		w, why := inScopeWindow(args, "target", m, s)
		if why != "" {
			return deny("hypr %s: %s", act, why)
		}
		return Verdict{Kind: Allow, Args: args, Target: &w}
	case "move_window", "close_window":
		return deny("hypr %s is not allowed: Den keeps the mission's windows where they are and closes them itself", act)
	}
	return deny("hypr: unknown action")
}

// inScopeWindow resolves a window argument: it must be given, as an
// address (not "active"), and name a mission window.
func inScopeWindow(args map[string]any, key string, m Mission, s Snapshot) (Window, string) {
	addr := str(args, key)
	if addr == "" || addr == "active" {
		return Window{}, key + " must be the address of a mission window"
	}
	w, ok := s.Window(addr)
	if !ok {
		return Window{}, "no window " + addr
	}
	if !m.InScope(w) {
		return Window{}, "window " + addr + " is not a mission window"
	}
	return w, ""
}

// windowsAtPoint lists the windows under a point, all of which must be
// the mission's: with any other window there, Den does not guess the
// stacking and refuses.
func windowsAtPoint(x, y float64, m Mission, s Snapshot) ([]Window, string) {
	under := s.At(x, y)
	if len(under) == 0 {
		return nil, fmt.Sprintf("no window at %.0f,%.0f", x, y)
	}
	for _, w := range under {
		if !m.InScope(w) {
			return nil, fmt.Sprintf("%s (not a mission window) is at %.0f,%.0f", w.Class, x, y)
		}
	}
	return under, ""
}

// top is the window most likely drawn on top: a floating one first.
func top(under []Window) Window {
	for _, w := range under {
		if w.Floating {
			return w
		}
	}
	return under[0]
}

func str(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return s
}

func num(args map[string]any, key string) (float64, bool) {
	switch v := args[key].(type) {
	case float64:
		return v, true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	}
	return 0, false
}
