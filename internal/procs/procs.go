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

// Descends reports whether pid runs under ancestor, the process that
// started at start (StartTicks): a parent, grandparent or further up, and
// still the same process, not a reused pid.
func Descends(pid, ancestor int, start string) bool {
	if pid <= 1 || ancestor <= 1 || pid == ancestor || start == "" {
		return false
	}
	chain := Ancestors(pid)
	if len(chain) < 2 {
		return false
	}
	for _, p := range chain[1:] {
		if p.PID == ancestor {
			return StartTicks(ancestor) == start
		}
	}
	return false
}
