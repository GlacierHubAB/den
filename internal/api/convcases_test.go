package api

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/GlacierHubAB/den/internal/api/convcases"
)

// Den sends the follow-up table's entries exactly, and every case's
// reading follows Den's own rules: a turn only in its shape on origin
// report or desktop, an ending only from the closed set on speaker den.
func TestFollowupTable(t *testing.T) {
	h, con := provedConsole(t)
	cur, _ := h.s.CurrentConsole()
	if !cur.Powers {
		h.s.ConsolePowersAnswered(cur.ID, true)
	}
	h.s.SetMirror(cur.ID, true)
	report := func(origin, speaker, agent, id, turn, ending, text string) Converse {
		return Converse{Origin: origin, Speaker: speaker, Agent: agent, AgentID: id, Turn: turn, TurnLineage: cur.Lineage, Ending: ending, Text: text}
	}
	sends := map[string]Converse{
		"a report from the console's turn":                    report(OriginReport, SpeakerDen, "Mossy Lemur", "s1", convcases.OwnTurn, "", "x"),
		"the orchestrator's reply to it":                      report(OriginReport, SpeakerOrchestrator, "Mossy Lemur", "s1", convcases.OwnTurn, "", "Mossy Lemur fixed the login test."),
		"a report from Den's own chat":                        report(OriginReport, SpeakerDen, "Mossy Lemur", "s2", "", "", "x"),
		"finished without a report":                           report(OriginReport, SpeakerDen, "Mossy Lemur", "s3", convcases.OwnTurn, EndingNoReport, ""),
		"finished without a report, a name Den will not show": report(OriginReport, SpeakerDen, "Den", "s4", convcases.OwnTurn, EndingNoReport, ""),
		"a desktop report from the console's turn":            report(OriginDesktop, SpeakerDen, "Quiet Panda", "s5", convcases.OwnTurn, "", "x"),
		"a desktop agent finished without a report":           report(OriginDesktop, SpeakerDen, "Quiet Panda", "s6", convcases.OwnTurn, EndingNoReport, ""),
		"a report about another console's turn":               report(OriginReport, SpeakerDen, "Mossy Lemur", "s7", convcases.OtherTurn, "", "x"),
	}
	sent := 0
	for _, c := range convcases.FollowupCases {
		var want map[string]any
		if err := json.Unmarshal([]byte(c.Entry), &want); err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		// The reading follows Den's rules.
		origin, _ := want["origin"].(string)
		speaker, _ := want["speaker"].(string)
		turn, _ := want["turn"].(string)
		ending, _ := want["ending"].(string)
		reportish := origin == OriginReport || origin == OriginDesktop
		wantTurn := ""
		if reportish && turnShape.MatchString(turn) {
			wantTurn = turn
		}
		wantEnding := ""
		if reportish && speaker == SpeakerDen && ending == EndingNoReport {
			wantEnding = ending
		}
		if c.Turn != wantTurn || c.Ending != wantEnding || c.Own != (wantTurn == convcases.OwnTurn) {
			t.Errorf("%s: the table reads turn %q own %v ending %q; Den's rules give %q, %q", c.Name, c.Turn, c.Own, c.Ending, wantTurn, wantEnding)
		}
		in, ok := sends[c.Name]
		if !ok {
			continue // a console's reading only: Den never sends it
		}
		sent++
		if !h.s.Converse(in) {
			t.Fatalf("%s: refused", c.Name)
		}
		e := nextConversation(t, con)
		raw, _ := json.Marshal(e)
		var got map[string]any
		json.Unmarshal(raw, &got)
		delete(got, "id")
		delete(got, "at")
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: Den sends\n%s\nthe table says\n%s", c.Name, raw, c.Entry)
		}
	}
	if sent != len(sends) {
		t.Errorf("%d of %d sends matched a case", sent, len(sends))
	}
}
