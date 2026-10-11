package mission

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStoreRecordsAndRestoresLastState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missions.jsonl")
	s := New(path)
	now := time.Date(2026, 10, 11, 15, 0, 0, 0, time.UTC)
	m := &Mission{ID: "m1", Project: "/r", Name: "Tests green", Mission: "Keep tests green", Shape: Loop, State: Proposed, Created: now}
	s.Save(m)
	m.State = Running
	m.StartRun(now)
	s.Save(m)
	got := Load(path)
	if got.Get("m1") == nil || got.Get("m1").State != Running || len(got.Get("m1").Runs) != 1 {
		t.Fatalf("restored %+v", got.Get("m1"))
	}
	if got.Running() != 1 || len(got.ForProject("/r")) != 1 || len(got.ForProject("/x")) != 0 {
		t.Errorf("counts wrong")
	}
}

func TestParseProposalAndUpdate(t *testing.T) {
	text := "Here is my proposal.\n```json\n{\"mission\": {\"name\": \"Tests green\", \"mission\": \"Keep den's tests green.\", \"done_when\": \"standing\", \"constraints\": [\"no skipped tests\"], \"shape\": \"fan-out\", \"reason\": \"two checks\", \"agents\": [\"Fuzzy Sprite\"]}}\n```\nThat is all."
	p, ok := ParseProposal(text)
	if !ok || p.Shape != FanOut || len(p.Constraints) != 1 || p.Agents[0] != "Fuzzy Sprite" {
		t.Fatalf("proposal = %+v %v", p, ok)
	}
	if s := Strip(text); s != "Here is my proposal.\n\nThat is all." {
		t.Errorf("strip = %q", s)
	}
	u, ok := ParseUpdate("Done.\n```json\n{\"mission_update\": {\"id\": \"m1\", \"state\": \"done\", \"summary\": \"tag pushed\"}}\n```")
	if !ok || u.ID != "m1" || u.State != "done" {
		t.Errorf("update = %+v %v", u, ok)
	}
	if _, ok := ParseProposal("no json here"); ok {
		t.Error("parsed a proposal from plain text")
	}
	var m Mission
	m.Apply(Proposal{Mission: "Keep the build fast and the tests green always", Shape: "weird"})
	if m.Name != "Keep the build fast and the" || m.Shape != "weird" {
		t.Errorf("apply: name=%q shape=%q", m.Name, m.Shape)
	}
}

func TestNextCadence(t *testing.T) {
	from := time.Date(2026, 10, 11, 15, 4, 0, 0, time.UTC) // a Sunday
	cases := map[string]time.Time{
		"every 30m":        from.Add(30 * time.Minute),
		"every 2 hours":    from.Add(2 * time.Hour),
		"daily 08:30":      time.Date(2026, 10, 12, 8, 30, 0, 0, time.UTC),
		"daily 16:00":      time.Date(2026, 10, 11, 16, 0, 0, 0, time.UTC),
		"weekly mon 09:00": time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC),
		"*/15 * * * *":     time.Date(2026, 10, 11, 15, 15, 0, 0, time.UTC),
		"0 9 * * 1-5":      time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC),
	}
	for c, want := range cases {
		got, err := Next(c, from)
		if err != nil || !got.Equal(want) {
			t.Errorf("%q: got %v (%v), want %v", c, got, err, want)
		}
	}
	if _, err := Next("whenever", from); err == nil {
		t.Error("accepted a cadence it cannot run")
	}
}
