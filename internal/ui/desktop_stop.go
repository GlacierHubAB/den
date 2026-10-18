package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/desktop"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/notify"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// Stopping desktop control. Every way in ends in stopDesktop: /desktop
// stop in Den, `den desktop stop` over den.sock (for a Hyprland bind the
// human adds), desktop.stop from the console. None is ever refused for
// who asks: it only lowers.

// Seams for tests.
var (
	deskStopFn = desktop.Stop
	deskBeacon = desktop.BeaconPath
	deskNotify = notify.Post
)

// deskTickMsg drives the desktop chip and notices an agent that ended.
type deskTickMsg struct{}

func deskTick() tea.Cmd {
	return tea.Tick(250*time.Millisecond, func(time.Time) tea.Msg { return deskTickMsg{} })
}

// deskLive is the live mission, nil when none.
func (m Model) deskLive() *deskMission {
	for _, dm := range m.desk.missions {
		if dm.Ended == "" {
			return dm
		}
	}
	return nil
}

// targets are a mission's processes as Den recorded them.
func (dm *deskMission) targets() desktop.Targets {
	return desktop.Targets{
		Child: desktop.Proc{PID: dm.ChildPID, Start: dm.ChildStart, Exe: dm.ChildExe},
		Proxy: desktop.Proc{PID: dm.ProxyPID, Start: dm.ProxyStart, Exe: dm.ProxyExe},
		Agent: desktop.Proc{PID: dm.AgentPID, Start: dm.AgentStart, Exe: dm.AgentExe},
	}
}

// deskState is what `den desktop stop` reads when no Den is running: the
// mission's processes and the windows Den launched.
type deskStateFile struct {
	Mission string          `json:"mission"`
	Session string          `json:"session"` // the agent's session id: never restored or reopened
	Targets desktop.Targets `json:"targets"`
	Windows []string        `json:"windows"`
	Ended   bool            `json:"ended"`
}

// saveDeskState writes the mission's state file (0600, in its 0700 dir).
func (dm *deskMission) save() {
	if dm.Dir == "" {
		return
	}
	st := deskStateFile{Mission: dm.ID, Session: dm.SessionID, Targets: dm.targets(), Ended: dm.Ended != ""}
	for a := range dm.Windows {
		st.Windows = append(st.Windows, a)
	}
	data, _ := json.Marshal(st)
	tmp := filepath.Join(dm.Dir, "mission.json.tmp")
	if os.WriteFile(tmp, data, 0o600) == nil {
		os.Rename(tmp, filepath.Join(dm.Dir, "mission.json"))
	}
}

// deskStopFailedChip is the header chip while a stop could not signal one
// of a mission's processes.
const deskStopFailedChip = "DESKTOP · STOP FAILED · /desktop stop"

// stopDesk stops dm's processes, and ends the mission only once none of
// them still runs. A stop that could not signal one keeps the mission live:
// every call of its agent refused (refusal), the console still told it is
// active, the chip red; Den says so loudly the first time, and each later
// stop (the tick's, or /desktop stop) tries again. It reports whether the
// mission ended.
func (m *Model) stopDesk(dm *deskMission, why string) (desktop.StopReport, bool) {
	r := deskStopFn(dm.targets(), deskBeacon())
	if !r.OK() {
		first := dm.stopFailed == ""
		dm.stopFailed = r.Line()
		if first {
			dm.stopWhy = why
			m.desk.chip = deskStopFailedChip
			deskNotify(notify.Note{Summary: "Desktop control NOT stopped", Body: r.Line(), Urgent: true})
			m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: "Desktop stop FAILED (" + why + "): " + r.Line() + ". The mission stays on, and every call of its agent is refused, until Den can stop it; Den tries again every few seconds, and /desktop stop tries at once.", at: time.Now()}))
			m.flash, m.flashErr = "Desktop control NOT stopped: "+sanitize.Label(r.Line(), 160), true
		}
		return r, false
	}
	if dm.stopWhy != "" {
		why = dm.stopWhy
	}
	dm.stopFailed = ""
	os.Remove(desktop.PointerPath())
	m.endDeskMission(dm, why)
	return r, true
}

// stopDesktop ends every live mission now: hypruse and the agent stopped
// by pidfd, the mission's windows closed. It returns what it did.
func (m *Model) stopDesktop(why string) string {
	var lines []string
	stopped := false
	for _, dm := range m.desk.missions {
		if dm.Ended != "" {
			continue
		}
		stopped = true
		r, ended := m.stopDesk(dm, why)
		lines = append(lines, r.Line())
		if !ended {
			dm.stopRestore = true
			continue
		}
		m.restoreDeskMission(dm)
		dm.save()
		deskNotify(notify.Note{Summary: "Desktop control stopped", Body: why})
	}
	if !stopped {
		// Nothing of Den's: still say who holds the desktop, if anyone.
		r := deskStopFn(desktop.Targets{}, deskBeacon())
		lines = append(lines, r.Line())
	}
	m.refresh()
	return strings.Join(lines, "\n")
}

// desktopCommand is /desktop: stop, or how it stands.
func (m Model) desktopCommand(args []string) (tea.Model, tea.Cmd) {
	if len(args) > 0 && strings.EqualFold(args[0], "keep") {
		var last *deskMission
		for _, dm := range m.desk.missions {
			if dm.Ended != "" && !dm.purged && (last == nil || dm.endedAt.After(last.endedAt)) {
				last = dm
			}
		}
		if last == nil {
			return m.say("No ended desktop mission's files are waiting to be deleted.", true)
		}
		last.kept = true
		return m.say("Kept the last desktop mission's journal, screenshots and log in "+last.Dir+".", false)
	}
	if len(args) > 0 && strings.EqualFold(args[0], "stop") {
		out := m.stopDesktop("you stopped it with /desktop stop")
		m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: "Desktop stop: " + out, at: time.Now()}))
		return m.say("Desktop stop: "+sanitize.Label(out, 200), strings.Contains(out, "did not start") || strings.Contains(out, "could not stop"))
	}
	dm := m.deskLive()
	if dm == nil {
		return m.say("No desktop mission is running. /desktop stop stops one at once.", false)
	}
	if dm.Task {
		left := time.Until(dm.deadline).Round(time.Second)
		return m.say(fmt.Sprintf("Desktop task by %s: %d of %d actions used, %s left. /desktop stop ends it at once.", sanitize.Label(dm.AgentName, 40), dm.actions, dm.limitActions, left), false)
	}
	return m.say("Desktop mission on workspace "+strconv.Itoa(dm.Workspace)+" by "+sanitize.Label(dm.AgentName, 40)+": /desktop stop ends it at once.", false)
}

// handleDesktopStop is `den desktop stop` over den.sock: never refused.
func (m *Model) handleDesktopStop(r *bridge.Request) tea.Cmd {
	out := m.stopDesktop("den desktop stop")
	m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: "Desktop stop: " + out, at: time.Now()}))
	behavior := "allow"
	if strings.Contains(out, "could not stop") {
		behavior = "deny" // `den desktop stop` then fails, never reads as done
	}
	r.Reply(bridge.Decision{Behavior: behavior, Message: out})
	return nil
}

// deskOnTick follows the live mission: the beacon for the chip, and the
// agent's process, whose end ends the mission.
func (m *Model) deskOnTick() tea.Cmd {
	dm := m.deskLive()
	if dm == nil {
		m.desk.chip = ""
		if m.deskPurge(time.Now()) {
			return tea.Tick(5*time.Second, func(time.Time) tea.Msg { return deskTickMsg{} })
		}
		return nil
	}
	if dm.stopFailed != "" {
		// A stop that could not signal: try again each tick, quietly, and
		// keep the chip red until nothing of the mission runs.
		if cmd := m.finishDeskMission(dm, dm.stopWhy); dm.Ended != "" {
			if dm.stopRestore {
				m.restoreDeskMission(dm)
			}
			return cmd
		}
		m.desk.chip = deskStopFailedChip
		return deskTick()
	}
	if dm.AgentPID > 1 && deskStart(dm.AgentPID) != dm.AgentStart {
		m.finishDeskMission(dm, "the agent's session ended")
		return nil
	}
	if dm.Task && !dm.deadline.IsZero() && time.Now().After(dm.deadline) {
		return m.finishDeskMission(dm, "the task's time ran out")
	}
	if dm.AgentPID <= 1 && !dm.launchedAt.IsZero() && time.Since(dm.launchedAt) > deskStartWait {
		// Den learns the agent's process when its desktop server first
		// calls; one that never does would hold the one mission slot.
		m.finishDeskMission(dm, "the agent did not start")
		m.restoreDeskMission(dm)
		return nil
	}
	b, active := desktop.ReadBeacon(deskBeacon())
	switch {
	case active && b.PID == dm.ChildPID && b.Acting(time.Now()):
		m.desk.chip = "DESKTOP · acting · /desktop stop"
	default:
		m.desk.chip = "DESKTOP · on · /desktop stop"
	}
	if check := m.deskTaskCheck(dm); check != nil {
		return tea.Batch(deskTick(), check)
	}
	return deskTick()
}

// deskAgentStopped takes a desktop agent's Stop hook: its turn ended with
// its report, so the mission is done. Den passes the report on, fenced,
// and ends the mission; the apps' windows stay for the human.
func (m *Model) deskAgentStopped(r *bridge.Request) tea.Cmd {
	for _, dm := range m.desk.missions {
		if dm.Ended != "" || dm.SessionID == "" || dm.SessionID != r.SessionID {
			continue
		}
		if dm.Paused {
			return nil // waiting on the human's step
		}
		dm.report = r.Input.LastAssistant
		return m.finishDeskMission(dm, "the agent finished")
	}
	return nil
}

// finishDeskMission ends a mission that ran its course: hypruse and the
// agent stopped, the report passed to the orchestrator inside Den's
// agent_report fence, never as plain text.
func (m *Model) finishDeskMission(dm *deskMission, why string) tea.Cmd {
	r, over := m.stopDesk(dm, why)
	if !over {
		m.refresh()
		return nil // still live: the tick tries again
	}
	why = dm.Ended
	dm.save()
	deskNotify(notify.Note{Summary: "Desktop control ended", Body: why})
	ended := "Desktop mission ended: " + why + "; " + r.Line() + ". Its journal, screenshots and log are deleted in 10 minutes; /desktop keep keeps them."
	if dm.stepsHidden > 0 {
		ended += fmt.Sprintf(" (%d more steps of the agent's not shown.)", dm.stepsHidden)
	}
	m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: ended, at: time.Now()}))
	m.refresh()
	if m.orch.proc == nil {
		return nil
	}
	body := "Desktop mission for " + dm.AskedBy + " ended: " + why + "."
	if strings.TrimSpace(dm.report) != "" {
		body += "\nThe agent's report, in its words:\n" + dm.report
	}
	name := dm.AgentName
	if name == "" {
		name = "desktop agent"
	}
	return m.sendReport(fleet.Agent{Name: name}, body)
}

// startDeskWatch begins the tick and says desktop control is on.
func (m *Model) startDeskWatch(dm *deskMission) tea.Cmd {
	body := "A Den agent may drive workspace " + strconv.Itoa(dm.Workspace) + ". /desktop stop or den desktop stop ends it."
	if dm.Task {
		body = "A Den agent may use your whole desktop for one task. /desktop stop or den desktop stop ends it."
	}
	deskNotify(notify.Note{Summary: "Desktop control on", Body: body})
	m.desk.chip = "DESKTOP · on · /desktop stop"
	return deskTick()
}

// isDesktopSession reports whether a session is (or was) a desktop
// mission's agent: a live mission's, or one recorded in a mission file. A
// desktop agent is never restored or reopened: Den's ordinary resume would
// give its conversation, which read the screen, an ordinary agent's tools.
func (m Model) isDesktopSession(id string) bool {
	if id == "" {
		return false
	}
	for _, dm := range m.desk.missions {
		if dm.SessionID == id {
			return true
		}
	}
	recorded, unsure := deskRecorded()
	// A mission file Den cannot read could be any session's: none is
	// restored while one is there (fail closed).
	return unsure || recorded[id]
}

// knownDesktopSession is isDesktopSession without the fail-closed part:
// only sessions Den knows were desktop agents. The roster uses it, so an
// unreadable mission file blocks restoring without forgetting the fleet.
func (m Model) knownDesktopSession(id string) bool {
	if id == "" {
		return false
	}
	for _, dm := range m.desk.missions {
		if dm.SessionID == id {
			return true
		}
	}
	recorded, _ := deskRecorded()
	return recorded[id]
}

// deskRecorded is every session id Den recorded as a desktop agent's (the
// permanent list, then the mission files), and whether any record could
// not be read.
var deskRecorded = func() (map[string]bool, bool) {
	out := map[string]bool{}
	unsure := false
	raw, err := os.ReadFile(deskSessionsPath())
	switch {
	case err == nil:
		for _, line := range strings.Split(string(raw), "\n") {
			if id := strings.TrimSpace(line); id != "" {
				out[id] = true
			}
		}
	case !os.IsNotExist(err):
		unsure = true
	}
	dirs, _ := filepath.Glob(filepath.Join(deskStateDir(), "*"))
	for _, d := range dirs {
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(d, "mission.json"))
		var st deskStateFile
		if err != nil || json.Unmarshal(raw, &st) != nil || st.Session == "" {
			unsure = true
			continue
		}
		out[st.Session] = true
	}
	return out, unsure
}

// deskKeepFor is how long an ended mission's private files (the journal,
// the screenshot crops, the proxy log) stay before Den deletes them,
// unless the human types /desktop keep.
var deskKeepFor = 10 * time.Minute

// deskPurge deletes the private files of missions that ended more than
// deskKeepFor ago and were not kept. It reports whether any ended mission
// still waits for that.
func (m *Model) deskPurge(now time.Time) bool {
	waiting := false
	for _, dm := range m.desk.missions {
		if dm.Ended == "" || dm.kept || dm.purged || dm.Dir == "" {
			continue
		}
		if now.Sub(dm.endedAt) < deskKeepFor {
			waiting = true
			continue
		}
		dm.purged = true
		files, _ := filepath.Glob(filepath.Join(dm.Dir, "crop-*.png"))
		for _, f := range append(files, filepath.Join(dm.Dir, "journal.ndjson"), filepath.Join(dm.Dir, "desktop.log"), filepath.Join(dm.Dir, "agent.log")) {
			os.Remove(f)
		}
	}
	return waiting
}

// deskSessionsPath is Den's permanent list of desktop agents' session ids:
// appended before each agent starts, never purged, so a desktop agent
// stays out of restore even when its mission folder is gone.
func deskSessionsPath() string { return filepath.Join(deskStateDir(), "sessions") }

// recordDeskSession appends a session id to the permanent list (0600).
func recordDeskSession(id string) error {
	if err := os.MkdirAll(deskStateDir(), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(deskSessionsPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(id + "\n")
	return err
}

// deskStartWait is how long after its terminal opens an agent's desktop
// server has to call in before Den ends the mission.
var deskStartWait = time.Minute
