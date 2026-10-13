package api

import (
	"strings"
	"time"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/mission"
	"github.com/GlacierHubAB/den/internal/orch"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// Snapshot is what the UI hands the API after each refresh, on its own
// goroutine. Project turns it into a View of fresh wire types: a whitelist
// of fields, every string sanitised, nothing shared with the Model.
type Snapshot struct {
	Taken time.Time
	// Agents are the fleet's agents without the orchestrator and the lane.
	Agents []fleet.Agent
	// Names maps a session id to the name Den shows for it.
	Names map[string]string
	// Items are the inbox's items.
	Items []*inbox.Item
	// Missions are every mission.
	Missions []*mission.Mission
	// FullHosts are the remote hosts whose applied policy is full.
	FullHosts map[string]bool
	// Roster is the lane's roster: orch roster entries with no recent
	// activity, no earlier tasks and waiting lines of kind and title only.
	Roster []orch.RosterAgent
	// AgentPIDs are the pids of every local Claude session Den sees,
	// the orchestrator's and the lane's: a client under one is an agent.
	AgentPIDs []int
	// Guidance is the approved orchestrator guidance, "" for none.
	Guidance string
}

// View is what the server serves until the next one.
type View struct {
	Fleet     Fleet
	Items     []Item
	Bodies    map[string]Body // local items only
	Missions  []Mission
	Roster    []orch.RosterAgent
	AgentPIDs map[int]bool
	Guidance  string
}

// labelMax caps names, roles, tasks, worktrees, models and titles.
const labelMax = 80

// summaryMax caps a mission run's summary.
const summaryMax = 200

// Project builds the View. It reads only the fields it names.
func Project(s Snapshot) *View {
	v := &View{
		Fleet:     Fleet{Taken: s.Taken, Agents: []Agent{}, Windows: []Window{}, Missions: []MissionRef{}, Cost: Cost{Currency: "USD"}},
		Items:     []Item{},
		Bodies:    map[string]Body{},
		Missions:  []Mission{},
		Roster:    append([]orch.RosterAgent(nil), s.Roster...),
		AgentPIDs: map[int]bool{},
		Guidance:  s.Guidance,
	}
	for _, p := range s.AgentPIDs {
		v.AgentPIDs[p] = true
	}
	waiting := map[string]string{}
	for _, it := range s.Items {
		if it.State == inbox.Resolved {
			continue
		}
		key := it.SessionID
		switch it.Kind {
		case inbox.Permission, inbox.Question, inbox.Plan:
			if _, ok := waiting[key]; !ok {
				waiting[key] = kindName(it.Kind)
			}
		case inbox.Stalled:
			if _, ok := waiting[key]; !ok {
				waiting[key] = StateStalled
			}
		}
	}
	for _, a := range s.Agents {
		if a.Status == fleet.Gone {
			continue
		}
		ag := Agent{
			ID:       sanitize.Label(a.Session.SessionID, labelMax),
			Name:     sanitize.Label(a.Name, labelMax),
			Role:     sanitize.Label(a.Labels.Role, labelMax),
			Model:    sanitize.Label(a.Model, labelMax),
			Worktree: sanitize.Label(a.Where(), labelMax),
			Since:    a.Session.Started().UTC(),
			LastSeen: a.LastActive.UTC(),
			CostUSD:  a.Cost.USD,
			Host:     sanitize.Label(a.Host, labelMax),
			Remote:   a.Host != "",
		}
		switch a.Status {
		case fleet.Working:
			ag.State = StateWorking
		case fleet.NeedsYou:
			ag.State = StateWaiting
		default:
			ag.State = StateIdle
		}
		switch w := waiting[a.Session.SessionID]; w {
		case StateStalled:
			if ag.State == StateWorking {
				ag.State = StateStalled
			}
		case "":
		default:
			ag.State, ag.Waiting = StateWaiting, w
		}
		if a.Window != nil && a.Host == "" {
			c := a.Window.Client
			ag.Window = sanitize.Label(c.Address, labelMax)
			v.Fleet.Windows = append(v.Fleet.Windows, Window{
				Address: ag.Window,
				Agent:   ag.Name,
				Class:   sanitize.Label(c.Class, labelMax),
				Title:   sanitize.Label(c.Title, labelMax),
				PID:     c.PID,
			})
		}
		v.Fleet.Agents = append(v.Fleet.Agents, ag)
	}
	for _, it := range s.Items {
		if it.State == inbox.Resolved {
			continue
		}
		kind := kindName(it.Kind)
		if kind == "" {
			continue
		}
		item := Item{
			ID:    sanitize.Label(it.ID, labelMax),
			Kind:  kind,
			Agent: sanitize.Label(s.Names[it.SessionID], labelMax),
			Since: it.At.UTC(),
			Title: sanitize.Label(it.Title, labelMax),
			Host:  sanitize.Label(it.Host, labelMax),
		}
		if it.Host != "" {
			// A host's request body is the host's own text: never served,
			// and its digest describes it without hashing it.
			summary := "a " + kind + " request"
			if it.Kind == inbox.Question {
				summary = "a question"
			}
			if s.FullHosts[it.Host] {
				summary += " from " + item.Host
			}
			item.Title = summary
			item.Digest = digest(summary)
		} else {
			text := strings.Join(it.Body, "\n")
			item.Digest = digest(text)
			v.Bodies[item.ID] = Body{ID: item.ID, Kind: kind, Text: text, Hash: item.Digest.Hash}
		}
		v.Items = append(v.Items, item)
	}
	for _, ms := range s.Missions {
		m := projectMission(ms)
		v.Missions = append(v.Missions, m)
		ref := MissionRef{ID: m.ID, Title: m.Title, State: m.State}
		if len(m.Agents) > 0 {
			ref.Agent = m.Agents[0]
		}
		v.Fleet.Missions = append(v.Fleet.Missions, ref)
	}
	return v
}

func kindName(k inbox.Kind) string {
	switch k {
	case inbox.Permission:
		return KindPermission
	case inbox.Question:
		return KindQuestion
	case inbox.Plan:
		return KindPlan
	}
	return ""
}

func digest(text string) Digest {
	first, _, _ := strings.Cut(text, "\n")
	return Digest{FirstLine: sanitize.Label(first, labelMax), Bytes: len(text), Hash: HashOf(text)}
}

// missionState maps Den's mission states onto the API's four.
func missionState(s mission.State) string {
	switch s {
	case mission.Proposed:
		return "proposed"
	case mission.Done, mission.Failed:
		return "done"
	case mission.Dropped:
		return "dropped"
	}
	return "running"
}

func projectMission(ms *mission.Mission) Mission {
	m := Mission{
		ID:        sanitize.Label(ms.ID, labelMax),
		Title:     sanitize.Label(ms.Name, labelMax),
		State:     missionState(ms.State),
		Lines:     []string{},
		Proposed:  ms.Created.UTC(),
		BudgetUSD: ms.Budget.USD,
	}
	for _, a := range ms.Agents {
		m.Agents = append(m.Agents, sanitize.Label(a, labelMax))
	}
	if ms.Mission != "" {
		m.Lines = append(m.Lines, sanitize.Label(ms.Mission, summaryMax))
	}
	if ms.DoneWhen != "" {
		m.Lines = append(m.Lines, sanitize.Label("done when: "+ms.DoneWhen, summaryMax))
	}
	for _, r := range ms.Runs {
		if r.EndedAt.IsZero() {
			continue
		}
		m.Done++
		if r.Summary != "" {
			m.Lines = append(m.Lines, sanitize.Label(r.Summary, summaryMax))
		}
		if r.Cost != nil {
			m.SpendUSD += r.Cost.USD
		}
	}
	return m
}
