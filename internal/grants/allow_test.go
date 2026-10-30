package grants

import (
	"testing"

	"github.com/GlacierHubAB/den/internal/api/convcases"
)

// Every row of the answer table, against Allow and Listed.
func TestAnswerTable(t *testing.T) {
	for _, row := range convcases.AnswerCases {
		st := State{Powers: row.Powers, Locked: row.Locked, God: row.God, Confirmed: row.Confirmed, Kept: row.Kept, Sessions: map[string]Session{}}
		for class, level := range row.Lowered {
			st.Sessions[class] = Session{Level: level}
		}
		a := Answer{Kept: row.KeptID, Class: row.Class, Step: row.Step, Verb: row.Verb, GodTTY: row.GodTTY, SelfRaised: row.SelfRaised}
		if got := Allow(st, a); got != row.Code {
			t.Errorf("%s: Allow %q, want %q", row.Name, got, row.Code)
		}
		if got := Listed(st, a); got != row.Listed {
			t.Errorf("%s: Listed %v, want %v", row.Name, got, row.Listed)
		}
	}
}

// The read's kept: each held id with Den's label, active only with the
// powers, after the start confirm, not locked or held, and, for what God
// mode makes Den's alone, not in God mode.
func TestKeptInTheRead(t *testing.T) {
	st := State{Powers: true, Confirmed: true, Kept: []string{KeptClose, KeptMerge}}
	g := Read(st)
	if len(g.Kept) != 2 || g.Kept[0] != (Kept{ID: KeptClose, Label: "Close an agent", Active: true}) || !g.Kept[1].Active || !g.Enforced {
		t.Fatalf("kept %+v, enforced %v", g.Kept, g.Enforced)
	}
	st.God = true
	if g := Read(st); !g.Kept[0].Active || g.Kept[1].Active {
		t.Errorf("God mode: %+v", g.Kept)
	}
	for name, st := range map[string]State{
		"before the confirm": {Powers: true, Kept: []string{KeptClose}},
		"locked":             {Powers: true, Confirmed: true, Locked: true, Kept: []string{KeptClose}},
		"held":               {Powers: true, Confirmed: true, Held: true, Kept: []string{KeptClose}},
		"no powers":          {Confirmed: true, Kept: []string{KeptClose}},
	} {
		if g := Read(st); g.Kept[0].Active {
			t.Errorf("%s: active", name)
		}
	}
	if g := Read(State{Powers: true, Kept: []string{"admin"}}); len(g.Kept) != 0 {
		t.Errorf("an id outside the list was read: %+v", g.Kept)
	}
}
