package stats

import (
	"errors"
	"fmt"
	"time"
)

// DefaultDays is the window when none is given.
const DefaultDays = 7

// maxDays keeps --days to a span a time.Duration can hold.
const maxDays = 36500

// ParseWindow turns the flags of `den stats` into the window [since,
// until). days counts back from now; since and until are RFC 3339, or a
// local "2006-01-02 15:04" or a local date, which is that day's start.
// With nothing given the window is the last DefaultDays days; with only
// since, it runs to now.
func ParseWindow(since, until string, days int, now time.Time) (from, to time.Time, err error) {
	now = now.Truncate(time.Second)
	switch {
	case days != 0 && (since != "" || until != ""):
		return from, to, errors.New("--days cannot be used with --since or --until")
	case days < 0 || days > maxDays:
		return from, to, fmt.Errorf("--days is between 1 and %d", maxDays)
	case until != "" && since == "":
		return from, to, errors.New("--until needs --since")
	case days == 0 && since == "":
		days = DefaultDays
	}
	if days > 0 {
		return now.Add(-time.Duration(days) * 24 * time.Hour), now, nil
	}
	if from, err = parseTime(since); err != nil {
		return from, to, fmt.Errorf("--since: %w", err)
	}
	to = now
	if until != "" {
		if to, err = parseTime(until); err != nil {
			return from, to, fmt.Errorf("--until: %w", err)
		}
	}
	if !to.After(from) {
		return from, to, errors.New("the window is empty: --until is not after --since")
	}
	return from, to, nil
}

func parseTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, errors.New("not a time: use RFC 3339 (2026-10-11T00:00:00Z), 2026-10-11 15:04 or 2026-10-11")
}
