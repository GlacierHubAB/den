package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/closer"
	"github.com/GlacierHubAB/den/internal/fleet"

	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/names"
	"github.com/GlacierHubAB/den/internal/sanitize"
	"github.com/GlacierHubAB/den/internal/tree"
	"github.com/GlacierHubAB/den/internal/winmark"
)

// Closing agents. The human (^X, /close) or the orchestrator (close_agent)
// proposes; Den turns the proposal into a plan under the safety rules and
// puts it in the inbox as a question that lists exactly what would close.
// ^Y carries it out, ^N declines. The orchestrator's request waits on that
// answer and gets the outcome back.

// closeOps is how a close is carried out; tests replace it so no real
// process is ever signalled.
var closeOps closer.Ops = closer.System{}

// closeNow is the clock for idle durations; tests pin it.
var closeNow = time.Now

// ensureGuard gives a zero Model (tests build one) an empty guard.
func (m *Model) ensureGuard() {
	if m.guard == nil {
		m.guard = &closer.Guard{}
	}
}

// idleFor is how long the agent has been idle by its last activity, and
// whether that is known at all.
func idleFor(a fleet.Agent, now time.Time) (time.Duration, bool) {
	if a.LastActive.IsZero() {
		return 0, false
	}
	return now.Sub(a.LastActive), true
}

// guardKey is the guard's key for an agent: its session id as Den keys it
// (an agent on a host carries the host, "id@host"). The guard never
// matches a name.
func guardKey(a fleet.Agent) string { return a.Session.SessionID }

// guardReason says why the guard keeps an agent out of a bulk close.
func (m Model) guardReason(a fleet.Agent) string {
	if m.guard == nil {
		return ""
	}
	if _, ok := m.guard.IsProtected(guardKey(a)); ok {
		return "protected (" + m.guardLabel(a) + ")"
	}
	// Folder rules are folders on this machine: an agent on a host has no
	// path here, so only its session, or a task that mentions the folder's
	// name, can protect it.
	dirs := append([]string{a.Session.CWD}, rootsOf(a)...)
	return m.guard.Protected(guardKey(a), dirs, a.Labels.Task)
}

// unprotectHint says how the human lets a protected agent be closed:
// /unprotect for the agent itself, or the folder in protected.json.
func (m Model) unprotectHint(a fleet.Agent) string {
	if _, ok := m.guard.IsProtected(guardKey(a)); ok {
		return "/unprotect " + m.guardLabel(a) + " to allow it"
	}
	return "remove its folder from protected.json to allow it"
}

// guardHeld is the flag note of a flagged agent.
func (m Model) guardHeld(a fleet.Agent) (string, bool) {
	return m.guard.Held(guardKey(a))
}

// guardLabel is how a guard command names an agent back to the human: its
// name, with its host when it is elsewhere.
func (m Model) guardLabel(a fleet.Agent) string {
	if a.Host != "" {
		return a.Name + "@" + a.Host
	}
	return a.Name
}

// staleAgents picks the agents idle for at least d: idle by status, with a
// known last activity that long ago, not protected (those come back in
// skipped) and not flagged (those come back in held). among, when not
// empty, limits the pick to those session ids.
//
// Agents on remote hosts count only when remotes is true (the human's own
// /close idle, never the orchestrator's close_agent) and their host is live
// and allows actions; one whose host does not is listed as skipped, with why.
func (m Model) staleAgents(d time.Duration, among map[string]bool, remotes bool) (picked []fleet.Agent, skipped, held []closer.Refusal) {
	now := closeNow()
	for _, a := range m.agents {
		if a.Status != fleet.Idle || (a.Host != "" && !remotes) || (among != nil && !among[a.Session.SessionID]) {
			continue
		}
		// A duration needs a known last activity; unknown is never stale.
		if idle, known := idleFor(a, now); d > 0 && (!known || idle < d) {
			continue
		}
		if why := m.guardReason(a); why != "" {
			skipped = append(skipped, closer.Refusal{Target: m.closeTarget(a), Why: why})
			continue
		}
		if note, ok := m.guardHeld(a); ok {
			held = append(held, closer.Refusal{Target: m.closeTarget(a), Why: note})
			continue
		}
		if a.Host != "" {
			if err := m.remoteCan(a.Host); err != nil {
				skipped = append(skipped, closer.Refusal{Target: m.closeTarget(a), Why: err.Error()})
				continue
			}
		}
		picked = append(picked, a)
	}
	return picked, skipped, held
}

// closeDoneMsg is the end of a close run.
type closeDoneMsg struct {
	itemID  string
	plan    closer.Plan
	results []closer.Result
}

// closeTarget describes an agent to the closer.
func (m Model) closeTarget(a fleet.Agent) closer.Target {
	t := closer.Target{
		SessionID: a.Session.SessionID,
		Name:      a.Name,
		Peer:      a.Session.Name,
		PID:       a.Session.PID,
		Dir:       a.Session.CWD,
	}
	// Only the marker Den recorded when it spawned this session ties a
	// window to it; none for an agent Den did not start.
	t.Marker = winmark.For(a.Session.SessionID)
	if a.Host != "" {
		// SessionID stays the UI's key (session id and host); the host does
		// the signalling, so there is no pid, window or checkout path here.
		t.Host, t.RemoteDirty, t.PID, t.Dir = a.Host, a.Dirty, 0, ""
	}
	switch a.Status {
	case fleet.Working:
		t.Status = "working"
	case fleet.NeedsYou:
		t.Status = "needs you"
	case fleet.Idle:
		t.Status = "idle"
	case fleet.Gone:
		t.Status = "gone"
	default:
		t.Status = "unknown"
	}
	if idle, ok := idleFor(a, closeNow()); ok && a.Status == fleet.Idle {
		t.Idle = idle
	}
	for _, c := range m.childrenOf(a.Session.SessionID) {
		t.Children = append(t.Children, c.Name)
	}
	return t
}

// closeSelf is what must never close: Den, its window, the orchestrator.
// Den's window is the one window of a process above Den's, not whichever
// window is in front; the closer refuses the window in front separately.
func (m Model) closeSelf() closer.Self {
	orc := ""
	if m.orch.proc != nil {
		orc = m.orch.proc.SessionID()
	}
	window := ""
	if wo, ok := closeOps.(closer.WindowOps); ok {
		if ws, err := wo.Windows(); err == nil {
			window = closer.OwnWindow(ws, closer.AncestorPIDs(os.Getpid()))
		}
	}
	return closer.DenSelf(orc, window)
}

// proposeClose builds the plan for the candidates and puts it in the inbox.
// Agents that are the orchestrator are refused before the rules run. It
// returns the item, or nil with a note when nothing would close.
func (m *Model) proposeClose(cands []fleet.Agent, force bool, from, reason string, req *bridge.Request, extraRefused []closer.Refusal) (*inbox.Item, string) {
	return m.proposePlan(cands, force, from, reason, req, extraRefused, nil, nil, 0)
}

// proposePlan is proposeClose with what a bulk pick left out.
func (m *Model) proposePlan(cands []fleet.Agent, force bool, from, reason string, req *bridge.Request, extraRefused, skipped, held []closer.Refusal, idleFor time.Duration) (*inbox.Item, string) {
	m.ensureGuard()
	var targets []closer.Target
	var refused []closer.Refusal
	for _, a := range cands {
		if m.isOrchestrator(a) {
			refused = append(refused, closer.Refusal{Target: m.closeTarget(a), Why: "it is the orchestrator"})
			continue
		}
		// Protected agents are never proposed, even by name, on this machine
		// or on a host.
		if why := m.guardReason(a); why != "" {
			refused = append(refused, closer.Refusal{Target: m.closeTarget(a), Why: why + " · " + m.unprotectHint(a)})
			continue
		}
		if a.Host != "" {
			// Only the human closes a remote agent, and only while its host is
			// live and allows it; the question is asked for nothing else.
			why := ""
			if from == "orchestrator" {
				why = "the orchestrator cannot close agents on other hosts"
			} else if err := m.remoteCan(a.Host); err != nil {
				why = err.Error()
			}
			if why != "" {
				refused = append(refused, closer.Refusal{Target: m.closeTarget(a), Why: why})
				continue
			}
		}
		targets = append(targets, m.closeTarget(a))
	}
	plan := closer.Build(targets, force, m.closeSelf())
	plan.From, plan.Reason, plan.IdleFor = from, reason, idleFor
	plan.Skipped, plan.Held = skipped, held
	plan.Refused = append(append(refused, extraRefused...), plan.Refused...)
	if plan.Empty() {
		note := "Nothing to close."
		var whys []string
		for _, r := range plan.Refused {
			whys = append(whys, r.Target.Name+": "+r.Why)
		}
		for _, r := range plan.Skipped {
			whys = append(whys, "skipped "+r.Target.Name+": "+r.Why)
		}
		for _, r := range plan.Held {
			whys = append(whys, r.Target.Name+" holds context, name it to include it")
		}
		if len(whys) > 0 {
			note = "Nothing to close. " + strings.Join(whys, "; ")
		}
		if req != nil {
			req.Reply(bridge.Decision{Behavior: "deny", Message: note})
		}
		return nil, note
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	closer.Inspect(ctx, &plan)
	cancel()
	// Each window is tied to its agent now, so the question shows exactly
	// which windows may close; Run checks again before closing any.
	if wo, ok := closeOps.(closer.WindowOps); ok {
		if ws, err := wo.Windows(); err == nil {
			closer.Locate(&plan, ws, closer.AncestorPIDs)
		}
	}
	sid, host := splitKey(plan.Targets[0].SessionID)
	it := m.store.AddCloseOn(host, sid, plan.Title(), plan.Lines(), req)
	if m.closing == nil {
		m.closing = map[string]closer.Plan{}
	}
	m.closing[it.ID] = plan
	return it, ""
}

// closeSelected is ^X: close the selected agent (Fleet) or the agent of the
// selected inbox item.
func (m Model) closeSelected(force bool) (tea.Model, tea.Cmd) {
	var a fleet.Agent
	var ok bool
	switch m.tab {
	case tabInbox:
		if it := m.inboxSelected(); it != nil {
			a, ok = m.agentFor(itemKey(it))
		}
	default:
		a, ok = m.selected()
	}
	if !ok {
		return m.say("Select an agent first.", true)
	}
	return m.askClose([]fleet.Agent{a}, force, nil)
}

// closeCommand is /close: the selected agent, "idle" for every idle agent,
// or names; --force closes working ones too and kills what does not stop.
func (m Model) closeCommand(args []string) (tea.Model, tea.Cmd) {
	force := false
	var words []string
	for _, w := range args {
		if w == "--force" || w == "-f" {
			force = true
			continue
		}
		words = append(words, w)
	}
	if len(words) == 0 {
		return m.closeSelected(force)
	}
	if len(words) >= 1 && (strings.EqualFold(words[0], "idle") || strings.EqualFold(words[0], "all-idle")) {
		var d time.Duration
		if len(words) > 1 {
			var err error
			if d, err = closer.ParseIdle(strings.Join(words[1:], "")); err != nil {
				return m.say(err.Error(), true)
			}
		}
		return m.askStale(d, force, nil)
	}
	// Names: critter names, possibly several words each, or peer names; an
	// agent on a remote host is named with its host, "Mossy Lemur@devbox".
	var cands []fleet.Agent
	var refused []closer.Refusal
	for _, name := range splitNames(strings.Join(words, " ")) {
		a, why := m.closeNamed(name)
		if why != "" {
			refused = append(refused, closer.Refusal{Target: closer.Target{Name: name}, Why: why})
			continue
		}
		cands = append(cands, a)
	}
	return m.askClose(cands, force, refused)
}

// askStale proposes every agent idle for at least d (every idle agent when
// d is 0), protected and flagged ones listed but left out.
func (m Model) askStale(d time.Duration, force bool, among map[string]bool) (tea.Model, tea.Cmd) {
	m.ensureGuard()
	picked, skipped, held := m.staleAgents(d, among, true)
	if len(picked) == 0 && len(skipped) == 0 && len(held) == 0 {
		if d > 0 {
			return m.say("No agent has been idle for "+closer.ShortDuration(d)+".", true)
		}
		return m.say("No idle agents.", true)
	}
	it, note := m.proposePlan(picked, force, "you", "", nil, nil, skipped, held, d)
	if it == nil {
		return m.say(note, true)
	}
	return m.showClose(it)
}

// guardCommand is /protect, /unprotect, /flag and /unflag. Each names an
// agent the way /close does (names.Resolve) and guards its session: the
// agent stays guarded through a restore or /reopen, and another agent that
// later gets its name does not.
func (m Model) guardCommand(verb string, args []string) (tea.Model, tea.Cmd) {
	m.ensureGuard()
	if len(args) == 0 {
		if verb == "/protect" || verb == "/flag" {
			var names []string
			for _, e := range m.guard.Sessions {
				names = append(names, m.guardEntryLabel(e))
			}
			for _, p := range m.guard.Paths {
				names = append(names, "folder "+p)
			}
			for _, e := range m.guard.Flags {
				names = append(names, "flagged "+m.guardEntryLabel(e))
			}
			if len(names) == 0 {
				return m.say("Nothing is protected. /protect <Name> keeps an agent out of every close.", false)
			}
			return m.say("Protected: "+strings.Join(names, ", "), false)
		}
		return m.say("Which agent? "+verb+" <Name>", true)
	}
	name := strings.Join(args, " ")
	note := ""
	if verb == "/flag" {
		if i := strings.Index(name, ":"); i > 0 {
			name, note = strings.TrimSpace(name[:i]), strings.TrimSpace(name[i+1:])
		}
	}
	if verb == "/protect" || verb == "/flag" {
		return m.guardAll(verb, name, note)
	}
	a, why := m.closeNamed(name)
	if why != "" {
		return m.say(name+": "+why+".", true)
	}
	key, label := guardKey(a), m.guardLabel(a)
	var msg string
	switch verb {
	case "/unprotect":
		if !m.guard.Unprotect(key) {
			return m.say(label+" was not protected.", true)
		}
		msg = label + " is no longer protected."
	case "/unflag":
		if !m.guard.Unflag(key) {
			return m.say(label+" was not flagged.", true)
		}
		msg = label + " is no longer flagged."
	}
	if err := m.guard.Save(m.guardPath); err != nil {
		return m.say(msg+" (not saved: "+err.Error()+")", true)
	}
	return m.say(msg, false)
}

// guardAll is /protect and /flag. Guarding only keeps an agent out of
// closes, so they guard every agent the name names (guardTargets): a bare
// name covers this machine's agent of that name and every host's, and the
// message names each one. /unprotect, /unflag and /close, which let a
// close through, name one agent and take name@host for a host's.
func (m Model) guardAll(verb, name, note string) (tea.Model, tea.Cmd) {
	targets, why := m.guardTargets(name)
	if why != "" {
		return m.say(name+": "+why+".", true)
	}
	var labels, fresh []string
	for _, a := range targets {
		key, label := guardKey(a), m.guardLabel(a)
		labels = append(labels, label)
		if verb == "/flag" {
			m.guard.SetFlag(key, label, note)
			fresh = append(fresh, label)
		} else if m.guard.Protect(key, label) {
			fresh = append(fresh, label)
		}
	}
	isAre, it := " is", "it"
	if len(fresh) > 1 {
		isAre, it = " are", "them"
	}
	var msg string
	switch {
	case len(fresh) == 0 && len(labels) > 1:
		return m.say(strings.Join(labels, ", ")+" are protected already.", false)
	case len(fresh) == 0:
		return m.say(labels[0]+" is protected already.", false)
	case verb == "/flag":
		msg = strings.Join(fresh, ", ") + isAre + " flagged: a bulk close lists " + it + " without selecting " + it + "."
	default:
		msg = strings.Join(fresh, ", ") + isAre + " protected: no close will propose " + it + "."
	}
	if err := m.guard.Save(m.guardPath); err != nil {
		return m.say(msg+" (not saved: "+err.Error()+")", true)
	}
	return m.say(msg, false)
}

// guardTargets are the agents /protect or /flag name: the one the name
// names as /close would, and for a bare name every host's agent of that
// name as well (names.NeedsHost's candidates among the hosts' agents). A
// name two of this machine's agents share is refused as /close refuses it.
func (m Model) guardTargets(name string) ([]fleet.Agent, string) {
	agents := m.guardAgents()
	var out []fleet.Agent
	a, err := m.resolveAgent(name, agents, names.Opts{})
	var elsewhere *names.NeedsHost
	switch {
	case err == nil:
		out = append(out, a)
	case errors.Is(err, names.ErrNoMatch), errors.As(err, &elsewhere):
	default:
		return nil, err.Error()
	}
	if !strings.Contains(strings.TrimPrefix(strings.TrimSpace(name), "@"), "@") {
		var hosts []fleet.Agent
		for _, h := range agents {
			if h.Host != "" {
				hosts = append(hosts, h)
			}
		}
		_, err := names.Resolve(name, fleet.Candidates(hosts), names.Opts{})
		var there *names.NeedsHost
		if errors.As(err, &there) {
			for _, c := range there.Candidates {
				for _, h := range hosts {
					if h.Session.SessionID == c.ID && h.Host == c.Host {
						out = append(out, h)
					}
				}
			}
		}
	}
	if len(out) == 0 {
		return nil, "not in the fleet"
	}
	return out, ""
}

// guardEntryLabel names a guarded session in /protect's list: the agent's
// name now when Den sees it, else the name it had when guarded.
func (m Model) guardEntryLabel(e closer.Entry) string {
	for _, a := range m.guardAgents() {
		if guardKey(a) == e.Session {
			return m.guardLabel(a)
		}
	}
	if e.Name != "" {
		return e.Name
	}
	return e.Session
}

// guardAgents are the agents a guard entry may be: this machine's and the
// hosts'.
func (m Model) guardAgents() []fleet.Agent {
	return append(append([]fleet.Agent(nil), m.base...), m.remote.agents...)
}

// askClose proposes and shows the question in the inbox.
func (m Model) askClose(cands []fleet.Agent, force bool, refused []closer.Refusal) (tea.Model, tea.Cmd) {
	it, note := m.proposeClose(cands, force, "you", "", nil, refused)
	if it == nil {
		return m.say(note, true)
	}
	return m.showClose(it)
}

// showClose opens the Inbox on a close question.
func (m Model) showClose(it *inbox.Item) (tea.Model, tea.Cmd) {
	m.refresh()
	m.tab = tabInbox
	m.selectItem(it)
	return m.say(it.Title+" ^Y closes, ^N keeps them.", false)
}

// handleCloseRequest is the orchestrator's close_agent call: the plan goes
// to the inbox and the request waits for the human's decision. With
// idle_for, Den picks the agents itself, the orchestrator's peers (if any)
// narrowing the pick.
func (m *Model) handleCloseRequest(r *bridge.Request) {
	m.ensureGuard()
	var in bridge.CloseInput
	_ = json.Unmarshal(r.Payload, &in)
	var cands []fleet.Agent
	var refused []closer.Refusal
	var among map[string]bool
	if in.IdleFor != "" {
		among = map[string]bool{}
	}
	for _, peer := range in.Peers {
		// The orchestrator names agents by peer handle; a name it gives
		// instead is resolved the same way, and refused when it fits more
		// than one.
		a, err := m.resolveAgent(peer, m.base, names.Opts{})
		if err != nil {
			why := "not in the fleet"
			if !errors.Is(err, names.ErrNoMatch) {
				why = err.Error()
			}
			refused = append(refused, closer.Refusal{Target: closer.Target{Name: peer}, Why: why})
			continue
		}
		if among != nil {
			among[a.Session.SessionID] = true
		}
		cands = append(cands, a)
	}
	var skipped, held []closer.Refusal
	var d time.Duration
	if in.IdleFor != "" {
		var err error
		if d, err = closer.ParseIdle(in.IdleFor); err != nil {
			r.Reply(bridge.Decision{Behavior: "deny", Message: "Nothing closed: " + err.Error()})
			return
		}
		if len(among) == 0 {
			among = nil
		}
		cands, skipped, held = m.staleAgents(d, among, false)
	}
	it, note := m.proposePlan(cands, false, "orchestrator", in.Reason, r, refused, skipped, held, d)
	if note != "" {
		// Replied already; nothing waits.
		return
	}
	it.Intent = orchAsked
	m.flash, m.flashErr = "The orchestrator asks: "+it.Title+" ^Y closes, ^N keeps them.", false
	m.refresh()
}

// orchAsked marks a close question the orchestrator raised.
const orchAsked = "asked by the orchestrator"

// pendingOrchClose is the orchestrator's close question still waiting on
// the human, nil when there is none.
func (m Model) pendingOrchClose() *inbox.Item {
	for _, it := range m.store.Items() {
		if it.Kind == inbox.Close && it.Answerable() && it.Intent == orchAsked {
			return it
		}
	}
	return nil
}

// decideClose is ^Y or ^N on a close question.
func (m Model) decideClose(it *inbox.Item, verb string) (tea.Model, tea.Cmd) {
	plan, ok := m.closing[it.ID]
	if !ok {
		m.store.Settle(it, false, "the plan was lost; nothing closed")
		return m.say("That close request is stale; nothing was closed.", true)
	}
	if verb != "allow" {
		m.store.Settle(it, false, "declined in den; nothing closed")
		delete(m.closing, it.ID)
		m.refresh()
		return m.say("Kept them.", false)
	}
	m.store.Start(it, "closing…", 2*time.Minute)
	self := m.closeSelf()
	id := it.ID
	feed := m.remote.feed
	cmd := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		return closeDoneMsg{itemID: id, plan: plan, results: runClose(ctx, plan, self, feed)}
	}
	model, say := m.say("Closing "+strings.TrimSuffix(strings.TrimPrefix(it.Title, "Close "), "?")+"…", false)
	return model, tea.Batch(say, cmd)
}

// runClose carries the plan out: agents on this machine through the closer,
// agents on a host through that host's live session, which signals them
// there and refuses what the closer would refuse. Results keep the plan's
// order.
func runClose(ctx context.Context, plan closer.Plan, self closer.Self, feed remoteFeed) []closer.Result {
	local := plan
	local.Targets = nil
	for _, t := range plan.Targets {
		if t.Host == "" {
			local.Targets = append(local.Targets, t)
		}
	}
	done := closer.Run(ctx, local, self, closeOps)
	var out []closer.Result
	for _, t := range plan.Targets {
		if t.Host == "" {
			out = append(out, done[0])
			done = done[1:]
			continue
		}
		r := closer.Result{Target: t}
		sid, host := splitKey(t.SessionID)
		if feed == nil {
			r.Err = fmt.Errorf("%s is offline; nothing was closed", host)
		} else if killed, err := feed.CloseAgent(host, sid, plan.Force); err != nil {
			r.Err = err
		} else {
			r.Ended, r.Killed = true, killed
		}
		out = append(out, r)
	}
	return out
}

// finishClose records what happened and clears Den's own traces of the
// closed agents: their open inbox items, their place in the tree, and the
// roster on the next scan.
func (m *Model) finishClose(msg closeDoneMsg) tea.Cmd {
	summary := closer.Summary(msg.results)
	var closed, left []string
	for _, r := range msg.results {
		if r.Ended {
			closed = append(closed, r.Target.Name)
		} else {
			left = append(left, r.Target.Name)
		}
	}
	failed := len(left) > 0
	for _, it := range m.store.Items() {
		if it.ID == msg.itemID && it.State != inbox.Resolved {
			m.store.Settle(it, true, summary)
			if failed && it.Outcome == inbox.OutcomeDone {
				it.Outcome = inbox.OutcomeFailed // an agent it was to close is still there
			}
			m.cardEnd(it, "close", closed, left, false, summary)
		}
	}
	delete(m.closing, msg.itemID)
	for _, r := range msg.results {
		if !r.Ended {
			continue
		}
		if r.Target.Host != "" {
			// Gone on its host; the next snapshot from there agrees. The
			// roster and tree are this machine's, so they are left alone.
			sid, host := splitKey(r.Target.SessionID)
			m.store.SessionClosedOn(host, sid)
			for i := range m.remote.agents {
				if m.remote.agents[i].Session.SessionID == r.Target.SessionID {
					m.remote.agents[i].Status = fleet.Gone
					m.remote.agents[i].Session.Alive = false
				}
			}
			continue
		}
		m.store.SessionClosed(r.Target.SessionID)
		m.markClosed(r.Target.SessionID)
		parent := m.parentOf(r.Target.SessionID)
		if parent == "" {
			parent = tree.Orchestrator
		}
		m.learnEdge(tree.Edge{Parent: parent, Child: r.Target.SessionID, Drop: true})
		for i := range m.base {
			if m.base[i].Session.SessionID == r.Target.SessionID {
				m.base[i].Status = fleet.Gone
				m.base[i].Session.Alive = false
			}
		}
	}
	m.flash, m.flashErr = strings.ToUpper(summary[:1])+summary[1:], failed
	m.refresh()
	return tea.Batch(snapshot, flashTick(4*time.Second, m.flash))
}

// closeNamed finds the agent a /close name means, through names.Resolve:
// a peer handle, a name or an alias on this machine, or on a host when
// written "name@host". A name that fits more than one agent is refused,
// listing them. why is "" when an agent was found.
func (m Model) closeNamed(name string) (a fleet.Agent, why string) {
	a, err := m.resolveAgent(name, m.guardAgents(), names.Opts{})
	if err != nil {
		if errors.Is(err, names.ErrNoMatch) {
			return fleet.Agent{}, "not in the fleet"
		}
		return fleet.Agent{}, err.Error()
	}
	return a, ""
}

// resolveAgent is the agent among agents that query names
// (names.Resolve), or why none is.
func (m Model) resolveAgent(query string, agents []fleet.Agent, o names.Opts) (fleet.Agent, error) {
	c, err := names.Resolve(query, fleet.Candidates(agents), o)
	if err != nil {
		return fleet.Agent{}, err
	}
	for _, a := range agents {
		if a.Session.SessionID == c.ID && a.Host == c.Host {
			return a, nil
		}
	}
	return fleet.Agent{}, names.ErrNoMatch
}

// agentByPeer finds this machine's agent with exactly that peer handle;
// two with it is none.
func (m Model) agentByPeer(peer string) (fleet.Agent, bool) {
	p := strings.TrimPrefix(peer, "@")
	var hit fleet.Agent
	n := 0
	for _, a := range m.base {
		if p != "" && a.Session.Name == p {
			hit, n = a, n+1
		}
	}
	return hit, n == 1
}

// splitNames splits "Mossy Lemur, Snug Capybara" or "mossy snug"; a
// comma separates names, otherwise pairs of words are tried as one name.
func splitNames(s string) []string {
	if strings.Contains(s, ",") {
		var out []string
		for _, p := range strings.Split(s, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	words := strings.Fields(s)
	var out []string
	for i := 0; i < len(words); i++ {
		if i+1 < len(words) {
			out = append(out, words[i]+" "+words[i+1])
			i++
			continue
		}
		out = append(out, words[i])
	}
	return out
}

// migrateGuard turns an older protected.json's name entries into sessions,
// once Den has seen its agents: every agent with the name now, live or in
// the roster, is guarded by its session (a name used to cover them all).
// An entry for a host that has not reported yet waits for it; a name no
// agent has is dropped. Den's chat says what changed, once.
func (m *Model) migrateGuard() {
	if m.guard == nil {
		return
	}
	protect, flags := m.guard.Legacy()
	if len(protect) == 0 && len(flags) == 0 {
		return
	}
	type who struct{ key, label string }
	matching := func(name string) (hits []who, wait bool) {
		host, qualified := "", false
		if i := strings.LastIndexByte(name, '@'); i > 0 {
			name, host, qualified = strings.TrimSpace(name[:i]), strings.ToLower(strings.TrimSpace(name[i+1:])), true
			if host == "local" {
				host = ""
			}
		}
		if qualified && host != "" {
			seen := false
			for _, a := range m.remote.agents {
				if names.Fold(a.Host) != names.Fold(host) {
					continue
				}
				seen = true
				if names.Fold(a.Name) == names.Fold(name) || names.Fold(a.Session.Name) == names.Fold(name) {
					hits = append(hits, who{guardKey(a), m.guardLabel(a)})
				}
			}
			return hits, !seen
		}
		keys := map[string]bool{}
		for _, a := range m.guardAgents() {
			if (qualified && a.Host != "") || !(names.Fold(a.Name) == names.Fold(name) || names.Fold(a.Session.Name) == names.Fold(name)) {
				continue
			}
			keys[guardKey(a)] = true
			hits = append(hits, who{guardKey(a), m.guardLabel(a)})
		}
		if m.restore.r != nil {
			for _, r := range m.restore.r.Agents {
				if !keys[r.SessionID] && (names.Fold(r.Critter) == names.Fold(name) || names.Fold(r.Peer) == names.Fold(name)) {
					keys[r.SessionID] = true
					hits = append(hits, who{r.SessionID, r.Name()})
				}
			}
		}
		return hits, false
	}
	var moved, dropped []string
	var keepProtect []string
	var keepFlags []closer.Flag
	for _, name := range protect {
		hits, wait := matching(name)
		if wait {
			keepProtect = append(keepProtect, name)
			continue
		}
		if len(hits) == 0 {
			dropped = append(dropped, name)
		}
		for _, h := range hits {
			m.guard.Protect(h.key, h.label)
			moved = append(moved, h.label)
		}
	}
	for _, f := range flags {
		hits, wait := matching(f.Name)
		if wait {
			keepFlags = append(keepFlags, f)
			continue
		}
		if len(hits) == 0 {
			dropped = append(dropped, "flagged "+f.Name)
		}
		for _, h := range hits {
			m.guard.SetFlag(h.key, h.label, f.Note)
			moved = append(moved, "flagged "+h.label)
		}
	}
	if len(moved) == 0 && len(dropped) == 0 {
		return
	}
	m.guard.TakeLegacy(keepProtect, keepFlags)
	text := "Den now protects and flags each agent by its session, not by name: a protect or flag covers that agent through a restore or /reopen, and no other agent that later gets its name."
	if len(moved) > 0 {
		text += " Moved: " + strings.Join(moved, ", ") + "."
	}
	if len(dropped) > 0 {
		text += " No agent has these names now, so they are no longer kept: " + strings.Join(dropped, ", ") + "."
	}
	if err := m.guard.Save(m.guardPath); err != nil {
		text += " (Not saved: " + sanitize.Line(err.Error()) + ".)"
	}
	m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: text, at: time.Now()}))
}
