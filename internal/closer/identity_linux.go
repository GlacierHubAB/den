//go:build linux

package closer

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// clockTicks is USER_HZ, the unit of a process's start time in
// /proc/<pid>/stat: 100 on every Linux architecture Den builds for.
const clockTicks = 100

// Hold opens the target's pid as a pidfd, then checks through /proc that
// the process is Claude Code and started no later than its session (plus
// startSlack), then that the pidfd's process is still alive, so what /proc
// showed was that very process. Anything it cannot establish is a refusal.
func (System) Hold(t Target) (Held, error) {
	started := t.Started
	if started.IsZero() {
		started = sessionStart(t.SessionID)
	}
	if t.PID <= 1 {
		return nil, fmt.Errorf("pid %d is not an agent's process", t.PID)
	}
	if started.IsZero() {
		return nil, errors.New("its session's start is not known, so Den cannot tell pid " + strconv.Itoa(t.PID) + " is still that agent")
	}
	fd, err := unix.PidfdOpen(t.PID, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil, ErrGone
	}
	if err != nil {
		return nil, fmt.Errorf("cannot hold pid %d safely (pidfd: %v); nothing was signalled", t.PID, err)
	}
	h := &pidfd{fd: fd}
	if err := checkClaude(t.PID, started); err != nil {
		h.Release()
		return nil, err
	}
	if !h.Alive() {
		h.Release()
		return nil, ErrGone
	}
	return h, nil
}

// checkClaude reads /proc/<pid> and says why it is not the session's Claude
// Code, nil when it is.
func checkClaude(pid int, started time.Time) error {
	dir := "/proc/" + strconv.Itoa(pid)
	comm, err := os.ReadFile(dir + "/comm")
	if errors.Is(err, os.ErrNotExist) {
		return ErrGone
	}
	if err != nil {
		return fmt.Errorf("cannot read pid %d: %v", pid, err)
	}
	exe, _ := os.Readlink(dir + "/exe")
	raw, _ := os.ReadFile(dir + "/cmdline")
	argv := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
	name := strings.TrimSpace(string(comm))
	if !isClaudeProc(name, exe, argv) {
		return fmt.Errorf("pid %d is not Claude Code (it is %q); the session's process has ended and its pid was reused", pid, name)
	}
	at, err := procStart(dir)
	if err != nil {
		return fmt.Errorf("cannot read when pid %d started: %v", pid, err)
	}
	if at.After(started.Add(startSlack)) {
		return fmt.Errorf("pid %d started %s after the session did, so it is another process; the session's has ended", pid, at.Sub(started).Round(time.Second))
	}
	return nil
}

// procStart is when the process in dir started: boot time plus field 22
// of its stat line.
func procStart(dir string) (time.Time, error) {
	stat, err := os.ReadFile(dir + "/stat")
	if err != nil {
		return time.Time{}, err
	}
	// The command name (field 2) is in parentheses and may hold anything.
	i := bytes.LastIndexByte(stat, ')')
	if i < 0 {
		return time.Time{}, errors.New("no command name in stat")
	}
	f := strings.Fields(string(stat[i+1:]))
	const starttime = 22 - 3 // fields after ")" start at field 3
	if len(f) <= starttime {
		return time.Time{}, errors.New("short stat")
	}
	ticks, err := strconv.ParseInt(f[starttime], 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	boot, err := bootTime()
	if err != nil {
		return time.Time{}, err
	}
	return boot.Add(time.Duration(ticks) * time.Second / clockTicks), nil
}

func bootTime() (time.Time, error) {
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

// pidfd is a held process.
type pidfd struct{ fd int }

func (p *pidfd) Signal(sig syscall.Signal) error {
	return unix.PidfdSendSignal(p.fd, sig, nil, 0)
}

// Alive reports whether the held process still exists; a signal 0 to a
// pidfd fails with ESRCH once it is gone, whoever has its pid now.
func (p *pidfd) Alive() bool {
	err := unix.PidfdSendSignal(p.fd, 0, nil, 0)
	return err == nil || errors.Is(err, unix.EPERM)
}

func (p *pidfd) Release() { unix.Close(p.fd) }
