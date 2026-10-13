package closer

import (
	"os"
	"syscall"

	"github.com/GlacierHubAB/den/internal/hypr"
	"github.com/GlacierHubAB/den/internal/procs"
)

// System is the real Ops: signals through the kernel, windows through
// Hyprland.
type System struct{}

func (System) Signal(pid int, sig syscall.Signal) error { return syscall.Kill(pid, sig) }

// Alive reports whether the process still exists (a zombie counts as gone
// once its parent reaps it; a signal 0 probe says whether it is there).
func (System) Alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// Windows lists Hyprland's windows, none when it is not running.
func (System) Windows() ([]Window, error) {
	if !hypr.Available() {
		return nil, nil
	}
	cs, err := hypr.Clients()
	var out []Window
	for _, c := range cs {
		out = append(out, Window{Address: c.Address, Class: c.InitialClass, PID: c.PID})
	}
	return out, err
}

// Focused is the address of the window in front.
func (System) Focused() string {
	if !hypr.Available() {
		return ""
	}
	return hypr.ActiveAddress()
}

func (System) CloseWindow(address string) error { return hypr.CloseWindow(address) }

// AncestorPIDs lists the pids above pid, for Locate and OwnWindow.
func AncestorPIDs(pid int) []int {
	var out []int
	for _, p := range procs.Ancestors(pid) {
		if p.PID != pid {
			out = append(out, p.PID)
		}
	}
	return out
}

// DenSelf is Self for the running Den: its process, the orchestrator's
// session and the window it runs in.
func DenSelf(orchestratorSession, window string) Self {
	me := os.Getpid()
	return Self{
		PID:          me,
		Orchestrator: orchestratorSession,
		Window:       window,
		IsAncestor: func(pid int) bool {
			for _, p := range procs.Ancestors(me) {
				if p.PID == pid && pid != me {
					return true
				}
			}
			return false
		},
	}
}
