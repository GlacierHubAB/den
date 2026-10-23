package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/mission"
	"github.com/GlacierHubAB/den/internal/names"
	"github.com/GlacierHubAB/den/internal/sanitize"
	"github.com/GlacierHubAB/den/internal/spawn"
)

// propose_mission. The orchestrator proposes a mission through its tool
// (`den mcp`), behind the same gate as update_mission: a process the
// orchestrator started, during one of its turns. Den resolves the project
// itself and refuses one on another host, checks the criteria and the
// owners, and keeps the proposal for the human: nothing starts until ^Y.
// In a paired app's turn the tool call itself waits on the human first
// (control mode asks for every Den tool).

// handlePropose answers the tool's call at once.
func (m *Model) handlePropose(r *bridge.Request) tea.Cmd {
	var in bridge.ProposeInput
	if json.Unmarshal(r.Envelope.Payload, &in) != nil {
		r.Reply(bridge.Decision{Behavior: "deny", Message: "Den could not read the proposal; nothing was proposed."})
		return nil
	}
	if m.orch.proc == nil || m.turnOrigin() == "" {
		r.Reply(bridge.Decision{Behavior: "deny", Message: "Den takes proposals only from the orchestrator, during a turn; nothing was proposed."})
		return nil
	}
	ms, err := m.proposal(in)
	if err != nil {
		r.Reply(bridge.Decision{Behavior: "deny", Message: err.Error() + "; nothing was proposed."})
		return nil
	}
	m.missions.Save(ms)
	m.ov.proposal = ms.ID
	r.Reply(bridge.Decision{Behavior: "allow", Message: "Den shows the human mission " + ms.ID + " (" + ms.Name + ") as a proposal: nothing starts, and nothing may be dispatched for it, until they select it in Den's chat (↑, in the strip above the chat) and press ^Y; tell them so in those words. Change it with propose_mission and this id."})
	m.flash, m.flashErr = "The orchestrator proposes mission "+ms.Name+": select it with ↑ above the chat, then ^Y starts it, ^N drops it.", false
	return flashTick(4*time.Second, m.flash)
}

// proposal builds the proposed mission from the call, a new one or the
// refined proposal it names, or says what to fix. It changes nothing.
func (m Model) proposal(in bridge.ProposeInput) (*mission.Mission, error) {
	now := time.Now()
	ms := &mission.Mission{ID: mission.NewID(now), State: mission.Proposed, Created: now}
	if id := sanitize.Line(in.ID); id != "" {
		old := m.missions.Get(id)
		if old == nil || old.State != mission.Proposed {
			return nil, fmt.Errorf("no proposal has id %q", sanitize.Label(id, 20))
		}
		ms = cloneMission(old)
	}
	root, err := m.proposalProject(in.Project)
	if err != nil {
		return nil, err
	}
	if ms.Project != "" && ms.Project != root {
		return nil, fmt.Errorf("a proposal stays on its project (%s); propose a new mission for another", ms.ProjectName)
	}
	ms.SetProject(root)
	ms.Mission = sanitize.Label(in.Goal, 600)
	if ms.Mission == "" {
		return nil, fmt.Errorf("give the goal in one sentence")
	}
	ms.Name = sanitize.Label(in.Name, 80)
	if ms.Name == "" {
		ms.Name = sanitize.Label(strings.Join(firstWords(ms.Mission, 6), " "), 80)
	}
	shape := mission.Shape(strings.ToLower(strings.ReplaceAll(strings.TrimSpace(in.Shape), "-", "")))
	switch shape {
	case mission.Once, mission.Loop, mission.FanOut, mission.Schedule:
	default:
		return nil, fmt.Errorf("shape is once, loop, fanout or schedule")
	}
	ms.Shape = shape
	ms.Cadence = sanitize.Label(in.Cadence, 80)
	if shape == mission.Schedule {
		if _, err := mission.Next(ms.Cadence, now); err != nil {
			return nil, err
		}
	}
	ms.Reason = sanitize.Label(in.Reason, 200)
	ms.Constraints = nil
	for _, c := range in.Constraints {
		if c = sanitize.Label(c, 200); c != "" && len(ms.Constraints) < 24 {
			ms.Constraints = append(ms.Constraints, c)
		}
	}
	ms.Team = nil
	for _, t := range in.Team {
		if role := sanitize.Label(t.Role, 80); role != "" && len(ms.Team) < 24 {
			ms.Team = append(ms.Team, mission.Member{Role: role, Model: sanitize.Label(t.Model, 20), Under: sanitize.Label(t.Under, 80)})
		}
	}
	if err := ms.SetCriteria(in.Criteria); err != nil {
		return nil, err
	}
	if len(in.Goals) > mission.MaxGoals {
		return nil, fmt.Errorf("at most %d sub-goals", mission.MaxGoals)
	}
	ms.Goals, ms.Agents = nil, nil
	for i, g := range in.Goals {
		if strings.TrimSpace(g.Owner) == "" {
			return nil, fmt.Errorf("sub-goal %d needs its owner: a running agent's name or a team role", i+1)
		}
		sid, name, role, err := m.resolveOwner(g.Owner, ms.Team)
		if err != nil {
			return nil, err
		}
		if _, err := ms.AddGoal(mission.SubGoal{Text: g.Text, Owner: sid, Name: name, Role: role, State: mission.GoalTodo}); err != nil {
			return nil, fmt.Errorf("sub-goal %d: %v", i+1, err)
		}
		if name != "" && !containsS(ms.Agents, name) {
			ms.Agents = append(ms.Agents, name)
		}
	}
	ms.Updated = now
	return ms, nil
}

// proposalProject resolves the project a proposal names: a project Den
// lists (its name, root or folder name), or the repository of a directory
// on this machine. Den computes the name; another host's is refused.
func (m Model) proposalProject(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("name the project")
	}
	if p, ok := m.projectNamed(name); ok {
		if projectHost(p.Root) != "" {
			return "", fmt.Errorf("%s is on another host: missions stay on this machine", sanitize.Label(p.Name, 80))
		}
		return p.Root, nil
	}
	if strings.Contains(name, "@") || strings.Contains(name, ":") {
		return "", fmt.Errorf("missions stay on this machine: name a project Den lists here")
	}
	dir, err := spawn.ExpandDir(name)
	if err != nil {
		return "", fmt.Errorf("no project at %s: the human can /project add it first", sanitize.Label(name, 120))
	}
	root := gitRootOf(dir)
	if root == "" {
		return "", fmt.Errorf("%s is not in a git repository: the human can /project add it first", sanitize.Label(name, 120))
	}
	return root, nil
}

// resolveOwner finds who a sub-goal's owner names: a running agent on this
// machine (never the orchestrator), through names.Resolve, so a peer
// handle, a name or an alias, and a name two agents share is refused; or,
// when no agent has it, a role in the team. An owner grants nothing; it
// says who does what, and the mission keeps the session id.
func (m Model) resolveOwner(owner string, team []mission.Member) (sid, name, role string, err error) {
	o := strings.TrimSpace(owner)
	var live []fleet.Agent
	for _, a := range append(append([]fleet.Agent(nil), m.base...), m.remote.agents...) {
		if a.Status != fleet.Gone && !m.isOrchestrator(a) {
			live = append(live, a)
		}
	}
	a, rerr := m.resolveAgent(o, live, names.Opts{})
	switch {
	case rerr == nil && a.Host != "":
		return "", "", "", fmt.Errorf("%s is on %s: missions stay on this machine", sanitize.Label(a.Name, 80), sanitize.Label(a.Host, 80))
	case rerr == nil:
		return a.Session.SessionID, a.Name, "", nil
	case !errors.Is(rerr, names.ErrNoMatch):
		var elsewhere *names.NeedsHost
		if errors.As(rerr, &elsewhere) {
			return "", "", "", fmt.Errorf("owner %q is on another host: missions stay on this machine", sanitize.Label(o, 80))
		}
		return "", "", "", fmt.Errorf("owner %s", sanitize.Line(rerr.Error()))
	}
	for _, t := range team {
		if strings.EqualFold(t.Role, o) {
			return "", "", t.Role, nil
		}
	}
	return "", "", "", fmt.Errorf("owner %q is not a running agent's name or a role in team", sanitize.Label(o, 80))
}

// cloneMission is a deep copy, so a call is checked whole on the copy
// before anything changes.
func cloneMission(ms *mission.Mission) *mission.Mission {
	raw, _ := json.Marshal(ms)
	var cp mission.Mission
	json.Unmarshal(raw, &cp)
	return &cp
}

func firstWords(s string, n int) []string {
	w := strings.Fields(s)
	if len(w) > n {
		w = w[:n]
	}
	return w
}

func containsS(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
