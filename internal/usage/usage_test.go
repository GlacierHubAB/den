package usage

import (
	"path/filepath"
	"testing"
	"time"
)

func pct(p float64) *float64 { return &p }

var now = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

func TestNewestTakesTheLatestFreshReading(t *testing.T) {
	reset := now.Add(3 * 24 * time.Hour).Format(time.RFC3339)
	samples := []Sample{
		{Percent: pct(40), Resets: reset, At: now.Add(-10 * time.Minute).UnixMilli()},
		{Percent: pct(63), Resets: reset, At: now.Add(-2 * time.Minute).UnixMilli()},
		{Percent: nil, At: now.UnixMilli()},                            // no weekly figure
		{Percent: pct(99), At: 0},                                      // no time
		{Percent: pct(150), At: now.Add(-3 * time.Minute).UnixMilli()}, // older than the 63
	}
	r, ok := Newest(samples, now)
	if !ok || r.Percent != 63 || r.Source != Statusline || r.Resets.IsZero() || r.Estimate() {
		t.Fatalf("newest = %+v %v", r, ok)
	}
	if got := Label(r, ok, State{}, now); got != "usage 63% · 2m ago" {
		t.Errorf("label %q", got)
	}
	// Past Stale: unknown.
	if _, ok := Newest([]Sample{{Percent: pct(50), At: now.Add(-16 * time.Minute).UnixMilli()}}, now); ok {
		t.Error("a 16-minute-old reading is not fresh")
	}
	if got := Label(Reading{}, false, State{}, now); got != "usage unknown" {
		t.Errorf("label %q", got)
	}
}

func TestFallbacksAreEstimates(t *testing.T) {
	var s State
	s.LastReset = now.Add(48 * time.Hour)
	s.Set(87, now)
	if !s.SetUntil.Equal(s.LastReset) {
		t.Errorf("a set figure holds until the next reset: %v", s.SetUntil)
	}
	r, ok := s.Current(Reading{}, false, 0, 0, now)
	if !ok || r.Percent != 87 || r.Source != Set || !r.Estimate() {
		t.Fatalf("set: %+v", r)
	}
	if got := Label(r, ok, s, now); got != "usage ~87% (estimate, set by you)" {
		t.Errorf("label %q", got)
	}
	// After the reset it no longer holds; the budget estimate stands in.
	later := now.Add(49 * time.Hour)
	r, ok = s.Current(Reading{}, false, 30, 100, later)
	if !ok || r.Percent != 30 || r.Source != Budget {
		t.Errorf("budget: %+v %v", r, ok)
	}
	if _, ok := s.Current(Reading{}, false, 30, 0, later); ok {
		t.Error("no budget, no figure: unknown")
	}
	// A fresh reading wins over both.
	fresh := Reading{Percent: 12, Source: Statusline, At: now, Resets: now.Add(96 * time.Hour)}
	if r, _ := s.Current(fresh, true, 30, 100, later); r.Source != Statusline || !s.LastReset.Equal(fresh.Resets) {
		t.Errorf("fresh: %+v, last reset %v", r, s.LastReset)
	}
}

func TestWarningsOncePerReset(t *testing.T) {
	var s State
	week1 := now.Add(24 * time.Hour)
	r := Reading{Percent: 85, Resets: week1, Source: Statusline}
	if got := s.Due(r, now); len(got) != 1 || got[0] != 80 {
		t.Fatalf("85%%: %v", got)
	}
	if got := s.Due(r, now); len(got) != 0 {
		t.Errorf("warned twice: %v", got)
	}
	r.Percent = 93
	if got := s.Due(r, now); len(got) != 1 || got[0] != 90 {
		t.Errorf("93%%: %v", got)
	}
	// A new week starts them over.
	r = Reading{Percent: 95, Resets: week1.Add(7 * 24 * time.Hour), Source: Statusline}
	if got := s.Due(r, now.Add(25*time.Hour)); len(got) != 2 {
		t.Errorf("new week: %v", got)
	}
}

func TestStopOnlyWhenSetAndNotOnTheBudget(t *testing.T) {
	r := Reading{Percent: 97, Source: Statusline}
	if ShouldStop(r, 0) {
		t.Error("stop_percent off (the default) never stops")
	}
	if !ShouldStop(r, 95) || ShouldStop(Reading{Percent: 94, Source: Statusline}, 95) {
		t.Error("the threshold")
	}
	if !ShouldStop(Reading{Percent: 96, Source: Set}, 95) {
		t.Error("the human's figure counts")
	}
	if ShouldStop(Reading{Percent: 100, Source: Budget}, 95) {
		t.Error("Den's cost estimate never stops the fleet")
	}
}

func TestStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "den", "usage.json")
	s := State{Stopped: true, StopWhy: "usage 96%", StoppedAt: now}
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", filepath.Dir(filepath.Dir(path)))
	if stopped, why := Stopped(); !stopped || why != "usage 96%" {
		t.Errorf("Stopped() = %v %q", stopped, why)
	}
	if got := Label(Reading{}, false, Load(path), now); got != "usage stop" {
		t.Errorf("label %q", got)
	}
}
