package ui

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/mission"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// update_mission. The orchestrator's word on a mission comes through its
// update_mission tool (`den mcp`), never from its text, and only from a
// process it started, during one of its turns.
//
// What it may change, and how:
//   - sub-goals (add, re-own, state, note) and criterion claims are its
//     bookkeeping: Den applies them at once in the human's turns and its
//     own (runs, judging, reports). In a paired app's turn, or when turns
//     are mixed, the whole call waits on an inbox question.
//   - continue and waiting apply at once only in a turn that is the human's
//     alone, as before; failed is always a question (MissionUpdate).
//   - done, and the moment every criterion is claimed, become one
//     MissionDone question; a change to a criterion's text becomes a
//     MissionCriteria question. Both are answered by the human in Den
//     only: never from the dock, never by God mode. Only their decide
//     paths, and the human's own /mission met and /mission unmet, mark a
//     criterion met or change its text.

// missionAsk is what ^Y on a criteria or done question does.
type missionAsk struct {
	mission string
	texts   []bridge.CriterionText // MissionCriteria: the new texts
	summary string                 // MissionDone: the orchestrator's line, if it said done
}

// Caps on one call.
const maxCallItems = 12

// criterionID is what /mission met and unmet take: c1..c8.
var criterionID = regexp.MustCompile(`^c[1-8]$`)

// handleMissionUpdate answers the tool's call at once.
func (m *Model) handleMissionUpdate(r *bridge.Request) tea.Cmd {
	var raw bridge.MissionInput
	if json.Unmarshal(r.Envelope.Payload, &raw) != nil {
		r.Reply(bridge.Decision{Behavior: "deny", Message: "Den could not read the update; the mission was not changed."})
		return nil
	}
	in := cleanMissionInput(raw)
	deny := func(why string) tea.Cmd {
		r.Reply(bridge.Decision{Behavior: "deny", Message: why})
		return nil
	}
	ms := m.missions.Get(in.ID)
	switch {
	case m.orch.proc == nil || m.turnOrigin() == "":
		return deny("Den takes mission updates only from the orchestrator, during a turn; the mission was not changed.")
	case ms == nil:
		return deny("No mission has that id; nothing was changed.")
	case ms.State == mission.Proposed:
		return deny("That mission is a proposal: change it with propose_mission and its id.")
	case !ms.Live():
		return deny("That mission is " + string(ms.State) + "; nothing was changed.")
	}
	switch in.State {
	case "", "done", "failed", "continue", "waiting":
	default:
		return deny("The state must be done, continue, waiting or failed; the mission was not changed.")
	}
	if in.State == "" && len(in.Goals) == 0 && len(in.Claims) == 0 && len(in.CriteriaText) == 0 {
		return deny("Nothing to change: give a state, goals, claims or criteria_text.")
	}
	// The whole call is checked on a copy first: nothing changes unless all
	// of it can.
	cp := cloneMission(ms)
	if err := m.applyBookkeeping(cp, in, time.Now()); err != nil {
		return deny(err.Error() + "; the mission was not changed.")
	}
	if err := checkCriteriaText(cp, in.CriteriaText); err != nil {
		return deny(err.Error() + "; the mission was not changed.")
	}
	if in.State == "done" && len(cp.Criteria) > 0 {
		if open := openCriteria(cp); len(open) > 0 {
			return deny("Claim each criterion with its evidence first (" + strings.Join(open, ", ") + " open); the mission was not changed.")
		}
	}
	if m.appInTurn() {
		// A paired app's turn, or turns Den cannot tell apart: the human
		// sees the whole call first.
		it := m.store.AddMissionUpdate(missionSessionPrefix+ms.ID, "In an app's turn the orchestrator asks to update mission "+ms.Name, describeCall(ms, in))
		m.holdUpdate(it.ID, in)
		r.Reply(bridge.Decision{Behavior: "allow", Message: "Den asked the human to confirm it in Den; the mission changes only if they do."})
		m.flash, m.flashErr = "The orchestrator asks to update mission "+ms.Name+": ^Y in the Inbox applies it.", false
		return m.notifyCmd(missionSessionPrefix+ms.ID, "asks to update mission "+ms.Name)
	}
	var notes []string
	var cmds []tea.Cmd
	if len(in.Goals) > 0 || len(in.Claims) > 0 {
		noteChanges(cp, in, m.turnOrigin(), time.Now())
		*ms = *cp
		ms.Updated = time.Now()
		m.missions.Save(ms)
		notes = append(notes, "Den applied the sub-goals and claims")
	}
	if len(in.CriteriaText) > 0 {
		cmds = append(cmds, m.askCriteria(ms, in.CriteriaText))
		notes = append(notes, "Den asked the human to change the criteria; they change only if the human agrees")
	}
	switch in.State {
	case "continue", "waiting":
		if !m.orch.mixed && len(m.orch.origins) == 1 && m.orch.origins[0] == originHuman {
			m.applyUpdate(mission.Update{ID: ms.ID, State: in.State, Summary: in.Summary})
			notes = append(notes, "Den applied it: mission "+ms.Name+" is "+in.State)
		} else {
			it := m.store.AddMissionUpdate(missionSessionPrefix+ms.ID, "The orchestrator says mission "+ms.Name+" is "+in.State, []string{
				"its summary: " + in.Summary,
				"^Y applies it (" + in.State + "); ^N leaves the mission as it is.",
			})
			m.holdUpdate(it.ID, bridge.MissionInput{ID: ms.ID, State: in.State, Summary: in.Summary})
			notes = append(notes, "Den asked the human to confirm "+in.State)
			cmds = append(cmds, m.notifyCmd(missionSessionPrefix+ms.ID, "says mission "+ms.Name+" is "+in.State))
		}
	case "failed":
		it := m.store.AddMissionUpdate(missionSessionPrefix+ms.ID, "The orchestrator says mission "+ms.Name+" failed", []string{
			"its summary: " + in.Summary,
			"^Y marks it failed; ^N leaves the mission as it is.",
		})
		m.holdUpdate(it.ID, bridge.MissionInput{ID: ms.ID, State: "failed", Summary: in.Summary})
		notes = append(notes, "Den asked the human to confirm failed")
		cmds = append(cmds, m.notifyCmd(missionSessionPrefix+ms.ID, "says mission "+ms.Name+" failed"))
	}
	if in.State == "done" || (len(in.Claims) > 0 && ms.AllClaimed()) {
		if asked, c := m.askDone(ms, in.Summary); asked {
			cmds = append(cmds, c)
			notes = append(notes, "Den asked the human to confirm whether the mission is done; only they mark it done")
		}
	}
	if len(notes) == 0 {
		notes = append(notes, "Den is already asking the human whether the mission is done; only they mark it done")
	}
	r.Reply(bridge.Decision{Behavior: "allow", Message: strings.Join(notes, "; ") + "."})
	return tea.Batch(cmds...)
}

// cleanMissionInput sanitizes and caps everything in a call before Den
// looks at it.
func cleanMissionInput(in bridge.MissionInput) bridge.MissionInput {
	out := bridge.MissionInput{ID: sanitize.Line(in.ID), State: strings.ToLower(strings.TrimSpace(in.State)), Summary: sanitize.Label(in.Summary, 300)}
	for i, g := range in.Goals {
		if i == maxCallItems {
			break
		}
		out.Goals = append(out.Goals, bridge.GoalInput{ID: sanitize.Label(g.ID, 8), Text: sanitize.Label(g.Text, 200), Owner: sanitize.Label(g.Owner, 80), State: strings.ToLower(sanitize.Label(g.State, 10)), Note: sanitize.Label(g.Note, 200)})
	}
	for i, c := range in.Claims {
		if i == mission.MaxCriteria {
			break
		}
		out.Claims = append(out.Claims, bridge.ClaimInput{ID: sanitize.Label(c.ID, 8), Evidence: sanitize.Label(c.Evidence, 300)})
	}
	for i, c := range in.CriteriaText {
		if i == mission.MaxCriteria {
			break
		}
		out.CriteriaText = append(out.CriteriaText, bridge.CriterionText{ID: sanitize.Label(c.ID, 8), Text: sanitize.Label(c.Text, 300)})
	}
	return out
}

// applyBookkeeping applies a call's sub-goal changes and claims to ms (a
// copy, until the caller keeps it). It never marks a criterion met and
// never changes a criterion's text.
func (m Model) applyBookkeeping(ms *mission.Mission, in bridge.MissionInput, now time.Time) error {
	for _, g := range in.Goals {
		var sid, name, role string
		if g.Owner != "" {
			var err error
			if sid, name, role, err = m.resolveOwner(g.Owner, ms.Team); err != nil {
				return err
			}
		}
		if g.ID == "" {
			if _, err := ms.AddGoal(mission.SubGoal{Text: g.Text, Owner: sid, Name: name, Role: role, State: firstNonEmptyS(g.State, mission.GoalTodo), Note: g.Note}); err != nil {
				return err
			}
			continue
		}
		var p mission.GoalPatch
		if g.Text != "" {
			p.Text = &g.Text
		}
		if g.State != "" {
			p.State = &g.State
		}
		if g.Note != "" {
			p.Note = &g.Note
		}
		if err := ms.UpdateGoal(g.ID, p); err != nil {
			return err
		}
		if g.Owner != "" {
			if err := ms.SetOwner(g.ID, sid, "", name, role); err != nil {
				return err
			}
		}
	}
	for _, c := range in.Claims {
		if err := ms.Claim(c.ID, c.Evidence, now); err != nil {
			return err
		}
	}
	return nil
}

// checkCriteriaText checks a change request without applying it.
func checkCriteriaText(ms *mission.Mission, texts []bridge.CriterionText) error {
	for _, t := range texts {
		if ms.Criterion(t.ID) == nil {
			return fmt.Errorf("no criterion %q", t.ID)
		}
		if t.Text == "" {
			return fmt.Errorf("%s needs its new text", t.ID)
		}
	}
	return nil
}

// noteChanges writes a call's changes into the mission's history, with
// who started the turn.
func noteChanges(ms *mission.Mission, in bridge.MissionInput, origin string, now time.Time) {
	by := "by the orchestrator"
	switch origin {
	case originReport:
		by += " after an agent's report"
	case originDen:
		by += " in Den's turn"
	case originHuman:
		by += " in your turn"
	}
	for _, g := range in.Goals {
		what := firstNonEmptyS(g.ID, "new sub-goal")
		var parts []string
		if g.Owner != "" {
			parts = append(parts, "owner "+g.Owner)
		}
		if g.State != "" {
			parts = append(parts, g.State)
		}
		if g.Text != "" && g.ID != "" {
			parts = append(parts, "text changed")
		}
		ms.Note(now, what+": "+strings.Join(parts, ", ")+" "+by)
	}
	for _, c := range in.Claims {
		ms.Note(now, c.ID+" claimed "+by)
	}
}

// openCriteria lists the criteria neither claimed nor met.
func openCriteria(ms *mission.Mission) []string {
	var out []string
	for _, c := range ms.Criteria {
		if c.State == mission.CritOpen {
			out = append(out, c.ID)
		}
	}
	return out
}

// appInTurn reports whether a paired app's turn is in flight, or turns can
// no longer be told apart.
func (m Model) appInTurn() bool {
	if m.orch.mixed {
		return true
	}
	for _, o := range m.orch.origins {
		if o == originApp {
			return true
		}
	}
	return false
}

func (m *Model) holdUpdate(itemID string, in bridge.MissionInput) {
	if m.missionUpdates == nil {
		m.missionUpdates = map[string]bridge.MissionInput{}
	}
	m.missionUpdates[itemID] = in
}

func (m *Model) holdAsk(itemID string, a missionAsk) {
	if m.missionAsks == nil {
		m.missionAsks = map[string]missionAsk{}
	}
	m.missionAsks[itemID] = a
}

// describeCall is a held call as the human reads it, every part of it.
func describeCall(ms *mission.Mission, in bridge.MissionInput) []string {
	var out []string
	if in.State != "" {
		out = append(out, "state: "+in.State+" · "+in.Summary)
	}
	for _, g := range in.Goals {
		line := "sub-goal " + firstNonEmptyS(g.ID, "(new)")
		for _, kv := range [][2]string{{"text", g.Text}, {"owner", g.Owner}, {"state", g.State}, {"note", g.Note}} {
			if kv[1] != "" {
				line += " · " + kv[0] + ": " + kv[1]
			}
		}
		out = append(out, line)
	}
	for _, c := range in.Claims {
		text := ""
		if cr := ms.Criterion(c.ID); cr != nil {
			text = " (" + cr.Text + ")"
		}
		out = append(out, "claims "+c.ID+text+" is met · "+c.Evidence)
	}
	for _, t := range in.CriteriaText {
		out = append(out, "asks to change "+t.ID+" to: "+t.Text+" (a separate question if you apply this)")
	}
	return append(out, "^Y applies it; ^N leaves the mission as it is. Done and criteria changes still come to you on their own.")
}

// askCriteria raises the question to change criteria's text.
func (m *Model) askCriteria(ms *mission.Mission, texts []bridge.CriterionText) tea.Cmd {
	var body []string
	for _, t := range texts {
		old := ""
		if c := ms.Criterion(t.ID); c != nil {
			old = c.Text
		}
		body = append(body, t.ID+": "+old, "  → "+t.Text)
	}
	body = append(body, "The done criteria decide when the mission is done. ^Y changes them (a changed criterion opens again); ^N keeps them.")
	it := m.store.AddMissionCriteria(missionSessionPrefix+ms.ID, "The orchestrator asks to change the done criteria of "+ms.Name, body)
	m.holdAsk(it.ID, missionAsk{mission: ms.ID, texts: texts})
	return m.notifyCmd(missionSessionPrefix+ms.ID, "asks to change the done criteria of "+ms.Name)
}

// askDone raises the question whether the mission is done, once: asked
// is false when one is already waiting.
func (m *Model) askDone(ms *mission.Mission, summary string) (asked bool, cmd tea.Cmd) {
	for _, it := range m.store.Items() {
		if it.Kind == inbox.MissionDone && it.State == inbox.Waiting && it.SessionID == missionSessionPrefix+ms.ID {
			return false, nil
		}
	}
	var body []string
	for _, c := range ms.Criteria {
		line := c.ID + " " + c.Text
		switch c.State {
		case mission.CritMet:
			line += " · met"
		case mission.CritClaimed:
			line += " · claimed: " + c.Evidence
		default:
			line += " · open"
		}
		body = append(body, line)
	}
	if summary != "" {
		body = append(body, "the orchestrator says: "+summary)
	}
	body = append(body, "^Y marks every criterion met and the mission done; ^N keeps it running.")
	it := m.store.AddMissionDone(missionSessionPrefix+ms.ID, "Is mission "+ms.Name+" done?", body)
	m.holdAsk(it.ID, missionAsk{mission: ms.ID, summary: summary})
	return true, m.notifyCmd(missionSessionPrefix+ms.ID, "asks whether mission "+ms.Name+" is done")
}

// decideMissionUpdate is ^Y or ^N on a held update. It applies the call's
// state and bookkeeping; done and criteria changes still go to their own
// questions, so this question (which God mode may answer) never marks a
// criterion met or changes one.
func (m Model) decideMissionUpdate(it *inbox.Item, verb string) (tea.Model, tea.Cmd) {
	in, ok := m.missionUpdates[it.ID]
	delete(m.missionUpdates, it.ID)
	ms := m.missions.Get(in.ID)
	if !ok || ms == nil {
		m.store.Settle(it, false, "the mission is gone")
		m.refresh()
		return m.say("That mission is gone.", true)
	}
	if verb != "allow" {
		m.store.Settle(it, false, "left as it was")
		m.refresh()
		return m.say("Left the mission as it was.", false)
	}
	now := time.Now()
	var cmds []tea.Cmd
	if len(in.Goals) > 0 || len(in.Claims) > 0 {
		cp := cloneMission(ms)
		if err := m.applyBookkeeping(cp, in, now); err != nil {
			m.store.Settle(it, false, "no longer applies: "+err.Error())
			m.refresh()
			return m.say("That update no longer applies: "+err.Error(), true)
		}
		noteChanges(cp, in, originApp, now)
		*ms = *cp
		ms.Updated = now
		m.missions.Save(ms)
	}
	switch in.State {
	case "continue", "waiting", "failed":
		m.applyUpdate(mission.Update{ID: ms.ID, State: in.State, Summary: in.Summary})
	}
	if len(in.CriteriaText) > 0 && checkCriteriaText(ms, in.CriteriaText) == nil {
		cmds = append(cmds, m.askCriteria(ms, in.CriteriaText))
	}
	if ms.Live() && (in.State == "done" || (len(in.Claims) > 0 && ms.AllClaimed())) {
		_, c := m.askDone(ms, in.Summary)
		cmds = append(cmds, c)
	}
	m.store.Settle(it, true, "applied")
	m.refresh()
	nm, c := m.say("Mission updated.", false)
	return nm, tea.Batch(append(cmds, c)...)
}

// decideMissionCriteria is the human's ^Y or ^N on a criteria change.
func (m Model) decideMissionCriteria(it *inbox.Item, verb string) (tea.Model, tea.Cmd) {
	a, ok := m.missionAsks[it.ID]
	delete(m.missionAsks, it.ID)
	ms := m.missions.Get(a.mission)
	if !ok || ms == nil {
		m.store.Settle(it, false, "the mission is gone")
		m.refresh()
		return m.say("That mission is gone.", true)
	}
	if verb != "allow" {
		m.store.Settle(it, false, "kept the criteria")
		m.refresh()
		return m.say("Kept the done criteria as they were.", false)
	}
	now := time.Now()
	for _, t := range a.texts {
		if err := ms.ChangeCriterion(t.ID, t.Text); err == nil {
			ms.Note(now, t.ID+" changed by you")
		}
	}
	ms.Updated = now
	m.missions.Save(ms)
	m.store.Settle(it, true, "criteria changed")
	m.refresh()
	return m.say("Changed the done criteria of "+ms.Name+".", false)
}

// decideMissionDone is the human's ^Y or ^N on "is it done?".
func (m Model) decideMissionDone(it *inbox.Item, verb string) (tea.Model, tea.Cmd) {
	a, ok := m.missionAsks[it.ID]
	delete(m.missionAsks, it.ID)
	ms := m.missions.Get(a.mission)
	if !ok || ms == nil {
		m.store.Settle(it, false, "the mission is gone")
		m.refresh()
		return m.say("That mission is gone.", true)
	}
	if verb != "allow" {
		ms.Note(time.Now(), "not done yet, said you")
		m.missions.Save(ms)
		m.store.Settle(it, false, "not done yet")
		m.refresh()
		return m.say("Kept "+ms.Name+" running. Tell the orchestrator what is missing.", false)
	}
	now := time.Now()
	ms.MeetAll(now)
	ms.Note(now, "done, confirmed by you")
	m.applyUpdate(mission.Update{ID: ms.ID, State: "done", Summary: firstNonEmptyS(a.summary, "done, confirmed by you")})
	m.store.Settle(it, true, "mission done")
	m.refresh()
	return m.say("Mission "+ms.Name+" is done.", false)
}

// missionMetCommand is the human's /mission met <id> and /mission unmet
// <id>, typed in Den, on the open or selected mission.
func (m *Model) missionMetCommand(met bool, id string) (string, bool) {
	ms := m.ovMission()
	if ms == nil {
		return "Select a mission on the Overview first.", true
	}
	if !ms.Frozen() {
		return "That mission has not started: its criteria are still the proposal's.", true
	}
	now := time.Now()
	if err := ms.SetMet(id, met, now); err != nil {
		return err.Error() + ".", true
	}
	word := "met"
	if !met {
		word = "open again"
	}
	ms.Note(now, strings.ToLower(strings.TrimSpace(id))+" "+word+", said you")
	ms.Updated = now
	m.missions.Save(ms)
	return sanitize.Label(id, 8) + " is " + word + ".", false
}
