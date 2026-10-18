package names

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Names are numbered bare, 2, 3…; a session keeps its name; a name is
// free again only once its session holds it no longer; a peer handle, a
// live alias or a reserved word is never taken.
func TestAssignNumbersAndKeeps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "names.json")
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	held := map[string]bool{"s1": true, "s2": true, "s3": true, "s4": true, "s5": true}
	ns := Namespace{
		Taken: map[string]bool{"technical-writer": true, "pebble-quokka": true}, // a peer handle; an alias
		Held:  func(s string) bool { return held[s] },
	}
	assign := func(sid, role, title string) Entry {
		t.Helper()
		e, err := Assign(path, sid, role, title, "Critter "+sid, ns, now)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	if e := assign("s1", "backend engineer", ""); e.Name() != "backend-engineer" || e.Alias != "Critter s1" {
		t.Fatalf("first: %+v", e)
	}
	if e := assign("s2", "Backend Engineer 2", ""); e.Name() != "backend-engineer2" {
		t.Fatalf("second, asking for a number itself: %+v", e)
	}
	if e := assign("s1", "something else", ""); e.Name() != "backend-engineer" {
		t.Fatalf("a session keeps its name: %+v", e)
	}
	if e := assign("s3", "technical writer", ""); e.Name() != "technical-writer2" {
		t.Fatalf("a peer handle is taken: %+v", e)
	}
	if e := assign("s4", "coding agent", ""); e.Name() != "" || e.Role != "coding agent" {
		t.Fatalf("a generic role with no title keeps the critter name: %+v", e)
	}
	if e := assign("s5", "coding agent", "Den"); e.Name() != "" {
		t.Fatalf("a reserved title: %+v", e)
	}
	// s1 leaves the roster and the restore list: its name is free again,
	// and its entry goes.
	held["s1"] = false
	held["s6"] = true
	if e := assign("s6", "backend engineer", ""); e.Name() != "backend-engineer" {
		t.Fatalf("a freed name: %+v", e)
	}
	got := Load(path)
	if _, ok := got["s1"]; ok || got["s2"].Name() != "backend-engineer2" || len(got) != 5 {
		t.Fatalf("registry %+v", got)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", fi.Mode(), err)
	}
}
