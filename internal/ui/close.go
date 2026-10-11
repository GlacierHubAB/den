package ui

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/closer"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/hypr"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/tree"
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

// guardReason says why the guard keeps an agent out of a bulk close.
func (m Model) guardReason(a fleet.Agent) string {
	if m.guard == nil {
		return ""
	}
	dirs := append([]string{a.Session.CWD}, rootsOf(a)...)
	return m.guard.Protected(a.Name, a.Session.Name, dirs, a.Labels.Task)
}

// staleAgents picks the agents idle for at least d: idle by status, with a
// known last activity that long ago, not protected (those come back in
// skipped) and not flagged (those come back in held). among, when not
// empty, limits the pick to those session ids.
func (m Model) staleAgents(d time.Duration, among map[string]bool) (picked []fleet.Agent, skipped, held []closer.Refusal) {
	now := closeNow()
	for _, a := range m.agents {
		if a.Status != fleet.Idle || (among != nil && !among[a.Session.SessionID]) {
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
		if note, ok := m.guard.Held(a.Name, a.Session.Name); ok {
			held = append(held, closer.Refusal{Target: m.closeTarget(a), Why: note})
			continue
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
	if a.Window != nil {
		t.Window, t.WindowHow = a.Window.Client.Address, a.Window.How
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
func (m Model) closeSelf() closer.Self {
	orc := ""
	if m.orch.proc != nil {
		orc = m.orch.proc.SessionID
	}
	window := ""
	if hypr.Available() {
		window = hypr.ActiveAddress()
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
		// Protected agents are never proposed, even by name.
		if why := m.guardReason(a); why != "" {
			refused = append(refused, closer.Refusal{Target: m.closeTarget(a), Why: why + " · /unprotect to allow"})
			continue
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
	sid := plan.Targets[0].SessionID
	it := m.store.AddClose(sid, plan.Title(), plan.Lines(), req)
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
			a, ok = m.agentFor(it.SessionID)
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
	// Names: critter names, possibly several words each, or peer names.
	var cands []fleet.Agent
	var refused []closer.Refusal
	for _, name := range splitNames(strings.Join(words, " ")) {
		a, ok := m.agentNamed(name)
		if !ok {
			refused = append(refused, closer.Refusal{Target: closer.Target{Name: name}, Why: "not in the fleet"})
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
	picked, skipped, held := m.staleAgents(d, among)
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

// guardCommand is /protect, /unprotect, /flag and /unflag.
func (m Model) guardCommand(verb string, args []string) (tea.Model, tea.Cmd) {
	m.ensureGuard()
	if len(args) == 0 {
		if verb == "/protect" || verb == "/flag" {
			var names []string
			names = append(names, m.guard.Names...)
			for _, p := range m.guard.Paths {
				names = append(names, "folder "+p)
			}
			for _, f := range m.guard.Flagged {
				names = append(names, "flagged "+f.Name)
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
	if a, ok := m.agentNamed(name); ok {
		name = a.Name
	}
	var msg string
	switch verb {
	case "/protect":
		if !m.guard.Protect(name) {
			return m.say(name+" is protected already.", false)
		}
		msg = name + " is protected: no close will propose it."
	case "/unprotect":
		if !m.guard.Unprotect(name) {
			return m.say(name+" was not protected by name.", true)
		}
		msg = name + " is no longer protected."
	case "/flag":
		m.guard.SetFlag(name, note)
		msg = name + " is flagged: a bulk close lists it without selecting it."
	case "/unflag":
		if !m.guard.Unflag(name) {
			return m.say(name+" was not flagged.", true)
		}
		msg = name + " is no longer flagged."
	}
	if err := m.guard.Save(m.guardPath); err != nil {
		return m.say(msg+" (not saved: "+err.Error()+")", true)
	}
	return m.say(msg, false)
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
	for i, o := range m.store.Items() {
		if o == it {
			m.inboxCur = i
		}
	}
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
		a, ok := m.agentByPeer(peer)
		if !ok {
			refused = append(refused, closer.Refusal{Target: closer.Target{Name: peer}, Why: "not in the fleet"})
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
		cands, skipped, held = m.staleAgents(d, among)
	}
	if _, note := m.proposePlan(cands, false, "orchestrator", in.Reason, r, refused, skipped, held, d); note != "" {
		// Replied already; nothing waits.
		return
	}
	m.refresh()
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
	it.Resolved = "closing…"
	self := m.closeSelf()
	id := it.ID
	cmd := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		return closeDoneMsg{itemID: id, plan: plan, results: closer.Run(ctx, plan, self, closeOps)}
	}
	model, say := m.say("Closing "+strings.TrimSuffix(strings.TrimPrefix(it.Title, "Close "), "?")+"…", false)
	return model, tea.Batch(say, cmd)
}

// finishClose records what happened and clears Den's own traces of the
// closed agents: their open inbox items, their place in the tree, and the
// roster on the next scan.
func (m *Model) finishClose(msg closeDoneMsg) tea.Cmd {
	summary := closer.Summary(msg.results)
	for _, it := range m.store.Items() {
		if it.ID == msg.itemID {
			m.store.Settle(it, true, summary)
		}
	}
	delete(m.closing, msg.itemID)
	for _, r := range msg.results {
		if !r.Ended {
			continue
		}
		m.store.SessionClosed(r.Target.SessionID)
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
	m.flash, m.flashErr = strings.ToUpper(summary[:1])+summary[1:], false
	m.refresh()
	return tea.Batch(snapshot, tea.Tick(4*time.Second, func(time.Time) tea.Msg { return flashClearMsg{} }))
}

// agentByPeer finds an agent by its session name.
func (m Model) agentByPeer(peer string) (fleet.Agent, bool) {
	p := strings.TrimPrefix(peer, "@")
	for _, a := range m.base {
		if a.Session.Name == p {
			return a, true
		}
	}
	return fleet.Agent{}, false
}

// agentNamed finds an agent by critter name or peer name, case-insensitively.
func (m Model) agentNamed(name string) (fleet.Agent, bool) {
	for _, a := range m.base {
		if strings.EqualFold(a.Name, name) || strings.EqualFold(a.Session.Name, name) {
			return a, true
		}
	}
	return fleet.Agent{}, false
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
