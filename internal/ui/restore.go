package ui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/cost"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/roster"
	"github.com/GlacierHubAB/den/internal/sanitize"
	"github.com/GlacierHubAB/den/internal/skills"
	"github.com/GlacierHubAB/den/internal/spawn"
	"github.com/GlacierHubAB/den/internal/transcript"
	"github.com/GlacierHubAB/den/internal/tree"
)

// Bringing the fleet back. Den keeps a snapshot of every live agent (the
// roster). After a restart, the agents that were live and are gone become
// one question in the inbox; ^Y reopens them with claude --resume in new
// terminals the way spawn_agent starts agents, ^N leaves them closed.
// /reopen asks the same for one agent, closed by mistake or still running
// (--fork). Nothing is reopened without ^Y.
//
// Agents closed through Den, and agents that ended on their own while Den
// watched, are left out of the restart question: someone ended them on
// purpose. /reopen still reaches them.

// restoreRun opens one agent's terminal; tests replace it so no terminal
// opens and no real session is resumed.
var restoreRun = spawn.Run

// restoreNow is the clock for the roster; tests pin it.
var restoreNow = time.Now

// rosterSaveEvery is how often the sighting times alone are written.
const rosterSaveEvery = time.Minute

// restoreState is the roster and the restore questions in the inbox.
type restoreState struct {
	r       *roster.Roster
	path    string    // the snapshot on disk, "" to keep it in memory
	since   time.Time // when this Den started watching
	saved   time.Time // last write
	asked   bool      // the restart question has been considered
	plans   map[string]restorePlan
	pending []reopened // agents reopened, waiting to show up so their edges come back
}

// restorePlan is what a restore question would reopen.
type restorePlan struct {
	agents []roster.Agent
	fork   bool
	reopen bool // asked by /reopen, not at startup: ^N dismisses nothing
}

// reopened is an agent whose terminal opened; its parent edge is put back
// once its session is in the fleet.
type reopened struct {
	agent     roster.Agent
	sessionID string // its session id now, "" until it registered
	peer      string
	at        time.Time
}

// restoreDoneMsg is the end of a restore run.
type restoreDoneMsg struct {
	itemID  string
	plan    restorePlan
	results []restoreResult
}

type restoreResult struct {
	agent roster.Agent
	res   spawn.Result
	err   error
}

func newRestoreState() restoreState {
	path := roster.Path()
	return restoreState{r: roster.Load(path), path: path, since: restoreNow(), plans: map[string]restorePlan{}}
}

// ensureRestore gives a zero Model (tests build one) an in-memory roster.
func (m *Model) ensureRestore() {
	if m.restore.r == nil {
		m.restore.r = &roster.Roster{}
		m.restore.since = restoreNow()
	}
	if m.restore.plans == nil {
		m.restore.plans = map[string]restorePlan{}
	}
}

// afterScan runs once per fleet snapshot: the first one after start may
// raise the restart question; every one re-links reopened agents and
// updates the roster.
func (m *Model) afterScan() {
	m.ensureRestore()
	if !m.restore.asked {
		m.restore.asked = true
		if m.cfg.Restore.Ask {
			if gone := m.restorable(); len(gone) > 0 {
				m.askRestore(gone, restorePlan{agents: gone})
			}
		}
	}
	m.relinkReopened()
	m.observeRoster()
}

// alive reports whether a session is running now.
func (m Model) alive(id string) bool {
	for _, a := range m.base {
		if a.Session.SessionID == id && a.Session.Alive && a.Status != fleet.Gone {
			return true
		}
	}
	return false
}

// restorable is the roster's gone agents that can still be resumed: their
// transcript is on disk.
func (m Model) restorable() []roster.Agent {
	var out []roster.Agent
	for _, a := range m.restore.r.Gone(m.alive) {
		if transcript.Path(a.SessionID) != "" {
			out = append(out, a)
		}
	}
	return out
}

// rosterEntry describes a live agent for the roster.
func (m Model) rosterEntry(a fleet.Agent) roster.Agent {
	id := a.Session.SessionID
	e := m.edgeFor(id)
	return roster.Agent{
		SessionID: id,
		Peer:      a.Session.Name,
		Critter:   a.Name,
		Role:      a.Labels.Role,
		Model:     a.Model,
		CWD:       a.Session.CWD,
		Parent:    e.Parent,
		Brief:     e.Brief,
		Mission:   m.missionOf(id),
		Spawned:   e.Source == "spawn",
	}
}

// observeRoster records the live agents and writes the roster when it
// changed, or when the sighting times are a minute old.
func (m *Model) observeRoster() {
	m.ensureRestore()
	now := restoreNow()
	var live []roster.Agent
	var skillsLooked bool
	var prov *skills.Provenance
	for _, a := range m.agents {
		if a.Status == fleet.Gone || !a.Session.Alive || m.isOrchestrator(a) || a.Session.Name == m.cfg.Orchestrator.Name {
			continue
		}
		// Only terminal sessions can be reopened in a terminal.
		if a.Session.Kind != "" && a.Session.Kind != "interactive" {
			continue
		}
		e := m.rosterEntry(a)
		if old := m.forkedFrom(e.SessionID); old.SessionID != "" {
			if e.Parent == "" {
				e.Parent, e.Brief, e.Spawned = old.Parent, old.Brief, old.Spawned
			}
			if e.Mission == "" {
				e.Mission = old.Mission
			}
		}
		if _, known := m.restore.r.Get(e.SessionID); !known && e.Peer != "" {
			// A new agent: its private skills folder, if the human approved
			// one at spawn, is recorded beside its name in skills.json.
			if !skillsLooked {
				prov, _ = skills.LoadProvenance(skills.ProvenancePath())
				skillsLooked = true
			}
			e.SkillsDir = agentSkillsDir(prov, e.Peer, e.CWD)
		}
		live = append(live, e)
	}
	changed := m.restore.r.Observe(live, now, m.restore.since)
	if changed || now.Sub(m.restore.saved) >= rosterSaveEvery {
		_ = m.restore.r.Save(m.restore.path, now) // best effort: Den works without the file
		m.restore.saved = now
	}
}

// forkedFrom is the roster entry the session was forked from, if any.
func (m Model) forkedFrom(id string) roster.Agent {
	for old, fork := range m.restore.r.Forks {
		if fork == id {
			a, _ := m.restore.r.Get(old)
			return a
		}
	}
	return roster.Agent{}
}

// agentSkillsDir is the newest private skills folder installed for the
// agent, "" when none or when it is gone.
func agentSkillsDir(p *skills.Provenance, peer, cwd string) string {
	if p == nil {
		return ""
	}
	dir := ""
	var at time.Time
	for _, r := range p.Installed {
		if r.Scope != skills.Agent || r.Peer != peer || (r.Dir != "" && r.Dir != cwd) || r.InstalledAt.Before(at) {
			continue
		}
		dir, at = r.Root, r.InstalledAt
	}
	if dir == "" {
		return ""
	}
	if _, err := os.Stat(dir); err != nil {
		return ""
	}
	return dir
}

// missionOf is the mission the agent works for: the mission of its own
// dispatch card, else of the nearest ancestor's.
func (m Model) missionOf(id string) string {
	ids := []string{id}
	if m.tree.edges != nil {
		chain := m.tree.edges.Chain(id)
		for i := len(chain) - 1; i >= 0; i-- {
			ids = append(ids, chain[i])
		}
	}
	for _, sid := range ids {
		for _, d := range m.cards() {
			if d.SessionID == sid && d.MissionID != "" {
				return d.MissionID
			}
		}
	}
	return ""
}

// askRestore puts the question in the inbox.
func (m *Model) askRestore(agents []roster.Agent, plan restorePlan) *inbox.Item {
	m.ensureRestore()
	plan.agents = agents
	var title string
	switch {
	case plan.fork:
		title = "Fork " + agents[0].Name() + "?"
	case plan.reopen:
		title = "Reopen " + agents[0].Name() + "?"
	case len(agents) == 1:
		title = "Restore 1 agent?"
	default:
		title = fmt.Sprintf("Restore %d agents?", len(agents))
	}
	var body []string
	total, priced := 0.0, 0
	for _, a := range agents {
		line, usd, ok := m.restoreLines(a)
		body = append(body, line...)
		if ok {
			total += usd
			priced++
		}
	}
	if priced > 1 {
		body = append(body, cost.Dollars(total, false)+" in all to reload their conversations.")
	}
	it := m.store.AddRestore(agents[0].SessionID, title, body)
	switch {
	case plan.fork:
		it.Intent = "Opens a new terminal with claude --resume --fork-session: a copy of its conversation under a new session id, the original left as it is."
	case plan.reopen:
		it.Intent = "Opens a new terminal on this workspace with claude --resume, its conversation as it was."
	default:
		it.Intent = "They were running when Den last stopped. Each opens in a new terminal on this workspace with claude --resume, its conversation as it was. Agents closed on purpose are not listed; /reopen <name> brings one back."
	}
	m.restore.plans[it.ID] = plan
	return it
}

// restoreLines describes one agent: who and where, then its place, when it
// was last seen and what reopening it is estimated to cost.
func (m Model) restoreLines(a roster.Agent) (lines []string, usd float64, priced bool) {
	head := []string{a.Name()}
	if a.Role != "" {
		head = append(head, a.Role)
	}
	if a.Model != "" {
		head = append(head, transcript.PrettyModel(a.Model))
	}
	head = append(head, sanitize.Line(shortPath(a.CWD, "")))
	var more []string
	switch {
	case a.Parent == tree.Orchestrator:
		more = append(more, "under the orchestrator")
	case a.Parent != "":
		if p, ok := m.restore.r.Get(a.Parent); ok {
			more = append(more, "under "+p.Name())
		}
	}
	if ms := m.missions.Get(a.Mission); ms != nil {
		more = append(more, "mission "+ms.Name)
	}
	if !a.LastSeen.IsZero() {
		more = append(more, "last seen "+humanAge(restoreNow().Sub(a.LastSeen))+" ago")
	}
	if u, ok := transcript.ReadUsage(transcript.Path(a.SessionID)); ok {
		model := a.Model
		if model == "" {
			model = u.Model
		}
		reload := cost.Tok(int64(u.Tokens)) + " tokens to reload"
		if usd, priced = roster.ResumeEstimate(fleet.Prices, model, u.Tokens); priced {
			reload += ", " + cost.Dollars(usd, false)
		}
		more = append(more, reload)
	}
	lines = []string{strings.Join(head, " · ")}
	if len(more) > 0 {
		lines = append(lines, "  "+strings.Join(more, " · "))
	}
	return lines, usd, priced
}

// decideRestore is ^Y or ^N on a restore question.
func (m Model) decideRestore(it *inbox.Item, verb string) (tea.Model, tea.Cmd) {
	m.ensureRestore()
	plan, ok := m.restore.plans[it.ID]
	if !ok {
		m.store.Settle(it, false, "the question was lost; nothing reopened")
		return m.say("That restore question is stale; nothing was reopened.", true)
	}
	if verb != "allow" {
		delete(m.restore.plans, it.ID)
		if !plan.reopen {
			// Asked once; a later restart does not ask about them again.
			now := restoreNow()
			for _, a := range plan.agents {
				m.restore.r.Dismiss(a.SessionID, now)
			}
			_ = m.restore.r.Save(m.restore.path, now)
			m.restore.saved = now
		}
		m.store.Settle(it, false, "declined in den; nothing reopened")
		m.refresh()
		return m.say("Left them closed.", false)
	}
	if !plan.fork {
		// One reopened by hand meanwhile is running already.
		var still []roster.Agent
		for _, a := range plan.agents {
			if !m.alive(a.SessionID) {
				still = append(still, a)
			}
		}
		plan.agents = still
	}
	if len(plan.agents) == 0 {
		delete(m.restore.plans, it.ID)
		m.store.Settle(it, false, "they are running already; nothing reopened")
		m.refresh()
		return m.say("They are running already.", false)
	}
	it.Resolved = "reopening…"
	reqs := m.restoreRequests(plan)
	id := it.ID
	cmd := func() tea.Msg {
		var results []restoreResult
		for i, req := range reqs {
			ctx, cancel := context.WithTimeout(context.Background(), spawn.SessionWait+spawn.WindowWait+5*time.Second)
			res, err := restoreRun(ctx, req)
			cancel()
			results = append(results, restoreResult{agent: plan.agents[i], res: res, err: err})
		}
		return restoreDoneMsg{itemID: id, plan: plan, results: results}
	}
	names := make([]string, len(plan.agents))
	for i, a := range plan.agents {
		names[i] = a.Name()
	}
	model, say := m.say("Reopening "+strings.Join(names, ", ")+"…", false)
	return model, tea.Batch(say, cmd)
}

// restoreRequests are the spawn requests that reopen the plan's agents:
// the same name, model, skills folder and, for agents Den started, Den's
// tools, so each comes back as it was.
func (m Model) restoreRequests(plan restorePlan) []spawn.Request {
	exe, _ := os.Executable()
	var out []spawn.Request
	for _, a := range plan.agents {
		req := spawn.Request{Dir: a.CWD, Agent: true, Resume: a.SessionID, Fork: plan.fork, Name: a.Peer, Orchestrator: m.cfg.Orchestrator.Name}
		if strings.HasPrefix(a.Model, "claude-") {
			req.Model = a.Model
		}
		if a.SkillsDir != "" {
			if _, err := os.Stat(a.SkillsDir); err == nil {
				req.AddDirs = []string{a.SkillsDir}
			}
		}
		if a.Spawned && m.cfg.Orchestrator.Spawn && exe != "" {
			req.Spawner = exe
		}
		out = append(out, req)
	}
	return out
}

// finishRestore records what happened and queues the reopened agents for
// their place in the tree.
func (m *Model) finishRestore(msg restoreDoneMsg) tea.Cmd {
	m.ensureRestore()
	delete(m.restore.plans, msg.itemID)
	var done, failed []string
	now := restoreNow()
	for _, r := range msg.results {
		if r.err != nil {
			failed = append(failed, r.agent.Name()+" ("+r.err.Error()+")")
			continue
		}
		name := r.agent.Name()
		if r.res.Untrusted {
			name += " (trust the folder in its terminal first)"
		}
		done = append(done, name)
		p := reopened{agent: r.agent, peer: r.res.Name, at: now}
		if r.res.Registered {
			p.sessionID = r.res.SessionID
		}
		if msg.plan.fork && p.sessionID != "" {
			m.restore.r.Fork(r.agent.SessionID, p.sessionID)
		}
		m.restore.pending = append(m.restore.pending, p)
	}
	var parts []string
	if len(done) > 0 {
		verb := "reopened "
		if msg.plan.fork {
			verb = "forked "
		}
		parts = append(parts, verb+strings.Join(done, ", "))
	}
	if len(failed) > 0 {
		parts = append(parts, "could not reopen "+strings.Join(failed, ", "))
	}
	summary := strings.Join(parts, "; ")
	if summary == "" {
		summary = "nothing to reopen"
	}
	for _, it := range m.store.Items() {
		if it.ID == msg.itemID {
			m.store.Settle(it, len(done) > 0, summary)
		}
	}
	_ = m.restore.r.Save(m.restore.path, now)
	m.restore.saved = now
	m.flash, m.flashErr = strings.ToUpper(summary[:1])+summary[1:], len(failed) > 0
	m.refresh()
	return tea.Batch(snapshot, tea.Tick(4*time.Second, func(time.Time) tea.Msg { return flashClearMsg{} }))
}

// relinkReopened puts reopened agents back under their parents once they
// are in the fleet: a resumed session keeps its id, so its old edge
// applies again; a fork takes the place of the agent it was forked from.
// A parent that is gone and not on its way back leaves the agent at the
// top.
func (m *Model) relinkReopened() {
	var keep []reopened
	for _, p := range m.restore.pending {
		sid := p.sessionID
		if sid == "" {
			for _, a := range m.base {
				if a.Session.Name == p.peer && a.Session.Alive && !a.Session.Started().Before(p.at.Add(-time.Minute)) {
					sid = a.Session.SessionID
				}
			}
			if sid != "" && sid != p.agent.SessionID {
				m.restore.r.Fork(p.agent.SessionID, sid)
			}
		}
		if sid == "" || !m.alive(sid) {
			if restoreNow().Sub(p.at) < time.Hour {
				keep = append(keep, p)
			}
			continue
		}
		parent := p.agent.Parent
		if fork, ok := m.restore.r.Forks[parent]; ok && m.alive(fork) {
			parent = fork
		}
		switch {
		case parent == "":
		case parent == tree.Orchestrator || m.alive(parent):
			m.learnEdge(tree.Edge{Parent: parent, Child: sid, Brief: p.agent.Brief, Source: "restore"})
		case m.reopening(parent) && restoreNow().Sub(p.at) < time.Hour:
			keep = append(keep, p) // its parent is coming back too; wait for it
		}
	}
	m.restore.pending = keep
}

// reopening reports whether the session is among the agents on their way
// back.
func (m Model) reopening(id string) bool {
	for _, p := range m.restore.pending {
		if p.agent.SessionID == id {
			return true
		}
	}
	return false
}

// reopenCommand is /reopen <name> [--fork]: one agent from the roster,
// asked about in the inbox.
func (m Model) reopenCommand(args []string) (tea.Model, tea.Cmd) {
	m.ensureRestore()
	fork := false
	var words []string
	for _, w := range args {
		if w == "--fork" || w == "--fork-session" {
			fork = true
			continue
		}
		words = append(words, w)
	}
	name := strings.Join(words, " ")
	if name == "" {
		return m.say("Which agent? /reopen <name>, or /reopen --fork <name> for a copy of its conversation.", true)
	}
	a, ok := m.restore.r.Find(name)
	if !ok {
		return m.say("Den has not seen an agent called "+name+" in the last two weeks.", true)
	}
	if m.alive(a.SessionID) && !fork {
		return m.say(a.Name()+" is running. /reopen --fork "+a.Name()+" opens a copy of its conversation.", true)
	}
	if transcript.Path(a.SessionID) == "" {
		return m.say(a.Name()+"'s conversation is no longer on disk; it cannot be reopened.", true)
	}
	it := m.askRestore([]roster.Agent{a}, restorePlan{fork: fork, reopen: true})
	return m.showRestore(it)
}

// showRestore opens the Inbox on a restore question.
func (m Model) showRestore(it *inbox.Item) (tea.Model, tea.Cmd) {
	m.refresh()
	m.tab = tabInbox
	m.selectItem(it)
	return m.say(it.Title+" ^Y reopens, ^N leaves it closed.", false)
}

// markClosed records in the roster that Den closed the session, so a
// restart does not offer it back.
func (m *Model) markClosed(id string) {
	m.ensureRestore()
	now := restoreNow()
	if m.restore.r.MarkClosed(id, now) {
		_ = m.restore.r.Save(m.restore.path, now)
		m.restore.saved = now
	}
}

// rosterName is the name the roster remembers for a session not in the
// fleet.
func (m Model) rosterName(id string) string {
	if m.restore.r == nil {
		return ""
	}
	a, _ := m.restore.r.Get(id)
	return a.Name()
}
