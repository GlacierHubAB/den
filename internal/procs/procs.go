// Package procs reads the process tree from /proc.
package procs

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Proc is one process in an ancestry chain.
type Proc struct {
	PID  int
	Comm string
}

// Ancestors returns pid and its parents up to init, nearest first.
func Ancestors(pid int) []Proc {
	var out []Proc
	seen := map[int]bool{}
	for pid > 1 && !seen[pid] {
		seen[pid] = true
		comm, ppid, ok := read(pid)
		if !ok {
			break
		}
		out = append(out, Proc{PID: pid, Comm: comm})
		pid = ppid
	}
	return out
}

// HasAncestor reports whether any ancestor (including pid) has this comm.
func HasAncestor(pid int, comm string) bool {
	for _, p := range Ancestors(pid) {
		if p.Comm == comm {
			return true
		}
	}
	return false
}

func read(pid int) (comm string, ppid int, ok bool) {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "status"))
	if err != nil {
		return "", 0, false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if v, found := strings.CutPrefix(line, "Name:"); found {
			comm = strings.TrimSpace(v)
		} else if v, found := strings.CutPrefix(line, "PPid:"); found {
			ppid, _ = strconv.Atoi(strings.TrimSpace(v))
		}
	}
	return comm, ppid, comm != ""
}

// StartTicks is when pid started, in clock ticks since boot (field 22 of
// /proc/<pid>/stat), "" when it is gone. With the pid it names one process
// for good: a reused pid has another start.
func StartTicks(pid int) string {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return ""
	}
	s := string(raw)
	i := strings.LastIndexByte(s, ')') // the command may hold spaces and parentheses
	if i < 0 {
		return ""
	}
	f := strings.Fields(s[i+1:])
	const starttime = 22 - 3 // fields after ")" start at field 3
	if len(f) <= starttime {
		return ""
	}
	return f[starttime]
}

// Parent is pid's parent pid and its own command name, ok false when pid
// is gone.
func Parent(pid int) (ppid int, comm string, ok bool) {
	comm, ppid, ok = read(pid)
	return ppid, comm, ok
}

// Exe is the executable pid runs, resolved ("" when it cannot be read),
// as its path: Undeleted.
func Exe(pid int) string {
	p, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "exe"))
	if err != nil {
		return ""
	}
	return Undeleted(p)
}

// Undeleted is a /proc/<pid>/exe link without the " (deleted)" the kernel
// adds once the file at that path was replaced, as an install or an update
// replaces a binary that is running: the process still runs what was at
// the path, which is what Go's os.Executable says too. Without it, a Den
// (or Claude Code) updated in place stops matching its own path.
func Undeleted(link string) string {
	return strings.TrimSuffix(link, " (deleted)")
}

// shells are the commands a hook command line may run in before it execs
// the hook.
var shells = map[string]bool{"sh": true, "bash": true, "dash": true, "zsh": true}

// ChildOf reports whether pid runs exe and is the direct child of parent
// (the process that started at start), or the child of a shell that is:
// how Claude Code starts its MCP servers and hook commands. A process any
// deeper (in a terminal an agent opened, say) is not.
func ChildOf(pid, parent int, start, exe string) bool {
	if pid <= 1 || parent <= 1 || start == "" || exe == "" || Exe(pid) != exe {
		return false
	}
	pp, _, ok := Parent(pid)
	if !ok {
		return false
	}
	if pp != parent {
		ppp, comm, ok := Parent(pp)
		if !ok || ppp != parent || !shells[comm] {
			return false
		}
	}
	return StartTicks(parent) == start
}
