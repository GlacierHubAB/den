package colors

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/GlacierHubAB/den/internal/theme"
)

func TestAssignIsUniqueStickyAndOldestFirst(t *testing.T) {
	hues := []string{"#1", "#2", "#3", "#4"}
	agents := []Agent{{"s-a", 3}, {"s-b", 1}, {"s-c", 2}}
	got, changed := Assign(nil, agents, hues)
	if !changed || len(got) != 3 {
		t.Fatalf("first assignment: changed=%v got=%v", changed, got)
	}
	seen := map[string]bool{}
	for _, c := range got {
		if seen[c] {
			t.Errorf("duplicate colour in %v", got)
		}
		seen[c] = true
	}
	// Recorded colours survive even when they are not the seed's pick.
	prev := map[string]string{"s-a": "#4", "s-b": "#4", "gone": "#1"}
	got, _ = Assign(prev, agents, hues)
	if got["s-b"] != "#4" {
		t.Errorf("oldest session should keep its recorded colour: %v", got)
	}
	if got["s-a"] == "#4" {
		t.Errorf("younger session must yield the contested colour: %v", got)
	}
	if _, ok := got["gone"]; ok {
		t.Errorf("entries for absent sessions should be dropped")
	}
	again, changed := Assign(got, agents, hues)
	if changed {
		t.Errorf("stable input must not report a change: %v vs %v", got, again)
	}
}

// A record from before the agent colour rule (rose, mint, lilac, orchid)
// is replaced at the first assignment from the curated hues, each by a
// colour the rule takes, and the next assignment changes nothing: an
// agent is recoloured once.
func TestOldRecordsAreRecolouredOnce(t *testing.T) {
	agents := []Agent{{"s-a", 1}, {"s-b", 2}, {"s-c", 3}, {"s-d", 4}, {"s-e", 5}}
	prev := map[string]string{"s-a": "#ea9e9e", "s-b": "#66cb8e", "s-c": "#b9a3f0", "s-d": "#e39ad8", "s-e": "#86c6e8"}
	got, changed := Assign(prev, agents, theme.Hues)
	if !changed || got["s-e"] != "#86c6e8" {
		t.Fatalf("changed=%v got=%v: a passing record must stay", changed, got)
	}
	for sid, c := range got {
		if ok, why := theme.AgentColor(c); !ok {
			t.Errorf("%s kept %s, which the rule refuses (%s)", sid, c, why)
		}
	}
	if again, changed := Assign(got, agents, theme.Hues); changed {
		t.Errorf("a second assignment recoloured again: %v to %v", got, again)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "den", "colors.json")
	want := map[string]string{"s": "#abcdef"}
	if err := Save(p, want); err != nil {
		t.Fatal(err)
	}
	if got := Load(p); got["s"] != "#abcdef" {
		t.Errorf("got %v", got)
	}
	if got := Load(filepath.Join(t.TempDir(), "missing.json")); len(got) != 0 {
		t.Errorf("missing file should load empty, got %v", got)
	}
	// A record holding null loads as an empty map, not nil: the UI loads
	// the record once, while it has none.
	null := filepath.Join(t.TempDir(), "colors.json")
	if err := os.WriteFile(null, []byte("null\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Load(null); got == nil || len(got) != 0 {
		t.Errorf("a null record loads as %#v", got)
	}
}
