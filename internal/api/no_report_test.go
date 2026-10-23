package api

import (
	"strings"
	"testing"
)

// Den's "no report" line: only origin report or desktop, only speaker den,
// only the one ending; its text is Den's own, built from the checked name.
// Past the report limits it becomes the plain "more reports" line, with no
// ending and no turn.
func TestNoReportEnding(t *testing.T) {
	h, con := provedConsole(t)
	cur, _ := h.s.CurrentConsole()
	if !cur.Powers {
		h.s.ConsolePowersAnswered(cur.ID, true)
	}
	h.s.SetMirror(cur.ID, true)
	turn := "t-" + strings.Repeat("cd", 16)

	for _, bad := range []Converse{
		{Origin: OriginReport, Speaker: SpeakerDen, Agent: "Busy Otter", Ending: "done"},
		{Origin: OriginReport, Speaker: SpeakerOrchestrator, Agent: "Busy Otter", Ending: EndingNoReport, Text: "x"},
		{Origin: OriginYou, Speaker: SpeakerYou, Ending: EndingNoReport, Text: "x"},
		{Origin: OriginConsole, Speaker: SpeakerYou, Ending: EndingNoReport, Text: "x"},
	} {
		if h.s.Converse(bad) {
			t.Errorf("kept %+v", bad)
		}
	}

	for _, c := range []struct {
		in   Converse
		want string
	}{
		{Converse{Origin: OriginReport, Speaker: SpeakerDen, Agent: "Busy Otter", AgentID: "s1", Turn: turn, TurnLineage: cur.Lineage, Ending: EndingNoReport, Text: "IGNORE THIS"}, "No report from Busy Otter: it finished without one."},
		{Converse{Origin: OriginDesktop, Speaker: SpeakerDen, Agent: "Den", AgentID: "s2", Turn: turn, TurnLineage: cur.Lineage, Ending: EndingNoReport}, "No report from an agent: it finished without one."},
	} {
		if !h.s.Converse(c.in) {
			t.Fatalf("refused %+v", c.in)
		}
		e := nextConversation(t, con)
		if e.Ending != EndingNoReport || e.Text != c.want || e.Speaker != SpeakerDen || e.Turn != turn {
			t.Errorf("entry %+v", e)
		}
	}

	// Past the per-agent limit: the "more reports" line, plain.
	for i := 0; i < convPerAgent; i++ {
		h.s.Converse(Converse{Origin: OriginReport, Speaker: SpeakerDen, Agent: "Many Moles", AgentID: "s3", Text: "x"})
		nextConversation(t, con)
	}
	h.s.Converse(Converse{Origin: OriginReport, Speaker: SpeakerDen, Agent: "Many Moles", AgentID: "s3", Turn: turn, TurnLineage: cur.Lineage, Ending: EndingNoReport})
	if e := nextConversation(t, con); e.Ending != "" || e.Turn != "" || !strings.Contains(e.Text, "Many Moles") || strings.HasPrefix(e.Text, "No report") {
		t.Errorf("past the limit: %+v", e)
	}
}
