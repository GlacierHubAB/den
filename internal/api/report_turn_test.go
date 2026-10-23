package api

import (
	"encoding/json"
	"strings"
	"testing"
)

// A console's turn records its pairing's lineage; an app pairing's turn
// records none.
func TestConsoleTurnKeepsItsLineage(t *testing.T) {
	h, c := provedConsole(t)
	if l := c.call("orch.say", map[string]string{"text": "who is busy?"}); !l.OK {
		t.Fatalf("say: %+v", l.Error)
	}
	y := ctlAct(t, h.s, func(a Act) bool { return a.Say != nil }).Say
	cur, _ := h.s.CurrentConsole()
	if y.Lineage == "" || y.Lineage != cur.Lineage {
		t.Errorf("the console's turn has lineage %q, the pairing %q", y.Lineage, cur.Lineage)
	}
	y.Done()

	h2 := newHarness(t, 2)
	app, _, _ := paired(t, h2)
	if l := app.call("orch.say", map[string]string{"text": "hi"}); !l.OK {
		t.Fatalf("app say: %+v", l.Error)
	}
	if y := ctlAct(t, h2.s, func(a Act) bool { return a.Say != nil }).Say; y.Lineage != "" {
		t.Errorf("an app's turn has lineage %q", y.Lineage)
	}
}

// A report or desktop entry carries the console turn its work came from
// only in a turn's own shape and only while that turn's pairing is the
// current one; an entry of any other origin never carries one but a
// console's own. History drops the turn once the pairing has changed.
func TestReportTurnOnlyForTheCurrentPairing(t *testing.T) {
	h, con := provedConsole(t)
	cur, _ := h.s.CurrentConsole()
	if !cur.Powers {
		h.s.ConsolePowersAnswered(cur.ID, true)
	}
	h.s.SetMirror(cur.ID, true)
	turn := "t-" + strings.Repeat("ab", 16)

	cases := []struct {
		name string
		in   Converse
		want string
	}{
		{"report, Den's line", Converse{Origin: OriginReport, Speaker: SpeakerDen, Agent: "Busy Otter", AgentID: "s1", Turn: turn, TurnLineage: cur.Lineage}, turn},
		{"report, the reply", Converse{Origin: OriginReport, Speaker: SpeakerOrchestrator, Agent: "Busy Otter", AgentID: "s1", Turn: turn, TurnLineage: cur.Lineage, Text: "Otter is done."}, turn},
		{"desktop", Converse{Origin: OriginDesktop, Speaker: SpeakerDen, Agent: "Desk Agent", AgentID: "s2", Turn: turn, TurnLineage: cur.Lineage}, turn},
		{"another pairing's turn", Converse{Origin: OriginReport, Speaker: SpeakerDen, Agent: "Busy Otter", AgentID: "s3", Turn: turn, TurnLineage: "f00"}, ""},
		{"no lineage", Converse{Origin: OriginReport, Speaker: SpeakerDen, Agent: "Busy Otter", AgentID: "s4", Turn: turn}, ""},
		{"not a turn's shape", Converse{Origin: OriginReport, Speaker: SpeakerDen, Agent: "Busy Otter", AgentID: "s5", Turn: "t-1", TurnLineage: cur.Lineage}, ""},
		{"upper-case hex", Converse{Origin: OriginReport, Speaker: SpeakerDen, Agent: "Busy Otter", AgentID: "s6", Turn: strings.ToUpper(turn), TurnLineage: cur.Lineage}, ""},
		{"origin you", Converse{Origin: OriginYou, Speaker: SpeakerYou, Turn: turn, TurnLineage: cur.Lineage, Text: "hi"}, ""},
		{"origin mission", Converse{Origin: OriginMission, Speaker: SpeakerDen, Turn: turn, TurnLineage: cur.Lineage, Text: "a run"}, ""},
	}
	for _, c := range cases {
		if c.in.Text == "" {
			c.in.Text = "Report from " + c.in.Agent + "."
		}
		if !h.s.Converse(c.in) {
			t.Fatalf("%s: not kept", c.name)
		}
		if e := nextConversation(t, con); e.Turn != c.want {
			t.Errorf("%s: turn %q, want %q", c.name, e.Turn, c.want)
		}
	}

	history := func() []ConvEntry {
		t.Helper()
		l := con.call("conversation.history", nil)
		var hist struct{ Entries []ConvEntry }
		if !l.OK || json.Unmarshal(l.Result, &hist) != nil {
			t.Fatalf("history %+v %s", l.Error, l.Result)
		}
		return hist.Entries
	}
	withTurn := 0
	for _, e := range history() {
		if e.Turn != "" {
			withTurn++
		}
	}
	if withTurn != 3 {
		t.Errorf("%d history entries carry a turn, want 3", withTurn)
	}
	// Another pairing now (as a re-pair makes one): the old turns stay out.
	h.s.con.mu.Lock()
	h.s.con.cur.Lineage = randHex()
	h.s.con.mu.Unlock()
	for _, e := range history() {
		if e.Turn != "" {
			t.Errorf("after the pairing changed, history carries turn %q: %+v", e.Turn, e)
		}
	}
}
