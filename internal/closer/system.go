package closer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// Busy reports whether pid has a live child process, from
// /proc/<pid>/task/*/children; zombies do not count. Anything it cannot
// read counts as busy.
func (System) Busy(pid int) bool {
	if pid <= 0 {
		return true
	}
	tasks, err := filepath.Glob(fmt.Sprintf("/proc/%d/task/*/children", pid))
	if err != nil || len(tasks) == 0 {
		return true
	}
	for _, f := range tasks {
		raw, err := os.ReadFile(f)
		if err != nil {
			return true
		}
		for _, c := range strings.Fields(string(raw)) {
			stat, err := os.ReadFile("/proc/" + c + "/stat")
			if err != nil {
				continue // gone since
			}
			// The state follows the parenthesised command, which may hold
			// spaces and parentheses itself.
			s := string(stat)
			if i := strings.LastIndexByte(s, ')'); i >= 0 && i+2 < len(s) && s[i+2] == 'Z' {
				continue
			}
			return true
		}
	}
	return false
}

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
