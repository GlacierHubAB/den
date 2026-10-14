package ui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/mission"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// A mission belongs to one project, set when it is made. Only the human
// moves it, with /mission move typed in Den's own input: nothing that
// reaches Den from outside (den.sock, den mcp, the orchestrator's replies)
// can. Den records who it put on a mission (a dispatch to the agent, or a
// spawn for the mission), and an agent shows under the project of the
// mission it serves.

// pendingMove is what ^Y on a move question does.
type pendingMove struct {
	mission string
	root    string
}

// listsProject reports whether name is a project askMove would find.
func (m Model) listsProject(name string) bool {
	_, ok := m.projectNamed(name)
	return ok
}

// projectNamed is the listed project called name: its name as listed, its
// root, or for a local one its folder's name.
func (m Model) projectNamed(name string) (project, bool) {
	for _, p := range m.proj.list {
		if p.Name == name || p.Root == name || (projectHost(p.Root) == "" && strings.EqualFold(mission.ProjectLabel(p.Root), name)) {
			return p, true
		}
	}
	return project{}, false
}

// askMove raises the question for /mission move <project>: the open or
// selected mission to the project named, one Den lists here.
func (m Model) askMove(name string) (tea.Model, tea.Cmd) {
	ms := m.ovMission()
	if ms == nil {
		return m.say("Select a mission on the Overview first, then /mission move <project>.", true)
	}
	to, ok := m.projectNamed(name)
	switch {
	case !ok:
		return m.say("No project called "+sanitize.Label(name, 80)+" here. Pick or /pin it first.", true)
	case projectHost(to.Root) != "":
		return m.say(sanitize.Label(to.Name, 80)+" is on another host: missions stay on this machine.", true)
	case to.Root == ms.Project:
		return m.say(ms.Name+" is already in "+mission.ProjectLabel(to.Root)+".", true)
	}
	if m.ov.moves == nil {
		m.ov.moves = map[string]pendingMove{}
	}
	// One question per mission: a new move replaces one not answered yet.
	for _, it := range m.store.Items() {
		if it.Kind == inbox.MissionMove && it.State == inbox.Waiting && it.SessionID == missionSessionPrefix+ms.ID {
			m.store.Settle(it, false, "replaced by a newer move")
			delete(m.ov.moves, it.ID)
		}
	}
	from, dest := projectLabelOr(ms.ProjectName), projectLabelOr(mission.ProjectLabel(to.Root))
	it := m.store.AddMissionMove(missionSessionPrefix+ms.ID, "Move mission "+sanitize.Label(ms.Name, 80)+"?", []string{
		"from " + from,
		"to   " + dest,
		"Its agents show under " + dest + " from now on; nothing is sent to them.",
	})
	m.ov.moves[it.ID] = pendingMove{mission: ms.ID, root: to.Root}
	m.refresh()
	return m.say("Move "+ms.Name+" from "+from+" to "+dest+"? ^Y in the Inbox moves it.", false)
}

func projectLabelOr(s string) string {
	if s = sanitize.Label(s, 80); s == "" {
		return "no project"
	}
	return s
}

// decideMove is ^Y or ^N on a move question.
func (m Model) decideMove(it *inbox.Item, verb string) (tea.Model, tea.Cmd) {
	mv, ok := m.ov.moves[it.ID]
	delete(m.ov.moves, it.ID)
	ms := m.missions.Get(mv.mission)
	switch {
	case !ok || ms == nil:
		m.store.Settle(it, false, "the mission is gone")
		m.refresh()
		return m.say("That mission is gone.", true)
	case verb != "allow":
		m.store.Settle(it, false, "kept where it is")
		m.refresh()
		return m.say("Kept "+ms.Name+" in "+projectLabelOr(ms.ProjectName)+".", false)
	}
	ms.MoveTo(mv.root, time.Now())
	m.missions.Save(ms)
	m.store.Settle(it, true, "moved to "+ms.ProjectName)
	m.refresh()
	return m.say("Moved "+ms.Name+" to "+ms.ProjectName+".", false)
}

// assign records that Den put the card's agent on a mission: its session,
// host, name and role, as Den knows them.
func (m *Model) assign(ms *mission.Mission, d *dispatch, now time.Time) {
	if d.SessionID == "" {
		return
	}
	sid, host := splitKey(d.SessionID)
	a := mission.Assignment{Session: sid, Host: host, Peer: d.Agent, Since: now}
	if ag, ok := m.agentFor(d.SessionID); ok {
		a.Peer = firstNonEmptyS(ag.Name, a.Peer)
		a.Role = ag.Labels.Role
	}
	ms.Assign(a)
}

// releaseAssignments closes the assignments whose card has ended, whose
// session is gone, or whose mission no longer runs. It waits for the fleet
// and the restored cards, so a restart closes nothing by mistake.
func (m *Model) releaseAssignments(now time.Time) {
	if m.missions == nil || len(m.base) == 0 || m.orch.reconcile {
		return
	}
	live := map[string]bool{} // mission id + "\x00" + session key
	for _, d := range m.cards() {
		if d.live() && d.MissionID != "" {
			live[d.MissionID+"\x00"+d.SessionID] = true
		}
	}
	for _, ms := range m.missions.All() {
		changed := false
		for _, as := range ms.Assigned {
			if !as.Open() {
				continue
			}
			key := remoteKey(as.Host, as.Session)
			a, known := m.agentFor(key)
			gone := (as.Host == "" && !known) || (known && a.Status == fleet.Gone)
			if gone || !ms.Live() || !live[ms.ID+"\x00"+key] {
				changed = ms.Release(as.Session, as.Host, now) || changed
			}
		}
		if changed {
			m.missions.Save(ms)
		}
	}
}
