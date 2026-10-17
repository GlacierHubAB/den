package ui

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/config"
	"github.com/GlacierHubAB/den/internal/desktop"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/sanitize"
	"github.com/GlacierHubAB/den/internal/spawn"
)

// A desktop agent runs with no window by default: no terminal opens in
// front of the work. Den starts it as a child of its own, and shows its
// steps (its words and tool calls, as the agent view would print them,
// sanitised) in Den's own chat, which den-api never serves; Drey's overlay shows
// it at work on the desktop. It ends with Den (a parent-death signal), and
// /desktop stop or den desktop stop ends it at once. [desktop] agent_view =
// true brings the terminal back.

// Seams for tests.
var (
	deskShowView = config.DesktopAgentView
	// deskRunAgent starts a headless desktop agent and returns its pid;
	// the lines its stream shows come on lines, which is closed when it
	// ends.
	deskRunAgent = runHeadless
	// deskKillGroup ends a headless agent's process group (it has its own).
	deskKillGroup = func(pid int) { syscall.Kill(-pid, syscall.SIGKILL) }
)

// deskHeadlessStarted is a headless agent's start.
type deskHeadlessStarted struct {
	itemID  string
	mission string
	focused string
	pid     int
	lines   chan string
	err     error
}

// deskLineMsg is one line of a headless agent's steps.
type deskLineMsg struct {
	mission string
	line    string
	lines   chan string
}

// startHeadless starts the agent off the update loop.
func (m *Model) startHeadless(itemID, mission, focused, dir, logPath string, argv []string) tea.Cmd {
	return func() tea.Msg {
		lines := make(chan string, 256)
		pid, err := deskRunAgent(argv, dir, logPath, lines)
		// The apps Den opened take the keyboard when they map: the focus
		// goes back where it was.
		if focused != "" {
			deskFocus(focused)
		}
		return deskHeadlessStarted{itemID: itemID, mission: mission, focused: focused, pid: pid, lines: lines, err: err}
	}
}

// runHeadless starts argv as Den's child with no terminal: stdin empty,
// stdout read for its steps, stderr to logPath. It is signalled when the
// thread that started it ends; that thread is pinned and lives until the
// agent has ended, so the agent ends with Den, however Den ends.
func runHeadless(argv []string, dir, logPath string, lines chan<- string) (int, error) {
	started := make(chan error, 1)
	var pid int
	go func() {
		runtime.LockOSThread() // never unlocked: the thread ends with this goroutine
		defer close(lines)
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Dir = dir
		cmd.Env = spawn.ChildEnv(os.Environ(), nil)
		cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM, Setpgid: true}
		if f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			defer f.Close()
			cmd.Stderr = f
		}
		out, err := cmd.StdoutPipe()
		if err != nil {
			started <- err
			return
		}
		if err := cmd.Start(); err != nil {
			started <- err
			return
		}
		pid = cmd.Process.Pid
		started <- nil
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 64<<10), desktop.MaxMessage)
		for sc.Scan() {
			for _, l := range desktop.ViewLines(sc.Bytes()) {
				lines <- l
			}
		}
		cmd.Wait()
	}()
	if err := <-started; err != nil {
		return 0, err
	}
	return pid, nil
}

// waitDeskLine waits for the next line of a headless agent's steps.
func waitDeskLine(mission string, lines chan string) tea.Cmd {
	return func() tea.Msg {
		l, ok := <-lines
		if !ok {
			return nil
		}
		return deskLineMsg{mission: mission, line: l, lines: lines}
	}
}

// Limits on a headless agent's steps in Den's chat: they are the agent's
// own words and can carry screen text, so an agent printing in a loop
// cannot flood the chat or the dock.
const (
	deskStepsPerMinute = 30
	deskStepRunes      = 200
)

// showDeskLine puts a step in Den's chat: the agent's words and its tool
// calls, and a failed call; a call that went fine says no more than the
// call already did. Each is cut to deskStepRunes, and at most
// deskStepsPerMinute show; the rest are counted, and the mission's end
// says how many. It keeps reading until the stream ends, so the agent
// never blocks on its output.
func (m *Model) showDeskLine(msg deskLineMsg) tea.Cmd {
	l := msg.line
	dm := m.deskMission(msg.mission)
	if dm == nil || dm.Ended != "" || strings.TrimSpace(l) == "" || strings.HasPrefix(l, "  ✓ ") {
		return waitDeskLine(msg.mission, msg.lines)
	}
	now := time.Now()
	if now.Sub(dm.stepsSince) >= time.Minute {
		dm.stepsSince, dm.stepsShown = now, 0
	}
	if dm.stepsShown >= deskStepsPerMinute {
		dm.stepsHidden++
		return waitDeskLine(msg.mission, msg.lines)
	}
	dm.stepsShown++
	m.addUnthreaded(chatEntry{role: "note", text: "desktop agent: " + sanitize.Label(l, deskStepRunes), at: now})
	m.refresh()
	return waitDeskLine(msg.mission, msg.lines)
}

// finishHeadless records the agent's process and answers the asker, as
// finishSpawn does for an agent in a terminal; with no agent view there is
// no window of its own to place.
func (m *Model) finishHeadless(s deskHeadlessStarted) tea.Cmd {
	dm := m.deskMission(s.mission)
	var it *inbox.Item
	for _, x := range m.store.Items() {
		if x.ID == s.itemID {
			it = x
		}
	}
	if dm == nil {
		return nil
	}
	if s.err != nil || s.pid <= 1 || !deskSameExe(s.pid, dm.Claude) {
		if s.pid > 1 {
			deskKillGroup(s.pid) // whatever it is, Den started it: it does not stay
		}
		why := "the agent did not start"
		if s.err != nil {
			why += ": " + sanitize.Line(s.err.Error())
		}
		end := m.finishDeskMission(dm, why)
		m.restoreDeskMission(dm)
		if it != nil {
			m.store.Settle(it, false, "approved, but "+why)
		}
		m.refresh()
		return end
	}
	dm.AgentName = "desktop"
	dm.launchedAt = time.Now()
	dm.AgentPID, dm.AgentStart, dm.AgentExe = s.pid, deskStart(s.pid), deskExeOf(s.pid)
	// No agent view: nothing of its own to place or to check it runs under.
	dm.placed, dm.viewChecked = true, true
	dm.save()
	msg := fmt.Sprintf("Started the desktop agent on workspace %d with %d app window(s), with no window of its own: its steps show in Den's chat. It reports to Den when done; Den passes the report to you.", dm.Workspace, len(dm.Windows))
	if dm.Task {
		msg = "Started the desktop agent on the task, with no window of its own: its steps show in Den's chat. It reports to Den when done, or Den stops it at the human's limits; Den passes the report to you."
	}
	if it != nil {
		m.store.Settle(it, true, msg)
	}
	m.flash, m.flashErr = msg, false
	m.refresh()
	return tea.Batch(m.startDeskWatch(dm), waitDeskLine(dm.ID, s.lines))
}
