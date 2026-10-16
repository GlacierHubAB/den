package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/metrics"
	"github.com/GlacierHubAB/den/internal/mission"
)

// A live mission's figures are recorded once a local day and an ended
// mission's once, never one that ended long ago; they ride with the next
// turn Den sends the orchestrator, once, and Den starts no turn for them.
func TestMissionMetrics(t *testing.T) {
	m, ms := costModel(t)
	path := filepath.Join(t.TempDir(), "den", "metrics.jsonl")
	old := metricsPath
	metricsPath = func() string { return path }
	t.Cleanup(func() { metricsPath = old })
	m.settleCosts()
	day := time.Date(2026, 10, 16, 12, 0, 0, 0, time.Local)
	// A pair on one mission counts once for it; a session on none, never.
	m.countProcess(func(s *mission.Stats) { s.Overlaps++ }, "mossy-lemur", "snug-capybara", "nobody")
	if got := m.missions.Get(ms.ID).Stats; got.Overlaps != 1 {
		t.Fatalf("stats %+v", got)
	}
	ms = m.missions.Get(ms.ID)
	ms.Stats.Nudges = 2
	ms.AddMerge(mission.Merge{Tip: "a", At: day, Since: day.Add(-4 * time.Hour)})
	ms.AddMerge(mission.Merge{Tip: "b", At: day, Since: day.Add(-10 * time.Hour)})
	m.missions.Save(ms)
	gone := &mission.Mission{ID: "m-old", Name: "old", State: mission.Done, Created: day.AddDate(0, 0, -5), Updated: day.AddDate(0, 0, -3)}
	m.missions.Save(gone)

	m.settleMetrics(day)
	m.settleMetrics(day.Add(time.Hour))
	if metrics.LastDay(path) != "2026-10-16" || len(m.metrics.unsent) != 1 {
		t.Fatalf("day one: %q %+v", metrics.LastDay(path), m.metrics.unsent)
	}
	got := m.metrics.unsent[0]
	if got.Kind != metrics.Daily || got.Mission != ms.ID || got.Merged != 2 || got.BranchHoursMedian != 7 || got.BranchHoursMax != 10 || got.Overlaps != 1 || got.Nudges != 2 || got.Cost == "" || got.PerMerge == "" {
		t.Errorf("daily %+v", got)
	}
	// It ends: recorded once, then never again.
	ms.State, ms.Updated = mission.Done, day.Add(2*time.Hour)
	m.missions.Save(ms)
	m.settleMetrics(day.Add(3 * time.Hour))
	m.settleMetrics(day.Add(4 * time.Hour))
	if len(m.metrics.unsent) != 2 || m.metrics.unsent[1].Kind != metrics.Ended || m.missions.Get(ms.ID).MetricsAt.IsZero() {
		t.Fatalf("ended: %+v", m.metrics.unsent)
	}
	// The next day: no live mission, nothing.
	m.settleMetrics(day.AddDate(0, 0, 1))
	body, _ := os.ReadFile(path)
	if n := strings.Count(string(body), "\n"); n != 2 || strings.Contains(string(body), "m-old") {
		t.Errorf("records:\n%s", body)
	}
	// The next turn carries them, once.
	proc, stdin := liveOrchestrator(t)
	m.orch.proc = proc
	runCmds(m.sendToOrchestrator("first", originHuman))
	m.orch.origins, m.orch.thinking = nil, false
	runCmds(m.sendToOrchestrator("second", originHuman))
	var sent string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		b, _ := os.ReadFile(stdin)
		if sent = string(b); strings.Contains(sent, "second") {
			break
		}
	}
	first, second, _ := strings.Cut(sent, "second")
	if strings.Count(first, "den_metrics") != 2 || strings.Contains(second, "den_metrics") || strings.Contains(first[strings.Index(first, "first"):], "den_metrics") {
		t.Errorf("sent:\n%s", sent)
	}
	if len(m.takeMetrics()) != 0 {
		t.Error("figures kept after the turn")
	}
}
