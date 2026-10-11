package ui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/mission"
)

// Missions live on the Overview tab. The human writes the goal, the
// orchestrator proposes the mission and its shape, ^Y starts it, and from
// then on Den drives the runs: it asks the orchestrator to dispatch, tags
// the cards that come out of that turn with the mission, notices when they
// settle, and asks again when the shape calls for another pass.

// maxRuns stops a loop that never says done.
const maxRuns = 50

// missionTurn is which mission the orchestrator's current turn works for,
// so the dispatch cards it creates are the mission's.
type missionTurn struct {
	id      string
	pending bool // waiting for the orchestrator's answer
}

// missionsDir is what the orchestrator is told about missions, appended to
// the first mission message of a session.
const missionsDir = `Missions are standing goals on a project that Den runs through you. A mission has: name (a few words), mission (the purpose in one sentence), done_when (an observable end state, or "standing" for one that never ends), constraints (short phrases that ride on every dispatch), shape, cadence, reason, agents.
Shapes: "once" (one dispatch to one agent, done when it reports back), "loop" (one agent iterates until the goal is met; you re-dispatch to it after each pass until done), "fanout" (a lead with children in parallel, repeated until the lead says done), "schedule" (Den fires a run on the cadence; each run is a once or a fanout). Cadence forms: "every 30m", "every 2h", "daily 08:30", "weekly mon 09:00", or a five-field cron line.
A mission names who does the work: "agents" are running agents from the roster to reuse; "team" are roles to start, each {"role": "...", "model": "haiku|sonnet|opus", "under": "the role it reports to, or orchestrator"}. Pick each model by how hard its task is: haiku for small mechanical tasks, sonnet for ordinary building and writing, opus for design, architecture, product judgment, review and leading others. Work that needs several specialists gets one lead on opus with the specialists under it.
When asked to propose, answer in two or three plain sentences and then one fenced json block: {"mission": {"name": "...", "mission": "...", "done_when": "...", "constraints": ["..."], "shape": "once|loop|fanout|schedule", "cadence": "...", "reason": "one line on why this shape", "agents": ["critter names"], "team": [{"role": "...", "model": "...", "under": "..."}]}}. Do not dispatch anything while proposing.
When asked to run a mission, dispatch per its shape: SendMessage to the agents it reuses, and spawn_agent in the project directory for each team role under the orchestrator, on its model, with a complete brief that carries the mission, its constraints and that role's part. A lead's brief lists the roles under it with their models and parts, and says to start them itself with its spawn_agent tool, wait for their reports, and report to you when the whole piece is done; never start a lead's people yourself. Then say in one line what you sent. When a run's agents have reported and you are asked whether the goal is met, answer in one or two sentences and end with one fenced json block: {"mission_update": {"id": "...", "state": "done|continue|waiting|failed", "summary": "one line"}}; with "continue", dispatch the next pass in the same reply.`

// proposeMission starts a proposal for the picked project from the
// human's words.
func (m *Model) proposeMission(text string) tea.Cmd {
	p, ok := m.curProject()
	if !ok {
		return nil
	}
	now := time.Now()
	ms := &mission.Mission{ID: mission.NewID(now), Project: p.Root, Text: text, State: mission.Proposed, Created: now, Updated: now}
	m.missions.Save(ms)
	m.ov.proposal = ms.ID
	prompt := fmt.Sprintf("Propose a mission for project %s (repository %s, branch %s). The human's words:\n%s", p.Name, p.Root, p.Branch, text)
	return m.sendMission(ms.ID, true, text, prompt)
}

// refineMission sends the human's follow-up on the pending proposal.
func (m *Model) refineMission(text string) tea.Cmd {
	ms := m.missions.Get(m.ov.proposal)
	if ms == nil {
		return nil
	}
	prompt := fmt.Sprintf("Refine the mission proposal. Current proposal:\n%sThe human says:\n%s\nReply with the full updated json block.", ms.Describe(), text)
	return m.sendMission(ms.ID, false, text, prompt)
}

// messageMission talks to the orchestrator about an open mission; any
// dispatch it makes joins the mission's current run.
func (m *Model) messageMission(ms *mission.Mission, text string) tea.Cmd {
	m.turn = missionTurn{id: ms.ID}
	prompt := fmt.Sprintf("About mission %s (%q):\n%sThe human says:\n%s", ms.ID, ms.Name, ms.Describe(), text)
	return m.sendMission(ms.ID, false, text, prompt)
}

// sendMission sends a mission message to the orchestrator without leaving
// the Overview tab. The mission's thread shows the human's own words; the
// orchestrator receives the framed prompt, with the mission vocabulary the
// first time.
func (m *Model) sendMission(id string, withDir bool, shown, prompt string) tea.Cmd {
	if !m.missionDirSent {
		prompt = missionsDir + "\n\n" + prompt
		m.missionDirSent = true
	}
	m.turn.pending = true
	return m.tell(id, shown, prompt)
}

// missionReply reads what the orchestrator said for a mission: a proposal
// fills the pending one, an update moves a mission on. It returns the text
// with the json blocks removed, for the chat and the mission's thread.
func (m *Model) missionReply(text string) string {
	shown := text
	if p, ok := mission.ParseProposal(text); ok && m.ov.proposal != "" {
		if ms := m.missions.Get(m.ov.proposal); ms != nil && ms.State == mission.Proposed {
			ms.Apply(p)
			ms.Updated = time.Now()
			m.missions.Save(ms)
		}
		shown = mission.Strip(text)
	}
	if u, ok := mission.ParseUpdate(text); ok {
		m.applyUpdate(u)
		shown = mission.Strip(text)
	}
	return shown
}

func (m *Model) applyUpdate(u mission.Update) {
	ms := m.missions.Get(u.ID)
	if ms == nil {
		return
	}
	now := time.Now()
	switch strings.ToLower(u.State) {
	case "done":
		if r := ms.CurrentRun(); r != nil {
			r.EndedAt, r.Summary = now, u.Summary
		} else if n := len(ms.Runs); n > 0 && u.Summary != "" {
			ms.Runs[n-1].Summary = u.Summary
		}
		ms.State = mission.Done
	case "failed":
		if r := ms.CurrentRun(); r != nil {
			r.EndedAt, r.Summary = now, u.Summary
		}
		ms.State = mission.Failed
	case "continue", "waiting":
		if r := ms.CurrentRun(); r != nil && u.Summary != "" {
			r.Summary = u.Summary
		}
	}
	ms.Updated = now
	m.missions.Save(ms)
}

// startMission turns the pending proposal into a running mission and asks
// for its first run.
func (m *Model) startMission() tea.Cmd {
	ms := m.missions.Get(m.ov.proposal)
	if ms == nil || ms.Mission == "" {
		return nil
	}
	ms.State = mission.Running
	ms.Updated = time.Now()
	m.ov.proposal = ""
	m.ov.open = ms.ID
	m.missions.Save(ms)
	return m.runMission(ms, "Start")
}

// dropMission declines the pending proposal.
func (m *Model) dropMission() {
	if ms := m.missions.Get(m.ov.proposal); ms != nil {
		ms.State = mission.Dropped
		ms.Updated = time.Now()
		m.missions.Save(ms)
	}
	m.ov.proposal = ""
}

// runMission opens a run and asks the orchestrator to dispatch it.
func (m *Model) runMission(ms *mission.Mission, verb string) tea.Cmd {
	if len(ms.Runs) >= maxRuns {
		ms.State, ms.Updated = mission.Failed, time.Now()
		m.missions.Save(ms)
		return nil
	}
	now := time.Now()
	r := ms.StartRun(now)
	if ms.Shape == mission.Schedule {
		if next, err := mission.Next(ms.Cadence, now); err == nil {
			ms.NextRun = next
		}
	}
	m.missions.Save(ms)
	m.turn = missionTurn{id: ms.ID}
	prompt := fmt.Sprintf("%s mission %s, run %d.\n%sDispatch this run now, then say in one line what you sent.", verb, ms.ID, r.N, ms.Describe())
	return m.sendMission(ms.ID, false, fmt.Sprintf("%s mission %q, run %d", strings.ToLower(verb), ms.Name, r.N), prompt)
}

// judgeMission asks whether a finished run met the goal; a loop or a
// fan-out dispatches its next pass in the same reply, and those cards open
// the next run.
func (m *Model) judgeMission(ms *mission.Mission, r *mission.Run) tea.Cmd {
	var results []string
	for _, d := range m.cards() {
		if d.MissionID != ms.ID {
			continue
		}
		for _, id := range r.Cards {
			if d.ID == id {
				line := d.Agent + ": " + firstNonEmptyS(inbox.Summarize(d.Result), d.Reason, "no reply")
				results = append(results, "- "+line)
			}
		}
	}
	m.turn = missionTurn{id: ms.ID}
	prompt := fmt.Sprintf("Mission %s run %d: every agent has reported.\n%sResults:\n%s\nIs the goal met? Answer with the mission_update block; with \"continue\", dispatch the next pass now.", ms.ID, r.N, ms.Describe(), strings.Join(results, "\n"))
	return m.sendMission(ms.ID, false, fmt.Sprintf("mission %q run %d finished · judging", ms.Name, r.N), prompt)
}

// attachCard gives a new dispatch card to the mission the orchestrator's
// turn is working for.
func (m *Model) attachCard(d *dispatch) { m.attachCardTo(d, m.turn.id) }

// attachCardTo gives a card to a mission's current run.
func (m *Model) attachCardTo(d *dispatch, missionID string) {
	if missionID == "" {
		return
	}
	ms := m.missions.Get(missionID)
	if ms == nil || !ms.Live() {
		return
	}
	d.MissionID = ms.ID
	r := ms.CurrentRun()
	if r == nil {
		r = ms.StartRun(time.Now())
	}
	r.Cards = append(r.Cards, d.ID)
	ms.Updated = time.Now()
	m.missions.Save(ms)
}

// missionCards lists a run's cards in chat order.
func (m Model) missionCards(ms *mission.Mission, r *mission.Run) []*dispatch {
	var out []*dispatch
	for _, d := range m.cards() {
		if d.MissionID != ms.ID {
			continue
		}
		for _, id := range r.Cards {
			if d.ID == id {
				out = append(out, d)
			}
		}
	}
	return out
}

// settleMissions moves missions on from what their cards did: a run whose
// cards have all settled ends, and the shape says what comes next. It also
// fires scheduled runs that are due.
func (m *Model) settleMissions() tea.Cmd {
	if m.missions == nil {
		return nil
	}
	now := time.Now()
	var cmds []tea.Cmd
	for _, ms := range m.missions.All() {
		if !ms.Live() {
			continue
		}
		r := ms.CurrentRun()
		if r != nil && len(r.Cards) > 0 && m.turn.id != ms.ID {
			cards := m.missionCards(ms, r)
			live := false
			for _, d := range cards {
				if d.live() {
					live = true
				}
			}
			if !live && len(cards) == len(r.Cards) {
				r.EndedAt = now
				var parts []string
				for _, d := range cards {
					if s := inbox.Summarize(d.Result); s != "" {
						parts = append(parts, d.Agent+": "+s)
					}
				}
				r.Summary = strings.Join(parts, " · ")
				ms.Updated = now
				switch {
				case ms.State == mission.Paused:
				case ms.Shape == mission.Once:
					ms.State = mission.Done
				case ms.Shape == mission.Schedule:
					// The next tick fires the next run; the orchestrator is
					// not asked to judge a chore.
				default:
					cmds = append(cmds, m.judgeMission(ms, r))
				}
				m.missions.Save(ms)
			}
		}
		if ms.Shape == mission.Schedule && ms.State == mission.Running && ms.CurrentRun() == nil && !ms.NextRun.IsZero() && !ms.NextRun.After(now) && !m.turn.pending {
			cmds = append(cmds, m.runMission(ms, "Scheduled run of"))
		}
	}
	return tea.Batch(cmds...)
}

// missionState is what the row shows: the stored state, with "waiting"
// when a run's card needs the human.
func (m Model) missionState(ms *mission.Mission) mission.State {
	if ms.State != mission.Running {
		return ms.State
	}
	if r := ms.CurrentRun(); r != nil {
		for _, d := range m.missionCards(ms, r) {
			if d.State == cardNeedsYou {
				return mission.Waiting
			}
		}
	}
	return ms.State
}

// missionCommand runs /pause, /resume and /stop on the open or selected
// mission. It reports whether the command was one of those.
func (m *Model) missionCommand(verb string) (note string, isErr, ok bool) {
	ms := m.ovMission()
	switch verb {
	case "/pause", "/resume", "/stop":
	default:
		return "", false, false
	}
	if ms == nil {
		return "Select a mission first.", true, true
	}
	now := time.Now()
	switch verb {
	case "/pause":
		if !ms.Live() {
			return "That mission is not running.", true, true
		}
		ms.State = mission.Paused
	case "/resume":
		if ms.State != mission.Paused {
			return "That mission is not paused.", true, true
		}
		ms.State = mission.Running
		if ms.Shape == mission.Schedule {
			if next, err := mission.Next(ms.Cadence, now); err == nil {
				ms.NextRun = next
			}
		}
	case "/stop":
		if r := ms.CurrentRun(); r != nil {
			r.EndedAt, r.Summary = now, firstNonEmptyS(r.Summary, "stopped by you")
		}
		ms.State = mission.Done
	}
	ms.Updated = now
	m.missions.Save(ms)
	return strings.TrimPrefix(verb, "/") + "d " + ms.Name, false, true
}

// ovMission is the mission the Overview acts on: the open one, else the
// selected row.
func (m Model) ovMission() *mission.Mission {
	if m.ov.open != "" {
		return m.missions.Get(m.ov.open)
	}
	list := m.missions.ForProject(m.proj.cur)
	if m.ov.cur >= 0 && m.ov.cur < len(list) {
		return list[m.ov.cur]
	}
	return nil
}

// missionText is what typed text does on the Overview tab: refine the
// pending proposal, talk to the open mission, or propose a new one.
func (m *Model) missionText(text string) tea.Cmd {
	switch {
	case m.ov.proposal != "":
		return m.refineMission(text)
	case m.ov.open != "":
		if ms := m.missions.Get(m.ov.open); ms != nil {
			return m.messageMission(ms, text)
		}
	}
	return m.proposeMission(text)
}

// toggleMission opens the selected mission, reopens a proposal, or closes
// the open one.
func (m *Model) toggleMission() {
	if m.ov.open != "" {
		m.ov.open, m.ov.scroll = "", 0
		return
	}
	if m.ov.proposal != "" {
		return
	}
	ms := m.ovMission()
	if ms == nil {
		return
	}
	m.ov.scroll = 0
	if ms.State == mission.Proposed {
		m.ov.proposal = ms.ID
		return
	}
	m.ov.open = ms.ID
}
