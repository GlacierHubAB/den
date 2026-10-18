package ui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/mission"
	"github.com/GlacierHubAB/den/internal/orch"
)

// missionUpdateModel is a running mission and a live orchestrator on turn
// origin.
func missionUpdateModel(t *testing.T, origin string) (Model, *mission.Mission) {
	t.Helper()
	m := missionModel()
	proc, _ := liveOrchestrator(t)
	m.orch.proc = proc
	if origin != "" {
		m.orch.origins = []string{origin}
	}
	ms := &mission.Mission{ID: "m1", Name: "green tests", Mission: "keep the tests green", State: mission.Running, Shape: mission.Once}
	ms.StartRun(time.Now())
	m.missions.Save(ms)
	return m, ms
}

func updateMission(t *testing.T, m *Model, state string) bridge.Decision {
	t.Helper()
	raw, _ := json.Marshal(bridge.MissionInput{ID: "m1", State: state, Summary: "all green"})
	r := bridge.TestRequest(bridge.Envelope{Event: bridge.MissionEvent, Payload: raw})
	r.PeerPID = orchChildPID
	m.handleRequest(r)
	d, ok := r.Decided()
	if !ok {
		t.Fatal("update_mission got no answer")
	}
	return d
}

func missionItems(m Model) []*inbox.Item {
	var out []*inbox.Item
	for _, it := range m.store.Items() {
		if it.Kind == inbox.MissionUpdate || it.Kind == inbox.MissionDone {
			out = append(out, it)
		}
	}
	return out
}

// TestUpdateMissionAppliesOnlyFromTheHumansTurn: continue or waiting in a
// turn the human started applies at once; anything else asks the human.
func TestUpdateMissionAppliesOnlyFromTheHumansTurn(t *testing.T) {
	for _, c := range []struct {
		origin, state string
		direct        bool
	}{
		{originHuman, "continue", true},
		{originHuman, "waiting", true},
		{originHuman, "done", false},
		{originHuman, "failed", false},
		{originDen, "continue", false}, // a run Den judged on its own
		{originDen, "done", false},
		{originReport, "continue", false}, // a peer's report started the turn
		{originReport, "done", false},
	} {

		m, ms := missionUpdateModel(t, c.origin)
		d := updateMission(t, &m, c.state)
		items := missionItems(m)
		got := m.missions.Get(ms.ID)
		switch {
		case c.direct && (len(items) != 0 || !strings.Contains(d.Message, "applied")):
			t.Errorf("%s/%s: should apply at once: %+v, %d items", c.origin, c.state, d, len(items))
		case !c.direct && (len(items) != 1 || got.State != mission.Running || !strings.Contains(d.Message, "confirm")):
			t.Errorf("%s/%s: should ask the human: %+v, %d items, state %v", c.origin, c.state, d, len(items), got.State)
		}
	}
}

// TestUpdateMissionMixedTurnsAsk: the human's turn with a report or a
// turn of Den's in flight with it is not the human's alone.
func TestUpdateMissionMixedTurnsAsk(t *testing.T) {
	for _, origins := range [][]string{{originHuman, originReport}, {originHuman, originDen}, {originReport, originHuman}} {
		m, ms := missionUpdateModel(t, "")
		m.orch.origins = origins
		updateMission(t, &m, "continue")
		if len(missionItems(m)) != 1 || m.missions.Get(ms.ID).Runs[0].Summary == "all green" {
			t.Errorf("origins %v: should ask the human", origins)
		}
	}
}

// TestUpdateMissionAfterAnUnplacedBlock: once a refused prompt could not
// be placed, even the human's own turn asks.
func TestUpdateMissionAfterAnUnplacedBlock(t *testing.T) {
	m, _ := missionUpdateModel(t, originHuman)
	m.handleOrchEvent(orch.Event{Kind: orch.Blocked})
	updateMission(t, &m, "continue")
	if len(missionItems(m)) != 1 {
		t.Error("should ask the human")
	}
}

// TestUpdateMissionCardIsTheHumans: ^Y applies the update, ^N leaves the
// mission as it was.
func TestUpdateMissionCardIsTheHumans(t *testing.T) {
	m, ms := missionUpdateModel(t, originReport)
	updateMission(t, &m, "done")
	it := missionItems(m)[0]
	m.tab, m.inboxSel = tabInbox, it.ID
	nm, _ := m.answer("allow")
	m = nm.(Model)
	if got := m.missions.Get(ms.ID); got.State != mission.Done || it.State != inbox.Resolved {
		t.Errorf("^Y: state %v, item %v", got.State, it.State)
	}

	m, ms = missionUpdateModel(t, originReport)
	updateMission(t, &m, "failed")
	it = missionItems(m)[0]
	m.tab, m.inboxSel = tabInbox, it.ID
	nm, _ = m.answer("deny")
	m = nm.(Model)
	if got := m.missions.Get(ms.ID); got.State != mission.Running || it.State != inbox.Resolved {
		t.Errorf("^N: state %v, item %v", got.State, it.State)
	}
}

func TestUpdateMissionRefusals(t *testing.T) {
	m, _ := missionUpdateModel(t, "")
	if d := updateMission(t, &m, "continue"); d.Behavior != "deny" {
		t.Errorf("outside a turn: %+v", d)
	}
	m, _ = missionUpdateModel(t, originHuman)
	if d := updateMission(t, &m, "paused"); d.Behavior != "deny" {
		t.Errorf("a made-up state: %+v", d)
	}
	raw, _ := json.Marshal(bridge.MissionInput{ID: "nope", State: "done"})
	r := bridge.TestRequest(bridge.Envelope{Event: bridge.MissionEvent, Payload: raw})
	r.PeerPID = orchChildPID
	m.handleRequest(r)
	if d, _ := r.Decided(); d.Behavior != "deny" {
		t.Errorf("unknown mission: %+v", d)
	}
	// A host never reaches it.
	raw, _ = json.Marshal(bridge.MissionInput{ID: "m1", State: "continue"})
	r = bridge.TestRequest(bridge.Envelope{Event: bridge.MissionEvent, Payload: raw})
	r.Host = "build1"
	m.handleRequest(r)
	if d, _ := r.Decided(); d.Behavior != "deny" || len(missionItems(m)) != 0 {
		t.Errorf("from a host: %+v", d)
	}
}

// TestMissionUpdateTextIsNotApplied: a mission_update block in the
// orchestrator's text changes nothing and says so.
func TestMissionUpdateTextIsNotApplied(t *testing.T) {
	m, ms := missionUpdateModel(t, originHuman)
	m.handleOrchEvent(orch.Event{Kind: orch.Text, Text: "All green.\n\n```json\n{\"mission_update\": {\"id\": \"m1\", \"state\": \"done\", \"summary\": \"ok\"}}\n```"})
	if got := m.missions.Get(ms.ID); got.State != mission.Running {
		t.Errorf("a text block applied: %v", got.State)
	}
	last := m.orch.chat[len(m.orch.chat)-1].text
	if !strings.Contains(last, "did not apply the mission_update block") || strings.Contains(last, `"mission_update"`) {
		t.Errorf("chat: %q", last)
	}
}
