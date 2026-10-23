package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/api"
	"github.com/GlacierHubAB/den/internal/desktop"
	"github.com/GlacierHubAB/den/internal/orch"
)

var (
	turnA = "t-" + strings.Repeat("a", 32)
	turnB = "t-" + strings.Repeat("b", 32)
)

// A card made in a console's turn keeps that turn and its pairing's
// lineage; one made in an app pairing's turn, after the turn ended, or
// outside one keeps none.
func TestCardKeepsItsConsoleTurn(t *testing.T) {
	cases := []struct {
		name  string
		turn  *api.Say
		sent  bool
		ended bool
		want  string
	}{
		{"console turn", &api.Say{Turn: turnA, Console: "c-1", Lineage: "L1"}, true, false, turnA},
		{"not sent yet", &api.Say{Turn: turnA, Console: "c-1", Lineage: "L1"}, false, false, ""},
		{"ended", &api.Say{Turn: turnA, Console: "c-1", Lineage: "L1"}, true, true, ""},
		{"an app pairing's turn", &api.Say{Turn: turnA, Pairing: "p-1"}, true, false, ""},
		{"no turn", nil, false, false, ""},
	}
	for _, c := range cases {
		m := testModel(120, 40)
		m.ctl.turn, m.ctl.sent, m.ctl.ended = c.turn, c.sent, c.ended
		d := &dispatch{ID: "tu-1", SessionID: "s-otter", Agent: "Busy Otter", State: cardWorking}
		m.addChat(chatEntry{role: "card", card: d})
		if d.consoleTurn != c.want || (c.want != "" && d.turnLineage != "L1") || (c.want == "" && d.turnLineage != "") {
			t.Errorf("%s: turn %q lineage %q", c.name, d.consoleTurn, d.turnLineage)
		}
	}
}

// A report carries the turn of the newest card of its agent's that was
// made in a console turn of the current pairing and is live or ended
// within reportTurnFor; the orchestrator's reply to it carries the same.
// With none such, the report carries no turn, rather than an older one.
func TestReportCarriesItsCardsTurn(t *testing.T) {
	defer func(f func(*api.Server, api.Converse)) { mirrorSink = f }(mirrorSink)
	var got []api.Converse
	mirrorSink = func(_ *api.Server, c api.Converse) { got = append(got, c) }
	defer func(f func(Model) string) { currentLineage = f }(currentLineage)
	lineage := "L1"
	currentLineage = func(Model) string { return lineage }

	now := time.Now()
	card := func(turn, lin string, state cardState, ended time.Time) *dispatch {
		return &dispatch{SessionID: "s-otter", Agent: "Busy Otter", State: state, EndedAt: ended, consoleTurn: turn, turnLineage: lin}
	}
	cases := []struct {
		name  string
		cards []*dispatch
		want  string
	}{
		{"one live card", []*dispatch{card(turnA, "L1", cardWorking, time.Time{})}, turnA},
		{"the newer of two", []*dispatch{card(turnA, "L1", cardDone, now.Add(-time.Minute)), card(turnB, "L1", cardWorking, time.Time{})}, turnB},
		{"ended within the window", []*dispatch{card(turnA, "L1", cardDone, now.Add(-9*time.Minute))}, turnA},
		{"ended too long ago", []*dispatch{card(turnA, "L1", cardDone, now.Add(-11*time.Minute))}, ""},
		{"another pairing's", []*dispatch{card(turnA, "L0", cardWorking, time.Time{})}, ""},
		{"the newest of the current pairing", []*dispatch{card(turnA, "L1", cardWorking, time.Time{}), card(turnB, "L0", cardWorking, time.Time{})}, turnA},
		{"a card from Den's own chat", []*dispatch{card("", "", cardWorking, time.Time{})}, ""},
	}
	for _, c := range cases {
		m := testModel(120, 40)
		for _, d := range c.cards {
			m.orch.chat = append(m.orch.chat, chatEntry{role: "card", card: d})
		}
		got = nil
		m.mirrorReportTurn("Busy Otter", "s-otter")
		m.mirrorReply(originReport, "Otter is done.")
		if len(got) != 2 {
			t.Fatalf("%s: %d entries", c.name, len(got))
		}
		for _, e := range got {
			if e.Turn != c.want || (c.want != "" && e.TurnLineage != "L1") {
				t.Errorf("%s: %s entry turn %q lineage %q, want %q", c.name, e.Speaker, e.Turn, e.TurnLineage, c.want)
			}
		}
	}

	// A desktop agent's report: origin desktop, its mission's turn.
	dm := testModel(120, 40)
	dm.desk.missions = map[string]*deskMission{"d1": {SessionID: "s9", consoleTurn: turnA, turnLineage: "L1"}}
	got = nil
	dm.mirrorReportTurn("Quiet Panda", "s9")
	if len(got) != 1 || got[0].Origin != api.OriginDesktop || got[0].Turn != turnA {
		t.Errorf("the desktop report: %+v", got)
	}

	// No pairing now: no turn at all.
	lineage = ""
	m := testModel(120, 40)
	m.orch.chat = append(m.orch.chat, chatEntry{role: "card", card: card(turnA, "L1", cardWorking, time.Time{})})
	got = nil
	m.mirrorReportTurn("Busy Otter", "s-otter")
	if len(got) != 1 || got[0].Turn != "" {
		t.Errorf("with no pairing: %+v", got)
	}
}

// A desktop mission keeps the turn its request came in, not the one in
// flight when the human says ^Y: asked in turn A, A ends, B starts, ^Y,
// and the mission's turn is A. A request from anything but the
// orchestrator keeps none.
func TestDesktopMissionKeepsTheTurnItWasAskedIn(t *testing.T) {
	defer func(f func(*Model, int) bool) { fromOrchestrator = f }(fromOrchestrator)
	for _, orchestrator := range []bool{true, false} {
		fromOrchestrator = func(*Model, int) bool { return orchestrator }
		m, d, _, _, _ := taskRig(t)
		m.ctl.turn, m.ctl.sent, m.ctl.ended = &api.Say{Turn: turnA, Console: "c-1", Lineage: "L1"}, true, false
		r := taskReq("open a terminal and type hello from Den")
		r.PeerPID = 4242
		runBatch(m.handleRequest(r))
		m.ctl.ended = true // turn A ends
		m.ctl.turn, m.ctl.sent, m.ctl.ended = &api.Say{Turn: turnB, Console: "c-1", Lineage: "L1"}, true, false
		runTask(t, m, d, "allow")
		dm := taskLive(t, *m)
		want := ""
		if orchestrator {
			want = turnA
		}
		if dm.consoleTurn != want || (want != "" && dm.turnLineage != "L1") {
			t.Errorf("from the orchestrator %v: the mission's turn is %q (lineage %q), want %q", orchestrator, dm.consoleTurn, dm.turnLineage, want)
		}
	}
}

// A finished desktop mission's report goes out through finishDeskMission
// itself as a desktop report with its mission's turn: the mission is still
// Den's when the report goes. One that ended before its agent started has
// no session, and its report is a plain one with no agent id.
func TestFinishedDesktopMissionReportsAsDesktop(t *testing.T) {
	defer func(f func(*api.Server, api.Converse)) { mirrorSink = f }(mirrorSink)
	var got []api.Converse
	mirrorSink = func(_ *api.Server, c api.Converse) { got = append(got, c) }
	defer func(f func(Model) string) { currentLineage = f }(currentLineage)
	currentLineage = func(Model) string { return "L1" }

	m, _, _ := taskMission(t)
	if m.orch.proc == nil {
		m.orch.proc = &orch.Orchestrator{PID: 1} // the report turn's command is not run here
	}
	dm := m.deskMission("m1")
	dm.consoleTurn, dm.turnLineage = turnA, "L1"
	m.finishDeskMission(dm, "the agent finished")
	if len(got) == 0 || got[0].Origin != api.OriginDesktop || got[0].AgentID != "sess-task" || got[0].Turn != turnA {
		t.Fatalf("the finished mission's report: %+v", got)
	}

	got = nil
	m.addDeskMission(&deskMission{Mission: desktop.Mission{ID: "m2", Windows: map[string]bool{}, PIDs: map[int]bool{}}, Dir: t.TempDir(), consoleTurn: turnA, turnLineage: "L1"})
	m.finishDeskMission(m.deskMission("m2"), "the launch failed")
	if len(got) == 0 || got[0].Origin != api.OriginReport || got[0].AgentID != "" || got[0].Turn != "" {
		t.Errorf("a mission with no agent: %+v", got)
	}
}
