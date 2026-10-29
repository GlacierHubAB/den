package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/api"
)

// loweredNotesIn are Den's chat notes about a console's lowerings.
func loweredNotesIn(m Model) []string {
	var out []string
	for _, e := range m.orch.chat {
		if e.role == "note" && (strings.Contains(e.text, " lowered ") || strings.HasPrefix(e.text, "More lowerings")) {
			out = append(out, e.text)
		}
	}
	return out
}

// A console's lowering is one note in Den's chat; a lowering of the same
// class within 10 s rewrites it, another class gets its own, and past 20
// notes a minute one note says more came.
func TestLoweredNotes(t *testing.T) {
	m := testModel(140, 40)
	t0 := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	low := func(class, label string, level int, at time.Time) {
		m.noteLowered(&api.GrantsLowered{Class: class, Label: label, Level: level, Console: "Drey"}, at)
	}
	low("code", "Run commands", 0, t0)
	if n := loweredNotesIn(m); len(n) != 1 || n[0] != "Drey lowered Run commands to 0 of 5 for this Den session." {
		t.Fatalf("notes %q", n)
	}
	low("routine", "Everyday answers", 3, t0.Add(time.Second))
	low("routine", "Everyday answers", 2, t0.Add(5*time.Second))
	n := loweredNotesIn(m)
	if len(n) != 2 || n[1] != "Drey lowered Everyday answers to 2 of 5 for this Den session." {
		t.Fatalf("folded notes %q", n)
	}
	low("routine", "Everyday answers", 1, t0.Add(20*time.Second))
	if n := loweredNotesIn(m); len(n) != 3 {
		t.Fatalf("after the fold window: %q", n)
	}
	classes := []string{"routine", "destructive", "code", "desktop", "outward"}
	// A new minute: 25 notes in 52.5 s, each class every 10.5 s, past the
	// fold, so 20 are written and one says more came.
	for i := 0; i < 25; i++ {
		low(classes[i%5], "a class", 0, t0.Add(2*time.Minute+time.Duration(i)*2100*time.Millisecond))
	}
	n = loweredNotesIn(m)
	more := 0
	for _, s := range n {
		if strings.HasPrefix(s, "More lowerings") {
			more++
		}
	}
	if more != 1 || len(n) != 3+20+1 {
		t.Errorf("%d 'more came' notes in %d", more, len(n))
	}
}
