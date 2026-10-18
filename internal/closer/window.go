package closer

import (
	"fmt"
	"slices"

	"github.com/GlacierHubAB/den/internal/hypr"
)

// Windows. An agent Den spawned runs as its terminal's command, so the
// terminal closes by itself when the agent's process ends; that is the
// normal way a window goes. Den closes a window itself only when it is
// still open after its agent ended, and only one it can tie to the agent:
// the single window whose class is the marker Den recorded for the agent's
// session at spawn (winmark), run by a process above the agent's own, and
// with nothing else left running in it. A window found any other way (by
// title or working directory, which other agents share) is never closed.

// Window is one compositor window as the closer sees it. Class is the class
// it was created with.
type Window struct {
	Address string
	Class   string
	PID     int
}

// WindowOps is what Run needs to close a window still open after its agent
// ended. System has it; with Ops that lack it, Run closes no window. Busy
// reports whether a process still has live children (a shell in another
// tab, a second claude), or cannot tell.
type WindowOps interface {
	Windows() ([]Window, error)
	Focused() string
	CloseWindow(address string) error
	Busy(pid int) bool
}

// Locate ties each target on this machine to its window, for the plan to
// show and Run to check again. A target's Marker is the one Den recorded
// for its session at spawn, "" for an agent Den did not spawn; it is never
// worked out again from a name. ancestors lists the pids above a process.
func Locate(p *Plan, ws []Window, ancestors func(pid int) []int) {
	for i := range p.Targets {
		t := &p.Targets[i]
		if t.Host != "" || t.PID == 0 {
			continue
		}
		t.Window, t.WindowPID, t.WindowNote = "", 0, ""
		hits := marked(ws, t.Marker)
		switch {
		case t.Marker == "":
			t.WindowNote = "Den did not open a marked window for it: if its terminal stays open, close it yourself"
		case len(hits) == 0:
			t.WindowNote = "no window marked for it: if its terminal stays open, close it yourself"
		case len(hits) > 1:
			t.WindowNote = fmt.Sprintf("%d windows carry its marker, so Den closes none of them: close its terminal yourself if it stays open", len(hits))
		case !slices.Contains(ancestors(t.PID), hits[0].PID):
			t.WindowNote = "the window marked for it does not run its process, so Den leaves it alone"
		default:
			t.Window, t.WindowPID = hits[0].Address, hits[0].PID
		}
	}
}

// marked lists the windows created with class marker.
func marked(ws []Window, marker string) []Window {
	var out []Window
	if marker == "" {
		return nil
	}
	for _, w := range ws {
		if w.Class == marker {
			out = append(out, w)
		}
	}
	return out
}

// OwnWindow is the window Den runs in: the one window of a process above
// Den's. A terminal process that holds several windows cannot say which,
// and the answer is "".
func OwnWindow(ws []Window, ancestors []int) string {
	for _, w := range ws {
		if !slices.Contains(ancestors, w.PID) {
			continue
		}
		n := 0
		for _, o := range ws {
			if o.PID == w.PID {
				n++
			}
		}
		if n == 1 {
			return w.Address
		}
	}
	return ""
}

// leftOpen deals with a target's window once its process has ended. The
// terminal normally closes it by itself; Den waits for that, and closes it
// only if it is still open and still exactly what the plan showed: the
// one window with the agent's marker, at the same address and run by the
// same process, neither focused nor Den's own. Otherwise it says why the
// human should close it. A close is tried once, never again.
func leftOpen(r *Result, t Target, self Self, wo WindowOps) {
	if t.Window == "" {
		return
	}
	var hits []Window
	deadline := timeNow().Add(WindowWait)
	for {
		ws, err := wo.Windows()
		if err != nil {
			r.WindowNote = "Den could not list the windows; close its terminal yourself if it is still open"
			return
		}
		hits = marked(ws, t.Marker)
		if len(hits) == 0 {
			return // it closed with its process
		}
		if !timeNow().Before(deadline) {
			break
		}
		sleepFor(pollEvery)
	}
	if len(hits) != 1 || hits[0].Address != t.Window || hits[0].PID != t.WindowPID {
		r.WindowNote = "its window is still open but is no longer the one the plan showed: close it yourself"
		return
	}
	// A view switch (internal/view) moves windows under this same lock, so
	// it and the close below never run together. A window hidden on
	// special:den is closed like any other: it is found by its address.
	defer hypr.LockWindows()()
	if t.Window == wo.Focused() || t.Window == self.Window {
		r.WindowNote = "its window is still open; Den does not close the window in front or its own: close it yourself"
		return
	}
	if wo.Busy(t.WindowPID) {
		r.WindowNote = "its window is still open with something else running in it: close it yourself"
		return
	}
	if err := wo.CloseWindow(t.Window); err != nil {
		r.WindowNote = "its window is still open and would not close: close it yourself"
		return
	}
	r.WindowClosed = true
}
