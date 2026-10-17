package desktop

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Beacon is hypruse's status file: which process holds the desktop, and
// when it last acted. It is an ordinary file of the user's: status only,
// never authorisation.
type Beacon struct {
	PID        int     `json:"pid"`
	Started    float64 `json:"started"`
	LastAction string  `json:"last_action"`
	LastTS     float64 `json:"last_ts"`
}

// BeaconPath is $XDG_RUNTIME_DIR/hypruse/state.json.
func BeaconPath() string {
	return filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "hypruse", "state.json")
}

// procRoot is /proc; tests replace it.
var procRoot = "/proc"

// ReadBeacon reads the beacon. active is true only when its pid is a live
// process that is not a zombie: a killed agent leaves the file behind, and
// a zombie still has a /proc entry.
func ReadBeacon(path string) (b Beacon, active bool) {
	raw, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(raw, &b) != nil || b.PID <= 1 {
		return b, false
	}
	return b, procState(b.PID) != 0 && procState(b.PID) != 'Z'
}

// Acting reports whether the beacon's last act is recent.
func (b Beacon) Acting(now time.Time) bool {
	return b.LastTS > 0 && now.Sub(time.Unix(0, int64(b.LastTS*1e9))) < 2*time.Second
}

// procState is the state letter of a process (R, S, Z ...), 0 when gone.
func procState(pid int) byte {
	st, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0
	}
	i := bytes.LastIndexByte(st, ')') // the command name may hold ')' and spaces
	if i < 0 || i+2 >= len(st) {
		return 0
	}
	return st[i+2]
}

// Proc names one process for good: its pid, its start in clock ticks, and
// the executable it runs (resolved), as Den recorded them when it started.
type Proc struct {
	PID   int    `json:"pid"`
	Start string `json:"start"`
	Exe   string `json:"exe"`
}

// Targets are a mission's processes, for the stop.
type Targets struct {
	Child Proc `json:"child"` // hypruse
	Proxy Proc `json:"proxy"` // den desktop-mcp
	Agent Proc `json:"agent"` // the agent's Claude Code
}

// StopReport says what a stop did.
type StopReport struct {
	Stopped []string // what was signalled
	Foreign string   // the beacon is held by a process Den did not start: said loudly, never signalled
	Notes   []string
	Took    time.Duration
}

// Line is the report in one line.
func (r StopReport) Line() string {
	var parts []string
	if r.Foreign != "" {
		parts = append(parts, r.Foreign)
	}
	if len(r.Stopped) > 0 {
		parts = append(parts, "stopped "+strings.Join(r.Stopped, ", "))
	} else {
		parts = append(parts, "nothing of Den's was running")
	}
	parts = append(parts, r.Notes...)
	return strings.Join(parts, "; ") + fmt.Sprintf(" (%d ms)", r.Took.Milliseconds())
}

// Held is a process held by a pidfd: every signal goes to it and never to
// whatever takes its pid next.
type Held interface {
	Signal(sig syscall.Signal) error
	Alive() bool
	Release()
}

// hold opens pid as a pidfd and checks it is the process recorded: same
// start, same executable. Tests replace it.
var hold = func(p Proc) (Held, error) {
	if p.PID <= 1 || p.Start == "" {
		return nil, errors.New("not recorded")
	}
	fd, err := unix.PidfdOpen(p.PID, 0)
	if err != nil {
		return nil, err
	}
	h := &pidfd{fd: fd}
	if startTicks(p.PID) != p.Start || (p.Exe != "" && exeOf(p.PID) != p.Exe) || !h.Alive() {
		h.Release()
		return nil, errors.New("pid " + strconv.Itoa(p.PID) + " is another process now")
	}
	return h, nil
}

// Stop ends a mission's desktop control, fastest first: hypruse (it
// releases any held button and drops the beacon on SIGTERM), then the
// agent's Claude Code, which also ends the proxy. Every process is held by
// a pidfd and checked (start, executable) before a signal. The beacon is
// read for one thing: if a process Den did not start holds it, Stop says
// so and leaves it alone.
func Stop(t Targets, beacon string) StopReport {
	began := time.Now()
	var r StopReport
	if b, active := ReadBeacon(beacon); active && (b.PID != t.Child.PID || startTicks(b.PID) != t.Child.Start) {
		r.Foreign = fmt.Sprintf("the desktop is held by a process Den did not start (pid %d); Den stopped only its own agent", b.PID)
	}
	term := func(name string, p Proc, grace time.Duration) {
		h, err := hold(p)
		if err != nil {
			return
		}
		defer h.Release()
		if h.Signal(syscall.SIGTERM) != nil {
			return
		}
		r.Stopped = append(r.Stopped, name)
		for deadline := time.Now().Add(grace); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if !h.Alive() || procState(p.PID) == 'Z' {
				return
			}
		}
		if h.Alive() && procState(p.PID) != 'Z' {
			h.Signal(syscall.SIGKILL)
			r.Notes = append(r.Notes, name+" did not end on SIGTERM and was killed")
		}
	}
	term("hypruse", t.Child, 300*time.Millisecond)
	term("the desktop server", t.Proxy, 100*time.Millisecond)
	term("the agent", t.Agent, 300*time.Millisecond)
	r.Took = time.Since(began)
	return r
}

// startTicks is a process's start in clock ticks since boot, "" when gone.
func startTicks(pid int) string {
	raw, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return ""
	}
	s := string(raw)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return ""
	}
	f := strings.Fields(s[i+1:])
	if len(f) <= 19 {
		return ""
	}
	return f[19]
}

// exeOf is the executable a process runs, resolved.
func exeOf(pid int) string {
	p, _ := os.Readlink(filepath.Join(procRoot, strconv.Itoa(pid), "exe"))
	return p
}

// RecordProc is the Proc for a live pid, read now.
func RecordProc(pid int) Proc {
	return Proc{PID: pid, Start: startTicks(pid), Exe: exeOf(pid)}
}

type pidfd struct{ fd int }

func (p *pidfd) Signal(sig syscall.Signal) error { return unix.PidfdSendSignal(p.fd, sig, nil, 0) }

func (p *pidfd) Alive() bool {
	err := unix.PidfdSendSignal(p.fd, 0, nil, 0)
	return err == nil || errors.Is(err, unix.EPERM)
}

func (p *pidfd) Release() { unix.Close(p.fd) }
