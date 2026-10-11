package roster

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/cost"
)

var t0 = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

func lemur() Agent {
	return Agent{SessionID: "s1", Peer: "demo-app-1a2b", Critter: "Mossy Lemur", Role: "backend developer", Model: "claude-opus-5-5", CWD: "/home/u/code/demo-app", Parent: "orchestrator", Mission: "m1", Spawned: true}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "den", "roster.json")
	r := &Roster{}
	r.Observe([]Agent{lemur()}, t0, t0)
	r.Fork("s1", "s9")
	if err := r.Save(path, t0); err != nil {
		t.Fatal(err)
	}
	got := Load(path)
	a, ok := got.Get("s1")
	if !ok || a.Critter != "Mossy Lemur" || a.Mission != "m1" || !a.Spawned || !a.LastSeen.Equal(t0) {
		t.Fatalf("entry not round-tripped: %+v", a)
	}
	if got.Forks["s1"] != "s9" || !got.Saved.Equal(t0) {
		t.Fatalf("forks or saved lost: %+v", got)
	}
	// No temporary files left beside it.
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("want only roster.json, got %d files", len(entries))
	}
}

func TestLoadMissingIsEmpty(t *testing.T) {
	if r := Load(filepath.Join(t.TempDir(), "nope.json")); len(r.Agents) != 0 {
		t.Fatal("missing file should be an empty roster")
	}
}

func TestGoneAfterRestart(t *testing.T) {
	// Den saw the lemur live, saved, and the machine went down.
	r := &Roster{}
	r.Observe([]Agent{lemur()}, t0, t0)
	path := filepath.Join(t.TempDir(), "roster.json")
	r.Save(path, t0)

	// A new Den, an hour later: the lemur is not running.
	r = Load(path)
	gone := r.Gone(func(string) bool { return false })
	if len(gone) != 1 || gone[0].SessionID != "s1" {
		t.Fatalf("want the lemur offered, got %+v", gone)
	}
	// The new Den's first sighting must not mark it as ended on its own.
	start := t0.Add(time.Hour)
	r.Observe(nil, start, start)
	if a, _ := r.Get("s1"); !a.Ended.IsZero() {
		t.Fatal("an agent gone before this Den started is not ended while watched")
	}
	// Still live: nothing to offer.
	if gone := Load(path).Gone(func(string) bool { return true }); len(gone) != 0 {
		t.Fatalf("a live agent is not offered: %+v", gone)
	}
}

func TestEndedWhileWatchedIsNotOffered(t *testing.T) {
	r := &Roster{}
	r.Observe([]Agent{lemur()}, t0, t0)
	if !r.Observe(nil, t0.Add(time.Minute), t0) {
		t.Fatal("an agent going away is a change")
	}
	a, _ := r.Get("s1")
	if !a.Ended.Equal(t0.Add(time.Minute)) {
		t.Fatalf("ended not recorded: %+v", a)
	}
	// Den kept running for an hour after, then went down.
	r.Saved = t0.Add(time.Hour)
	if gone := r.Gone(func(string) bool { return false }); len(gone) != 0 {
		t.Fatalf("an agent that ended on its own is not offered: %+v", gone)
	}
	// But one that ended in the same burst as Den's last save is.
	r.Saved = t0.Add(time.Minute + 30*time.Second)
	if gone := r.Gone(func(string) bool { return false }); len(gone) != 1 {
		t.Fatalf("an agent that went down with Den is offered: %+v", gone)
	}
}

func TestClosedAndDismissedAreNotOffered(t *testing.T) {
	r := &Roster{}
	b := lemur()
	b.SessionID, b.Critter = "s2", "Snug Capybara"
	r.Observe([]Agent{lemur(), b}, t0, t0)
	r.MarkClosed("s1", t0)
	r.Dismiss("s2", t0)
	if gone := r.Gone(func(string) bool { return false }); len(gone) != 0 {
		t.Fatalf("closed and dismissed agents are not offered: %+v", gone)
	}
	// Seen live again (reopened), they are ordinary entries once more.
	r.Observe([]Agent{lemur()}, t0.Add(time.Minute), t0.Add(time.Minute))
	if a, _ := r.Get("s1"); !a.Closed.IsZero() {
		t.Fatal("a live agent is not closed")
	}
}

func TestObserveChangeOnlyOnSubstance(t *testing.T) {
	r := &Roster{}
	if !r.Observe([]Agent{lemur()}, t0, t0) {
		t.Fatal("a new agent is a change")
	}
	if r.Observe([]Agent{lemur()}, t0.Add(5*time.Second), t0) {
		t.Fatal("a later sighting alone is not a change")
	}
	if a, _ := r.Get("s1"); !a.LastSeen.Equal(t0.Add(5 * time.Second)) {
		t.Fatal("the sighting time still moves")
	}
	moved := lemur()
	moved.Parent = "s7"
	moved.Mission = ""
	if !r.Observe([]Agent{moved}, t0.Add(10*time.Second), t0) {
		t.Fatal("a new parent is a change")
	}
	if a, _ := r.Get("s1"); a.Mission != "m1" {
		t.Fatal("a known mission is kept when the sighting has none")
	}
}

func TestObserveDropsOldEntries(t *testing.T) {
	r := &Roster{}
	r.Observe([]Agent{lemur()}, t0, t0)
	r.Fork("s1", "s9")
	r.Observe(nil, t0.Add(Keep+time.Hour), t0.Add(Keep+time.Hour))
	if len(r.Agents) != 0 || len(r.Forks) != 0 {
		t.Fatalf("entries past Keep go, with their forks: %+v", r)
	}
}

func TestFind(t *testing.T) {
	r := &Roster{}
	older := lemur()
	r.Observe([]Agent{older}, t0, t0)
	newer := lemur()
	newer.SessionID = "s3"
	r.Observe([]Agent{newer}, t0.Add(time.Hour), t0)
	a, ok := r.Find("mossy lemur")
	if !ok || a.SessionID != "s3" {
		t.Fatalf("want the newest Mossy Lemur, got %+v", a)
	}
	if a, ok := r.Find("@demo-app-1a2b"); !ok || a.SessionID != "s3" {
		t.Fatalf("peer names work too: %+v", a)
	}
	if _, ok := r.Find("Nobody"); ok {
		t.Fatal("unknown name")
	}
}

func TestResumeEstimate(t *testing.T) {
	// Claude Code's own SessionStart figure for this case was 0.3088.
	usd, ok := ResumeEstimate(cost.NewTable(nil), "claude-sonnet-5-5", 123510)
	if !ok || math.Abs(usd-0.3088) > 0.0005 {
		t.Fatalf("sonnet 5.5: got %v %v", usd, ok)
	}
	if usd, _ := ResumeEstimate(cost.NewTable(nil), "claude-opus-5-5", 1_000_000); usd != 5 {
		t.Fatalf("opus 5.5: got %v", usd)
	}
	if _, ok := ResumeEstimate(cost.NewTable(nil), "some-other-model", 1000); ok {
		t.Fatal("unknown model has no estimate")
	}
	if _, ok := ResumeEstimate(cost.NewTable(nil), "claude-haiku-4-5", 0); ok {
		t.Fatal("no tokens, no estimate")
	}
}
