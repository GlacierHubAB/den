package ui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/config"
	"github.com/GlacierHubAB/den/internal/desktop"
	"github.com/GlacierHubAB/den/internal/hypr"
	"github.com/GlacierHubAB/den/internal/procs"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// A task mission: one start card, "Control your mouse and keyboard to:
// <task>", then the whole desktop for that task, with no app config.
// Den's own surfaces stay off limits by Hyprland address (desktop/task.go):
// the window Den runs in, found by a title handshake before any agent
// exists, and the agent view, the terminal Den opens for the agent. Den
// counts every forwarded call and the minutes against the human's
// [desktop] limits and ends the task at either.

// Seams for tests.
var (
	deskLimits = config.DesktopTask
	// deskAncestors are Den's own process and the ones above it.
	deskAncestors = func() []int {
		var out []int
		for _, p := range procs.Ancestors(os.Getpid()) {
			out = append(out, p.PID)
		}
		return append(out, os.Getpid())
	}
	deskKeepOnTop = hypr.KeepOnTop
	// deskAncestorOf reports whether anc is pid or a process above it.
	deskAncestorOf = func(anc, pid int) bool {
		if anc > 1 && anc == pid {
			return true
		}
		for _, p := range procs.Ancestors(pid) {
			if p.PID == anc && anc > 1 {
				return true
			}
		}
		return false
	}
	// deskOwnWait is how long Den looks for its own window by the title.
	deskOwnWait = 3 * time.Second
	// deskSettle is the pause for a title change to reach the compositor.
	deskSettle = 150 * time.Millisecond
	// deskTaskEvery is how often Den checks its own windows during a task
	// and pins the agent view again.
	deskTaskEvery = time.Second
)

// ownTitlePrefix starts the title Den shows while it finds its window.
const ownTitlePrefix = "den-own-window-"

// restingTitle is Den's terminal title once the handshake is over.
const restingTitle = "den"

// offerTask puts a task on its start card. Only the human at Den's own
// window answers it (deskTTYOnly; the dock and God mode never get it).
func (m *Model) offerTask(r *bridge.Request, in bridge.DesktopMissionInput, setup *deskSetup, note string) tea.Cmd {
	task, err := desktop.TaskText(in.Task)
	if err == nil {
		var steps []string
		if steps, err = desktop.TaskSteps(in.HumanSteps); err == nil {
			in.HumanSteps = steps
		}
	}
	if err != nil {
		r.Reply(bridge.Decision{Behavior: "deny", Message: err.Error()})
		return nil
	}
	minutes, actions := deskLimits()
	sentence := desktop.TaskSentence + task
	var body []string
	if note != "" {
		// A widening says so before anything else.
		body = append(body, note)
	}
	body = append(body, "Asked by "+sanitize.Label(in.AskedBy, 60)+" (as it says).", sentence)
	if len(in.HumanSteps) > 0 {
		body = append(body, "Steps you do (the agent hands each to you with your turn and waits):")
		for _, st := range in.HumanSteps {
			body = append(body, "  "+st)
		}
	}
	body = append(body,
		"While it runs, the agent can do anything you can at the keyboard: run commands as you in a terminal, type into other agents' terminals and their permission prompts, use the clipboard, read the screen, and act in apps you are signed in to.",
		"Den keeps its own window and the agent view off limits and refuses a short fixed list (ending your session, reloading or rebinding Hyprland, the panic stop's bind).",
		fmt.Sprintf("Den stops it after %d minutes or %d actions, whichever comes first (task_minutes and task_actions in your config's [desktop]).", minutes, actions),
		"This one task only. Model: "+in.Model+". Stop it any time: /desktop stop, or den desktop stop; it also stops if Den quits.",
		"Den needs its own window to start: run Den in its own terminal window, with its tab in front.",
	)
	if setup != nil {
		body = append(body, setup.lines()...)
	}
	it := m.store.AddDesktopMission(sanitize.Label(sentence, 120), body, r)
	if m.desk.offers == nil {
		m.desk.offers = map[string]deskOffer{}
	}
	m.desk.offers[it.ID] = deskOffer{in: in, task: task, steps: in.HumanSteps, minutes: minutes, actions: actions, setup: setup}
	m.flash, m.flashErr = "Desktop control asked: ^Y starts it, ^N declines.", false
	m.refresh()
	return nil
}

// deskOwnMsg is Den's own window, as the title handshake found it.
type deskOwnMsg struct {
	itemID string
	offer  deskOffer
	addr   string
	pid    int
	err    error
}

// startTask begins a task the human approved: Den shows a fresh token as
// its terminal title and looks for the one window that carries it. No
// agent exists yet, and the token is never logged.
func (m Model) startTask(itemID string, offer deskOffer) (tea.Model, tea.Cmd) {
	b := make([]byte, 16)
	rand.Read(b)
	token := ownTitlePrefix + hex.EncodeToString(b)
	m.desk.title = token
	cmd := func() tea.Msg {
		addr, pid, err := findOwnWindow(token)
		return deskOwnMsg{itemID: itemID, offer: offer, addr: addr, pid: pid, err: err}
	}
	model, say := m.say("Finding Den's own window…", false)
	return model, tea.Batch(say, cmd)
}

// findOwnWindow is the window whose title is exactly token, of a process
// above Den's. More than one such title, or one on another process,
// refuses: Den would not know which window is its own.
func findOwnWindow(token string) (string, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), deskOwnWait+3*time.Second)
	defer cancel()
	chain := deskAncestors()
	deadline := time.Now().Add(deskOwnWait)
	for {
		time.Sleep(deskSettle)
		snap, err := deskSnapshot(ctx)
		if err != nil {
			return "", 0, err
		}
		var hits []desktop.Window
		for _, w := range snap.Windows {
			if w.Title == token {
				hits = append(hits, w)
			}
		}
		switch {
		case len(hits) > 1:
			return "", 0, errors.New("more than one window shows Den's title, so Den cannot tell which is its own")
		case len(hits) == 1:
			for _, pid := range chain {
				if pid == hits[0].PID && pid > 1 {
					return hits[0].Address, pid, nil
				}
			}
			return "", 0, errors.New("the window showing Den's title is not Den's terminal")
		}
		if time.Now().After(deadline) {
			return "", 0, errors.New("Den could not find its own window: run Den in its own terminal window (not inside tmux or screen), or switch to its tab, and ask again")
		}
	}
}

// finishOwn puts Den's title back and checks the window once more before
// the task is prepared: the same address, the same process.
func (m *Model) finishOwn(msg deskOwnMsg) tea.Cmd {
	m.desk.title = restingTitle
	if msg.err != nil {
		return m.taskDidNotStart(msg.itemID, sanitize.Line(msg.err.Error()))
	}
	return func() tea.Msg {
		time.Sleep(deskSettle)
		return prepareTask(msg.itemID, msg.offer, msg.addr, msg.pid)
	}
}

// taskDidNotStart settles a task's card when nothing started.
func (m *Model) taskDidNotStart(itemID, why string) tea.Cmd {
	for _, it := range m.store.Items() {
		if it.ID == itemID {
			m.store.Settle(it, false, "approved, but it did not start: "+why)
		}
	}
	m.flash, m.flashErr = "The desktop task did not start: "+why, true
	m.refresh()
	return nil
}

// prepareTask confirms Den's window and makes the mission's folder. It
// runs off the update loop.
func prepareTask(itemID string, offer deskOffer, own string, pid int) deskPrepared {
	p := deskPrepared{itemID: itemID, offer: offer, id: newMissionID(), own: own}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	snap, err := deskSnapshot(ctx)
	if err != nil {
		p.err = err
		return p
	}
	if w, ok := snap.Window(own); !ok || w.PID != pid {
		p.err = errors.New("Den's own window changed while Den found it; ask again")
		return p
	}
	p.focused = snap.Active
	p.dir = filepath.Join(deskStateDir(), p.id)
	if err := os.MkdirAll(p.dir, 0o700); err != nil {
		p.err = err
		return p
	}
	os.Chmod(filepath.Dir(p.dir), 0o700)
	return p
}

// taskSpend counts one forwarded call against a task's limits. It says
// why the call is refused, and ends the mission, at either limit.
func (m *Model) taskSpend(dm *deskMission) (string, tea.Cmd) {
	if !dm.Task {
		return "", nil
	}
	if !dm.deadline.IsZero() && time.Now().After(dm.deadline) {
		why := "the task's time ran out"
		return why, m.finishDeskMission(dm, why)
	}
	if dm.actions >= dm.limitActions {
		why := "the task used all " + strconv.Itoa(dm.limitActions) + " actions it may"
		return why, m.finishDeskMission(dm, why)
	}
	dm.actions++
	return "", nil
}

// deskTaskCheckMsg is one look at a task's surfaces: what is gone, and
// whether pinning the agent view failed.
type deskTaskCheckMsg struct {
	mission string
	gone    string
	pinErr  error
}

// deskTaskCheck reads the desktop for a task: Den's window and the agent
// view must still be there, and the view floats, pinned, on top (P5).
func (m *Model) deskTaskCheck(dm *deskMission) tea.Cmd {
	if !dm.Task || !dm.placed || dm.checking || time.Since(dm.checkedAt) < deskTaskEvery {
		return nil
	}
	dm.checking, dm.checkedAt = true, time.Now()
	id, view := dm.ID, dm.view
	protected := make([]string, 0, len(dm.Protected))
	for a := range dm.Protected {
		protected = append(protected, a)
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		msg := deskTaskCheckMsg{mission: id}
		snap, err := deskSnapshot(ctx)
		if err != nil {
			msg.gone = "Den cannot read the desktop (" + sanitize.Line(err.Error()) + ")"
			return msg
		}
		if why := goneSurface(protected, view, snap); why != "" {
			msg.gone = why
			return msg
		}
		w, _ := snap.Window(view)
		msg.pinErr = deskKeepOnTop(view, w.Floating, w.Pinned)
		return msg
	}
}

// goneSurface says which of Den's surfaces is no longer on the desktop.
func goneSurface(protected []string, view string, snap desktop.Snapshot) string {
	for _, a := range protected {
		if _, ok := snap.Window(a); !ok {
			if a == view {
				return "the agent view was closed"
			}
			return "Den's own window is gone"
		}
	}
	return ""
}

// finishTaskCheck ends a task whose surfaces went, and says once if the
// agent view could not be pinned.
func (m *Model) finishTaskCheck(msg deskTaskCheckMsg) tea.Cmd {
	dm := m.deskMission(msg.mission)
	if dm == nil {
		return nil
	}
	dm.checking = false
	if dm.Ended != "" {
		return nil
	}
	if msg.gone != "" {
		return m.finishDeskMission(dm, msg.gone)
	}
	if msg.pinErr != nil && !dm.pinWarned {
		dm.pinWarned = true
		m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: "Den could not keep the agent view on top: " + sanitize.Line(msg.pinErr.Error()) + ". It is still off limits to the agent; /desktop stop ends the task.", at: time.Now()}))
	}
	return nil
}

// taskGone is the reason a task ends when a call finds one of Den's
// surfaces gone, "" while all are there.
func (dm *deskMission) taskGone(snap desktop.Snapshot) string {
	if !dm.Task || !dm.placed {
		return ""
	}
	protected := make([]string, 0, len(dm.Protected))
	for a := range dm.Protected {
		protected = append(protected, a)
	}
	return goneSurface(protected, dm.view, snap)
}
