package api

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Peer is who is on the other end of a connection, as the kernel and
// /proc say: never what the client says about itself.
type Peer struct {
	UID int
	PID int
	// Exe is /proc/<pid>/exe at accept, the peer's identity: the key
	// grants, pairings, groups and limits go by. When the kernel will not
	// let Den read it (a non-dumpable process), it is unreadableExe's
	// identity of that one process instead; never "".
	Exe   string
	Start uint64 // the process's start time (clock ticks since boot)
}

// Label is how Den names the peer to the human (ExeLabel).
func (p Peer) Label() string { return ExeLabel(p.Exe) }

// unreadablePrefix starts the identity of a peer whose executable Den
// cannot read: a process that made itself non-dumpable (PR_SET_DUMPABLE
// 0), whose /proc/<pid>/exe the kernel keeps from other processes of the
// same user. Any program can do that, so "unreadable" is no evidence of
// which program it is: the identity is that one process in this boot,
// so it never matches another process, nor any path.
const unreadablePrefix = "unreadable:"

// unreadableExe is the identity of process pid, started at start ticks
// after this boot began.
func unreadableExe(pid int, start uint64) string {
	return unreadablePrefix + bootID() + ":" + strconv.Itoa(pid) + ":" + strconv.FormatUint(start, 10)
}

// bootID is this boot's id (pids and start ticks begin again at each
// boot), "unknown" when it cannot be read.
var bootID = sync.OnceValue(func() string {
	raw, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if id := strings.TrimSpace(string(raw)); err == nil && id != "" {
		return id
	}
	return "unknown"
})

// Unidentified reports whether exe is the identity of a peer whose
// executable Den could not read.
func Unidentified(exe string) bool { return exe == "" || strings.HasPrefix(exe, unreadablePrefix) }

// unidentified names such a peer, for the human.
const unidentified = "a program Den cannot identify"

// ConsoleLabel names the paired console in Den's notes: Den knows a turn,
// an answer or a command is the console's from its proof, never from
// anything the program says, and never names it by what it says it is.
// Which program it is stays unverified while Den cannot read it.
const ConsoleLabel = "the console"

// ExeLabel is how Den names a peer to the human: its executable's base
// name, or, when Den could not read it, unidentified; never a path's
// "." or the identity itself.
func ExeLabel(exe string) string {
	if Unidentified(exe) {
		return unidentified
	}
	if b := filepath.Base(exe); b != "." && b != "/" {
		return b
	}
	return unidentified
}

// ExeShownShort is the path Den read, or unidentified, for one-line notes.
func ExeShownShort(exe string) string {
	if Unidentified(exe) {
		return unidentified
	}
	return exe
}

// ExeShown is the executable as a card shows it: the path Den read, or
// why there is none.
func ExeShown(exe string) string {
	if Unidentified(exe) {
		return "cannot be read (the program hides it), so Den cannot check which program this is"
	}
	return exe
}

// peerCred reads SO_PEERCRED.
func peerCred(c net.Conn) (uid, pid int, err error) {
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
func (s *Server) identify(c net.Conn) (Peer, error) {
	cred := peerCred
	if f := s.credHook.Load(); f != nil {
		cred = *f
	}
	uid, pid, err := cred(c)
	if err != nil {
		return Peer{}, err
	}
	p := Peer{UID: uid, PID: pid}
	if pid <= 0 {
		// No pid (another pid namespace): this connection alone.
		p.Exe = unreadablePrefix + bootID() + ":conn:" + randHex()
		return p, nil
	}
	start, _, err := procStat(pid)
	if err != nil {
		return p, fmt.Errorf("peer %d: %w", pid, err)
	}
	exe, exeErr := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	again, _, err := procStat(pid)
	if err != nil || again != start {
		return p, fmt.Errorf("peer %d changed while it was read", pid)
	}
	p.Start = start
	p.Exe = exe
	if exeErr != nil || exe == "" {
		p.Exe = unreadableExe(pid, start)
	}
	if f := s.idHook.Load(); f != nil {
		p = (*f)(p)
	}
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

// underAgent reports whether pid or one of its ancestors is one of agents
// (Den's live local Claude sessions, the orchestrator and the lane), and
// which: the same pid and, for a session, the same start time, so a pid
// reused by another program never counts. Ancestry can be escaped by a
// process that reparents itself; this stops the easy path, a tool call
// that runs a client.
func underAgent(pid int, agents map[int]AgentProc) (AgentProc, bool) {
	for i := 0; i < 64 && pid > 1; i++ {
		start, ppid, err := procStatFn(pid)
		if a, ok := agents[pid]; ok && (a.Start == 0 || (err == nil && a.Start == start)) {
			return a, true
		}
		if err != nil {
			return AgentProc{}, false
		}
		pid = ppid
	}
	return AgentProc{}, false
}

// procStatFn is procStat; tests replace it with a made-up process tree.
var procStatFn = procStat

// SessionProc is a live session's process for AgentProcs: its pid and
// start, false when the process at pid started more than a little after
// the session did (startedAt, Unix milliseconds), so it is not the
// session's but a later one that reused the pid.
func SessionProc(pid int, startedAt int64, name string) (AgentProc, bool) {
	start, _, err := procStatFn(pid)
	if err != nil || pid <= 1 {
		return AgentProc{}, false
	}
	if startedAt > 0 {
		boot, err := bootTime()
		if err != nil {
			return AgentProc{}, false
		}
		at := boot.Add(time.Duration(start) * time.Second / clockTicks)
		if at.After(time.UnixMilli(startedAt).Add(sessionStartSlack)) {
			return AgentProc{}, false
		}
	}
	return AgentProc{PID: pid, Start: start, Name: name}, true
}

// sessionStartSlack is how long after its process Claude Code may write a
// session's startedAt.
const sessionStartSlack = 30 * time.Second

// clockTicks is USER_HZ, the unit of /proc/<pid>/stat's times on Linux.
const clockTicks = 100

// bootTime is when the machine booted (/proc/stat's btime); tests
// replace it.
var bootTime = func() (time.Time, error) {
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if v, ok := strings.CutPrefix(line, "btime "); ok {
			s, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err != nil {
				return time.Time{}, err
			}
			return time.Unix(s, 0), nil
		}
	}
	return time.Time{}, errors.New("no btime in /proc/stat")
}
