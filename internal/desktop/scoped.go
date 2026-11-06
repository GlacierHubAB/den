package desktop

import "strings"

// A scoped task is held to its own windows: those of the processes in the
// mission's slice (what it launched) and the ones the human picked on the
// start card. Every task rule still applies first; scoping only refuses
// more, never less. Reading the screen stays whole-screen, and workspace
// moves stay allowed (the human's Q1 and Q2), since input still can't
// reach a window outside the set.
//
// X11 windows are never in the set: an X window's pid is the X client's
// own claim, so membership can't rest on it.

// Pick is a window the human picked, fixed at the start by address and
// the pid that owned it then: an address that comes back on another
// process is a different window.
type Pick struct {
	PID int
}

// InSet reports whether a window is one a scoped task may act on.
func (m Mission) InSet(w Window) bool {
	if w.Address == "" || m.Protects(w.Address) || w.XWayland {
		return false
	}
	if p, ok := m.Picked[w.Address]; ok && p.PID == w.PID && w.PID > 1 {
		return true
	}
	return w.PID > 1 && m.InSlice != nil && m.InSlice(w.PID)
}

// setWindow is the window at an address if it is in the set.
func (m Mission) setWindow(addr string, s Snapshot) (Window, bool) {
	w, ok := s.Window(addr)
	if !ok || !m.InSet(w) {
		return Window{}, false
	}
	return w, true
}

// outsideAt says why a point isn't the set's, "" when every window under
// it is in the set: Den can't tell stacking order, so one outside window
// beneath a set window refuses too.
func outsideAt(x, y float64, m Mission, s Snapshot) string {
	under := s.At(x, y)
	if len(under) == 0 {
		return "no window of the task's is there"
	}
	for _, w := range under {
		if !m.InSet(w) {
			return "a window that isn't the task's is there (" + windowName(w) + ")"
		}
	}
	return ""
}

// windowName is a window for a reason: its class, short and printable.
func windowName(w Window) string {
	if w.XWayland {
		return sanitizeNS(w.Class) + ", an X window"
	}
	return sanitizeNS(w.Class)
}

// topAt is the window a click at a point lands on, as well as Den can
// tell: a floating window lies over tiled ones.
func topAt(x, y float64, s Snapshot) *Window {
	under := s.At(x, y)
	for i := range under {
		if under[i].Floating {
			return &under[i]
		}
	}
	if len(under) > 0 {
		return &under[0]
	}
	return nil
}

// bindsOutside says why a bind reaches outside the set, "" when none
// does: a bind acts on the focused window (active: the one the call has
// focused when the bind runs), or on the one it names.
func bindsOutside(bs []Bind, active string, m Mission, s Snapshot) string {
	for _, b := range bs {
		d, arg := strings.ToLower(strings.TrimSpace(b.Dispatcher)), strings.TrimSpace(b.Arg)
		window := ""
		switch taskDispatch[d] {
		case argView:
			continue
		case argWindow, argAddress:
			window = arg
		case argDirOrAddress:
			if !swapDir.MatchString(strings.ToLower(arg)) {
				window = arg
			}
		case argWorkspaceWindow:
			_, window, _ = strings.Cut(arg, ",")
			window = strings.TrimSpace(window)
		}
		if _, ok := m.setWindow(active, s); !ok {
			return "the focused window isn't the task's"
		}
		if window != "" {
			addr := strings.TrimPrefix(strings.ToLower(window), "address:")
			found := false
			for _, w := range s.Windows {
				if strings.EqualFold(w.Address, addr) && m.InSet(w) {
					found = true
				}
			}
			if !found {
				return "it names a window that isn't the task's"
			}
		}
	}
	return ""
}

// narrowTask applies the scoped rules to a verdict the task rules allowed.
func narrowTask(tool string, args map[string]any, m Mission, s Snapshot, v Verdict) Verdict {
	switch tool {
	case "keyboard":
		if v.Target == nil || !m.InSet(*v.Target) {
			return deny("keyboard: that window isn't the task's; keys go only to windows it opened or you picked")
		}
		if str(args, "action") == "key" {
			if why := bindsOutside(findBinds(str(args, "keys"), s.Binds), v.Target.Address, m, s); why != "" {
				return deny("keyboard: %s runs a bind: %s", str(args, "keys"), why)
			}
		}
	case "pointer":
		act := str(args, "action")
		if act == "move" {
			return v
		}
		x, _ := num(args, "x")
		y, _ := num(args, "y")
		if why := outsideAt(x, y, m, s); why != "" {
			return deny("pointer %s: %s", act, why)
		}
		if act == "drag" {
			tx, _ := num(args, "to_x")
			ty, _ := num(args, "to_y")
			if why := outsideAt(tx, ty, m, s); why != "" {
				return deny("pointer drag: the end: %s", why)
			}
		}
		top := topAt(x, y, s) // outsideAt found one, and every one is the set's
		if why := bindsOutside(mouseBinds(act, str(args, "button"), s.Binds), top.Address, m, s); why != "" {
			return deny("pointer %s runs a bind: %s", act, why)
		}
	case "click_ui":
		w, ok := m.setWindow(str(args, "window"), s)
		if !ok {
			return deny("click_ui: that window isn't the task's")
		}
		v.Target = &w
		if why := bindsOutside(mouseBinds("click", str(args, "button"), s.Binds), w.Address, m, s); why != "" {
			return deny("click_ui runs a bind: %s", why)
		}
	case "hypr":
		if act := str(args, "action"); act != "workspace" {
			w, ok := m.setWindow(str(args, "target"), s)
			if !ok {
				return deny("hypr %s: that window isn't the task's", act)
			}
			v.Target = &w
		}
	case "use_bind":
		if _, ok := m.setWindow(s.Active, s); !ok {
			return deny("use_bind: the focused window isn't the task's; focus one of its windows first")
		}
		if why := bindsOutside(findBinds(str(args, "combo"), s.Binds), s.Active, m, s); why != "" {
			return deny("use_bind: %s", why)
		}
	}
	return v
}
