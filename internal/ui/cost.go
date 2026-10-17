package ui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/cost"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/mission"
	"github.com/GlacierHubAB/den/internal/transcript"
)

// The cost ledger: tokens per agent, tree and mission run from the
// transcripts, dollars as an estimate. Figures that need a walk through
// the cards and the tree are worked out on the tick and kept here, so
// drawing a frame only reads them.

// budgetTool marks the inbox item that asks about a mission's budget; its
// session id is "mission:<id>".
const budgetTool = "mission_budget"

const missionSessionPrefix = "mission:"

// spent is what a run or a mission used.
type spent struct {
	Tokens  int64
	USD     float64
	Partial bool
}

func (s spent) add(o spent) spent {
	return spent{s.Tokens + o.Tokens, s.USD + o.USD, s.Partial || o.Partial}
}

func (s spent) line() string {
	return cost.Tok(s.Tokens) + " tok · " + cost.Dollars(s.USD, s.Partial)
}

// costState is the cost figures the tick keeps for the views.
type costState struct {
	runs map[string]spent // live runs, by mission id and run number
}

func runKey(ms *mission.Mission, r *mission.Run) string { return fmt.Sprintf("%s/%d", ms.ID, r.N) }

// sessionCost is one agent's own usage, zero when it is not in view.
func (m Model) sessionCost(id string) cost.Summary {
	if a, ok := m.agentFor(id); ok {
		return a.Cost
	}
	return cost.Summary{}
}

// treeCost is an agent's usage with everyone it directs.
func (m Model) treeCost(id string) cost.Summary {
	return cost.Tree(m.tree.edges, id, m.sessionCost)
}

// fleetCost is every session Den sees.
func (m Model) fleetCost() cost.Summary {
	var s cost.Summary
	for _, a := range m.agents {
		s = s.Add(a.Cost)
	}
	return s
}

// runWindow is the session ids and the time span a run's usage is counted
// over: the agents its dispatches went to and everyone they direct, from
// the first dispatch until the run ended (or now).
func (m Model) runWindow(ms *mission.Mission, r *mission.Run) (ids []string, from, to time.Time) {
	seen := map[string]bool{}
	for _, d := range m.missionCards(ms, r) {
		if d.SessionID == "" {
			continue
		}
		if from.IsZero() || d.SentAt.Before(from) {
			from = d.SentAt
		}
		for _, id := range append([]string{d.SessionID}, cost.Descendants(m.tree.edges, d.SessionID)...) {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	return ids, from, r.EndedAt
}

// countRun works out a run's usage from the transcripts of its agents.
func (m Model) countRun(ms *mission.Mission, r *mission.Run) spent {
	ids, from, to := m.runWindow(ms, r)
	if len(ids) == 0 {
		return spent{}
	}
	byModel := map[string]transcript.Tokens{}
	for _, id := range ids {
		a, ok := m.agentFor(id)
		if !ok || a.Path == "" {
			continue
		}
		for model, tk := range transcript.Shared.Window(a.Path, from, to) {
			byModel[model] = byModel[model].Add(tk)
		}
	}
	s := fleet.Prices.OfWindow(byModel)
	return spent{s.Tokens.Total(), s.USD, s.Partial}
}

// runSpent is a run's usage: the figure recorded when it ended, else the
// live one from the last tick.
func (m Model) runSpent(ms *mission.Mission, r *mission.Run) spent {
	if r.Cost != nil {
		return spent{r.Cost.Tokens, r.Cost.USD, r.Cost.Partial}
	}
	return m.costs.runs[runKey(ms, r)]
}

// missionSpent is all the mission's runs together.
func (m Model) missionSpent(ms *mission.Mission) spent {
	var s spent
	for i := range ms.Runs {
		s = s.add(m.runSpent(ms, &ms.Runs[i]))
	}
	return s
}

// settleCosts is the tick's cost work: it counts the live runs, records
// the usage of runs that ended, and asks about budgets that were crossed.
func (m *Model) settleCosts() {
	if m.missions == nil {
		return
	}
	m.costs.runs = map[string]spent{}
	for _, ms := range m.missions.All() {
		changed := false
		for i := range ms.Runs {
			r := &ms.Runs[i]
			if r.Cost != nil {
				continue
			}
			s := m.countRun(ms, r)
			if r.EndedAt.IsZero() {
				m.costs.runs[runKey(ms, r)] = s
				continue
			}
			if s.Tokens > 0 {
				r.Cost = &mission.RunCost{Tokens: s.Tokens, USD: s.USD, Partial: s.Partial}
				changed = true
			}
		}
		if changed {
			m.missions.Save(ms)
		}
		m.checkBudget(ms)
	}
}

// checkBudget pauses a mission that crossed its budget and asks the human
// once. Agents are left running: pausing only holds the next run.
func (m *Model) checkBudget(ms *mission.Mission) {
	b := ms.Budget
	if !b.Set() || b.Waived {
		return
	}
	if !b.Asked {
		if ms.State != mission.Running && ms.State != mission.Waiting {
			return
		}
		s := m.missionSpent(ms)
		if !b.Crossed(s.Tokens, s.USD) {
			return
		}
		ms.State, ms.Updated = mission.Paused, time.Now()
		ms.Budget.Asked = true
		m.missions.Save(ms)
	}
	// Asked and still waiting on the answer: make sure the question is in
	// the inbox, also after a restart.
	if ms.State == mission.Paused && m.budgetItem(ms.ID) == nil {
		s := m.missionSpent(ms)
		m.store.AddBudget(missionSessionPrefix+ms.ID, "Mission "+ms.Name+" reached its budget", []string{
			"used " + s.line() + " (an estimate)",
			"budget " + budgetLabel(b),
			"The mission is paused; its agents keep working until you decide.",
		})
		m.flash, m.flashErr = "Mission "+ms.Name+" reached its budget: ^Y keeps going, ^N stops the mission.", false
	}
}

// budgetItem is the open budget question for a mission, if any.
func (m Model) budgetItem(missionID string) *inbox.Item {
	for _, it := range m.store.Items() {
		if it.Kind == inbox.Budget && it.State == inbox.Waiting && it.SessionID == missionSessionPrefix+missionID {
			return it
		}
	}
	return nil
}

func budgetLabel(b mission.Budget) string {
	switch {
	case b.Tokens > 0 && b.USD > 0:
		return cost.Tok(b.Tokens) + " tok or " + cost.Dollars(b.USD, false)
	case b.Tokens > 0:
		return cost.Tok(b.Tokens) + " tok"
	case b.USD > 0:
		return cost.Dollars(b.USD, false)
	}
	return "none"
}

// decideBudget is ^Y or ^N on a budget question: keep going or stop.
func (m Model) decideBudget(it *inbox.Item, verb string) (tea.Model, tea.Cmd) {
	id := strings.TrimPrefix(it.SessionID, missionSessionPrefix)
	ms := m.missions.Get(id)
	if ms == nil {
		m.store.Settle(it, false, "the mission is gone")
		m.refresh()
		return m.say("That mission is gone.", true)
	}
	now := time.Now()
	if verb == "allow" {
		ms.Budget.Waived = true
		if ms.State == mission.Paused {
			ms.State = mission.Running
		}
		ms.Updated = now
		m.missions.Save(ms)
		m.store.Settle(it, true, "kept going past the budget")
		m.refresh()
		return m.say("Resumed "+ms.Name+" past its budget.", false)
	}
	m.stopMission(ms, "stopped: over budget")
	m.store.Settle(it, false, "stopped in Den; the agents were not touched")
	m.refresh()
	return m.say("Stopped "+ms.Name+". Its agents were left as they are.", false)
}

// stopMission ends the mission and its current run without touching any
// agent.
func (m *Model) stopMission(ms *mission.Mission, why string) {
	now := time.Now()
	if r := ms.CurrentRun(); r != nil {
		r.EndedAt, r.Summary = now, firstNonEmptyS(r.Summary, why)
	}
	ms.State, ms.Updated = mission.Done, now
	m.missions.Save(ms)
}

// settleBudgetItem closes a pending budget question when the mission was
// moved on by hand.
func (m *Model) settleBudgetItem(missionID, outcome string) {
	if it := m.budgetItem(missionID); it != nil {
		m.store.Settle(it, false, outcome)
	}
}

// budgetCommand is /budget on the open or selected mission: /budget 2M,
// /budget $5, /budget off, or /budget alone to show it.
func (m *Model) budgetCommand(args []string) (note string, isErr bool) {
	ms := m.ovMission()
	if ms == nil {
		return "Select a mission first.", true
	}
	if len(args) == 0 {
		if !ms.Budget.Set() {
			return "No budget on " + ms.Name + ". /budget 2M sets tokens, /budget $5 estimated dollars.", false
		}
		return ms.Name + ": " + m.missionSpent(ms).line() + " of " + budgetLabel(ms.Budget), false
	}
	// A budget that paused the mission and is still unanswered is replaced
	// by the new one: the mission goes on, and is paused again at the next
	// tick if the new ceiling is also crossed.
	releasePause := func() {
		if ms.Budget.Asked && !ms.Budget.Waived && ms.State == mission.Paused {
			ms.State = mission.Running
		}
	}
	if strings.EqualFold(args[0], "off") || args[0] == "0" {
		releasePause()
		ms.SetBudget(0, 0)
		m.settleBudgetItem(ms.ID, "budget removed")
		ms.Updated = time.Now()
		m.missions.Save(ms)
		return "Removed the budget on " + ms.Name, false
	}
	tokens, usd, err := cost.ParseBudget(args[0])
	if err != nil {
		return err.Error(), true
	}
	releasePause()
	ms.SetBudget(tokens, usd)
	m.settleBudgetItem(ms.ID, "budget changed")
	ms.Updated = time.Now()
	m.missions.Save(ms)
	return "Budget on " + ms.Name + ": " + budgetLabel(ms.Budget), false
}

// costLines are the cost rows of an open mission: what it used, against
// its budget when one is set.
func (m Model) costLines(ms *mission.Mission, w int) []string {
	s := m.st
	tot := m.missionSpent(ms)
	idle := m.idleAgents(ms, m.cardMissions(), time.Now())
	if tot.Tokens == 0 && !ms.Budget.Set() && len(ms.Merges) == 0 && len(idle) == 0 {
		return nil
	}
	label := func(k string) string { return s.dim.Render(fmt.Sprintf("  %-12s", k)) }
	var out []string
	if tot.Tokens > 0 || ms.Budget.Set() {
		out = append(out, label("used")+s.fg.Render(tot.line())+s.dim.Render(" estimate"))
	}
	if ms.Budget.Set() {
		line := budgetLabel(ms.Budget)
		if ms.Budget.Waived {
			line += " · passed, kept going"
		}
		out = append(out, label("budget")+s.fg.Render(line))
	}
	if len(ms.Merges) > 0 {
		out = append(out, label("merged")+s.fg.Render(m.mergeLine(ms)))
	}
	if len(idle) > 0 {
		var names []string
		for _, d := range idle {
			names = append(names, d.agent.Name+" "+idleText(d.idle))
		}
		out = append(out, label("idle")+s.fg.Render(strings.Join(names, ", "))+s.dim.Render(" no turn, nothing open"))
	}
	for i := range out {
		out[i] = ansi.Truncate(out[i], w, "…")
	}
	return out
}

// agentCostLines are the cost rows of the Fleet details pane.
func (m Model) agentCostLines(a fleet.Agent) [][2]string {
	if a.Cost.Total() == 0 {
		return nil
	}
	rows := [][2]string{{"tokens", cost.Tok(a.Cost.Tokens.Total()) + " tok" + cacheNote(a.Cost)}}
	if a.Cost.Subagents.Total() > 0 {
		rows = append(rows, [2]string{"subs", cost.Tok(a.Cost.Subagents.Total()) + " tok"})
	}
	rows = append(rows, [2]string{"cost", cost.Dollars(a.Cost.USD, a.Cost.Partial) + " estimate"})
	if kids := m.treeCost(a.Session.SessionID); len(m.childrenOf(a.Session.SessionID)) > 0 {
		rows = append(rows, [2]string{"team", kids.Line()})
	}
	if a.Cost.ReportedUSD != nil {
		rows = append(rows, [2]string{"reported", "Claude Code says ≈ $" + fmt.Sprintf("%.2f", *a.Cost.ReportedUSD)})
	}
	return rows
}

func cacheNote(s cost.Summary) string {
	t := s.Tokens
	return fmt.Sprintf(" · in %s out %s cache %s", cost.Tok(t.Input), cost.Tok(t.Output), cost.Tok(t.CacheCreation+t.CacheRead))
}
