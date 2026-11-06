package ui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/desksand"
	"github.com/GlacierHubAB/den/internal/desktop"
	"github.com/GlacierHubAB/den/internal/sysd"
)

// What a desktop mission launches runs in a slice of the mission's own
// under the systemd user manager: Den freezes the slice when the mission
// pauses, and Den's window answers only once it is frozen; Den thaws it
// when the mission goes on, and stops it when the mission ends, the
// window holding answers until it is gone. Den also counts the calls it
// let through to hypruse as in flight until the proxy reports each done:
// a pause first lets them end (deskDrainBound), else the mission ends.

// The manager, as Den drives it; tests replace these.
var (
	deskSysStart  = sysd.StartService
	deskSysFreeze = sysd.Freeze
	deskSysThaw   = sysd.Thaw
	deskSysStop   = sysd.Stop
	deskSysState  = sysd.State
	deskSysList   = sysd.List
	// deskCgroupOf is a process's cgroup (v2), "" when it does not read.
	deskCgroupOf = func(pid int) string {
		raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cgroup")
		if err != nil {
			return ""
		}
		for _, l := range strings.Split(string(raw), "\n") {
			if p, ok := strings.CutPrefix(l, "0::"); ok {
				return p
			}
		}
		return ""
	}
	// deskDispatch runs one hyprctl dispatch for Den itself.
	deskDispatch = func(args ...string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, "hyprctl", append([]string{"dispatch"}, args...)...).Run()
	}
)

const (
	// deskDrainBound is how long a pause waits for the calls in flight;
	// hypruse's own typing gives up after 15 s.
	deskDrainBound = 20 * time.Second
)

// deskFreezeWait bounds the read of the slice going frozen, and a freeze
// that failed or did not confirm is tried again after deskStopRetry, never
// at once; deskStopWait bounds the wait for a stopped slice to be gone,
// and deskStopRetry is also when a stop that failed is tried again. Tests
// shorten them.
var (
	deskFreezeWait = 2 * time.Second
	deskStopWait   = 15 * time.Second
	deskStopRetry  = 5 * time.Second
)

// deskInstance names this Den's slices apart from another Den's (a second
// one, an isolated test Den): the start of the SHA-256 of its state dir.
func deskInstance() string {
	sum := sha256.Sum256([]byte(stateDir()))
	return hex.EncodeToString(sum[:6])
}

// deskSlicePrefix is the start of every slice of this Den's. No '-' in a
// slice's name: systemd reads each '-' as a level of slices above it
// (den.slice, den-desk.slice, ...) and leaves those standing after.
func deskSlicePrefix() string { return "den_desk_" + deskInstance() + "_" }

// slice is the mission's slice.
func (dm *deskMission) slice() string { return deskSliceOf(dm.ID) }

// deskSliceOf is the slice of the mission with that id.
func deskSliceOf(id string) string { return deskSlicePrefix() + sysd.EscapePart(id) + ".slice" }

// sliceOK says why the mission's unit names do not hold, "" when they do:
// the escape gives the id back, and the longest name fits systemd's limit.
func (dm *deskMission) sliceOK() string {
	// EscapePart holds no '_' (Den's separator) and no '-' (a level of
	// slices), and gives the id back: one id, one set of names.
	if e := sysd.EscapePart(dm.ID); strings.ContainsAny(e, "_-") {
		return "the mission's id does not make a unit name"
	} else if back, ok := sysd.Unescape(e); !ok || back != dm.ID {
		return "the mission's id does not make a unit name"
	}
	if len(dm.service(999999)) > sysd.MaxUnitName {
		return "the mission's id is too long for a unit name"
	}
	return ""
}

// service is the n-th launch's service.
func (dm *deskMission) service(n int) string {
	return strings.TrimSuffix(dm.slice(), ".slice") + "_" + strconv.Itoa(n) + ".service"
}

// inSlice reports whether a process runs in the mission's slice.
func (dm *deskMission) inSlice(pid int) bool {
	return pid > 1 && strings.Contains(deskCgroupOf(pid), "/"+dm.slice()+"/")
}

// issueCall counts a call Den lets through as in flight, and names it for
// the proxy's done. A look, and launch (Den's own), are not counted.
func (dm *deskMission) issueCall(tool string) string {
	if deskLooks[tool] || tool == "launch" {
		return ""
	}
	if dm.inflight == nil {
		dm.inflight = map[string]time.Time{}
	}
	id := newToken()
	dm.inflight[id] = time.Now()
	return id
}

// deskDone is the proxy's done: only from the mission's own proxy, only
// for a call Den named for this mission, once.
func (m *Model) deskDone(r *bridge.Request, dm *deskMission, in bridge.DesktopInput) {
	if !m.fromProxy(dm, r) {
		r.Reply(bridge.Decision{Behavior: "deny", Message: "Den: only this mission's desktop server may say that"})
		return
	}
	delete(dm.inflight, in.Call) // an unknown or repeated id: nothing
	r.Reply(bridge.Decision{Behavior: "allow"})
}

// deskLaunchedMsg is a launch's outcome: the result for the agent, or why
// it did not run.
type deskLaunchedMsg struct {
	req     *bridge.Request
	mission string
	call    string
	result  string
	err     error
}

// deskLaunch runs an allowed launch in the mission's slice: the command,
// through /bin/sh -c as Hyprland's exec runs it, as a service of its own;
// Den then waits for its window (a new window whose process is in the
// slice) and places it.
func (m *Model) deskLaunch(r *bridge.Request, dm *deskMission, v desktop.Verdict, before desktop.Snapshot) tea.Cmd {
	command, _ := v.Args["command"].(string)
	workspace, _ := v.Args["workspace"].(string)
	wait := 8.0
	if w, ok := v.Args["wait_s"].(float64); ok {
		wait = min(max(w, 1), 30)
	}
	dm.launches++
	dm.launched = true
	call := newToken()
	if dm.inflight == nil {
		dm.inflight = map[string]time.Time{}
	}
	dm.inflight[call] = time.Now() // until its service runs and its window is placed
	svc := sysd.Service{Name: dm.service(dm.launches), Slice: dm.slice(), Description: "Den desktop mission " + dm.ID}
	seen := map[string]bool{}
	for _, w := range before.Windows {
		seen[w.Address] = true
	}
	id, dir, x11 := dm.ID, dm.Dir, dm.x11
	listen := ""
	if dm.Scoped {
		if dm.listener == nil || !dm.listener.Live() {
			return func() tea.Msg {
				return deskLaunchedMsg{req: r, mission: id, call: call, err: errors.New("the task's sandboxed Wayland socket is gone, and Den never opens apps on the compositor's own")}
			}
		}
		listen = dm.listener.Socket()
	}
	return func() tea.Msg {
		out := deskLaunchedMsg{req: r, mission: id, call: call}
		// The sandbox (desk_sand.go): refused rather than loosened.
		spec, err := deskSandSpec(dir, deskProfileName(command), x11)
		spec.WaylandListener = listen
		if err == nil {
			err = deskSandCheck(spec, id)
		}
		if err != nil {
			out.err = err
			return out
		}
		svc.Argv, svc.Env, svc.Dir = desksand.Argv(spec, command), desksand.Env(spec), desksand.Work(dir)
		if err := deskSysStart(svc); err != nil {
			out.err = err
			return out
		}
		deadline := time.Now().Add(time.Duration(wait * float64(time.Second)))
		for time.Now().Before(deadline) {
			snap, err := deskSnapshot(context.Background())
			if err == nil {
				for _, w := range snap.Windows {
					if seen[w.Address] || !strings.Contains(deskCgroupOf(w.PID), "/"+svc.Slice+"/") {
						continue
					}
					res := map[string]any{"address": w.Address, "class": w.Class, "title": w.Title, "workspace": strconv.Itoa(w.Workspace)}
					if workspace != "" && workspace != strconv.Itoa(w.Workspace) {
						deskDispatch("movetoworkspacesilent", workspace+",address:"+w.Address)
						res["workspace"] = workspace
					}
					raw, _ := json.Marshal(res)
					out.result = string(raw)
					return out
				}
			}
			time.Sleep(200 * time.Millisecond)
		}
		out.result = fmt.Sprintf("launched, but no window of it appeared within %.0fs; slow apps may open late: call desktop() to find the window", wait)
		return out
	}
}

// deskLaunched answers the agent's launch.
func (m *Model) deskLaunched(msg deskLaunchedMsg) {
	if dm := m.deskMission(msg.mission); dm != nil {
		delete(dm.inflight, msg.call)
	}
	if msg.err != nil {
		msg.req.Reply(bridge.Decision{Behavior: "deny", Message: "Den: could not start it in the mission's slice (" + oneLine(msg.err.Error()) + "); nothing runs"})
		return
	}
	msg.req.Reply(bridge.Decision{Behavior: "allow", Message: msg.result})
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// deskFrozenMsg, deskThawedMsg and deskStoppedMsg are the manager's
// answers for a mission's slice; deskDrainMsg looks at the calls in
// flight again.
type (
	deskFrozenMsg struct {
		mission string
		ok      bool
	}
	deskThawedMsg struct {
		mission string
		err     error
	}
	deskStoppedMsg struct {
		mission string
		gone    bool
	}
	deskDrainMsg    struct{ mission string }
	deskRetryMsg    struct{ mission string }
	deskLeftoverMsg struct{ still []string }
)

// deskSwept is what the start's sweep could not stop; New takes it.
var deskSwept []string

// deskListAgain stands in deskSwept and the leftovers for a listing that
// failed: Den holds until one is read.
const deskListAgain = "(the listing of this Den's slices)"

// deskManagerHere reports whether the user has a systemd manager at all:
// its private socket in the runtime dir. Tests replace it.
var deskManagerHere = func() bool {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		return false
	}
	_, err := os.Stat(dir + "/systemd/private")
	return err == nil
}

// deskDrive moves each mission's slice towards what its state asks:
// frozen while paused (after its calls in flight end), running while
// not, gone once the mission ended. It runs after every message.
func (m *Model) deskDrive() tea.Cmd {
	var cmds []tea.Cmd
	if len(m.desk.leftover) > 0 && !m.desk.leftoverBusy && !time.Now().Before(m.desk.leftoverRetry) {
		m.desk.leftoverBusy = true
		left := append([]string(nil), m.desk.leftover...)
		cmds = append(cmds, func() tea.Msg {
			var still []string
			for _, n := range left {
				if n == deskListAgain {
					// The start's listing failed: list again; until a listing
					// is read, this Den's slices are not known to be gone.
					names, err := deskSysList(deskSlicePrefix())
					if err != nil {
						still = append(still, deskListAgain)
						continue
					}
					still = append(still, names...)
					continue
				}
				deskSysStop(n)
				if u, err := deskSysState(n); err != nil || !u.Gone() {
					still = append(still, n)
				}
			}
			return deskLeftoverMsg{still: still}
		})
	}
	for _, dm := range m.desk.missions {
		if c := m.deskDriveOne(dm); c != nil {
			cmds = append(cmds, c)
		}
	}
	return tea.Batch(cmds...)
}

func (m *Model) deskDriveOne(dm *deskMission) tea.Cmd {
	id, slice := dm.ID, dm.slice()
	switch {
	case dm.Ended != "":
		if !dm.launched || dm.reaped || dm.reaping || time.Now().Before(dm.reapAfter) {
			return nil
		}
		dm.reaping = true
		return func() tea.Msg {
			if deskSysStop(slice) != nil {
				return deskStoppedMsg{mission: id}
			}
			deadline := time.Now().Add(deskStopWait)
			for time.Now().Before(deadline) {
				if u, err := deskSysState(slice); err == nil && u.Gone() {
					return deskStoppedMsg{mission: id, gone: true}
				}
				time.Sleep(100 * time.Millisecond)
			}
			return deskStoppedMsg{mission: id}
		}
	case dm.paused():
		if dm.frozen || dm.freezing || time.Now().Before(dm.freezeAfter) {
			return nil // frozen, freezing, or a failed freeze waiting to be tried again
		}
		if dm.pausedAt.IsZero() {
			dm.pausedAt = time.Now()
		}
		if len(dm.inflight) > 0 {
			if time.Since(dm.pausedAt) > deskDrainBound {
				return m.finishDeskMission(dm, "a call of the agent did not end within 20 s of the pause")
			}
			if dm.draining {
				return nil
			}
			dm.draining = true
			return tea.Tick(200*time.Millisecond, func(time.Time) tea.Msg { return deskDrainMsg{mission: id} })
		}
		dm.freezing = true
		if !dm.launched {
			return func() tea.Msg { return deskFrozenMsg{mission: id, ok: true} } // nothing launched: nothing runs
		}
		return func() tea.Msg {
			if deskSysFreeze(slice) != nil {
				// A slice that is not loaded holds nothing: frozen as it is.
				u, err := deskSysState(slice)
				return deskFrozenMsg{mission: id, ok: err == nil && u.Gone()}
			}
			deadline := time.Now().Add(deskFreezeWait)
			for time.Now().Before(deadline) {
				if u, err := deskSysState(slice); err == nil && (u.Freeze == "frozen" || u.Gone()) {
					return deskFrozenMsg{mission: id, ok: true}
				}
				time.Sleep(50 * time.Millisecond)
			}
			return deskFrozenMsg{mission: id}
		}
	default:
		dm.pausedAt, dm.freezeAfter = time.Time{}, time.Time{}
		if !dm.frozen {
			return nil
		}
		// Going on: the hold is back at once, then the slice thaws.
		dm.frozen = false
		if !dm.launched {
			return nil
		}
		return func() tea.Msg { return deskThawedMsg{mission: id, err: deskSysThaw(slice)} }
	}
}

// deskSliceMsg takes the manager's answers.
func (m *Model) deskSliceMsg(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case deskLeftoverMsg:
		m.desk.leftover, m.desk.leftoverBusy = msg.still, false
		if len(msg.still) > 0 {
			m.desk.leftoverRetry = time.Now().Add(deskStopRetry)
			return tea.Tick(deskStopRetry, func(time.Time) tea.Msg { return deskRetryMsg{} })
		}
		m.refresh()
	case deskDrainMsg:
		if dm := m.deskMission(msg.mission); dm != nil {
			dm.draining = false
		}
	case deskFrozenMsg:
		dm := m.deskMission(msg.mission)
		if dm == nil {
			return nil
		}
		dm.freezing = false
		switch {
		case msg.ok && dm.paused():
			dm.frozen, dm.freezeNoted = true, false
		case msg.ok:
			// It went on while freezing: thaw what froze.
			if dm.launched {
				slice := dm.slice()
				return func() tea.Msg { return deskThawedMsg{mission: msg.mission, err: deskSysThaw(slice)} }
			}
		default:
			// The hold stays; Den tries again after deskStopRetry, never
			// at once (a failing manager is not asked in a loop).
			dm.freezeAfter = time.Now().Add(deskStopRetry)
			id := msg.mission
			retry := tea.Tick(deskStopRetry, func(time.Time) tea.Msg { return deskRetryMsg{mission: id} })
			if !dm.freezeNoted {
				dm.freezeNoted = true
				m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: "Den could not pause the apps the desktop agent launched: Den's window still answers nothing that approves. End the mission (/desktop stop) to answer.", at: time.Now()}))
			}
			return retry
		}
	case deskThawedMsg:
		dm := m.deskMission(msg.mission)
		if dm != nil && msg.err != nil && dm.Ended == "" {
			return m.finishDeskMission(dm, "Den could not resume the apps it launched")
		}
	case deskStoppedMsg:
		dm := m.deskMission(msg.mission)
		if dm == nil {
			return nil
		}
		dm.reaping = false
		if msg.gone {
			dm.reaped = true
			dm.closeListener() // nothing it launched runs now
			m.refresh()
			return nil
		}
		dm.reapAfter = time.Now().Add(deskStopRetry)
		if !dm.stopNoted {
			dm.stopNoted = true
			m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: "Den could not stop the apps the desktop agent launched: Den's window answers nothing that approves until they are gone. Den tries again every 5 seconds.", at: time.Now()}))
		}
		return tea.Tick(deskStopRetry, func(time.Time) tea.Msg { return deskRetryMsg{mission: msg.mission} })
	}
	return nil
}

// SweepDesktopSlices stops what this Den's missions launched before it
// last ended (a crash leaves slices behind), before Den's window takes
// any answer: only this Den's own slices, never another Den's. It waits,
// bounded, for each to be gone, and says what it could not stop.
func SweepDesktopSlices() []string {
	names, err := deskSysList(deskSlicePrefix())
	if err != nil {
		if !deskManagerHere() {
			deskSwept = nil
			return nil // no user manager at all: nothing of Den's can run in one
		}
		// A listing that failed (a timeout, a bus error) is not a listing
		// that is empty: hold, and list again from the retry.
		deskSwept = []string{deskListAgain}
		return deskSwept
	}
	var left []string
	for _, n := range names {
		if !strings.HasPrefix(n, deskSlicePrefix()) || !strings.HasSuffix(n, ".slice") {
			continue
		}
		deskSysStop(n)
		gone := false
		for deadline := time.Now().Add(deskStopWait); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
			if u, err := deskSysState(n); err == nil && u.Gone() {
				gone = true
				break
			}
		}
		if !gone {
			left = append(left, n)
		}
	}
	deskSwept = left
	return left
}
