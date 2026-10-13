package api

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Peer is who is on the other end of a connection, as the kernel and
// /proc say: never what the client says about itself.
type Peer struct {
	UID   int
	PID   int
	Exe   string // /proc/<pid>/exe at accept, "" when unreadable
	Start uint64 // the process's start time (clock ticks since boot)
}

// Label is how Den names the peer to the human: the exe's base name, or
// "app" when it could not be read.
func (p Peer) Label() string {
	if p.Exe == "" {
		return "app"
	}
	return filepath.Base(p.Exe)
}

// peerCred reads SO_PEERCRED. Tests replace it.
var peerCred = func(c net.Conn) (uid, pid int, err error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0, 0, errors.New("not a unix socket")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, 0, err
	}
	var cred *syscall.Ucred
	var cerr error
	if err := raw.Control(func(fd uintptr) {
		cred, cerr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return 0, 0, err
	}
	if cerr != nil {
		return 0, 0, cerr
	}
	return int(cred.Uid), int(cred.Pid), nil
}

// identify reads the peer's uid and pid from the kernel, then its exe and
// start time from /proc. A process that is gone, or whose start time
// changed between the two reads (a reused pid), is not identified.
func identify(c net.Conn) (Peer, error) {
	uid, pid, err := peerCred(c)
	if err != nil {
		return Peer{}, err
	}
	p := Peer{UID: uid, PID: pid}
	if pid <= 0 {
		return p, nil
	}
	start, _, err := procStat(pid)
	if err != nil {
		return p, fmt.Errorf("peer %d: %w", pid, err)
	}
	p.Exe, _ = os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	again, _, err := procStat(pid)
	if err != nil || again != start {
		return p, fmt.Errorf("peer %d changed while it was read", pid)
	}
	p.Start = start
	return p, nil
}

// procStat reads a process's start time and parent from /proc/<pid>/stat.
var procStat = func(pid int) (start uint64, ppid int, err error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, 0, err
	}
	s := string(raw)
	// The command name is in parentheses and may hold spaces or ')'.
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, 0, errors.New("bad stat")
	}
	f := strings.Fields(s[i+1:])
	// After ')': state(0) ppid(1) ... starttime is field 22 of stat, 20 here.
	if len(f) < 20 {
		return 0, 0, errors.New("bad stat")
	}
	ppid, err = strconv.Atoi(f[1])
	if err != nil {
		return 0, 0, err
	}
	start, err = strconv.ParseUint(f[19], 10, 64)
	return start, ppid, err
}

// underAgent reports whether pid or one of its ancestors is in agents (the
// pids of Den's Claude sessions, the orchestrator and the lane). Ancestry
// can be escaped by a process that reparents itself; this stops the easy
// path, a tool call that runs a client.
func underAgent(pid int, agents map[int]bool) bool {
	for i := 0; i < 64 && pid > 1; i++ {
		if agents[pid] {
			return true
		}
		_, ppid, err := procStat(pid)
		if err != nil {
			return false
		}
		pid = ppid
	}
	return false
}
