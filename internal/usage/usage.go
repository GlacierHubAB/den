// Package usage keeps an eye on the account's weekly limit: the share of it
// used, as Claude Code reports it to the agent-badge statusline in each
// session, and what Den does about it: warn at 80% and 90%, once each per
// weekly reset, and, only when the human set usage.stop_percent, stop the
// fleet there. With no fresh reading it says so; a figure the human set,
// or a weekly $ budget against Den's own cost estimate, stand in for one,
// always labelled an estimate.
package usage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Stale is how old the newest statusline reading may be before Den says
// it does not know.
const Stale = 15 * time.Minute

// Warnings are the shares of the weekly limit Den warns at.
var Warnings = []int{80, 90}

// Source is where a reading came from.
type Source string

const (
	Statusline Source = "statusline" // Claude Code's own figure, via agent-badge
	Set        Source = "set"        // the human's /usage set
	Budget     Source = "budget"     // Den's cost estimate against usage.weekly_usd
)

// Reading is the weekly limit used, as best Den knows it.
type Reading struct {
	Percent float64
	Resets  time.Time // the weekly reset, zero when not known
	At      time.Time // when it was taken
	Source  Source
}

// Estimate reports whether the reading is not Claude Code's own.
func (r Reading) Estimate() bool { return r.Source != Statusline }

// Sample is one session's statusline figure.
type Sample struct {
	Percent *float64
	Resets  string // RFC 3339
	At      int64  // ms since the epoch, as agent-badge records it
}

// Newest picks the reading Den goes by from the sessions' samples: among
// those taken in the last Stale (a sample dated more than a minute ahead
// is ignored: it would stay fresh for ever) and for the latest weekly
// reset, the HIGHEST share, so a wrong or forged sample can only make Den
// warn or stop early, never late. ok false when there is none.
func Newest(samples []Sample, now time.Time) (Reading, bool) {
	var fresh []Reading
	var reset time.Time
	for _, s := range samples {
		if s.Percent == nil || s.At <= 0 {
			continue
		}
		at := time.UnixMilli(s.At)
		if at.After(now.Add(time.Minute)) || now.Sub(at) > Stale {
			continue
		}
		r := Reading{Percent: clamp(*s.Percent), At: at, Source: Statusline}
		if t, err := time.Parse(time.RFC3339, s.Resets); err == nil {
			r.Resets = t
			if t.After(reset) {
				reset = t
			}
		}
		fresh = append(fresh, r)
	}
	var best Reading
	found := false
	for _, r := range fresh {
		if !reset.IsZero() && !r.Resets.IsZero() && !r.Resets.Equal(reset) {
			continue // an older week's figure
		}
		if !found || r.Percent > best.Percent || r.Percent == best.Percent && r.At.After(best.At) {
			best, found = r, true
		}
	}
	if found && best.Resets.IsZero() {
		best.Resets = reset
	}
	return best, found
}

func clamp(p float64) float64 {
	switch {
	case p < 0:
		return 0
	case p > 100:
		return 100
	}
	return p
}

// State is what Den keeps between runs, in usage.json.
type State struct {
	// SetPercent is the human's figure, good until SetUntil (the next
	// reset, or a week on when that is not known).
	SetPercent *float64  `json:"set_percent,omitempty"`
	SetAt      time.Time `json:"set_at,omitzero"`
	SetUntil   time.Time `json:"set_until,omitzero"`
	// LastReset is the newest weekly reset seen, to start the week's
	// warnings over and to bound a figure set by hand.
	LastReset time.Time `json:"last_reset,omitzero"`
	// LastSeen is when Den last had a statusline reading: before the
	// first, there is nothing to say "unknown" about.
	LastSeen time.Time `json:"last_seen,omitzero"`
	// Warned holds, per threshold, the reset it was warned for.
	Warned map[string]time.Time `json:"warned,omitempty"`
	// Stopped is set while the fleet is stopped; Den refuses to start
	// agents or mission runs until the human resumes.
	Stopped   bool      `json:"stopped,omitempty"`
	StoppedAt time.Time `json:"stopped_at,omitzero"`
	StopWhy   string    `json:"stop_why,omitempty"`
	// ResumedFor is the week (its reset) the human let the fleet go on in,
	// at stop share ResumedAt (usage.stop_percent then): Den does not
	// stop it again by itself that week unless the share is changed.
	ResumedFor  time.Time `json:"resumed_for,omitzero"`
	ResumedAt   int       `json:"resumed_at,omitempty"`
	ResumedWhen time.Time `json:"resumed_when,omitzero"`
}

// Resume lets the fleet go on, and holds off the automatic stop for the
// rest of this week at this stop share.
func (s *State) Resume(stopPercent int, now time.Time) {
	s.Stopped, s.StopWhy = false, ""
	s.ResumedFor, s.ResumedAt, s.ResumedWhen = s.LastReset, stopPercent, now
}

// Armed reports whether the automatic stop may fire: not after the human
// resumed this week at the same stop share.
func (s State) Armed(stopPercent int, now time.Time) bool {
	if s.ResumedWhen.IsZero() || s.ResumedAt != stopPercent {
		return true
	}
	if !s.ResumedFor.IsZero() {
		return !s.LastReset.Equal(s.ResumedFor) // a new week re-arms it
	}
	return now.Sub(s.ResumedWhen) >= 7*24*time.Hour // no reset known: a week
}

// Path is usage.json in Den's state folder.
func Path() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "den", "usage.json")
}

// Load reads the state; a missing or unreadable file is a fresh one.
func Load(path string) State {
	var s State
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &s)
	}
	return s
}

// Save writes the state whole, through a temporary file, mode 0600.
func Save(path string, s State) error {
	if path == "" {
		return os.ErrNotExist
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".usage-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Stopped reports whether the fleet is stopped, as the file says: what
// `den mcp` checks before it starts an agent.
func Stopped() (bool, string) {
	s := Load(Path())
	return s.Stopped, s.StopWhy
}

// Set records the human's figure, good until the next reset Den knows of,
// else for a week.
func (s *State) Set(p float64, now time.Time) {
	v := clamp(p)
	s.SetPercent, s.SetAt = &v, now
	s.SetUntil = now.Add(7 * 24 * time.Hour)
	if s.LastReset.After(now) {
		s.SetUntil = s.LastReset
	}
}

// Current is the reading Den goes by: the statusline's when fresh, else
// the human's figure while it holds, else the budget estimate (spent of
// weeklyUSD, when that is set). ok false means Den does not know.
func (s *State) Current(fresh Reading, haveFresh bool, spent, weeklyUSD float64, now time.Time) (Reading, bool) {
	if haveFresh {
		if !fresh.Resets.IsZero() && fresh.Resets.After(s.LastReset) {
			s.LastReset = fresh.Resets
		}
		if fresh.At.After(s.LastSeen) {
			s.LastSeen = fresh.At
		}
		return fresh, true
	}
	if s.SetPercent != nil && now.Before(s.SetUntil) {
		return Reading{Percent: *s.SetPercent, Resets: s.SetUntil, At: s.SetAt, Source: Set}, true
	}
	if weeklyUSD > 0 {
		return Reading{Percent: clamp(100 * spent / weeklyUSD), Resets: s.LastReset, At: now, Source: Budget}, true
	}
	return Reading{}, false
}

// Due returns the thresholds r crosses that were not yet warned for in
// this week, and marks them warned. The week is known by its reset: a new
// reset starts the warnings over.
func (s *State) Due(r Reading, now time.Time) []int {
	week := r.Resets
	if week.IsZero() {
		week = s.LastReset
	}
	if s.Warned == nil {
		s.Warned = map[string]time.Time{}
	}
	var out []int
	for _, w := range Warnings {
		key := fmt.Sprint(w)
		if r.Percent < float64(w) {
			continue
		}
		if at, ok := s.Warned[key]; ok && at.Equal(week) && !week.IsZero() {
			continue
		}
		if at, ok := s.Warned[key]; ok && week.IsZero() && now.Sub(at) < 7*24*time.Hour {
			continue // no reset known: once a week
		}
		if week.IsZero() {
			s.Warned[key] = now
		} else {
			s.Warned[key] = week
		}
		out = append(out, w)
	}
	return out
}

// ShouldStop reports whether r reaches the stop threshold (0: off). Only
// Claude Code's own figure or the human's stops the fleet, never Den's
// cost estimate.
func ShouldStop(r Reading, stopPercent int) bool {
	return stopPercent > 0 && r.Source != Budget && r.Percent >= float64(stopPercent)
}

// Shown reports whether the header has anything to say about usage: a
// reading or estimate, a stop, a warning given, or a reading once seen
// that has gone stale. Before Den ever had one, it stays quiet.
func Shown(ok bool, s State) bool {
	return ok || s.Stopped || !s.LastSeen.IsZero() || len(s.Warned) > 0
}

// Label is the one-line state for the top bar.
func Label(r Reading, ok bool, s State, now time.Time) string {
	if s.Stopped {
		return "usage stop"
	}
	if !ok {
		return "usage unknown"
	}
	switch r.Source {
	case Set:
		return fmt.Sprintf("usage ~%.0f%% (estimate, set by you)", r.Percent)
	case Budget:
		return fmt.Sprintf("usage ~%.0f%% (estimate, of the weekly budget)", r.Percent)
	}
	return fmt.Sprintf("usage %.0f%% · %s", r.Percent, ago(now.Sub(r.At)))
}

func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh ago", int(d.Hours()))
}
