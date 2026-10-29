package main

import (
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/GlacierHubAB/den/internal/events"
	"github.com/GlacierHubAB/den/internal/metrics"
	"github.com/GlacierHubAB/den/internal/mission"
	"github.com/GlacierHubAB/den/internal/stats"
	"github.com/GlacierHubAB/den/internal/ui"
)

// statsCmd is `den stats`: what Den's state files prove about one window of
// time. It only reads them, and needs no running Den.
func statsCmd(args []string) {
	fs := flag.NewFlagSet("stats", flag.ExitOnError)
	days := fs.Int("days", 0, "the last N days, counted back from now (the default is 7)")
	since := fs.String("since", "", "start of the window, included: RFC 3339, \"2006-01-02 15:04\" or a date. A date, or a time without a zone, is local time, not UTC: 2026-10-18 is local midnight")
	until := fs.String("until", "", "end of the window, excluded; the formats of --since; the default is now. The output prints the exact UTC window and the command that repeats it")
	asJSON := fs.Bool("json", false, "print JSON")
	fs.Parse(args)
	from, to, err := stats.ParseWindow(*since, *until, *days, time.Now())
	if err != nil {
		fail("den: %v", err)
	}
	if err := writeStats(os.Stdout, statsPaths(), from, to, *asJSON); err != nil {
		fail("den: %v", err)
	}
}

// writeStats reads the files at p and prints the report for [from, to).
func writeStats(w io.Writer, p stats.Paths, from, to time.Time, asJSON bool) error {
	r := stats.Compute(p, from, to)
	if !asJSON {
		r.WriteText(w)
		return nil
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// statsPaths are the state files `den stats` reads, all in Den's state
// folder.
func statsPaths() stats.Paths {
	return stats.Paths{
		Events:   events.Path(),
		Hooks:    filepath.Join(filepath.Dir(events.Path()), "hooks.log"),
		Chat:     ui.ChatLogPath(),
		Missions: mission.Path(),
		Metrics:  metrics.Path(),
	}
}
