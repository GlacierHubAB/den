package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// Window is one Hyprland client as the gate needs it: geometry in global
// logical pixels, like hypruse's own.
type Window struct {
	Address   string `json:"address"`
	Class     string `json:"class"`
	Title     string `json:"title"`
	PID       int    `json:"pid"`
	Workspace int    `json:"workspace"`
	At        [2]int `json:"at"`
	Size      [2]int `json:"size"`
	Floating  bool   `json:"floating"`
	Mapped    bool   `json:"mapped"`
	Pinned    bool   `json:"-"` // shown on every workspace (Den pins the agent view)
}

// Contains reports whether the global point lies inside the window.
func (w Window) Contains(x, y float64) bool {
	return x >= float64(w.At[0]) && x < float64(w.At[0]+w.Size[0]) && y >= float64(w.At[1]) && y < float64(w.At[1]+w.Size[1])
}

// Monitor is one output: its logical rectangle and what it shows.
type Monitor struct {
	Name      string
	X, Y      int
	W, H      int
	Workspace int  // the workspace it shows
	Special   bool // a special workspace is pulled up over it
	// Mirror: it mirrors another monitor, or Hyprland says it is disabled
	// (plain `hyprctl monitors` lists only enabled ones): the console draws
	// no overlay of its own on it.
	Mirror bool
}

// Contains reports whether the global point lies on the monitor.
func (m Monitor) Contains(x, y float64) bool {
	return x >= float64(m.X) && x < float64(m.X+m.W) && y >= float64(m.Y) && y < float64(m.Y+m.H)
}

// Snapshot is the desktop as the gate decides on it.
type Snapshot struct {
	Windows  []Window
	Monitors []Monitor
	Active   string  // the focused window's address
	Layers   []Layer // layer-shell surfaces: bars, notifications, launchers
	Binds    []Bind  // the human's Hyprland key binds
}

// Window returns the window with that address.
func (s Snapshot) Window(addr string) (Window, bool) {
	for _, w := range s.Windows {
		if w.Address == addr && addr != "" {
			return w, true
		}
	}
	return Window{}, false
}

// Shown reports whether a workspace is on a monitor right now, with no
// special workspace drawn over it, and which monitor.
func (s Snapshot) Shown(ws int) (Monitor, bool) {
	for _, m := range s.Monitors {
		if m.Workspace == ws && !m.Special {
			return m, true
		}
	}
	return Monitor{}, false
}

// At lists the mapped windows under a point on the workspaces shown there.
func (s Snapshot) At(x, y float64) []Window {
	var shown []int
	for _, m := range s.Monitors {
		if m.Contains(x, y) {
			shown = append(shown, m.Workspace)
		}
	}
	var out []Window
	for _, w := range s.Windows {
		if !w.Mapped || !w.Contains(x, y) {
			continue
		}
		for _, ws := range shown {
			if w.Workspace == ws {
				out = append(out, w)
			}
		}
	}
	return out
}

// hyprctl runs hyprctl with the arguments; tests replace it.
var hyprctl = func(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "hyprctl", args...).Output()
}

// ReadSnapshot asks Hyprland for clients, monitors and the focused window.
func ReadSnapshot(ctx context.Context) (Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var s Snapshot
	out, err := hyprctl(ctx, "clients", "-j")
	if err != nil {
		return s, fmt.Errorf("hyprctl clients: %v", err)
	}
	var clients []struct {
		Address   string `json:"address"`
		Class     string `json:"class"`
		Title     string `json:"title"`
		PID       int    `json:"pid"`
		At        [2]int `json:"at"`
		Size      [2]int `json:"size"`
		Floating  bool   `json:"floating"`
		Pinned    bool   `json:"pinned"`
		Mapped    *bool  `json:"mapped"`
		Hidden    bool   `json:"hidden"`
		Workspace struct {
			ID int `json:"id"`
		} `json:"workspace"`
	}
	if err := json.Unmarshal(out, &clients); err != nil {
		return s, fmt.Errorf("hyprctl clients: %v", err)
	}
	for _, c := range clients {
		mapped := (c.Mapped == nil || *c.Mapped) && !c.Hidden
		s.Windows = append(s.Windows, Window{Address: c.Address, Class: c.Class, Title: c.Title, PID: c.PID, Workspace: c.Workspace.ID, At: c.At, Size: c.Size, Floating: c.Floating, Mapped: mapped, Pinned: c.Pinned})
	}
	out, err = hyprctl(ctx, "monitors", "-j")
	if err != nil {
		return s, fmt.Errorf("hyprctl monitors: %v", err)
	}
	var mons []struct {
		Name            string  `json:"name"`
		X               int     `json:"x"`
		Y               int     `json:"y"`
		Width           int     `json:"width"`
		Height          int     `json:"height"`
		Scale           float64 `json:"scale"`
		Transform       int     `json:"transform"`
		ActiveWorkspace struct {
			ID int `json:"id"`
		} `json:"activeWorkspace"`
		SpecialWorkspace struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"specialWorkspace"`
		MirrorOf string `json:"mirrorOf"`
		Disabled bool   `json:"disabled"`
	}
	if err := json.Unmarshal(out, &mons); err != nil {
		return s, fmt.Errorf("hyprctl monitors: %v", err)
	}
	for _, m := range mons {
		if m.Scale <= 0 {
			return s, errors.New("hyprctl monitors: a monitor reports no scale")
		}
		// width and height are physical mode pixels; the global space is
		// logical, and a quarter turn swaps the sides.
		w, h := int(float64(m.Width)/m.Scale+0.5), int(float64(m.Height)/m.Scale+0.5)
		if m.Transform%2 == 1 {
			w, h = h, w
		}
		s.Monitors = append(s.Monitors, Monitor{Name: m.Name, X: m.X, Y: m.Y, W: w, H: h, Workspace: m.ActiveWorkspace.ID, Special: m.SpecialWorkspace.ID != 0 || m.SpecialWorkspace.Name != "", Mirror: m.Disabled || m.MirrorOf != "" && m.MirrorOf != "none"})
	}
	if err := readExtras(ctx, &s); err != nil {
		return s, err
	}
	out, err = hyprctl(ctx, "activewindow", "-j")
	if err == nil {
		var a struct {
			Address string `json:"address"`
		}
		if json.Unmarshal(out, &a) == nil {
			s.Active = a.Address
		}
	}
	return s, nil
}

// App is one app the human allowed for desktop missions, from Den's
// config: never a command the agent chose.
type App struct {
	Name    string   `toml:"-"`
	Command []string `toml:"command"`
	Class   string   `toml:"class"`
	// Outward marks a browser or mail client, where a click or a keystroke
	// can send something: "ask" (the default for an outward app) asks for
	// every click and every typed text; "words" asks only on the sensitive
	// words and Enter; "" (or "no") is not outward.
	Outward string `toml:"outward"`
	// Profile is how to start the app on a profile of its own: arguments
	// added to Command with {dir} replaced by a fresh folder.
	Profile []string `toml:"profile"`
	// IfRunning is what to do when the app joins a process that was
	// already running (and its sign-ins): "refuse" (default) or "warn".
	IfRunning string `toml:"if_running"`
}

// Outward modes.
const (
	OutwardAsk   = "ask"
	OutwardWords = "words"
)

// Mission is a desktop mission's scope as Den holds it: the workspace Den
// picked, the apps the human allowed, and the windows and processes Den
// launched for it.
type Mission struct {
	ID        string
	Workspace int
	Apps      []App
	Windows   map[string]bool // addresses of the windows Den launched
	PIDs      map[int]bool    // processes Den launched (never one that was running before)
	Paused    bool            // a "your turn" card is open
	// MarksWindow is the window of the last marks call Den allowed: a
	// click_ui by mark is only in scope for it.
	MarksWindow string
	// Task is a task mission: the whole desktop, with Den's own surfaces
	// (Protected, by Hyprland address: the window Den runs in and the agent
	// view) off limits, and no per-app rules. See task.go.
	Task      bool
	Protected map[string]bool
	// Console is the console's proved processes (Drey's dock), for one
	// call: a task treats their windows as Den's own (the caller adds them
	// to Protected) and their top or overlay surfaces as off limits.
	Console map[int]bool
}

// InScope reports whether a window is the mission's: on its workspace, and
// one Den launched or opened by a process Den launched.
func (m Mission) InScope(w Window) bool {
	return w.Workspace == m.Workspace && m.Workspace != 0 && (m.Windows[w.Address] || m.PIDs[w.PID] && w.PID > 1)
}

// App returns the mission's app for a window, by class.
func (m Mission) App(w Window) (App, bool) {
	for _, a := range m.Apps {
		if a.Class == w.Class {
			return a, true
		}
	}
	return App{}, false
}

// Outward is the outward mode for a window: an in-scope window of an app
// marked outward, or of no app Den knows (asks, to be safe).
func (m Mission) Outward(w Window) string {
	a, ok := m.App(w)
	if !ok {
		return OutwardAsk
	}
	switch a.Outward {
	case "", "no", "false":
		return ""
	case OutwardWords:
		return OutwardWords
	}
	return OutwardAsk
}
