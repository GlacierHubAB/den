package remote

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/GlacierHubAB/den/internal/badge"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// Den's half of remote spawn (docs/REMOTE.md). Spawn is the one way in for
// the human's own /spawn, the CLI through the inbox, and the orchestrator.
// It checks everything it can here, then asks the host, which checks it all
// again and more.

// SpawnRequest is an agent to start on a host.
type SpawnRequest struct {
	// SessionID is the agent's session id; "" picks a fresh one. A start
	// after a check passes the check's, so the human confirms the agent
	// that starts.
	SessionID string
	// Peer is the wanted peer name; Spawn makes it [a-z0-9-], at most 32
	// characters, and unique on the host. A check's peer is kept as is.
	Peer string
	// Dir is the folder on the host, absolute or ~/, under its spawn_dirs.
	Dir string
	// Route is tmux or terminal; "" lets the host's spawn key choose.
	Route string
	// Model is haiku, sonnet, opus or "" for the host's default.
	Model string
	// Worktree is the name of a git worktree of its own, "" for none.
	Worktree string
	// Brief is the agent's full first prompt (spawn.Prompt and any
	// worktree note), at most 64 KB. It is refused, not altered, when
	// sanitising would change it. A check may leave it empty.
	Brief string
	// Check resolves and checks on the host and starts nothing.
	Check bool
	// Orchestrator says the orchestrator or a lead asks: the host's
	// orchestrator key must allow it.
	Orchestrator bool
}

// SpawnResult is what a spawn, or its check, came to.
type SpawnResult struct {
	SessionID string
	Peer      string
	Critter   string
	Route     string
	TmuxName  string
	Dir       string // the folder as the host resolved it
	Untrusted bool   // Claude Code there was never told to trust the folder
	Warning   string
}

// pendingSpawn is a spawn Den asked for whose agent is not in the host's
// fleet yet.
type pendingSpawn struct {
	peer, tmux string
	state      string // starting, started, unknown
	since      time.Time
}

// Spawn starts an agent on host, or with req.Check only checks. The error
// says why nothing started, in words fit for the human; a spawn whose
// outcome is not known says so.
func (m *Manager) Spawn(ctx context.Context, host string, req SpawnRequest) (SpawnResult, error) {
	h, st, err := m.spawnHost(host, req)
	if err != nil {
		return SpawnResult{}, err
	}
	route, err := spawnRoute(h, st.SpawnCap, req.Route)
	if err != nil {
		return SpawnResult{}, err
	}
	fresh := req.SessionID == ""
	if fresh {
		req.SessionID = NewSessionID()
	}
	if err := CheckSessionID(req.SessionID); err != nil {
		return SpawnResult{}, err
	}
	if fresh || CheckPeer(req.Peer) != nil {
		req.Peer = RemotePeer(req.Peer, req.Dir, m.peersOn(host))
	}
	res := SpawnResult{SessionID: req.SessionID, Peer: req.Peer, Critter: badge.Name(req.SessionID), Route: route}
	cmd := SpawnCmd{Type: TypeSpawn, SessionID: req.SessionID, Peer: req.Peer, TmuxName: TmuxName(req.Peer, req.SessionID),
		Dir: req.Dir, Route: route, Model: req.Model, Worktree: req.Worktree, Brief: req.Brief, Check: req.Check}
	if req.Check {
		r, err := m.act(host, "spawn", m.o.CheckWait, func(id string) any { cmd.ID = id; return cmd })
		return fillSpawn(res, r), err
	}
	for try := 0; ; try++ {
		m.setPending(host, cmd, "starting")
		r, err := m.act(host, "spawn", m.o.SpawnWait, func(id string) any { cmd.ID = id; return cmd })
		res = fillSpawn(res, r)
		switch {
		case err == nil:
			m.setPending(host, cmd, "started")
			return res, nil
		case r.Code == "duplicate" && try == 0:
			// Taken on the host by something else: one new id, never the
			// existing session.
			m.dropPending(host, cmd.SessionID)
			cmd.SessionID = NewSessionID()
			cmd.TmuxName = TmuxName(cmd.Peer, cmd.SessionID)
			res.SessionID, res.Critter, res.TmuxName = cmd.SessionID, badge.Name(cmd.SessionID), cmd.TmuxName
			continue
		case r.Code == "no_session":
			// It may still come up (a login or trust screen in its window).
			m.setPending(host, cmd, "started")
			return res, err
		case strings.Contains(err.Error(), "not known"):
			m.setPending(host, cmd, "unknown")
			return res, err
		}
		m.dropPending(host, cmd.SessionID)
		return res, err
	}
}

func fillSpawn(res SpawnResult, r Result) SpawnResult {
	if r.TmuxName != "" && CheckTmuxName(r.TmuxName, res.SessionID) == nil {
		res.TmuxName = r.TmuxName
	}
	if r.Route != "" {
		res.Route = sanitize.Label(r.Route, 20)
	}
	res.Dir = sanitize.Label(r.Dir, 300)
	res.Untrusted, res.Warning = r.Untrusted, sanitize.Label(r.Warning, 200)
	return res
}

// spawnHost runs the checks Den makes before it asks the host.
func (m *Manager) spawnHost(host string, req SpawnRequest) (Host, HostStatus, error) {
	var h Host
	found := false
	for _, x := range m.hosts {
		if x.Label == host {
			h, found = x, true
		}
	}
	if !found {
		return h, HostStatus{}, fmt.Errorf("no remote host called %s", sanitize.Label(host, 40))
	}
	if req.Orchestrator && !h.Orchestrator {
		return h, HostStatus{}, errors.New(ProtectedNote(host))
	}
	if err := h.SpawnAllowed(); err != nil {
		return h, HostStatus{}, err
	}
	if _, err := m.linkFor(host, "spawn"); err != nil {
		return h, HostStatus{}, err
	}
	m.mu.Lock()
	st := m.state[host]
	status := st.status
	running := 0
	for _, s := range st.spawned {
		if s.Live {
			running++
		}
	}
	var names []string
	for sid, p := range st.pending {
		if !st.inFleet(sid) {
			running++
			names = append(names, p.tmux)
		}
	}
	m.mu.Unlock()
	cap := status.SpawnCap
	switch {
	case cap == nil:
		return h, status, fmt.Errorf("den on %s is too old to start agents; update it: den remote install %s", host, host)
	case cap.Veto != "":
		return h, status, fmt.Errorf("%s: %s", host, cap.Veto)
	case CheckSpawnDir(req.Dir) != nil:
		return h, status, CheckSpawnDir(req.Dir)
	case !UnderSpawnDirs(req.Dir, h.SpawnDirs):
		return h, status, fmt.Errorf("%s is not under spawn_dirs for %s (%s); nothing was started", sanitize.Label(req.Dir, 120), host, strings.Join(h.SpawnDirs, ", "))
	case !req.Check && running >= h.MaxSpawned():
		return h, status, fmt.Errorf("max_agents reached on %s: %d agents Den started run there", host, running)
	}
	if err := CheckSpawnModel(req.Model, false); err != nil {
		return h, status, err
	}
	if err := CheckWorktreeName(req.Worktree); err != nil {
		return h, status, err
	}
	if !req.Check || req.Brief != "" {
		if err := CheckBrief(req.Brief); err != nil {
			return h, status, err
		}
	}
	return h, status, nil
}

// spawnRoute picks the route: what was asked, else what the host's spawn
// key and its tmux allow.
func spawnRoute(h Host, cap *SpawnCap, want string) (string, error) {
	if want == "" {
		want = string(SpawnTmux)
	}
	switch {
	case want == string(SpawnTerminal) || h.Spawn == SpawnTerminal:
		return "", fmt.Errorf("the terminal route is not supported yet; set spawn = \"tmux\" for %s", h.Label)
	case want != string(SpawnTmux):
		return "", fmt.Errorf("unknown route %q", sanitize.Label(want, 20))
	case cap != nil && cap.Tmux == "":
		why := cap.NoTmux
		if why == "" {
			why = "tmux is not usable"
		}
		return "", fmt.Errorf("no usable tmux on %s (%s); install tmux 3.0 or newer there", h.Label, sanitize.Label(why, 120))
	}
	return want, nil
}

// peersOn is every peer name in use on host, so a new one is unique there.
func (m *Manager) peersOn(host string) map[string]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	taken := map[string]bool{}
	st := m.state[host]
	for _, a := range st.known.Agents {
		if a.Status != fleet.Gone.Key() {
			taken[a.Peer] = true
		}
	}
	for _, p := range st.pending {
		taken[p.peer] = true
	}
	return taken
}

func (m *Manager) setPending(host string, c SpawnCmd, state string) {
	m.mu.Lock()
	st := m.state[host]
	if st.pending == nil {
		st.pending = map[string]*pendingSpawn{}
	}
	p := st.pending[c.SessionID]
	if p == nil {
		p = &pendingSpawn{peer: c.Peer, tmux: c.TmuxName, since: m.o.Now()}
		st.pending[c.SessionID] = p
	}
	p.state = state
	m.mu.Unlock()
	m.fleetChanged(host)
}

func (m *Manager) dropPending(host, sid string) {
	m.mu.Lock()
	delete(m.state[host].pending, sid)
	m.mu.Unlock()
	m.fleetChanged(host)
}

// fleetChanged tells the views the host's agents changed.
func (m *Manager) fleetChanged(host string) {
	for _, h := range m.hosts {
		if h.Label == host {
			m.mu.Lock()
			u := Update{Kind: FleetChanged, Host: host, Status: m.state[host].status, Agents: m.agentsLocked(h)}
			m.mu.Unlock()
			m.send(u)
		}
	}
}

// inFleet says whether the host's last snapshot has the session.
func (st *hostState) inFleet(sid string) bool {
	for _, a := range st.known.Agents {
		if a.Session == sid {
			return true
		}
	}
	return false
}

// pendingAgents are the rows of spawns not in the host's fleet yet.
func (st *hostState) pendingAgents(host string) []fleet.Agent {
	var out []fleet.Agent
	for sid, p := range st.pending {
		if st.inFleet(sid) {
			continue
		}
		note := "starting in tmux on " + host
		switch {
		case p.state == "unknown" || st.status.State != Live && st.status.State != AttachedElsewhere:
			note = "spawn unknown (" + host + " offline)"
		case p.state == "started":
			note = "started in tmux on " + host + ", not registered yet"
		}
		a := fleet.Agent{Name: badge.Name(sid), Host: host, Tmux: p.tmux, Spawn: note, Status: fleet.Unknown}
		a.Session.SessionID, a.Session.Name, a.Session.Alive = sid, p.peer, true
		a.Labels.Task = note
		a.HostState = string(st.status.State)
		out = append(out, a)
	}
	return out
}

// noteSpawned takes a snapshot's list of Den's tmux sessions: a pending
// spawn now in the fleet is done.
func (st *hostState) noteSpawned(list []Spawned) {
	st.spawned = st.spawned[:0]
	for _, s := range list {
		if CheckSessionID(s.SessionID) == nil && CheckTmuxName(s.TmuxName, s.SessionID) == nil && len(st.spawned) < 256 {
			st.spawned = append(st.spawned, s)
		}
	}
	for sid := range st.pending {
		if st.inFleet(sid) {
			delete(st.pending, sid)
		}
	}
}

// tmuxOf is the tmux session Den started the session in on the host.
func (st *hostState) tmuxOf(sid string) string {
	for _, s := range st.spawned {
		if s.SessionID == sid {
			return s.TmuxName
		}
	}
	return ""
}

// reconcile takes what a host's hello says of the spawns this Den asked
// for: a spawn whose outcome was unknown is either started (its row waits
// for the agent) or not, which is said once and never retried.
func (m *Manager) reconcile(h Host, states []SpawnState) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.state[h.Label]
	var notes []string
	for sid, p := range st.pending {
		if p.state != "unknown" && p.state != "starting" {
			continue
		}
		known := false
		for _, s := range states {
			if s.SessionID != sid {
				continue
			}
			known = true
			if s.Started {
				p.state = "started"
			} else {
				delete(st.pending, sid)
				notes = append(notes, fmt.Sprintf("%s on %s was not started (the link dropped first); spawn it again if you still want it", badge.Name(sid), h.Label))
			}
		}
		if !known {
			delete(st.pending, sid)
			notes = append(notes, fmt.Sprintf("%s on %s was not started (the host has no record of it); spawn it again if you still want it", badge.Name(sid), h.Label))
		}
	}
	return notes
}

// Leftovers are the tmux sessions Den started on host whose Claude Code is
// not running, from the host's last snapshot.
func (m *Manager) Leftovers(host string) []Spawned {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.state[host]
	if !ok {
		return nil
	}
	var out []Spawned
	for _, s := range st.spawned {
		if !s.Live && st.pending[s.SessionID] == nil {
			out = append(out, s)
		}
	}
	return out
}

// Cleanup ends a leftover tmux session on host. The caller has asked the
// human first.
func (m *Manager) Cleanup(host, sessionID, tmuxName string) error {
	if CheckSessionID(sessionID) != nil || CheckTmuxName(tmuxName, sessionID) != nil {
		return errors.New("not a tmux session Den started")
	}
	_, err := m.act(host, "cleanup", m.o.CloseWait, func(id string) any {
		return CleanupCmd{Type: TypeCleanup, ID: id, SessionID: sessionID, TmuxName: tmuxName}
	})
	return err
}

// AttachCommand is the local command that shows the agent in tmux session
// name on host: ssh -t with Den's fixed options running `den remote attach
// <name>` there. The remote string holds only den's path, checked and
// quoted, and the name, which the tmux name rule keeps free of anything a
// shell would read.
func (m *Manager) AttachCommand(host, name string) ([]string, error) {
	if err := CheckTmuxName(name, ""); err != nil {
		return nil, err
	}
	var h Host
	found := false
	for _, x := range m.hosts {
		if x.Label == host {
			h, found = x, true
		}
	}
	if !found {
		return nil, fmt.Errorf("no remote host called %s", sanitize.Label(host, 40))
	}
	m.mu.Lock()
	cap := m.state[host].status.SpawnCap
	m.mu.Unlock()
	if h.DenPath == "" && cap != nil && cap.Window != "" {
		h.DenPath = cap.Window // where serve runs from: no login shell needed
	}
	return m.ssh.TTYCommand(h.Target, denCommand(h, "remote attach "+name))
}

// TTYCommand is the argument list of ssh with a terminal (-t) running remote
// on target, with Den's fixed options, for a window Den opens.
func (s SSH) TTYCommand(target, remote string) ([]string, error) {
	if err := CheckTarget(target); err != nil {
		return nil, err
	}
	return append(append([]string{s.Bin}, s.options()...), "-t", "--", target, remote), nil
}
