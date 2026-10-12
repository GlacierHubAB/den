package ui

import (
	"os"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/events"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/hypr"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/notify"
	"github.com/GlacierHubAB/den/internal/tree"
)

// attention brings the human back when they are away from Den: desktop
// notifications for what waits on them, reminders, and the stalled notice.
// A notification names the agent and the kind of request, nothing else.
type attention struct {
	since    time.Time              // when Den started watching; nothing is stalled for longer
	lastHook map[string]time.Time   // session id → its last hook event
	noted    map[string]string      // session id → the waiting item a notification went out for
	reminded map[string]bool        // item id → its reminder went out
	send     func(notify.Note) bool // notify.Send; tests record instead
	looking  func(sessionID string) bool
	hosts    *hostNotes // remote notifications and [on] runs, coalesced and capped
}

// ready fills the maps of a zero attention, as a Model built without New has.
func (a *attention) ready() {
	if a.lastHook == nil {
		a.lastHook, a.noted, a.reminded = map[string]time.Time{}, map[string]string{}, map[string]bool{}
	}
	if a.hosts == nil {
		a.hosts = newHostNotes()
	}
	if a.since.IsZero() {
		a.since = time.Now()
	}
}

func newAttention() attention {
	return attention{
		since:    time.Now(),
		lastHook: map[string]time.Time{},
		noted:    map[string]string{},
		reminded: map[string]bool{},
		send:     notify.Send,
		hosts:    newHostNotes(),
	}
}

// needsWhat is how a notification says what an item asks of you.
func needsWhat(k inbox.Kind) string {
	switch k {
	case inbox.Plan:
		return "has a plan for you to review"
	case inbox.Question:
		return "has a question for you"
	}
	return "needs your permission"
}

// lookingAt reports whether the human already sees what a notification
// would say: the agent's terminal is the focused window, or Den's is.
func (m Model) lookingAt(sessionID string) bool {
	if m.att.looking != nil {
		return m.att.looking(sessionID)
	}
	if hostOf(sessionID) != "" {
		return lookingAtDen() // its terminal is on another machine
	}
	return m.atTerminal(sessionID) || hypr.ActiveHolds(os.Getpid())
}

// topOfBranch reports whether nobody in the fleet but the orchestrator
// directs the agent: its finished turns come back to the human.
func (m Model) topOfBranch(sessionID string) bool {
	p := m.parentOf(sessionID)
	return p == "" || p == tree.Orchestrator
}

// attend decides whether a hook event that made an inbox item is worth a
// desktop notification. Anything waiting on the human notifies, once per
// waiting item. A finished turn notifies only for the top of a branch with
// nobody under it still working, so workers reporting to a lead stay quiet.
func (m *Model) attend(r *bridge.Request, it *inbox.Item) tea.Cmd {
	m.att.ready()
	sid := r.SessionID
	m.att.lastHook[sid] = time.Now()
	if r.Event == "SessionEnd" {
		delete(m.att.lastHook, sid)
	}
	return m.attendItem(sid, it)
}

// attendItem is attend's decision for the item a hook event, or an event
// from a remote host, made for the session sid.
func (m *Model) attendItem(sid string, it *inbox.Item) tea.Cmd {
	m.att.ready()
	if it == nil {
		return nil
	}
	var what string
	switch it.Kind {
	case inbox.Permission, inbox.Plan, inbox.Question:
		if !m.cfg.Notify.NeedsYou || it.State == inbox.Resolved || m.itemOpen(m.att.noted[sid]) {
			return nil
		}
		m.att.noted[sid] = it.ID
		what = needsWhat(it.Kind)
		if it.Tool != "" && it.Kind == inbox.Permission && hostOf(sid) != "" {
			// A remote request carries the tool's name only (observe).
			what += " (" + it.Tool + ")"
		}
	case inbox.Done:
		if !m.cfg.Notify.Done || !m.topOfBranch(sid) || m.subtreeBusy(sid) > 0 {
			return nil
		}
		what = "finished"
	case inbox.Failed:
		if !m.cfg.Notify.Failed {
			return nil
		}
		what = "stopped on an error"
		if it.Tool != "" {
			what = "keeps failing tool calls"
		}
	default:
		return nil
	}
	if m.lookingAt(sid) {
		return nil
	}
	return m.notifyCmd(sid, what)
}

// checkStuck runs on the periodic tick: one reminder for anything still
// waiting on the human after notify.remind minutes, and a Stalled notice
// for an agent working with no hook event for stuck.stalled minutes.
func (m *Model) checkStuck(now time.Time) tea.Cmd {
	m.att.ready()
	var cmds []tea.Cmd
	present := map[string]bool{}
	for _, it := range m.store.Items() {
		present[it.ID] = true
		if mins := m.cfg.Notify.Remind; mins == 0 || it.Kind > inbox.Question || it.State == inbox.Resolved || m.att.reminded[it.ID] || now.Sub(it.At) < time.Duration(mins)*time.Minute {
			continue
		}
		m.att.reminded[it.ID] = true
		ev := eventFor(events.StillWaiting, itemKey(it))
		ev.Request, ev.Minutes = request(it.Kind), int(now.Sub(it.At).Minutes())
		cmds = append(cmds, m.emit(ev))
		if m.cfg.Notify.NeedsYou && !m.lookingAt(itemKey(it)) {
			cmds = append(cmds, m.notifyCmd(itemKey(it), "still "+needsWhat(it.Kind)))
		}
	}
	for id := range m.att.reminded {
		if !present[id] {
			delete(m.att.reminded, id)
		}
	}
	// Without hooks nothing ever reports in, and every agent would look stalled.
	if mins := m.cfg.Stuck.Stalled; mins > 0 && m.hooksReady {
		quiet := time.Duration(mins) * time.Minute
		for _, a := range m.agents {
			sid := a.Session.SessionID
			if a.Status != fleet.Working || a.Stale {
				continue // a stale agent's status is unknown, never working
			}
			last := m.att.since
			for _, t := range []time.Time{m.att.lastHook[sid], a.Session.StatusChanged()} {
				if t.After(last) {
					last = t
				}
			}
			if now.Sub(last) < quiet {
				continue
			}
			raw, host := splitKey(sid)
			it := m.store.AddStalledOn(host, raw, now.Sub(last))
			if it == nil {
				continue
			}
			ev := eventFor(events.Stalled, sid)
			ev.Minutes = int(now.Sub(last).Minutes())
			cmds = append(cmds, m.emit(ev))
			if m.cfg.Notify.Stalled && !m.lookingAt(sid) {
				cmds = append(cmds, m.notifyCmd(sid, "has gone quiet"))
			}
		}
	}
	return tea.Batch(cmds...)
}

// onHook runs the [on] command for a hook event, if one is set. A request
// Den answered itself (an always-allow) never waited on the human, so it
// is no needs_you here.
func (m Model) onHook(r *bridge.Request, it *inbox.Item) tea.Cmd {
	in := r.Input
	in.HookEventName = r.Event
	if in.SessionID == "" {
		in.SessionID = r.SessionID
	}
	ev, ok := events.FromHook(in)
	if !ok || (ev.Kind == events.NeedsYou && it == nil) {
		return nil
	}
	return m.runOn(ev)
}

// emit records an event only Den can tell (stalled, still waiting) in the
// stream den hook writes, and runs its [on] command.
func (m Model) emit(ev events.Event) tea.Cmd {
	if m.cfg.Events.Log {
		ev = m.named(ev)
		events.Append(events.Path(), ev) // best effort
	}
	return m.runOn(ev)
}

// runOn runs the [on] command for the event off the UI's goroutine.
func (m Model) runOn(ev events.Event) tea.Cmd {
	ev = m.named(ev)
	command := m.cfg.OnCommand(ev.Kind)
	if command == "" {
		return nil
	}
	return func() tea.Msg {
		events.RunOn(command, ev)
		return nil
	}
}

// named gives the event the name Den shows for the agent; den hook's
// name from the session id is the same unless the fleet says otherwise.
func (m Model) named(ev events.Event) events.Event {
	if a, ok := m.agentFor(remoteKey(ev.Host, ev.Session)); ok {
		ev.Agent = a.Name
	}
	return ev
}

// request names an inbox kind the way the event stream does.
func request(k inbox.Kind) string {
	switch k {
	case inbox.Plan:
		return "plan"
	case inbox.Question:
		return "question"
	}
	return "permission"
}

// itemOpen reports whether the item with this id is still unresolved.
func (m Model) itemOpen(id string) bool {
	if id == "" {
		return false
	}
	for _, it := range m.store.Items() {
		if it.ID == id {
			return it.State != inbox.Resolved
		}
	}
	return false
}

// notifyCmd shows "<agent> <what>" and, when it is clicked, focuses that
// agent's terminal as `den focus` would.
func (m Model) notifyCmd(sessionID, what string) tea.Cmd {
	n := notify.Note{Summary: m.nameFor(sessionID) + " " + what}
	send := m.att.send
	if send == nil {
		return nil
	}
	if host := hostOf(sessionID); host != "" {
		// No remote window to focus: the notice says where the agent is,
		// and offers no click (hostNotes posts it inert).
		n.Summary = m.nameFor(sessionID) + " on " + host + " " + what
		return m.att.hosts.note(host, sessionID, needsYou(what), n, send, time.Now())
	}
	return func() tea.Msg {
		if !send(n) {
			return nil
		}
		agents, err := fleet.Snapshot()
		if err != nil {
			return focusedMsg{err: err}
		}
		a, ok := fleet.Find(agents, sessionID)
		if !ok {
			return nil
		}
		note, err := fleet.Focus(a)
		return focusedMsg{note: note, err: err}
	}
}
