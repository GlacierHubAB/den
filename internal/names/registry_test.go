package names

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// sid is a session id for tests.
func sid(n int) string { return fmt.Sprintf("0000000%d-1111-4222-8333-444444444444", n) }

// Names are numbered bare, 2, 3 and so on; a session keeps its name; a
// name is free again only once its session holds it no longer; a peer
// handle, a live alias or a reserved word is never taken.
func TestAssignNumbersAndKeeps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "names.json")
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	held := map[string]bool{sid(1): true, sid(2): true, sid(3): true, sid(4): true, sid(5): true}
	ns := Namespace{
		Taken: map[string]bool{"technical-writer": true, "pebble-quokka": true}, // a peer handle; an alias
		Held:  func(s string) bool { return held[s] },
	}
	l := OpenLive(path)
	assign := func(n int, role, title string) Entry {
		t.Helper()
		e, err := l.Assign(sid(n), role, title, fmt.Sprintf("Critter %d", n), ns, now)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	if e := assign(1, "backend engineer", ""); e.Name() != "backend-engineer" || e.Alias != "Critter 1" {
		t.Fatalf("first: %+v", e)
	}
	if e := assign(2, "Backend Engineer 2", ""); e.Name() != "backend-engineer2" {
		t.Fatalf("second, asking for a number itself: %+v", e)
	}
	if e := assign(1, "something else", ""); e.Name() != "backend-engineer" {
		t.Fatalf("a session keeps its name: %+v", e)
	}
	if e := assign(3, "technical writer", ""); e.Name() != "technical-writer2" {
		t.Fatalf("a peer handle is taken: %+v", e)
	}
	if e := assign(4, "coding agent", ""); e.Name() != "" || e.Role != "coding agent" {
		t.Fatalf("a generic role with no title keeps the critter name: %+v", e)
	}
	if e := assign(5, "coding agent", "Den"); e.Name() != "" {
		t.Fatalf("a reserved title: %+v", e)
	}
	held[sid(1)], held[sid(6)] = false, true
	if e := assign(6, "backend engineer", ""); e.Name() != "backend-engineer" {
		t.Fatalf("a freed name: %+v", e)
	}
	got := Load(path)
	if _, ok := got[sid(1)]; ok || got[sid(2)].Name() != "backend-engineer2" || len(got) != 5 {
		t.Fatalf("registry %+v", got)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", fi.Mode(), err)
	}
	if _, err := l.Assign("not-a-session", "reviewer", "", "", ns, now); err == nil {
		t.Error("an id that is not a session's was named")
	}
}

// A registry file edited behind Den's back gives no name Den would not
// have: an entry with a reserved, unnormalised or numberless name, a name
// two entries share, or an id that is not a session's, is dropped; and a
// Den that holds the registry never rereads it.
func TestRegistryCheckedOnLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "names.json")
	write := func(es []Entry) {
		raw, _ := json.Marshal(registry{Entries: es})
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write([]Entry{
		{Session: sid(1), Base: "reviewer", N: 1},
		{Session: sid(2), Base: "orchestrator", N: 1},
		{Session: sid(3), Base: "Security Reviewer", N: 1},
		{Session: sid(4), Base: "builder", N: 0},
		{Session: sid(5), Base: "builder", N: 1000},
		{Session: sid(6), Base: "twin", N: 1},
		{Session: sid(7), Base: "twin", N: 1},
		{Session: "s-short", Base: "short", N: 1},
		{Session: sid(8), Alias: "Mossy Lemur", Role: "coding agent"},
	})
	got := Load(path)
	if len(got) != 2 || got[sid(1)].Name() != "reviewer" || got[sid(8)].Name() != "" {
		t.Fatalf("checked registry: %+v", got)
	}
	l := OpenLive(path)
	write([]Entry{{Session: sid(1), Base: "impostor", N: 1}})
	if e := l.All()[sid(1)]; e.Name() != "reviewer" {
		t.Fatalf("a name changed with the file: %+v", e)
	}
	// A link or a huge file is not read.
	big := filepath.Join(t.TempDir(), "big.json")
	os.WriteFile(big, make([]byte, maxFile+10), 0o600)
	if len(Load(big)) != 0 {
		t.Error("a huge file was read")
	}
	link := filepath.Join(t.TempDir(), "link.json")
	os.Symlink(path, link)
	if len(Load(link)) != 0 {
		t.Error("a link was followed")
	}
}
