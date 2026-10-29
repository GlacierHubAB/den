package ui

import (
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/badge"
	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/names"
	"github.com/GlacierHubAB/den/internal/procs"
	"github.com/GlacierHubAB/den/internal/roster"
	"github.com/GlacierHubAB/den/internal/sanitize"
	"github.com/GlacierHubAB/den/internal/tree"
)

// Den holds the names it gave (names.Live): read and checked once at
// start, changed only here, when it learns the session one of the spawns
// it saw started. The fleet reads this copy, never the file, so nothing
// written to it while Den runs changes a name.

// useNames makes l the names the fleet shows.
func (m *Model) useNames(l *names.Live) {
	m.names = l
	fleet.GivenNames = l.All
}

// nameSpawn names the session a spawn started, from the role and title it
// asked for, when Den learns the spawn's session (resolvePending): from the
// orchestrator's own stream, or a lead's spawn_agent result through its
// hook, the record the tree's spawned-by edge comes from. No request names
// a session: only a spawn Den saw, and only a session that started after
// it and has no name yet. It returns the name, "" for none.
func (m *Model) nameSpawn(sid string, p pendingSpawn) string {
	if m.names == nil || (strings.TrimSpace(p.role) == "" && strings.TrimSpace(p.title) == "") {
		return ""
	}
	a, ok := m.agentFor(sid)
	if !ok || a.Host != "" || m.isOrchestrator(a) || a.Session.Started().Before(p.at.Add(-time.Minute)) {
		return ""
	}
	if _, named := m.names.All()[sid]; named {
		return ""
	}
	// A lead's spawn comes through its hook, which anything in its tree can
	// send: only the lead's own den mcp saying so too makes it a spawn.
	if p.parent != tree.Orchestrator && !m.spawnReported(p.parent, p.peer, p.at) {
		return ""
	}
	e, err := m.names.Assign(sid, p.role, p.title, badge.Name(sid), m.nameSpace(sid), time.Now())
	if err != nil {
		return ""
	}
	// Shown at once, not only from the next scan.
	for i := range m.base {
		if m.base[i].Session.SessionID == sid && m.base[i].Host == "" {
			m.base[i].Name, m.base[i].Alias = fleet.Named(badge.Name(sid), e)
		}
	}
	m.refresh()
	return e.Name()
}

// spawnReport is a den mcp's word that it started an agent: the lead it
// serves, the peer handle it launched, and when.
type spawnReport struct {
	lead, peer string
	at         time.Time
}

// spawnReportFor is how close a den mcp's report and a lead's hook must
// come for one spawn.
const spawnReportFor = time.Minute

// spawnerOf is the lead whose den mcp pid is: Den's own binary, run as
// "den mcp", the child of that agent's Claude Code process (or of a shell
// it started), as Claude Code starts an MCP server. Tests replace it.
var spawnerOf = func(m *Model, pid int) (string, bool) {
	self, _ := os.Executable()
	if pid <= 1 || !runsMCP(pid) || !runsFile(pid, "/proc/self/exe", self) {
		return "", false
	}
	exe := procs.Exe(pid) // the same file, as runsFile found
	for _, a := range m.base {
		if a.Host == "" && a.Session.Alive && a.Session.PID > 1 && !m.isOrchestrator(a) &&
			procs.ChildOf(pid, a.Session.PID, procs.StartTicks(a.Session.PID), exe) {
			return a.Session.SessionID, true
		}
	}
	return "", false
}

// runsFile reports whether pid runs one of files: the very file (device
// and inode), however it is named now, never a path that only reads the
// same ("den (deleted)" is another file). A file that cannot be read
// matches nothing.
func runsFile(pid int, files ...string) bool {
	running, err := os.Stat("/proc/" + strconv.Itoa(pid) + "/exe")
	if err != nil {
		return false
	}
	for _, f := range files {
		if f == "" {
			continue
		}
		if fi, err := os.Stat(f); err == nil && os.SameFile(running, fi) {
			return true
		}
	}
	return false
}

// runsMCP reports whether pid's command line is "den mcp …".
func runsMCP(pid int) bool {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return false
	}
	args := strings.Split(string(raw), "\x00")
	return len(args) > 1 && args[1] == "mcp"
}

// handleSpawned records a lead's den mcp report of a spawn; any other
// sender is refused.
func (m *Model) handleSpawned(r *bridge.Request) {
	var in bridge.SpawnedInput
	lead, ok := spawnerOf(m, r.PeerPID)
	if json.Unmarshal(r.Envelope.Payload, &in) != nil || !ok || in.Peer == "" {
		r.Reply(bridge.Decision{Behavior: "deny", Message: "only a lead's own den mcp reports its spawns"})
		return
	}
	now := time.Now()
	kept := m.spawnReports[:0:0]
	for _, x := range m.spawnReports {
		if now.Sub(x.at) < spawnReportFor {
			kept = append(kept, x)
		}
	}
	m.spawnReports = append(kept, spawnReport{lead: lead, peer: in.Peer, at: now})
	r.Reply(bridge.Decision{Behavior: "allow"})
}

// spawnReported reports whether lead's den mcp said it launched peer
// within spawnReportFor of at.
func (m Model) spawnReported(lead, peer string, at time.Time) bool {
	for _, x := range m.spawnReports {
		if x.lead == lead && x.peer == peer && x.at.Sub(at).Abs() < spawnReportFor {
			return true
		}
	}
	return false
}

// nameSpace is what a new name for session must not be: the peer handles,
// critter names and given names of every other session Den sees and every
// one in the roster, which (with the live ones) also hold their names.
func (m Model) nameSpace(session string) names.Namespace {
	taken, held := map[string]bool{}, map[string]bool{}
	add := func(ns ...string) {
		for _, n := range ns {
			if n = strings.ToLower(strings.TrimSpace(n)); n != "" {
				taken[n] = true
				if nn := names.Normalise(n); nn != "" {
					taken[nn] = true
				}
			}
		}
	}
	for _, a := range m.base {
		held[a.Session.SessionID] = true
		if a.Session.SessionID != session {
			add(a.Session.Name, a.Name, a.Alias, badge.Name(a.Session.SessionID))
		}
	}
	if m.restore.r != nil {
		for _, a := range m.restore.r.Agents {
			held[a.SessionID] = true
			if a.SessionID != session {
				add(a.Peer, a.Critter, a.Given)
			}
		}
	}
	return names.Namespace{Taken: taken, Held: func(s string) bool { return held[s] }}
}

// Renaming is the human's own act, in Den's window only (sec's N5): no
// tool, request or dock command renames an agent. /rename <agent> <name>
// names one; /rename alone puts a card of names suggested from the agents'
// own labels, and only the ones the human picks are applied. Each goes
// through Normalise, Reserved and the namespace (names.Live.Rename), and a
// name an agent had stays taken while it lives (sec's PR4 point 3).

// renameShown is how many suggestions a rename card lists.
const renameShown = 9

// renamePick is an open rename card: the agents it suggests names for, by
// session, the text each name comes from, and the picks.
type renamePick struct {
	sids   []string
	labels []string
	picked map[int]bool
	more   int // agents past the card's last row
}

// renameCommand is /rename.
func (m Model) renameCommand(args []string) (tea.Model, tea.Cmd) {
	if m.names == nil {
		return m.say("Den holds no names here.", true)
	}
	if len(args) == 0 {
		return m.renameCard()
	}
	if len(args) < 2 {
		return m.say("/rename <agent> <name>, or /rename alone for names suggested from the agents' own labels.", true)
	}
	// The agent is the longest leading run of words that names one; the
	// rest is the new name.
	for n := len(args) - 1; n >= 1; n-- {
		a, why := m.closeNamed(strings.Join(args[:n], " "))
		if why != "" {
			continue
		}
		if a.Host != "" || m.isOrchestrator(a) {
			return m.say("Den names the agents of this machine only.", true)
		}
		e, err := m.renameOne(a, strings.Join(args[n:], " "))
		if err != nil {
			return m.say(err.Error()+".", true)
		}
		return m.say("Renamed "+critterOfAgent(a)+" → @"+e.Name()+".", false)
	}
	return m.say("No agent called "+args[0]+".", true)
}

// renameOne renames one agent of this machine to what the human typed or
// picked, and says so in Den's chat.
func (m *Model) renameOne(a fleet.Agent, text string) (names.Entry, error) {
	sid := a.Session.SessionID
	saved := backupBeforeRoles(names.RegistryPath(), roster.Path())
	critter := badge.Name(sid)
	e, err := m.names.Rename(sid, text, critter, m.nameSpace(sid), time.Now())
	if err != nil {
		return names.Entry{}, err
	}
	for i := range m.base {
		if m.base[i].Session.SessionID == sid && m.base[i].Host == "" {
			m.base[i].Name, m.base[i].Alias = fleet.Named(critter, e)
			m.base[i].Former = e.Former
		}
	}
	m.refresh()
	note := "Renamed " + critter + " → @" + e.Name() + " (by you)."
	if len(saved) > 0 && !m.renameBackedUp {
		m.renameBackedUp = true
		note += " The name files as they were before any rename: " + strings.Join(saved, ", ") + "."
	}
	m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: note, at: time.Now()}))
	return e, nil
}

// renameCard puts the card of suggested names: this machine's live agents
// that have no name from Den yet, each with the name Den would give from
// its own label, as Den would apply it.
func (m Model) renameCard() (tea.Model, tea.Cmd) {
	for id := range m.renames {
		for _, it := range m.store.Items() {
			if it.ID == id && it.State != inbox.Resolved {
				m.store.Settle(it, false, "replaced by a newer card")
			}
		}
		delete(m.renames, id)
	}
	p := &renamePick{picked: map[int]bool{}}
	more := 0
	for _, a := range m.base {
		if a.Host != "" || a.Alias != "" || !a.Session.Alive || a.Status == fleet.Gone || m.isOrchestrator(a) || m.isDesktopSession(a.Session.SessionID) {
			continue
		}
		if len(p.sids) == renameShown {
			more++
			continue
		}
		p.sids, p.labels = append(p.sids, a.Session.SessionID), append(p.labels, a.Labels.Role)
	}
	if len(p.sids) == 0 {
		return m.say("Every agent here has a name from Den, or none is running.", false)
	}
	p.more = more
	it := m.store.AddRenamePick("Name agents by their role?", m.renameBody(p))
	if m.renames == nil {
		m.renames = map[string]*renamePick{}
	}
	m.renames[it.ID] = p
	m.refresh()
	m.tab = tabInbox
	m.selectItem(it)
	return m.say(it.Title+" Pick with 1-9, ^Y renames the picked.", false)
}

// renameBody is a rename card's text: each agent, the name Den would give
// it, quoted, and the label it comes from beneath.
func (m *Model) renameBody(p *renamePick) []string {
	body := []string{"Press 1-" + strconv.Itoa(len(p.sids)) + " here to pick agents. ^Y renames the picked ones; each keeps its old name, which still works. ^N renames none. The names are suggested from the agents' own labels, which they can write: check each."}
	for i, sid := range p.sids {
		box := "[ ]"
		if p.picked[i] {
			box = "[x]"
		}
		name := "no usable name"
		if e, err := m.names.Preview(sid, p.labels[i], badge.Name(sid), m.nameSpace(sid)); err == nil {
			name = strconv.Quote("@" + e.Name())
		}
		body = append(body, box+" "+strconv.Itoa(i+1)+". "+badge.Name(sid)+" → "+name)
		label := sanitize.Label(p.labels[i], 80)
		if label == "" {
			label = "none"
		}
		body = append(body, "    the agent's own label: "+strconv.Quote(label))
	}
	if p.more > 0 {
		body = append(body, strconv.Itoa(p.more)+" more: /rename <agent> <name>")
	}
	return body
}

// toggleRename is a digit pressed while a rename card is selected and
// nothing is typed: it picks or unpicks that agent, if its label gives a
// usable name.
func (m *Model) toggleRename(it *inbox.Item, n int) bool {
	p, ok := m.renames[it.ID]
	if !ok || n < 1 || n > len(p.sids) || !it.Answerable() {
		return false
	}
	if _, err := m.names.Preview(p.sids[n-1], p.labels[n-1], "", m.nameSpace(p.sids[n-1])); err != nil {
		m.flash, m.flashErr = "That label gives no usable name: /rename <agent> <name> names it.", true
		return true
	}
	p.picked[n-1] = !p.picked[n-1]
	it.Body = m.renameBody(p)
	m.refresh()
	return true
}

// decideRename is ^Y or ^N on a rename card: ^Y renames the picked agents,
// each checked again as it is applied.
func (m Model) decideRename(it *inbox.Item, verb string) (tea.Model, tea.Cmd) {
	p, ok := m.renames[it.ID]
	if !ok {
		m.store.Settle(it, false, "stale")
		m.refresh()
		return m.say("That card is from before Den restarted: /rename again.", true)
	}
	var done []string
	if verb == "allow" {
		if !slices.Contains(mapValues(p.picked), true) {
			return m.say("Pick agents first: press their numbers while the card is selected.", true)
		}
		for i, sid := range p.sids {
			if !p.picked[i] {
				continue
			}
			a, ok := m.agentFor(sid)
			if !ok || a.Host != "" {
				continue
			}
			if e, err := m.renameOne(a, p.labels[i]); err == nil {
				done = append(done, "@"+e.Name())
			}
		}
	}
	delete(m.renames, it.ID)
	m.store.Settle(it, verb == "allow" && len(done) > 0, map[bool]string{true: "renamed " + strings.Join(done, ", "), false: "renamed none"}[len(done) > 0])
	m.refresh()
	return m, nil
}

// checkNames compares the names Den holds with the roster as Den last
// saved it, once at start: a session whose two records disagree (or one
// has a name the other lacks) keeps neither, shows its critter name, and
// Den's chat says so. A swap of two names then needs both files edited.
func (m *Model) checkNames() {
	if m.names == nil || m.restore.r == nil {
		return
	}
	held := m.names.All()
	var dropped []string
	for _, a := range m.restore.r.Agents {
		e, named := held[a.SessionID]
		switch {
		case !named && a.Given == "":
			continue
		case named && e.Name() == a.Given:
			continue
		}
		if named {
			m.names.Drop(a.SessionID)
		}
		critter := a.Critter
		if critter == "" {
			critter = badge.Name(a.SessionID)
		}
		dropped = append(dropped, critter)
	}
	if len(dropped) == 0 {
		return
	}
	for i := range m.base {
		if m.base[i].Host == "" && slices.Contains(dropped, badge.Name(m.base[i].Session.SessionID)) {
			m.base[i].Name, m.base[i].Alias, m.base[i].Former = badge.Name(m.base[i].Session.SessionID), "", nil
		}
	}
	m.refresh()
	for _, c := range dropped {
		m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: "Den dropped the name of " + c + ": its records disagree.", at: time.Now()}))
	}
}

// backupSuffix names the copy of a file as it was before any rename.
const backupSuffix = ".before-role-names"

// backupBeforeRoles copies each file that exists to file+backupSuffix:
// created new (O_EXCL, never through a link), 0600, and never overwritten,
// so the first copy is the one that undoes the renames. It returns the
// copies there.
func backupBeforeRoles(files ...string) []string {
	var out []string
	for _, f := range files {
		if f == "" {
			continue
		}
		dst := f + backupSuffix
		if _, err := os.Lstat(dst); err == nil {
			out = append(out, dst)
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		w, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
		if err != nil {
			continue
		}
		_, werr := w.Write(raw)
		if cerr := w.Close(); werr == nil && cerr == nil {
			out = append(out, dst)
		}
	}
	return out
}
