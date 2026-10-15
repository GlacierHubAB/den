package ui

import (
	"encoding/json"
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
		r := deskStopFn(dm.targets(), deskBeacon())
		os.Remove(desktop.PointerPath())
		m.endDeskMission(dm, why)
		m.restoreDeskMission(dm)
		dm.save()
		lines = append(lines, r.Line())
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
		return m.say("Desktop stop: "+sanitize.Label(out, 200), strings.Contains(out, "did not start"))
	}
	dm := m.deskLive()
	if dm == nil {
		return m.say("No desktop mission is running. /desktop stop stops one at once.", false)
	}
	return m.say("Desktop mission on workspace "+strconv.Itoa(dm.Workspace)+" by "+sanitize.Label(dm.AgentName, 40)+": /desktop stop ends it at once.", false)
}

// handleDesktopStop is `den desktop stop` over den.sock: never refused.
func (m *Model) handleDesktopStop(r *bridge.Request) tea.Cmd {
	out := m.stopDesktop("den desktop stop")
	m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: "Desktop stop: " + out, at: time.Now()}))
	r.Reply(bridge.Decision{Behavior: "allow", Message: out})
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
	if dm.AgentPID > 1 && deskStart(dm.AgentPID) != dm.AgentStart {
		m.finishDeskMission(dm, "the agent's session ended")
		return nil
	}
	b, active := desktop.ReadBeacon(deskBeacon())
	switch {
	case active && b.PID == dm.ChildPID && b.Acting(time.Now()):
		m.desk.chip = "DESKTOP · acting · /desktop stop"
	default:
		m.desk.chip = "DESKTOP · on · /desktop stop"
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
	r := deskStopFn(dm.targets(), deskBeacon())
	os.Remove(desktop.PointerPath())
	m.endDeskMission(dm, why)
	dm.save()
	deskNotify(notify.Note{Summary: "Desktop control ended", Body: why})
	m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: "Desktop mission ended: " + why + "; " + r.Line() + ". Its journal, screenshots and log are deleted in 10 minutes; /desktop keep keeps them.", at: time.Now()}))
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
	deskNotify(notify.Note{Summary: "Desktop control on", Body: "A Den agent may drive workspace " + strconv.Itoa(dm.Workspace) + ". /desktop stop or den desktop stop ends it."})
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

// deskRecorded is every session id in Den's desktop mission files, and
// whether any mission folder's file could not be read.
var deskRecorded = func() (map[string]bool, bool) {
	out := map[string]bool{}
	unsure := false
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
		for _, f := range append(files, filepath.Join(dm.Dir, "journal.ndjson"), filepath.Join(dm.Dir, "desktop.log")) {
			os.Remove(f)
		}
	}
	return waiting
}
