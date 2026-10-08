package colors

import (
	"path/filepath"
	"testing"
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
}
