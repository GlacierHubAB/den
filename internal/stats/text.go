package stats

import (
	"fmt"
	"io"
	"time"
)

const notRecorded = "not recorded"

// WriteText prints the report for a person. The figures are the report's;
// what it could not measure says "not recorded".
func (r Report) WriteText(w io.Writer) {
	fmt.Fprintf(w, "den stats  %s to %s UTC  (%s)\n", stamp(r.Since), stamp(r.Until), length(r.Until.Sub(r.Since)))
	fmt.Fprintf(w, "same window: den stats --since %s --until %s\n", r.Since.Format(time.RFC3339), r.Until.Format(time.RFC3339))

	fmt.Fprintln(w, "\nAgent work (events.jsonl)")
	if t := r.Turns; t == nil {
		kv(w, "turns", notRecorded)
	} else {
		turns := fmt.Sprintf("%d finished, by %s", t.Finished, count(t.Agents, "agent"))
		if t.Failed > 0 {
			turns += fmt.Sprintf("; %d more ended on an API error", t.Failed)
		}
		kv(w, "turns", turns)
		if t.Measured == 0 {
			kv(w, "working time", notRecorded)
		} else {
			kv(w, "working time", fmt.Sprintf("%s, over %s with a recorded start (a lower bound)", dur(t.WorkingSeconds), count(t.Measured, "turn")))
			kv(w, "turn length", fmt.Sprintf("median %s, longest %s", dur(*t.MedianSeconds), dur(*t.LongestSeconds)))
		}
		kv(w, "agents at once", fmt.Sprintf("most %d, average %.2f over the window", t.PeakAgents, t.AverageAgents))
		kv(w, "prompts submitted", fmt.Sprintf("%d: %d from peers, %d from terminals, %d source unknown", t.Prompts, t.PromptsPeer, t.PromptsTerminal, t.PromptsUnknown))
	}

	fmt.Fprintln(w, "\nWaiting on you (events.jsonl, hooks.log)")
	if x := r.Waits; x == nil {
		kv(w, "requests", notRecorded)
	} else {
		kv(w, "requests", fmt.Sprintf("%d: %d permission, %d plan, %d question, %d other", x.Requests, x.Permission, x.Plan, x.Question, x.Other))
		if x.Measured == 0 {
			kv(w, "wait", notRecorded)
		} else {
			kv(w, "wait", fmt.Sprintf("median %s, longest %s, measured for %d of %d", dur(*x.MedianSeconds), dur(*x.LongestSeconds), x.Measured, x.Requests))
			kv(w, "", "until the agent's next hook run, so it includes the answered tool's run time")
		}
		kv(w, "Den's own cards", notRecorded)
	}

	fmt.Fprintln(w, "\nYou and the orchestrator (orchestrator.jsonl)")
	if m := r.Messages; m == nil {
		kv(w, "messages", notRecorded)
	} else {
		kv(w, "from you", fmt.Sprint(m.FromYou))
		kv(w, "mission lines by Den", fmt.Sprintf("%d (run and judging lines in a mission thread; not yours)", m.MissionLines))
		kv(w, "from paired apps", fmt.Sprint(m.FromApps))
	}

	fmt.Fprintln(w, "\nMissions (missions.jsonl, metrics.jsonl)")
	if g := r.Merges; g == nil {
		kv(w, "merges", notRecorded)
	} else {
		kv(w, "merges", fmt.Sprintf("%d branches Den saw land", g.Count))
		if g.BranchHoursMedian == nil {
			kv(w, "branch lifetime", notRecorded)
		} else {
			kv(w, "branch lifetime", fmt.Sprintf("median %.1f h, longest %.1f h", *g.BranchHoursMedian, *g.BranchHoursLongest))
		}
	}
	if m := r.Metrics; m == nil {
		kv(w, "cost per merge", notRecorded)
		kv(w, "overlaps, nudges", notRecorded)
	} else {
		if m.CostPerMergeUSD == nil {
			kv(w, "cost per merge", notRecorded+" (no merged change)")
		} else {
			floor := ""
			if m.Partial {
				floor = ", at least (a model had no known price)"
			}
			kv(w, "cost per merge", fmt.Sprintf("$%.2f%s", *m.CostPerMergeUSD, floor))
		}
		kv(w, "overlaps, nudges", fmt.Sprintf("%d told, %d sent; totals since start of the %d missions with a record in the window", m.Overlaps, m.Nudges, m.Missions))
	}
	kv(w, "merged pull requests", notRecorded+" (Den records branches it saw land, not pull requests)")

	fmt.Fprintln(w, "\nRead")
	for _, s := range r.Sources {
		kv(w, s.File, s.describe(r.Since))
	}
}

// describe is one line on how much of the file was read.
func (s Source) describe(since time.Time) string {
	if s.Problem != "" {
		return notRecorded + " (" + s.Problem + ")"
	}
	out := fmt.Sprintf("%d records", s.Lines)
	if s.Skipped > 0 {
		out += fmt.Sprintf(", %d lines skipped", s.Skipped)
	}
	if s.First != nil {
		out += fmt.Sprintf(", %s to %s", s.First.Format("2006-01-02"), s.Last.Format("2006-01-02"))
		if s.First.After(since) {
			out += "; the window's first " + dur(s.First.Sub(since).Seconds()) + " is before the file starts"
		}
	}
	if s.Truncated {
		out += "; only the newest " + fmt.Sprint(maxFileBytes>>20) + " MiB was read"
	}
	return out
}

func kv(w io.Writer, label, value string) {
	fmt.Fprintf(w, "  %-21s %s\n", label, value)
}

func stamp(t time.Time) string { return t.UTC().Format("2006-01-02 15:04") }

// length writes the length of the window in hours up to two days, then in days.
func length(d time.Duration) string {
	if d < 48*time.Hour {
		return fmt.Sprintf("%.1f hours", d.Hours())
	}
	return fmt.Sprintf("%.1f days", d.Hours()/24)
}

func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// dur writes a length of time in the unit that reads best.
func dur(sec float64) string {
	switch {
	case sec < 90:
		return fmt.Sprintf("%.0f s", sec)
	case sec < 90*60:
		return fmt.Sprintf("%.1f min", sec/60)
	}
	return fmt.Sprintf("%.1f h", sec/3600)
}
