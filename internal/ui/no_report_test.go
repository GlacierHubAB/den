package ui

import (
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/api"
)

// A card from a console turn that ends done with no report from its agent
// gets Den's "no report" line once its grace is over: once, with the
// card's turn. A report within the grace, a card that failed since, one
// from Den's own chat or from another pairing gets none.
func TestNoReportAfterTheGrace(t *testing.T) {
	defer func(f func(*api.Server, api.Converse)) { mirrorSink = f }(mirrorSink)
	var got []api.Converse
	mirrorSink = func(_ *api.Server, c api.Converse) { got = append(got, c) }
	defer func(f func(Model) string) { currentLineage = f }(currentLineage)
	currentLineage = func(Model) string { return "L1" }

	now := time.Now()
	newCard := func(turn, lineage string) (*Model, *dispatch) {
		m := testModel(120, 40)
		d := &dispatch{SessionID: "s-otter", Agent: "Busy Otter", State: cardWorking, StartedAt: now, consoleTurn: turn, turnLineage: lineage}
		m.orch.chat = append(m.orch.chat, chatEntry{role: "card", card: d})
		m.finishCard(d, now)
		return &m, d
	}
	noReports := func() int {
		n := 0
		for _, c := range got {
			if c.Ending == api.EndingNoReport {
				n++
			}
		}
		return n
	}

	got = nil
	m, _ := newCard(turnA, "L1")
	m.missedReports(now.Add(noReportGrace - time.Second))
	if noReports() != 0 {
		t.Fatal("the line came before the grace was over")
	}
	m.missedReports(now.Add(noReportGrace + time.Second))
	m.missedReports(now.Add(2 * noReportGrace))
	if noReports() != 1 {
		t.Fatalf("%d lines, want 1: %+v", noReports(), got)
	}
	if c := got[len(got)-1]; c.Origin != api.OriginReport || c.Speaker != api.SpeakerDen || c.Agent != "Busy Otter" || c.Turn != turnA || c.TurnLineage != "L1" {
		t.Errorf("the line %+v", c)
	}

	cases := map[string]func(m *Model, d *dispatch){
		"a report within the grace": func(m *Model, d *dispatch) { m.mirrorReportTurn("Busy Otter", "s-otter") },
		"failed since":              func(m *Model, d *dispatch) { d.State = cardFailed },
	}
	for name, change := range cases {
		got = nil
		m, d := newCard(turnA, "L1")
		change(m, d)
		m.missedReports(now.Add(noReportGrace + time.Second))
		if noReports() != 0 {
			t.Errorf("%s: a no-report line", name)
		}
	}
	for name, c := range map[string][2]string{"Den's own chat": {"", ""}, "another pairing": {turnA, "L0"}} {
		got = nil
		m, _ := newCard(c[0], c[1])
		m.missedReports(now.Add(noReportGrace + time.Second))
		if noReports() != 0 {
			t.Errorf("%s: a no-report line", name)
		}
	}

	// A report before the card ended: no grace at all.
	got = nil
	m2 := testModel(120, 40)
	d := &dispatch{SessionID: "s-otter", Agent: "Busy Otter", State: cardWorking, StartedAt: now, consoleTurn: turnA, turnLineage: "L1"}
	m2.orch.chat = append(m2.orch.chat, chatEntry{role: "card", card: d})
	m2.mirrorReportTurn("Busy Otter", "s-otter")
	m2.finishCard(d, now)
	m2.missedReports(now.Add(noReportGrace + time.Second))
	if noReports() != 0 || !d.noReportDue.IsZero() {
		t.Error("a no-report line after the agent reported")
	}
}

// A desktop mission from a console turn whose agent finished with nothing
// to say: its desktop entry is Den's "no report" line. One that reported,
// or ended on a limit, has the plain report line.
func TestDesktopNoReport(t *testing.T) {
	defer func(f func(*api.Server, api.Converse)) { mirrorSink = f }(mirrorSink)
	var got []api.Converse
	mirrorSink = func(_ *api.Server, c api.Converse) { got = append(got, c) }
	defer func(f func(Model) string) { currentLineage = f }(currentLineage)
	currentLineage = func(Model) string { return "L1" }

	for _, c := range []struct {
		name, ended, report, turn, want string
	}{
		{"finished silent", deskAgentFinished, "", turnA, api.EndingNoReport},
		{"finished with a report", deskAgentFinished, "clicked it", turnA, ""},
		{"a limit", "the task's time ran out", "", turnA, ""},
		{"no console turn", deskAgentFinished, "", "", ""},
	} {
		m := testModel(120, 40)
		m.desk.missions = map[string]*deskMission{"d1": {SessionID: "s9", Ended: c.ended, report: c.report, consoleTurn: c.turn, turnLineage: "L1"}}
		got = nil
		m.mirrorReportTurn("Quiet Panda", "s9")
		if len(got) != 1 || got[0].Origin != api.OriginDesktop || got[0].Ending != c.want {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
}
