// Package closer ends Claude Code agents and closes their terminal windows,
// on purpose and with a plan the human sees first. A plan says exactly which
// agents close, which are refused and why, and what to watch out for; Run
// carries it out: a graceful stop, a short wait, and a hard kill only when
// the human forced it. Nothing here decides on its own; the UI asks.
package closer

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Target is one agent that may be closed.
type Target struct {
	SessionID string
	Name      string // critter name
	Peer      string // session name
	PID       int
	Status    string   // "working", "needs you", "idle", "unknown", "gone"
	Window    string   // Hyprland address of its terminal, "" when unknown
	WindowHow string   // how the window was matched: "title", "cwd", "herdr"
	Dir       string   // working directory, for the uncommitted-changes check
	Children  []string // names of the agents it directs
	Dirty     []string // uncommitted paths in Dir, filled by Inspect
	// Idle is how long since the agent's last activity (its last change
	// of busy/idle status as Claude Code recorded it), 0 when unknown.
	Idle time.Duration
}

// Refusal is a target the plan will not close.
type Refusal struct {
	Target Target
	Why    string
}

// Self is what must never be closed: Den's own process and window, and the
// orchestrator's session.
type Self struct {
	PID          int
	Orchestrator string             // the orchestrator's session id, "" when not running
	Window       string             // Hyprland address of the window Den runs in, "" when unknown
	IsAncestor   func(pid int) bool // reports whether pid is above Den's process
}

// Plan is what a confirmation shows and Run carries out.
type Plan struct {
	Targets []Target
	Refused []Refusal
	// Skipped are protected agents a bulk close left alone, and Held are
	// flagged ones left unselected; both are shown so the list is exact.
	Skipped []Refusal
	Held    []Refusal
	Force   bool   // close working agents too, and kill what does not stop
	From    string // who asked: "you" or "orchestrator"
	Reason  string // the orchestrator's stated reason, "" for the human's own
	// IdleFor is the duration an idle close asked for, 0 for none.
	IdleFor time.Duration
}

// Build applies the safety rules to the candidates. Targets keep their order.
func Build(cands []Target, force bool, self Self) Plan {
	p := Plan{Force: force}
	for _, t := range cands {
		why := refuse(t, force, self)
		if why != "" {
			p.Refused = append(p.Refused, Refusal{Target: t, Why: why})
			continue
		}
		p.Targets = append(p.Targets, t)
	}
	return p
}

func refuse(t Target, force bool, self Self) string {
	switch {
	case t.SessionID != "" && t.SessionID == self.Orchestrator:
		return "it is the orchestrator"
	case t.PID != 0 && t.PID == self.PID:
		return "it is Den itself"
	case t.PID != 0 && self.IsAncestor != nil && self.IsAncestor(t.PID):
		return "Den runs inside it"
	case t.PID == 0:
		return "no process to close"
	case t.Status == "gone":
		return "already gone"
	case (t.Status == "working" || t.Status == "needs you") && !force:
		return t.Status + " · add --force to close it anyway"
	}
	return ""
}

// Empty reports whether nothing will close.
func (p Plan) Empty() bool { return len(p.Targets) == 0 }

// Title is the one-line question.
func (p Plan) Title() string {
	switch n := len(p.Targets); {
	case n == 0:
		return "Nothing to close"
	case n == 1:
		return "Close " + p.Targets[0].Name + "?"
	default:
		return fmt.Sprintf("Close %d agents?", n)
	}
}

// Lines spells the plan out: one line per agent with its process and
// window, the warnings that matter, and what was refused.
func (p Plan) Lines() []string {
	var out []string
	if p.From == "orchestrator" {
		line := "The orchestrator asks to close these agents."
		if p.Reason != "" {
			line += " Its reason: " + p.Reason
		}
		out = append(out, line)
	}
	if p.IdleFor > 0 {
		out = append(out, "Agents idle for "+ShortDuration(p.IdleFor)+" or more, by their last change of busy/idle status.")
	}
	for _, t := range p.Targets {
		line := fmt.Sprintf("%s (%s) · %s · pid %d", t.Name, t.Peer, t.Status, t.PID)
		if t.Idle > 0 {
			line = fmt.Sprintf("%s (%s) · idle %s · pid %d", t.Name, t.Peer, roundIdle(t.Idle), t.PID)
		}
		switch {
		case t.Window != "":
			line += " · window " + t.Window + " closes"
		default:
			line += " · no window found, only the process ends"
		}
		out = append(out, line)
		if len(t.Dirty) > 0 {
			out = append(out, fmt.Sprintf("  ! %d uncommitted change(s) in %s: %s", len(t.Dirty), t.Dir, strings.Join(limit(t.Dirty, 4), ", ")))
		}
		if len(t.Children) > 0 {
			out = append(out, "  ! it directs "+strings.Join(t.Children, ", ")+"; they stay open unless also selected")
		}
	}
	if p.Force {
		out = append(out, "Forced: a process that does not stop within the wait is killed.")
	} else if len(p.Targets) > 0 {
		out = append(out, "Each gets a graceful stop first; one that does not stop is left running.")
	}
	for _, r := range p.Skipped {
		out = append(out, "Skipped "+r.Target.Name+": "+r.Why)
	}
	for _, r := range p.Held {
		line := "Holds context, not selected: " + r.Target.Name
		if r.Why != "" {
			line += " (" + r.Why + ")"
		}
		out = append(out, line+" · /close "+r.Target.Name+" to include it")
	}
	for _, r := range p.Refused {
		out = append(out, "Not "+r.Target.Name+": "+r.Why)
	}
	return out
}

// roundIdle prints a duration the way a human reads it: 1h12m, 45m, 3d2h.
func roundIdle(d time.Duration) string {
	d = d.Round(time.Minute)
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	case d >= time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
}

// ShortDuration prints a duration the way it was typed: 1h, 1h30m, 45m, 2d.
func ShortDuration(d time.Duration) string {
	d = d.Round(time.Minute)
	switch {
	case d >= 24*time.Hour && d%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", int(d.Hours())/24)
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d >= time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
}

// ParseIdle reads a duration like "30m", "1h", "2h", "1h30m", "1d".
func ParseIdle(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil {
			return 0, fmt.Errorf("not a duration: %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("not a duration: %q (try 30m, 1h, 2h)", s)
	}
	return d, nil
}

// Summary is the one-line outcome after Run.
func Summary(results []Result) string {
	var ended, killed, left, windows []string
	for _, r := range results {
		switch {
		case r.Killed:
			killed = append(killed, r.Target.Name)
		case r.Ended:
			ended = append(ended, r.Target.Name)
		default:
			left = append(left, r.Target.Name)
		}
		if r.WindowClosed {
			windows = append(windows, r.Target.Name)
		}
	}
	var parts []string
	if len(ended) > 0 {
		parts = append(parts, "closed "+strings.Join(ended, ", "))
	}
	if len(killed) > 0 {
		parts = append(parts, "killed "+strings.Join(killed, ", "))
	}
	if len(left) > 0 {
		parts = append(parts, "still running: "+strings.Join(left, ", "))
	}
	if len(parts) == 0 {
		return "nothing closed"
	}
	s := strings.Join(parts, " · ")
	if len(windows) > 0 {
		s += fmt.Sprintf(" · %d window(s) closed", len(windows))
	}
	return s
}

// Inspect fills what the plan shows but Build cannot know on its own: the
// uncommitted changes in each target's checkout.
func Inspect(ctx context.Context, p *Plan) {
	for i := range p.Targets {
		p.Targets[i].Dirty = dirty(ctx, p.Targets[i].Dir)
	}
}

// dirty lists uncommitted paths in dir, "" when it is not a checkout.
func dirty(ctx context.Context, dir string) []string {
	if dir == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "status", "--porcelain", "--untracked-files=normal").Output()
	if err != nil {
		return nil
	}
	var paths []string
	for _, l := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if len(l) > 3 {
			paths = append(paths, strings.TrimSpace(l[3:]))
		}
	}
	sort.Strings(paths)
	return paths
}

// Ops is what Run needs from the system; tests fake it.
type Ops interface {
	Signal(pid int, sig syscall.Signal) error
	Alive(pid int) bool
	CloseWindow(address string) error
}

// Waits, shortened in tests.
var (
	StopWait  = 5 * time.Second
	KillWait  = 2 * time.Second
	pollEvery = 100 * time.Millisecond
)

// Result is what happened to one target.
type Result struct {
	Target       Target
	Ended        bool // the process is gone
	Killed       bool // it needed SIGKILL
	WindowClosed bool
	Err          error
}

// Run carries the plan out: SIGTERM, a wait, SIGKILL only when forced, then
// the window. A window that is Den's own is never touched.
func Run(ctx context.Context, p Plan, self Self, ops Ops) []Result {
	var out []Result
	for _, t := range p.Targets {
		r := Result{Target: t}
		if err := ops.Signal(t.PID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
			r.Err = err
			out = append(out, r)
			continue
		}
		r.Ended = waitGone(ctx, ops, t.PID, StopWait)
		if !r.Ended && p.Force {
			if err := ops.Signal(t.PID, syscall.SIGKILL); err == nil || errors.Is(err, syscall.ESRCH) {
				r.Ended = waitGone(ctx, ops, t.PID, KillWait)
				r.Killed = r.Ended
			} else {
				r.Err = err
			}
		}
		if r.Ended && t.Window != "" && t.Window != self.Window {
			if err := ops.CloseWindow(t.Window); err == nil {
				r.WindowClosed = true
			} else if !strings.Contains(err.Error(), "not found") {
				r.Err = err
			}
		}
		out = append(out, r)
	}
	return out
}

func waitGone(ctx context.Context, ops Ops, pid int, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		if !ops.Alive(pid) {
			return true
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false
		}
		select {
		case <-ctx.Done():
			return !ops.Alive(pid)
		case <-time.After(pollEvery):
		}
	}
}

func limit(xs []string, n int) []string {
	if len(xs) <= n {
		return xs
	}
	return append(append([]string{}, xs[:n]...), fmt.Sprintf("… %d more", len(xs)-n))
}
