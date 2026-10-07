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
