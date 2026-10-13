package ui

import (
	"encoding/json"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/mission"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// update_mission. The orchestrator's word on a mission comes through its
// update_mission tool (`den mcp`), never from its text. Den applies it at
// once only when the turn began with the human's own words and the state
// is not final (continue, waiting); a final state (done, failed), or any
// update in a turn Den started or a report started, is put to the human
// as an inbox question, applied on ^Y after its whole text was shown.

// handleMissionUpdate answers the tool's call at once.
func (m *Model) handleMissionUpdate(r *bridge.Request) tea.Cmd {
	var in bridge.MissionInput
	if json.Unmarshal(r.Envelope.Payload, &in) != nil {
		r.Reply(bridge.Decision{Behavior: "deny", Message: "Den could not read the update; the mission was not changed."})
		return nil
	}
	u := mission.Update{ID: sanitize.Line(in.ID), State: strings.ToLower(in.State), Summary: sanitize.Label(in.Summary, 300)}
	ms := m.missions.Get(u.ID)
	switch {
	case m.orch.proc == nil || m.turnOrigin() == "":
		r.Reply(bridge.Decision{Behavior: "deny", Message: "Den takes mission updates only from the orchestrator, during a turn; the mission was not changed."})
		return nil
	case ms == nil:
		r.Reply(bridge.Decision{Behavior: "deny", Message: "No mission has that id; nothing was changed."})
		return nil
	}
	switch u.State {
	case "done", "failed", "continue", "waiting":
	default:
		r.Reply(bridge.Decision{Behavior: "deny", Message: "The state must be done, continue, waiting or failed; the mission was not changed."})
		return nil
	}
	// Only a turn that is the human's alone: no report or turn of Den's in
	// flight with it (Claude Code takes queued turns one by one, so a
	// longer list is another turn waiting, which Den treats as mixed).
	if len(m.orch.origins) == 1 && m.orch.origins[0] == originHuman && (u.State == "continue" || u.State == "waiting") {
		m.applyUpdate(u)
		r.Reply(bridge.Decision{Behavior: "allow", Message: "Den applied it: mission " + ms.Name + " is " + u.State + "."})
		return nil
	}
	it := m.store.AddMissionUpdate(missionSessionPrefix+ms.ID, "The orchestrator says mission "+ms.Name+" is "+u.State, []string{
		"its summary: " + u.Summary,
		"^Y applies it (" + u.State + "); ^N leaves the mission as it is.",
	})
	if m.missionUpdates == nil {
		m.missionUpdates = map[string]mission.Update{}
	}
	m.missionUpdates[it.ID] = u
	r.Reply(bridge.Decision{Behavior: "allow", Message: "Den asked the human to confirm it in Den; the mission changes only if they do."})
	m.flash, m.flashErr = "The orchestrator says mission "+ms.Name+" is "+u.State+": ^Y in the Inbox applies it.", false
	return m.notifyCmd(missionSessionPrefix+ms.ID, "says mission "+ms.Name+" is "+u.State)
}

// decideMissionUpdate is ^Y or ^N on the question.
func (m Model) decideMissionUpdate(it *inbox.Item, verb string) (tea.Model, tea.Cmd) {
	u, ok := m.missionUpdates[it.ID]
	delete(m.missionUpdates, it.ID)
	if !ok || m.missions.Get(u.ID) == nil {
		m.store.Settle(it, false, "the mission is gone")
		m.refresh()
		return m.say("That mission is gone.", true)
	}
	if verb != "allow" {
		m.store.Settle(it, false, "left as it was")
		m.refresh()
		return m.say("Left the mission as it was.", false)
	}
	m.applyUpdate(u)
	m.store.Settle(it, true, "applied: "+u.State)
	m.refresh()
	return m.say("Mission updated: "+u.State+".", false)
}
