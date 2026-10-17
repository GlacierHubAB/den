package ui

import (
	"sort"
	"time"

	"github.com/GlacierHubAB/den/internal/metrics"
	"github.com/GlacierHubAB/den/internal/mission"
	"github.com/GlacierHubAB/den/internal/orch"
)

// How missions go, in figures: Den records a mission's when it ends and
// each live mission's once a day, in metrics.jsonl, and hands them to the
// orchestrator with the next turn it sends anyway. Den never starts a turn
// for them, so they cost nothing while nobody talks to the orchestrator.

// metricsState is what the metrics keep between ticks.
type metricsState struct {
	loaded  bool
	lastDay string // the latest day with daily records
	unsent  []orch.MetricsInfo
}

// maxUnsentMetrics caps the figures waiting for a turn; the oldest go.
const maxUnsentMetrics = 24

// metricsRecent is how long after it ended a mission still gets its ended
// record: older missions, from before Den kept metrics, never do.
const metricsRecent = 24 * time.Hour

// metricsPath is the file; tests point it elsewhere.
var metricsPath = metrics.Path

// settleMetrics records the missions that ended and, once a local day,
// the live ones.
func (m *Model) settleMetrics(now time.Time) {
	if m.missions == nil {
		return
	}
	if !m.metrics.loaded {
		m.metrics.loaded, m.metrics.lastDay = true, metrics.LastDay(metricsPath())
	}
	day := now.Local().Format(time.DateOnly)
	daily := day > m.metrics.lastDay
	var out []metrics.Record
	for _, ms := range m.missions.All() {
		switch {
		case ms.Live() && daily:
			out = append(out, m.missionRecord(ms, metrics.Daily, now))
		case !ms.Live() && ms.State != mission.Proposed && ms.State != mission.Dropped && ms.MetricsAt.IsZero() && now.Sub(ms.Updated) < metricsRecent:
			out = append(out, m.missionRecord(ms, metrics.Ended, now))
			ms.MetricsAt = now
			m.missions.Save(ms)
		}
	}
	if daily {
		m.metrics.lastDay = day
	}
	if len(out) == 0 {
		return
	}
	if err := metrics.Append(metricsPath(), out...); err != nil {
		m.flash, m.flashErr = "Den could not write its metrics: "+err.Error(), true
	}
	for _, r := range out {
		m.metrics.unsent = append(m.metrics.unsent, metricsInfo(r))
	}
	if n := len(m.metrics.unsent) - maxUnsentMetrics; n > 0 {
		m.metrics.unsent = append([]orch.MetricsInfo(nil), m.metrics.unsent[n:]...)
	}
}

// countProcess adds one of Den's process messages to the stats of the
// missions the sessions work for, once per mission.
func (m *Model) countProcess(add func(*mission.Stats), sessions ...string) {
	if m.missions == nil {
		return
	}
	byCard, seen := m.cardMissions(), map[string]bool{}
	for _, id := range sessions {
		ms := m.missions.Get(m.missionVia(id, byCard))
		if ms == nil || !ms.Live() || seen[ms.ID] {
			continue
		}
		seen[ms.ID] = true
		add(&ms.Stats)
		m.missions.Save(ms)
	}
}

// takeMetrics hands over the figures waiting for a turn, once.
func (m *Model) takeMetrics() []orch.MetricsInfo {
	out := m.metrics.unsent
	m.metrics.unsent = nil
	return out
}

// requeueMetrics puts back figures a turn failed to carry, ahead of newer
// ones, within the cap.
func (m *Model) requeueMetrics(figures []orch.MetricsInfo) {
	m.metrics.unsent = append(append([]orch.MetricsInfo(nil), figures...), m.metrics.unsent...)
	if n := len(m.metrics.unsent) - maxUnsentMetrics; n > 0 {
		m.metrics.unsent = m.metrics.unsent[n:]
	}
}

// missionRecord is a mission's figures now.
func (m Model) missionRecord(ms *mission.Mission, kind string, now time.Time) metrics.Record {
	s := m.missionSpent(ms)
	r := metrics.Record{Kind: kind, At: now, Day: now.Local().Format(time.DateOnly), Mission: ms.ID, Name: ms.Name, Project: ms.ProjectName,
		State: string(ms.State), Runs: len(ms.Runs), Tokens: s.Tokens, USD: s.USD, Partial: s.Partial, Merged: len(ms.Merges),
		Overlaps: ms.Stats.Overlaps, Nudges: ms.Stats.Nudges}
	var hours []float64
	for _, g := range ms.Merges {
		if !g.Since.IsZero() && g.At.After(g.Since) {
			hours = append(hours, g.At.Sub(g.Since).Hours())
		}
	}
	if len(hours) > 0 {
		sort.Float64s(hours)
		r.BranchHoursMedian, r.BranchHoursMax = hours[len(hours)/2], hours[len(hours)-1]
		if len(hours)%2 == 0 {
			r.BranchHoursMedian = (hours[len(hours)/2-1] + hours[len(hours)/2]) / 2
		}
	}
	return r
}

// metricsInfo is a record as the orchestrator's block carries it.
func metricsInfo(r metrics.Record) orch.MetricsInfo {
	s := spent{r.Tokens, r.USD, r.Partial}
	info := orch.MetricsInfo{Kind: r.Kind, Mission: r.Mission, Name: r.Name, Project: r.Project, State: r.State, Runs: r.Runs, Merged: r.Merged,
		Overlaps: r.Overlaps, Nudges: r.Nudges, Cost: s.line(), BranchHoursMedian: int(r.BranchHoursMedian + 0.5), BranchHoursMax: int(r.BranchHoursMax + 0.5)}
	if r.Merged > 0 && r.Tokens > 0 {
		info.PerMerge = spent{r.Tokens / int64(r.Merged), r.USD / float64(r.Merged), r.Partial}.line()
	}
	return info
}
