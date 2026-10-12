package ui

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/config"
	"github.com/GlacierHubAB/den/internal/cost"
	"github.com/GlacierHubAB/den/internal/events"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/hypr"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/remote"
)

// Remote hosts (docs/REMOTE.md): agents on the
// hosts in remotes.toml join the fleet. Den shows them and their requests.
// On a host whose policy is approve or full, a permission, plan or
// question its hook holds is answered here like a local one (remoteHeld);
// elsewhere Den says where to answer. It never sends to, closes or focuses
// a remote agent.
//
// Inside the UI a remote agent is known by its key, "<session id>@<host>",
// which is what its Session.SessionID holds in m.agents. Everything the
// UI keys by session id (the selection, colours, inbox items, the tree,
// costs) is then keyed by host and session together, so the same session
// id on two hosts can never mix. The bare id is only used where the host
// itself needs it: the critter (so it matches the badge on that host) and
// events.

// Version is this Den's version, sent to remote hosts; main sets it.
var Version = "dev"

// remoteFeed is what the UI uses of remote.Manager; tests give a fake.
type remoteFeed interface {
	Start()
	Updates() <-chan remote.Update
	Hosts() []remote.HostStatus
	Agents() []fleet.Agent
	Reconnect(label string)
	Close()
	// Send and CloseAgent act on a remote agent through the host's live
	// session (internal/remote); Actionable says up front whether
	// they would be tried, and if not why.
	Send(host, sessionID, text string) error
	CloseAgent(host, sessionID string, force bool) (killed bool, err error)
	Actionable(host string) error
}

// remoteState is the remote hosts as the views show them.
type remoteState struct {
	feed    remoteFeed
	hosts   []remote.HostStatus     // in remotes.toml order
	agents  []fleet.Agent           // keyed (see above)
	last    map[string]fleet.Status // a stale agent's last known status, by key
	loadErr error                   // remotes.toml could not be read: no host is connected
	listing bool                    // /remote: the hosts show in place of the details pane
	pump    *remotePump             // drains the feed once Init starts it
}

type remoteMsg struct {
	u  remote.Update
	ok bool
}

// remoteKey is how the UI knows a remote agent: its session id and host.
func remoteKey(host, sessionID string) string {
	if host == "" {
		return sessionID
	}
	return sessionID + "@" + host
}

// splitKey undoes remoteKey: the session id and the host, "" for local.
// Session ids are UUIDs and labels letters, digits and '-', so the last
// '@' is the host's.
func splitKey(key string) (sessionID, host string) {
	if i := strings.LastIndexByte(key, '@'); i >= 0 {
		return key[:i], key[i+1:]
	}
	return key, ""
}

// openRemote starts nothing without remotes.toml or with no enabled host:
// Den is then exactly what it was without remote support.
func (m *Model) openRemote(cfg config.Config) {
	rc, err := remote.Load()
	if err != nil {
		m.remote.loadErr = err
		return
	}
	if len(rc.Enabled()) == 0 {
		return
	}
	m.remote.feed = remote.NewManager(rc, remote.Options{
		Version:    Version,
		Prices:     cost.NewTable(config.Prices()),
		NoEventLog: !cfg.Events.Log,
	})
	m.remote.pump = newPump()
	m.syncRemote()
}

// remotePump drains the manager's updates as they come, so a session is
// never held up by a busy frame, and hands them to the update loop one at
// a time, in order. The manager closes Updates on Close; the pump then
// says so once.
type remotePump struct {
	mu     sync.Mutex
	queue  []remote.Update
	closed bool
	sig    chan struct{}
}

func newPump() *remotePump { return &remotePump{sig: make(chan struct{}, 1)} }

// start begins draining f; Init calls it once.
func (p *remotePump) start(f remoteFeed) {
	go func() {
		for u := range f.Updates() {
			p.mu.Lock()
			p.queue = append(p.queue, u)
			p.mu.Unlock()
			p.wake()
		}
		p.mu.Lock()
		p.closed = true
		p.mu.Unlock()
		p.wake()
	}()
}

func (p *remotePump) wake() {
	select {
	case p.sig <- struct{}{}:
	default:
	}
}

// waitRemote delivers the next update from the hosts.
func waitRemote(p *remotePump) tea.Cmd {
	if p == nil {
		return nil
	}
	return func() tea.Msg {
		for {
			p.mu.Lock()
			if len(p.queue) > 0 {
				u := p.queue[0]
				p.queue = p.queue[1:]
				p.mu.Unlock()
				return remoteMsg{u: u, ok: true}
			}
			closed := p.closed
			p.mu.Unlock()
			if closed {
				return remoteMsg{}
			}
			<-p.sig
		}
	}
}

// remoteOn reports whether any remote host is configured: the UI shows
// host chips and host rows only then.
func (m Model) remoteOn() bool { return m.remote.feed != nil }

// syncRemote takes the hosts and their agents from the feed. A stale
// agent (its host not live) keeps its last known status aside and shows
// as unknown, so nothing counts it as working or needing you.
func (m *Model) syncRemote() {
	if m.remote.feed == nil {
		return
	}
	m.remote.hosts = m.remote.feed.Hosts()
	m.remote.last = map[string]fleet.Status{}
	m.remote.agents = m.remote.agents[:0:0]
	for _, a := range m.remote.feed.Agents() {
		if a.Host == "" {
			continue // never: the manager sets it from the connection
		}
		a.Session.SessionID = remoteKey(a.Host, a.Session.SessionID)
		a.Session.PID = 0
		if a.Stale {
			m.remote.last[a.Session.SessionID] = a.Status
			a.Status = fleet.Unknown
		}
		m.remote.agents = append(m.remote.agents, a)
	}
}

// handleRemote applies one update from the hosts.
func (m *Model) handleRemote(msg remoteMsg) tea.Cmd {
	if !msg.ok {
		return nil // the manager closed: Den is quitting
	}
	u := msg.u
	var cmd tea.Cmd
	switch u.Kind {
	case remote.HostChanged, remote.FleetChanged:
		m.syncRemote()
		if h, ok := m.hostStatus(u.Host); ok && m.flash == "Connecting to "+u.Host+"…" && h.State != remote.Connecting {
			// /remote connect said it was trying; say how it went.
			m.flash, m.flashErr = hostNow(u.Host, h.State), h.State != remote.Live
			cmd = tea.Tick(4*time.Second, func(time.Time) tea.Msg { return flashClearMsg{} })
		}
		if h, ok := m.hostStatus(u.Host); ok && !linked(h.State) {
			why := "released to the terminal on " + u.Host + " (link lost)"
			if h.State == remote.Connecting {
				why = "released to the terminal on " + u.Host + " (reconnecting)"
			}
			m.store.ReleaseHost(u.Host, why)
			m.att.ready()
			for k := range m.att.lastHook {
				if _, host := splitKey(k); host == u.Host {
					delete(m.att.lastHook, k)
				}
			}
		}
	case remote.EventArrived:
		cmd = m.remoteEvent(u.Event)
	case remote.HeldArrived:
		cmd = m.remoteHeld(u.Host, u.Request)
	case remote.HeldReleased:
		m.store.LetGo()
	}
	m.refresh()
	return tea.Batch(cmd, waitRemote(m.remote.pump))
}

// remoteEvent folds an event from a host into the inbox and the stuck
// timers, and notifies as a local hook would. It never reaches the hook
// bridge: there is no request behind it to answer.
func (m *Model) remoteEvent(ev events.Event) tea.Cmd {
	if ev.Host == "" || ev.Session == "" {
		return nil
	}
	if h, ok := m.hostStatus(ev.Host); !ok || !linked(h.State) {
		return nil // backfill from before the link dropped is history
	}
	key := remoteKey(ev.Host, ev.Session)
	m.att.ready()
	m.att.lastHook[key] = time.Now() // local receipt time: clocks are never compared across hosts
	if ev.Kind == events.SessionEnd {
		delete(m.att.lastHook, key)
	}
	it := m.store.ApplyRemote(ev)
	if it != nil {
		if a, ok := m.agentFor(key); ok {
			it.Role, it.Task = a.Labels.Role, a.Labels.Task
		}
		if it.Kind == inbox.Done {
			_ = inbox.AppendHistory(inbox.HistoryPath(), it) // best effort
		}
	}
	cmds := []tea.Cmd{m.attendItem(key, it)}
	if ev.Kind != events.NeedsYou || it != nil {
		cmds = append(cmds, m.runOn(ev))
	}
	return tea.Batch(cmds...)
}

// remoteCan says whether Den can act on host's agents now, in words for the
// human when it cannot.
func (m Model) remoteCan(host string) error {
	if m.remote.feed == nil {
		return fmt.Errorf("%s is offline; nothing was done", host)
	}
	if h, ok := m.hostStatus(host); ok && !linked(h.State) {
		return fmt.Errorf("%s is %s; nothing was done", host, strings.ReplaceAll(string(h.State), "_", " "))
	}
	return m.remote.feed.Actionable(host)
}

// remoteHeld puts a request a hook on host holds into the inbox,
// answerable: ^Y, ^N, /always and a question's answers go back down that
// host's link, bound to the request's id and body. It is only ever a
// PermissionRequest from that host: it never reaches handleRequest, so
// nothing from a host can reach the close, skills or install questions,
// the orchestrator's rules, or what Den learns from local hooks. A request
// Den cannot take (the host not live, or not approve or full here) goes
// back to its terminal at once.
func (m *Model) remoteHeld(host string, r *bridge.Request) tea.Cmd {
	if r == nil {
		return nil
	}
	h, ok := m.hostStatus(host)
	if r.Host == "" || r.Host != host || r.Event != "PermissionRequest" || r.Input.HookEventName != "PermissionRequest" ||
		!ok || h.State != remote.Live || !answersHere(h) {
		r.Reply(bridge.Decision{})
		return nil
	}
	key := remoteKey(host, r.SessionID)
	m.att.ready()
	m.att.lastHook[key] = time.Now()
	// The host's event for this wait may have come first and notified.
	noted := m.store.NeedsYouOn(host, r.SessionID)
	it := m.store.Apply(r)
	if it == nil {
		r.Reply(bridge.Decision{}) // a no-op when an always-allow answered it
		return nil
	}
	if a, ok := m.agentFor(key); ok {
		it.Role, it.Task = a.Labels.Role, a.Labels.Task
	}
	if noted {
		m.att.noted[key] = it.ID
		return nil
	}
	return m.attendItem(key, it)
}

// answersHere reports whether Den answers the host's held requests: its
// policy is approve or full here, and the host applies it.
func answersHere(h remote.HostStatus) bool {
	ok := func(p remote.Policy) bool { return p == remote.Approve || p == remote.Full }
	return ok(h.Policy) && ok(h.Applied)
}

// linked reports whether a host's link carries its fleet and events now.
// A host attached to another Den is linked, read-only.
func linked(s remote.State) bool { return s == remote.Live || s == remote.AttachedElsewhere }

// hostStatus is the status of the host with label.
func (m Model) hostStatus(label string) (remote.HostStatus, bool) {
	for _, h := range m.remote.hosts {
		if h.Label == label {
			return h, true
		}
	}
	return remote.HostStatus{}, false
}

// itemKey is the key of the agent an inbox item belongs to.
func itemKey(it *inbox.Item) string { return remoteKey(it.Host, it.SessionID) }

// hostOf is the host an agent or inbox item key belongs to, "" for local.
func hostOf(key string) string {
	_, host := splitKey(key)
	return host
}

// eventFor is a new event Den itself tells about an agent (stalled, still
// waiting): for a remote agent, its own session id and its host.
func eventFor(kind, key string) events.Event {
	sid, host := splitKey(key)
	ev := events.New(kind, sid)
	ev.Host = host
	return ev
}

// hostChip is " @devbox" after a remote agent's name, nothing for a local one.
func hostChip(a fleet.Agent) string {
	if a.Host == "" {
		return ""
	}
	return " @" + a.Host
}

// lastKnown is a stale agent's status as its host last reported it.
func (m Model) lastKnown(a fleet.Agent) (fleet.Status, bool) {
	st, ok := m.remote.last[a.Session.SessionID]
	return st, ok && a.Stale
}

// remoteHint is what Enter or /open says for a remote agent: Den cannot
// raise its window, so it names the command that reaches its terminal.
func (m Model) remoteHint(a fleet.Agent) string {
	target := a.Host
	if h, ok := m.hostStatus(a.Host); ok && h.Target != "" {
		target = h.Target
	}
	return fmt.Sprintf("%s is a remote agent on %s: run `ssh -t %s` to reach its terminal", a.Name, a.Host, target)
}

// hostLine is a host's state in a few words, for host rows and /remote:
// "devbox · offline since 14:02 · 3 agents (last known)".
func (m Model) hostLine(h remote.HostStatus) string {
	agents := agentsWord(h.Agents)
	since := ""
	if !h.Since.IsZero() {
		since = " since " + h.Since.Local().Format("15:04")
	}
	switch h.State {
	case remote.Live:
		return h.Label + " · live · " + agents
	case remote.Connecting:
		return h.Label + " · connecting… · " + agents + " (last known)"
	case remote.NeedsAttention:
		return h.Label + " · needs you" + since + " · " + agents + " (last known)"
	case remote.VersionMismatch:
		return h.Label + " · version mismatch · " + agents + " (last known)"
	case remote.AttachedElsewhere:
		return h.Label + " · live, another Den controls it · " + agents
	}
	return h.Label + " · " + strings.ReplaceAll(string(h.State), "_", " ") + since + " · " + agents + " (last known)"
}

// hostDetail is the reason and the hint under a host row, when there are any.
func hostDetail(h remote.HostStatus) []string {
	var out []string
	if h.Reason != "" && h.State != remote.Live {
		out = append(out, h.Reason)
	}
	if h.Hint != "" {
		out = append(out, h.Hint)
	}
	return out
}

// remoteCommand is /remote: list the hosts, or /remote connect <label>
// to retry one now.
func (m Model) remoteCommand(args []string) (tea.Model, tea.Cmd) {
	if m.remote.loadErr != nil {
		return m.say("remotes.toml: "+m.remote.loadErr.Error(), true)
	}
	if !m.remoteOn() {
		return m.say("No remote hosts. Add one with: den remote add <ssh target>", false)
	}
	if len(args) > 0 && (args[0] == "connect" || args[0] == "retry") {
		if len(args) < 2 {
			return m.say("Which host? /remote connect <label>", true)
		}
		label := strings.TrimPrefix(args[1], "@")
		if _, ok := m.hostStatus(label); !ok {
			return m.say("No host called "+label+". /remote lists them.", true)
		}
		m.remote.feed.Reconnect(label)
		return m.say("Connecting to "+label+"…", false)
	}
	if len(args) > 0 {
		return m.say("/remote lists the hosts; /remote connect <label> retries one now.", true)
	}
	m.tab = tabFleet
	m.focus = paneAgents
	m.tree.mapView = false
	m.remote.listing = true
	return m, nil
}

// remoteLines is the /remote listing: one block per host.
func (m Model) remoteLines(w int) []string {
	s := m.st
	out := []string{""}
	byHost := m.hostCosts()
	for _, h := range m.remote.hosts {
		st := s.fg
		switch h.State {
		case remote.Live:
			st = s.ok
		case remote.NeedsAttention, remote.VersionMismatch:
			st = s.attn
		case remote.Offline:
			st = s.muted
		}
		out = append(out, "  "+s.bright.Render(h.Label)+s.dim.Render("  ssh "+h.Target)+"  "+st.Render(strings.ReplaceAll(string(h.State), "_", " ")))
		policy := string(h.Policy)
		if h.Applied != "" && h.Applied != h.Policy {
			policy += " (the host applies " + string(h.Applied) + ")"
		}
		kv := func(k, v string) {
			if v != "" {
				out = append(out, "    "+s.dim.Render(fmt.Sprintf("%-10s", k))+s.fg.Render(v))
			}
		}
		kv("policy", policy)
		agents := fmt.Sprint(h.Agents)
		if !linked(h.State) {
			agents += " (last known)"
		}
		kv("agents", agents)
		if c, ok := byHost[h.Label]; ok && c.Total() > 0 {
			kv("cost", c.Line())
		}
		if !h.LastSeen.IsZero() {
			kv("last seen", humanAge(time.Since(h.LastSeen))+" ago")
		}
		if h.Den != "" || h.Claude != "" {
			kv("versions", strings.TrimSpace("den "+h.Den+"  claude "+h.Claude))
		}
		if off := h.Offset; off > 2*time.Second || off < -2*time.Second {
			kv("clock", offsetText(off))
		}
		if h.State != remote.Live {
			kv("reason", h.Reason)
		}
		kv("hint", h.Hint)
		for _, f := range h.Flags {
			out = append(out, "    "+s.attn.Render("dropped: ")+s.dim.Render(f))
		}
		out = append(out, "")
	}
	out = append(out, "  "+s.key.Render("/remote connect <label>")+s.keyLabel.Render(" retry now  ")+s.key.Render("Esc")+s.keyLabel.Render(" back"))
	return out
}

// offsetText says how far a host's clock is from this one's.
func offsetText(off time.Duration) string {
	dir := "ahead"
	if off < 0 {
		dir, off = "behind", -off
	}
	return "the host's clock is " + off.Round(time.Second).String() + " " + dir + "; ages are the host's own"
}

// hostCosts rolls the fleet's costs up per host; "" is this machine.
func (m Model) hostCosts() map[string]cost.Summary {
	out := map[string]cost.Summary{}
	for _, a := range m.agents {
		out[a.Host] = out[a.Host].Add(a.Cost)
	}
	return out
}

// hostCostNote is the per-host part of the fleet total, when remote
// agents have spent anything: "here $1.20 · devbox $0.40".
func (m Model) hostCostNote() string {
	if !m.remoteOn() {
		return ""
	}
	by := m.hostCosts()
	var parts []string
	labels := []string{""}
	for _, h := range m.remote.hosts {
		labels = append(labels, h.Label)
	}
	remoteSpent := false
	for _, l := range labels {
		c, ok := by[l]
		if !ok || c.Total() == 0 {
			continue
		}
		if l != "" {
			remoteSpent = true
		}
		name := l
		if l == "" {
			name = "here"
		}
		parts = append(parts, name+" "+cost.Dollars(c.USD, c.Partial))
	}
	if !remoteSpent {
		return ""
	}
	return strings.Join(parts, " · ")
}

// lookingAtRemote: the human sees a remote agent's notice only when Den's
// own window is the focused one; its terminal is on another machine.
func lookingAtDen() bool {
	return hypr.Available() && hypr.ActiveHolds(os.Getpid())
}

// remoteOrder sorts stale agents after everyone else, grouped by host in
// remotes.toml order, so each host's last known agents sit under its row.
func (m Model) remoteOrder(a fleet.Agent) int {
	if !a.Stale {
		return -1
	}
	return slices.IndexFunc(m.remote.hosts, func(h remote.HostStatus) bool { return h.Label == a.Host })
}

// hostRows is a host's row in the agents pane, above its last known
// agents: "devbox · offline since 14:02 · 3 agents (last known)", then
// the reason and what the human can do, dimmed.
func (m Model) hostRows(label string, w int) []string {
	s := m.st
	h, ok := m.hostStatus(label)
	if !ok {
		return []string{"  " + s.section.Render(label)}
	}
	st := s.muted
	switch h.State {
	case remote.NeedsAttention, remote.VersionMismatch:
		st = s.attn
	case remote.Connecting:
		st = s.dim
	}
	out := []string{ansi.Truncate("  "+st.Render(m.hostLine(h)), w, "…")}
	for _, d := range hostDetail(h) {
		for _, l := range strings.Split(ansi.Wrap(d, max(10, w-4), ""), "\n") {
			out = append(out, "    "+s.muted.Render(l))
		}
	}
	return out
}

// idleHostRows are the rows of hosts that are not live and have no
// agent in the list, so a host never disappears from view.
func (m Model) idleHostRows(w int) []string {
	var out []string
	for _, h := range m.remote.hosts {
		if h.State == remote.Live || slices.ContainsFunc(m.agents, func(a fleet.Agent) bool { return a.Host == h.Label }) {
			continue
		}
		out = append(out, m.hostRows(h.Label, w)...)
	}
	return out
}

// hostTarget is the ssh target of a host, its label when unknown.
func (m Model) hostTarget(label string) string {
	if h, ok := m.hostStatus(label); ok && h.Target != "" {
		return h.Target
	}
	return label
}

// remoteDetail is the host part of a remote agent's details: the host and
// its link, the policy, and the clock offset when it matters.
func (m Model) remoteDetail(a fleet.Agent) [][2]string {
	h, ok := m.hostStatus(a.Host)
	if !ok {
		return [][2]string{{"host", a.Host}}
	}
	host := h.Label
	if h.Hostname != "" && h.Hostname != h.Label {
		host += " (" + h.Hostname + ")"
	}
	link := strings.ReplaceAll(string(h.State), "_", " ")
	if !h.Since.IsZero() && h.State != remote.Live {
		link += " since " + h.Since.Local().Format("15:04")
	}
	if !h.LastSeen.IsZero() {
		link += " · last message " + humanAge(time.Since(h.LastSeen)) + " ago"
	}
	policy := string(h.Policy)
	if h.Applied != "" && h.Applied != h.Policy {
		policy += " requested · " + string(h.Applied) + " applied"
	}
	rows := [][2]string{{"host", host + " · ssh " + h.Target}, {"link", link}, {"policy", policy}}
	if off := h.Offset; off > 2*time.Second || off < -2*time.Second {
		rows = append(rows, [2]string{"clock", offsetText(off)})
	}
	if a.Session.Version != "" {
		rows = append(rows, [2]string{"claude", a.Session.Version})
	}
	return rows
}

// agentsWord is "1 agent" or "3 agents".
func agentsWord(n int) string {
	if n == 1 {
		return "1 agent"
	}
	return fmt.Sprintf("%d agents", n)
}

// hostNow says where a host's link stands, as a sentence: "devbox is
// live", "devbox needs your attention".
func hostNow(label string, s remote.State) string {
	switch s {
	case remote.Live:
		return label + " is live"
	case remote.Offline:
		return label + " is offline"
	case remote.Connecting:
		return label + " is connecting"
	case remote.NeedsAttention:
		return label + " needs your attention"
	case remote.VersionMismatch:
		return label + " has a den that does not match this one"
	case remote.AttachedElsewhere:
		return remote.ElsewhereNote(label)
	}
	return label + ": " + strings.ReplaceAll(string(s), "_", " ")
}
