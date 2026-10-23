package ui

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/GlacierHubAB/den/internal/badge"
	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/names"
	"github.com/GlacierHubAB/den/internal/procs"
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
