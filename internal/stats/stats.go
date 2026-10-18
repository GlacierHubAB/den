// Package stats counts what Den's own state files prove about a window of
// time: how much the agents worked, how often they waited on the human, what
// the human said to the orchestrator and what landed. It only reads; it
// writes no state file and calls none of the loaders that tidy the file they
// read. Every file is untrusted data: reads are bounded (read.go), a line
// that does not parse is skipped, and a Report holds counts, durations and
// times, never a message, a path, a session id, a prompt or an agent's
// output.
package stats

import (
	"bytes"
	"encoding/json"
	"math"
	"sort"
	"time"

	"github.com/GlacierHubAB/den/internal/events"
	"github.com/GlacierHubAB/den/internal/metrics"
	"github.com/GlacierHubAB/den/internal/mission"
)

// Paths are the state files Compute reads. "" leaves one out, which a
// report shows as not recorded. events.jsonl.1 and metrics.jsonl.1, the
// files they move to when they are full, are read first when present.
type Paths struct {
	Events   string // events.jsonl
	Hooks    string // hooks.log
	Chat     string // orchestrator.jsonl
	Missions string // missions.jsonl
	Metrics  string // metrics.jsonl
}

// Report is what the files prove about [Since, Until). A nil section was
// not recorded: its file is missing or could not be read.
type Report struct {
	Since    time.Time `json:"since"`
	Until    time.Time `json:"until"`
	Turns    *Turns    `json:"turns"`
	Waits    *Waits    `json:"waits"`
	Messages *Messages `json:"messages"`
	Merges   *Merges   `json:"merges"`
	Metrics  *Metrics  `json:"metrics"`
	// NotRecorded names what no file holds, so a reader does not take its
	// absence for a zero.
	NotRecorded []string `json:"not_recorded"`
	Sources     []Source `json:"sources"`
}

// Turns are the agents' turns, from events.jsonl.
type Turns struct {
	// Finished are the turns that ended in a done event in the window;
	// Failed those that ended on an API error.
	Finished int `json:"finished"`
	Failed   int `json:"failed"`
	// Prompts are the prompts submitted in the window (turn_start events).
	// A prompt sent while an agent works, or one that interrupted a turn,
	// is a prompt but not a turn of its own.
	Prompts int `json:"prompts"`
	// The prompts by the source field of their turn_start: delivered as a
	// peer's message, or any other prompt ("terminal": typed in the agent's
	// terminal, or Den's brief for a spawned agent). An event written
	// before the field has none: its source is unknown.
	PromptsPeer     int `json:"prompts_peer"`
	PromptsTerminal int `json:"prompts_terminal"`
	PromptsUnknown  int `json:"prompts_unknown"`
	// Agents are the sessions that submitted a prompt or ended a turn.
	Agents int `json:"agents"`
	// Measured are the finished turns whose start was recorded; their
	// durations are the working time. A turn runs from the session's latest
	// turn_start before its done, so the time is a lower bound.
	Measured       int      `json:"measured"`
	WorkingSeconds float64  `json:"working_seconds"`
	LongestSeconds *float64 `json:"longest_seconds"`
	MedianSeconds  *float64 `json:"median_seconds"`
	// PeakAgents is the most measured turns running at once; AverageAgents
	// the measured working time over the length of the window.
	PeakAgents    int     `json:"peak_agents"`
	AverageAgents float64 `json:"average_agents"`
}

// Waits are the requests that waited on the human: needs_you events from
// events.jsonl, and how long each took to be answered, read from hooks.log.
type Waits struct {
	Requests   int `json:"requests"`
	Permission int `json:"permission"`
	Plan       int `json:"plan"`
	Question   int `json:"question"`
	Other      int `json:"other"`
	// Measured are the requests with a later hook run of the same session
	// before its next request. The wait is the time to that run, the
	// agent's next sign of life: it includes the time the answered tool
	// then took, and a request a standing allow answered counts as a short
	// one. The two durations are nil when nothing was measured.
	Measured       int      `json:"measured"`
	MedianSeconds  *float64 `json:"median_seconds"`
	LongestSeconds *float64 `json:"longest_seconds"`
}

// Messages are the chat records of the orchestrator log, by who wrote them.
type Messages struct {
	// FromYou are the messages the human sent the orchestrator: a record
	// of kind "you" outside a mission thread. It includes review comments
	// handed to the orchestrator.
	FromYou int `json:"from_you"`
	// MissionLines are records of kind "you" inside a mission thread: the
	// lines Den writes to start a mission run or ask for its judging.
	// They are not the human's words.
	MissionLines int `json:"mission_lines"`
	// FromApps are the words of paired apps (kind "app").
	FromApps int `json:"from_apps"`
}

// Merges are the mission branches Den saw land, from missions.jsonl.
type Merges struct {
	Count int `json:"count"`
	// BranchHours are the median and longest lifetimes of the branches,
	// from their oldest commit of their own to when Den saw them land.
	BranchHoursMedian  *float64 `json:"branch_hours_median"`
	BranchHoursLongest *float64 `json:"branch_hours_longest"`
}

// Metrics are the mission figures Den recorded, from metrics.jsonl: for
// each mission with a record in the window, its latest record there.
// They are totals since each mission began, not figures of the window.
type Metrics struct {
	Missions int `json:"missions"`
	Merged   int `json:"merged"`
	Overlaps int `json:"overlaps"` // overlap pairs Den told
	Nudges   int `json:"nudges"`   // branch nudges Den sent
	// CostPerMergeUSD is the estimated cost over the merged changes; nil
	// with none merged. Partial is set when a model had no known price, so
	// the cost is a floor.
	CostPerMergeUSD *float64 `json:"cost_per_merge_usd"`
	Partial         bool     `json:"partial,omitempty"`
}

// Compute reads the files once and reports on [since, until).
func Compute(p Paths, since, until time.Time) Report {
	r := Report{
		Since: since.UTC(), Until: until.UTC(),
		NotRecorded: []string{"merged pull requests", "Den's own cards (merge, close, mission approval)", "CI results"},
	}
	r.fromEvents(p, since, until)
	r.fromChat(p.Chat, since, until)
	r.fromMissions(p.Missions, since, until)
	r.fromMetrics(p.Metrics, since, until)
	return r
}

func within(t, since, until time.Time) bool { return !t.Before(since) && t.Before(until) }

// withOld lists the file that path rotates to, then path itself.
func withOld(path string) []string {
	if path == "" {
		return []string{""}
	}
	return []string{path + ".1", path}
}

// event is the part of an events.jsonl line the counts use.
type event struct {
	t       time.Time
	kind    string
	session string
	request string
	reason  string
	source  string
}

// need is a request that waited on the human.
type need struct {
	t       time.Time
	session string
	request string
}

// span is a turn: the session's latest turn_start and its done.
type span struct{ start, end time.Time }

func (r *Report) fromEvents(p Paths, since, until time.Time) {
	src := Source{File: "events.jsonl"}
	var evs []event
	for _, path := range withOld(p.Events) {
		src.read(path, func(line []byte) (time.Time, bool) {
			var e events.Event
			if json.Unmarshal(line, &e) != nil || e.Time.IsZero() || e.Session == "" {
				return time.Time{}, false
			}
			evs = append(evs, event{t: e.Time, kind: e.Kind, session: e.Session, request: e.Request, reason: e.Reason, source: e.Source})
			return e.Time, true
		})
	}
	r.Sources = append(r.Sources, src)
	if !src.ok() {
		return
	}
	// Hook processes append as they run, so lines are nearly in order;
	// remote events carry this machine's clock. Sort to be sure.
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].t.Before(evs[j].t) })

	t := &Turns{}
	var spans []span
	var durs []float64
	open := map[string]time.Time{} // a session's latest turn_start while its turn is open
	agents := map[string]bool{}
	var needs []need
	for _, e := range evs {
		in := within(e.t, since, until)
		switch e.kind {
		case events.TurnStart:
			open[e.session] = e.t
			if in {
				t.Prompts++
				switch e.source {
				case events.SourcePeer:
					t.PromptsPeer++
				case events.SourceTerminal:
					t.PromptsTerminal++
				default:
					t.PromptsUnknown++
				}
				agents[e.session] = true
			}
		case events.Done, events.TurnFailed:
			start, was := open[e.session]
			delete(open, e.session)
			if !in {
				continue
			}
			agents[e.session] = true
			if e.kind == events.TurnFailed {
				t.Failed++
				continue
			}
			t.Finished++
			if was {
				spans = append(spans, span{start, e.t})
				durs = append(durs, e.t.Sub(start).Seconds())
			}
		case events.Idle, events.SessionEnd:
			// An idle notice is Claude Code waiting for input: the turn
			// was interrupted, and its end is not recorded.
			delete(open, e.session)
		case events.SessionStart:
			// A compaction runs in the middle of a turn, which goes on.
			if e.reason != "compact" {
				delete(open, e.session)
			}
		case events.NeedsYou:
			if in {
				needs = append(needs, need{e.t, e.session, e.request})
			}
		}
	}
	t.Agents = len(agents)
	t.Measured = len(durs)
	for _, d := range durs {
		t.WorkingSeconds += d
	}
	t.WorkingSeconds = round(t.WorkingSeconds)
	t.MedianSeconds, t.LongestSeconds = medianLongest(durs)
	t.PeakAgents, t.AverageAgents = concurrency(spans, since, until)
	r.Turns = t
	r.waits(p.Hooks, needs)
}

// concurrency is the most spans running at once and the average number
// running over [since, until), with each span cut to the window.
func concurrency(spans []span, since, until time.Time) (peak int, average float64) {
	type edge struct {
		t time.Time
		d int
	}
	var edges []edge
	total := 0.0
	for _, s := range spans {
		start, end := s.start, s.end
		if start.Before(since) {
			start = since
		}
		if end.After(until) {
			end = until
		}
		if !end.After(start) {
			continue
		}
		total += end.Sub(start).Seconds()
		edges = append(edges, edge{start, 1}, edge{end, -1})
	}
	// A span that ends where another begins does not overlap it.
	sort.Slice(edges, func(i, j int) bool {
		if !edges[i].t.Equal(edges[j].t) {
			return edges[i].t.Before(edges[j].t)
		}
		return edges[i].d < edges[j].d
	})
	cur := 0
	for _, e := range edges {
		cur += e.d
		peak = max(peak, cur)
	}
	if w := until.Sub(since).Seconds(); w > 0 {
		average = math.Round(total/w*1000) / 1000
	}
	return peak, average
}

// resumed are the hook runs that show an agent working again after a wait.
var resumed = map[string]bool{
	"PostToolUse": true, "PostToolUseFailure": true, "Stop": true, "StopFailure": true,
	"UserPromptSubmit": true, "SessionEnd": true,
}

// waits counts the requests and measures how long each waited, from the
// session's next hook run in hooks.log.
func (r *Report) waits(hooks string, needs []need) {
	w := &Waits{Requests: len(needs)}
	r.Waits = w
	want := map[string]bool{} // the first 8 characters of a session id, as hooks.log has them
	for _, n := range needs {
		switch n.request {
		case "permission":
			w.Permission++
		case "plan":
			w.Plan++
		case "question":
			w.Question++
		default:
			w.Other++
		}
		want[short(n.session)] = true
	}
	if len(needs) == 0 {
		return
	}
	src := Source{File: "hooks.log"}
	runs := map[string][]time.Time{}
	src.read(hooks, func(line []byte) (time.Time, bool) {
		f := bytes.Fields(line)
		if len(f) < 4 {
			return time.Time{}, false
		}
		t, err := time.ParseInLocation("2006-01-02 15:04:05", string(f[0])+" "+string(f[1]), time.Local)
		if err != nil {
			return time.Time{}, false
		}
		if resumed[string(f[2])] && want[string(f[3])] {
			runs[string(f[3])] = append(runs[string(f[3])], t)
		}
		return t, true
	})
	r.Sources = append(r.Sources, src)
	if !src.ok() {
		return
	}
	for _, ts := range runs {
		sort.Slice(ts, func(i, j int) bool { return ts[i].Before(ts[j]) })
	}
	// next[i] is the time of the request after needs[i] from the same session.
	next := make([]time.Time, len(needs))
	later := map[string]int{}
	for i := len(needs) - 1; i >= 0; i-- {
		if j, ok := later[needs[i].session]; ok {
			next[i] = needs[j].t
		}
		later[needs[i].session] = i
	}
	var waits []float64
	for i, n := range needs {
		ts := runs[short(n.session)]
		// hooks.log keeps whole seconds, so a run in the second of the
		// request may have been before it, such as another tool's: only
		// a run stamped at or after the request's own time counts.
		k := sort.Search(len(ts), func(i int) bool { return !ts[i].Before(n.t) })
		if k == len(ts) || !next[i].IsZero() && !ts[k].Before(next[i]) {
			continue
		}
		waits = append(waits, ts[k].Sub(n.t).Seconds())
	}
	w.Measured = len(waits)
	w.MedianSeconds, w.LongestSeconds = medianLongest(waits)
}

// short is a session id as hooks.log has it, its first 8 characters.
func short(session string) string {
	if len(session) > 8 {
		return session[:8]
	}
	return session
}

func (r *Report) fromChat(path string, since, until time.Time) {
	src := Source{File: "orchestrator.jsonl"}
	m := &Messages{}
	src.read(path, func(line []byte) (time.Time, bool) {
		var c struct {
			Kind    string    `json:"kind"`
			At      time.Time `json:"at"`
			Mission string    `json:"mission"`
		}
		if json.Unmarshal(line, &c) != nil || c.Kind == "" {
			return time.Time{}, false
		}
		if within(c.At, since, until) {
			switch {
			case c.Kind == "you" && c.Mission == "":
				m.FromYou++
			case c.Kind == "you":
				m.MissionLines++
			case c.Kind == "app":
				m.FromApps++
			}
		}
		return c.At, true
	})
	r.Sources = append(r.Sources, src)
	if src.ok() {
		r.Messages = m
	}
}

func (r *Report) fromMissions(path string, since, until time.Time) {
	src := Source{File: "missions.jsonl"}
	// A mission is saved as a whole line each time; the last line wins.
	merges := map[string][]mission.Merge{}
	src.read(path, func(line []byte) (time.Time, bool) {
		var m struct {
			ID     string          `json:"id"`
			Merges []mission.Merge `json:"merges"`
		}
		if json.Unmarshal(line, &m) != nil || m.ID == "" {
			return time.Time{}, false
		}
		merges[m.ID] = m.Merges
		return time.Time{}, true
	})
	r.Sources = append(r.Sources, src)
	if !src.ok() {
		return
	}
	g := &Merges{}
	var hours []float64
	for _, ms := range merges {
		for _, m := range ms {
			if !within(m.At, since, until) {
				continue
			}
			g.Count++
			if !m.Since.IsZero() && !m.At.Before(m.Since) {
				hours = append(hours, m.At.Sub(m.Since).Hours())
			}
		}
	}
	g.BranchHoursMedian, g.BranchHoursLongest = medianLongest(hours)
	r.Merges = g
}

func (r *Report) fromMetrics(path string, since, until time.Time) {
	src := Source{File: "metrics.jsonl"}
	latest := map[string]metrics.Record{}
	for _, f := range withOld(path) {
		src.read(f, func(line []byte) (time.Time, bool) {
			var rec metrics.Record
			if json.Unmarshal(line, &rec) != nil || rec.Mission == "" || rec.At.IsZero() {
				return time.Time{}, false
			}
			if within(rec.At, since, until) && !rec.At.Before(latest[rec.Mission].At) {
				latest[rec.Mission] = rec
			}
			return rec.At, true
		})
	}
	r.Sources = append(r.Sources, src)
	if !src.ok() {
		return
	}
	m := &Metrics{Missions: len(latest)}
	usd := 0.0
	for _, rec := range latest {
		m.Merged += rec.Merged
		m.Overlaps += rec.Overlaps
		m.Nudges += rec.Nudges
		usd += rec.USD
		m.Partial = m.Partial || rec.Partial
	}
	if m.Merged > 0 {
		per := math.Round(usd/float64(m.Merged)*100) / 100
		m.CostPerMergeUSD = &per
	}
	r.Metrics = m
}

// round keeps a figure to three decimals.
func round(x float64) float64 { return math.Round(x*1000) / 1000 }

// medianLongest are the median and the largest of xs (nil for none), to
// three decimals. It sorts xs.
func medianLongest(xs []float64) (median, longest *float64) {
	n := len(xs)
	if n == 0 {
		return nil, nil
	}
	sort.Float64s(xs)
	m, l := xs[n/2], xs[n-1]
	if n%2 == 0 {
		m = (xs[n/2-1] + xs[n/2]) / 2
	}
	m, l = round(m), round(l)
	return &m, &l
}
