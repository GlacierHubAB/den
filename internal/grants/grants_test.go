package grants

import (
	"strings"
	"testing"
)

// The table: the closed class order, five steps each at levels 1 to 5,
// names of 2 to 4 words and one-line words, steps only from level 1 up
// with no gap, and every default on an available step or 0.
func TestTable(t *testing.T) {
	g := Read(State{Powers: true})
	var order []string
	for _, c := range g.Classes {
		order = append(order, c.Class)
		if len(c.Steps) != Steps {
			t.Fatalf("%s: %d steps", c.Class, len(c.Steps))
		}
		gap := false
		for i, s := range c.Steps {
			if s.Level != i+1 {
				t.Errorf("%s: step %d has level %d", c.Class, i, s.Level)
			}
			if !s.Available {
				gap = true
				if s.Name != "" || s.Label != NoStep {
					t.Errorf("%s %d: an unavailable step reads %q %q", c.Class, s.Level, s.Name, s.Label)
				}
				continue
			}
			if gap {
				t.Errorf("%s %d: a step after a gap", c.Class, s.Level)
			}
			if n := len(strings.Fields(s.Name)); n < 2 || n > 4 {
				t.Errorf("%s %d: name %q is %d words", c.Class, s.Level, s.Name, n)
			}
			if strings.ContainsAny(s.Name+s.Label, "\n\r") {
				t.Errorf("%s %d: words span lines", c.Class, s.Level)
			}
		}
		if c.Default < 0 || c.Default > Steps || c.Default > 0 && !c.Steps[c.Default-1].Available {
			t.Errorf("%s: default %d is not an available step", c.Class, c.Default)
		}
		if c.Label == "" || c.About == "" || strings.ContainsAny(c.Label+c.About, "\n\r") {
			t.Errorf("%s: label %q about %q", c.Class, c.Label, c.About)
		}
	}
	if strings.Join(order, ",") != "routine,destructive,code,desktop,outward" {
		t.Errorf("classes %v", order)
	}
	if len(g.DenOnly) != 9 || len(g.Kept) != 0 || !g.Enforced {
		t.Errorf("den_only %d, kept %v, enforced %v", len(g.DenOnly), g.Kept, g.Enforced)
	}
}

// Lower takes only a level strictly below the class's cap, the lower of
// its Den level and its session, and reads it back as the session and the
// effective level.
func TestLower(t *testing.T) {
	st := State{Powers: true}
	for _, c := range []struct {
		class string
		level int
		want  string
	}{
		{Routine, 2, ""}, {Routine, 4, NotLower}, {Routine, 5, NotLower}, {Code, 0, ""}, {Code, 1, NotLower},
		{Desktop, 0, NotLower}, {"admin", 0, BadRequest}, {Routine, 6, BadRequest}, {Routine, -1, BadRequest},
	} {
		if _, got := Lower(st, c.class, c.level, "t", "Drey"); got != c.want {
			t.Errorf("%s to %d: %q, want %q", c.class, c.level, got, c.want)
		}
	}
	s, _ := Lower(st, Routine, 2, "2026-10-29T12:00:00Z", "Drey")
	st.Sessions = map[string]Session{Routine: s}
	if _, got := Lower(st, Routine, 3, "t", "Drey"); got != NotLower {
		t.Errorf("above the session: %q", got)
	}
	for _, c := range Read(st).Classes {
		if c.Class == Routine && (c.Level != 2 || c.DenLevel != 4 || c.Session == nil || *c.Session != s) {
			t.Errorf("routine read %+v", c)
		}
	}
	st.Locked = true
	for _, c := range Read(st).Classes {
		if c.Class == Routine && (c.Level != 0 || c.Session == nil || c.Session.Level != 2) {
			t.Errorf("routine read while locked %+v", c)
		}
	}
}

// levels is each class's effective level, by class.
func levels(g Grants) map[string]int {
	out := map[string]int{}
	for _, c := range g.Classes {
		out[c.Class] = c.Level
		if c.DenLevel != c.Default || c.Session != nil {
			panic("a level was set")
		}
	}
	return out
}

// The effective level: the default with powers; 0 everywhere without
// powers, while locked or held; God mode puts desktop and outward at 0.
// start_confirmed is the start confirm, never the powers.
func TestEffectiveLevels(t *testing.T) {
	defaults := map[string]int{Routine: 4, Destructive: 1, Code: 1, Desktop: 0, Outward: 0}
	zero := map[string]int{Routine: 0, Destructive: 0, Code: 0, Desktop: 0, Outward: 0}
	for _, c := range []struct {
		name string
		st   State
		want map[string]int
	}{
		{"powered", State{Powers: true}, defaults},
		{"no powers", State{}, zero},
		{"locked", State{Powers: true, Locked: true}, zero},
		{"held", State{Powers: true, Confirmed: true, Held: true}, zero},
		{"God mode", State{Powers: true, God: true}, map[string]int{Routine: 4, Destructive: 1, Code: 1, Desktop: 0, Outward: 0}},
	} {
		g := Read(c.st)
		got := levels(g)
		for k, v := range c.want {
			if got[k] != v {
				t.Errorf("%s: %s at %d, want %d", c.name, k, got[k], v)
			}
		}
		if g.StartConfirmed != c.st.Confirmed || g.Powers != c.st.Powers || g.Locked != c.st.Locked || g.God != c.st.God {
			t.Errorf("%s: flags %+v", c.name, g)
		}
	}
}
