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
	// AgentProcs are the processes of every live Claude session on this
	// machine Den sees, the orchestrator's and the lane's: a client under
	// one is an agent's.
	AgentProcs []AgentProc
	// Guidance is the approved orchestrator guidance, "" for none.
	Guidance string
	// Colors are the colours Den draws agents in, by session id.
	Colors map[string]string
	// Cards are the cards Den would show, as a console may read them
	// (card.go); Project sanitises them and computes their hashes.
	Cards []Card
	// Settled says how the answered cards Den still keeps were settled.
	Settled []Settled
	// Commands are Den's commands with their dock flags (cmd.go).
	Commands []CommandInfo
}

// View is what the server serves until the next one.
type View struct {
	Fleet      Fleet
	Items      []Item
	Bodies     map[string]Body // local items only
	Missions   []Mission
	Roster     []orch.RosterAgent
	AgentProcs map[int]AgentProc
	Guidance   string
	Cards      map[string]Card // by id
	Settled    map[string]Settled
	Commands   []CommandInfo
}

// AgentProc is one agent's process: its pid, and its start time in clock
// ticks since boot (field 22 of /proc/<pid>/stat), so that another process
// that got the pid later is not taken for it. Start 0 is a process of Den's
// own (the orchestrator, the lane), whose pid cannot be reused while Den
// holds it. Name says which agent, for the refusal note.
type AgentProc struct {
	PID   int
	Start uint64
	Name  string
}

// labelMax caps names, roles, tasks, worktrees, models and titles.
const labelMax = 80

// summaryMax caps a mission run's summary.
const summaryMax = 200

// hexColor is c as "#rrggbb" in lower case, "" unless it is one.
func hexColor(c string) string {
	if len(c) != 7 || c[0] != '#' {
		return ""
	}
	for _, r := range c[1:] {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return ""
		}
	}
	return strings.ToLower(c)
}

// Project builds the View. It reads only the fields it names.
func Project(s Snapshot) *View {
	v := &View{
		Fleet:      Fleet{Taken: s.Taken, Agents: []Agent{}, Windows: []Window{}, Missions: []MissionRef{}, Cost: Cost{Currency: "USD"}},
		Items:      []Item{},
		Bodies:     map[string]Body{},
		Missions:   []Mission{},
		Roster:     append([]orch.RosterAgent(nil), s.Roster...),
		AgentProcs: map[int]AgentProc{},
		Guidance:   s.Guidance,
		Cards:      map[string]Card{},
		Settled:    map[string]Settled{},
		Commands:   projectCommands(s.Commands),
	}
	for _, p := range s.AgentProcs {
		if p.PID > 1 {
			v.AgentProcs[p.PID] = p
		}
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
		if a.Host == "" {
			ag.Color = hexColor(s.Colors[a.Session.SessionID])
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
	for _, c := range s.Cards {
		c = projectCard(c)
		v.Cards[c.ID] = c
	}
	for _, st := range s.Settled {
		if settledResult[st.Verb] != "" && (st.By == "den" || st.By == "console") {
			v.Settled[st.ID] = st
		}
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

// ProjectCard is projectCard, for Den to check an answer against the card
// it would hand over now.
func ProjectCard(c Card) Card { return projectCard(c) }

// projectCard sanitises a card as Den hands it over: the id, kind, agent
// and title to one line each, every body line on its own, and the verbs
// to the closed set; then its hash over exactly that.
func projectCard(c Card) Card {
	out := Card{
		ID:          sanitize.Label(c.ID, labelMax),
		Kind:        sanitize.Label(c.Kind, labelMax),
		Agent:       sanitize.Label(c.Agent, labelMax),
		Tool:        sanitize.Label(c.Tool, labelMax),
		Title:       sanitize.Line(c.Title),
		Note:        sanitize.Line(c.Note),
		Truncated:   c.Truncated,
		CutAt:       c.CutAt,
		DockAnswers: c.DockAnswers,
		Verbs:       []string{},
	}
	lines := strings.Split(c.Body, "\n")
	for i, l := range lines {
		lines[i] = sanitize.Code(l) // as Den draws it: spacing kept, controls and bidi gone
	}
	out.Body = strings.Join(lines, "\n")
	for _, v := range c.Verbs {
		if v == "allow" || v == "deny" || v == "always" {
			out.Verbs = append(out.Verbs, v)
		}
	}
	out.Hash = CardHash(out.Kind, out.Agent, out.Tool, out.Title, out.Note, out.Body)
	return out
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
