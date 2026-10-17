package ui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/inbox"
)

// headlessRig is taskRig with the default: no agent view. The agent's
// start is faked: it returns pid (500, the rig's Claude Code) or err, and
// its steps come on the returned channel.
func headlessRig(t *testing.T, pid int, err error) (*Model, *taskDesk, chan string, *[]string) {
	t.Helper()
	m, d, _, _, _ := taskRig(t)
	deskShowView = func() bool { return false }
	lines := make(chan string, 16)
	var ran []string
	oldRun, oldKill := deskRunAgent, deskKillGroup
	t.Cleanup(func() { deskRunAgent, deskKillGroup = oldRun, oldKill })
	deskRunAgent = func(argv []string, dir, logPath string, out chan<- string) (int, error) {
		ran = append(ran, strings.Join(argv, " "))
		go func() {
			for l := range lines {
				out <- l
			}
			close(out)
		}()
		return pid, err
	}
	deskKillGroup = func(p int) { ran = append(ran, "killed") }
	return m, d, lines, &ran
}

// runHeadlessTask answers the task's card and runs the handshake, the
// prepare and the headless start as the event loop would; it returns the
// command that reads the agent's steps.
func runHeadlessTask(t *testing.T, m *Model, d *taskDesk) []tea.Msg {
	t.Helper()
	it := desktopCard(*m, inbox.DesktopMission)
	if it == nil {
		t.Fatal("no card")
	}
	m.tab = tabInbox
	m.selectItem(it)
	mm, cmd := m.answer("allow")
	*m = mm.(Model)
	d.title = m.View().WindowTitle
	var after []tea.Msg
	for _, msg := range runBatch(cmd) {
		own, ok := msg.(deskOwnMsg)
		if !ok {
			continue
		}
		next := m.finishOwn(own)
		d.title = m.View().WindowTitle
		for _, msg := range runBatch(next) {
			p, ok := msg.(deskPrepared)
			if !ok {
				continue
			}
			for _, msg := range runBatch(m.finishPrepare(p)) {
				if s, ok := msg.(deskHeadlessStarted); ok {
					after = append(after, runBatch(m.finishHeadless(s))...)
				}
			}
		}
	}
	return after
}

// By default no window opens: the agent is Den's child, with nothing of
// its own to place; its steps show in Den's chat (a call that went fine
// adds nothing), and only Den's window is off limits.
func TestDesktopTaskRunsHeadless(t *testing.T) {
	m, d, lines, ran := headlessRig(t, 500, nil)
	r := taskReq("open a terminal and type hello from Den")
	runBatch(m.handleRequest(r))
	lines <- "▶ keyboard: type \"hello from Den\""
	lines <- "  ✓ typed 14 characters"
	lines <- "  ✗ Den: that is one of Den's own windows"
	close(lines)
	after := runHeadlessTask(t, m, d)
	dv, done := r.Decided()
	if !done || dv.Behavior != "allow" || !strings.Contains(dv.Message, "no window of its own") {
		t.Fatalf("asker got %+v", dv)
	}
	if len(*ran) != 1 || strings.Contains((*ran)[0], "desktop-view") || !strings.Contains((*ran)[0], "--name desktop") {
		t.Fatalf("started %q", *ran)
	}
	dm := taskLive(t, *m)
	if dm.AgentPID != 500 || !dm.placed || dm.view != "" || len(dm.Protected) != 1 || !dm.Protects("0xden") {
		t.Fatalf("mission: pid %d placed %v view %q protected %v", dm.AgentPID, dm.placed, dm.view, dm.Protected)
	}
	// The steps, read as the event loop would.
	pending := after
	for len(pending) > 0 {
		msg := pending[0]
		pending = pending[1:]
		if l, ok := msg.(deskLineMsg); ok {
			pending = append(pending, runBatch(m.showDeskLine(l))...)
		}
	}
	var chat []string
	for _, e := range m.orch.chat {
		chat = append(chat, e.text)
	}
	all := strings.Join(chat, "\n")
	if !strings.Contains(all, "desktop agent: ▶ keyboard: type") || !strings.Contains(all, "desktop agent: ✗ Den: that is one of Den's own windows") {
		t.Errorf("chat lacks the steps:\n%s", all)
	}
	if strings.Contains(all, "typed 14 characters") {
		t.Errorf("a call that went fine was repeated:\n%s", all)
	}
}

// A start that fails ends the mission and tells the asker; a process
// that is not the Claude Code Den resolved is killed.
func TestDesktopHeadlessStartFails(t *testing.T) {
	m, d, lines, _ := headlessRig(t, 0, errors.New("no such file"))
	close(lines)
	r := taskReq("tidy my downloads folder")
	runBatch(m.handleRequest(r))
	runHeadlessTask(t, m, d)
	if dv, done := r.Decided(); !done || dv.Behavior != "deny" || !strings.Contains(dv.Message, "did not start") || m.deskLive() != nil {
		t.Fatalf("asker %+v, live %v", dv, m.deskLive() != nil)
	}

	m, d, lines, ran := headlessRig(t, 777, nil) // not the rig's Claude Code
	close(lines)
	r = taskReq("tidy my downloads folder")
	runBatch(m.handleRequest(r))
	runHeadlessTask(t, m, d)
	if dv, _ := r.Decided(); dv.Behavior != "deny" || m.deskLive() != nil || (*ran)[len(*ran)-1] != "killed" {
		t.Fatalf("a stranger process: asker %+v, ran %q", dv, *ran)
	}
}
