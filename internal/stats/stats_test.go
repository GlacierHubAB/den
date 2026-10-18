package stats

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/events"
	"github.com/GlacierHubAB/den/internal/metrics"
	"github.com/GlacierHubAB/den/internal/mission"
)

// base is the start of the test window; the window is one hour long.
var base = time.Date(2026, 1, 10, 10, 0, 0, 0, time.UTC)

const (
	sessA = "aaaaaaaa-0000-4000-8000-000000000001"
	sessB = "bbbbbbbb-0000-4000-8000-000000000002"
	sessC = "cccccccc-0000-4000-8000-000000000003"
)

// at is a time in the test window, mm:ss after base.
func at(mm, ss int) time.Time {
	return base.Add(time.Duration(mm)*time.Minute + time.Duration(ss)*time.Second)
}

func write(t *testing.T, dir, name string, lines ...string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func evLine(tm time.Time, kind, session string, mod ...func(*events.Event)) string {
	ev := events.Event{V: events.Version, Time: tm, Kind: kind, Session: session, Agent: "Marker Critter"}
	for _, m := range mod {
		m(&ev)
	}
	raw, _ := json.Marshal(ev)
	return string(raw)
}

func request(r string) func(*events.Event) { return func(e *events.Event) { e.Request = r } }
func reason(r string) func(*events.Event)  { return func(e *events.Event) { e.Reason = r } }

// hookLine is a line of hooks.log as `den hook` writes it: local time to the
// second, the event, the first 8 characters of the session, the outcome.
func hookLine(tm time.Time, name, session string) string {
	return fmt.Sprintf("%s %-18s %s delivered", tm.In(time.Local).Format("2006-01-02 15:04:05"), name, session[:8])
}

func f64(p *float64) float64 {
	if p == nil {
		return -1
	}
	return *p
}

// fixture writes a small state folder and returns its paths.
func fixture(t *testing.T) Paths {
	t.Helper()
	dir := t.TempDir()
	p := Paths{
		Events: write(t, dir, "events.jsonl",
			// Before the window: closes nothing that counts.
			evLine(base.Add(-10*time.Minute), events.TurnStart, sessA),
			evLine(base.Add(-9*time.Minute), events.Done, sessA),
			// A: 30 s, overlapping B's first turn.
			evLine(at(0, 10), events.TurnStart, sessA),
			evLine(at(0, 40), events.Done, sessA),
			evLine(at(0, 20), events.TurnStart, sessB),
			evLine(at(1, 20), events.Done, sessB),
			// A: interrupted, closed by an idle notice, then a turn of 20 s.
			evLine(at(10, 0), events.TurnStart, sessA),
			evLine(at(12, 0), events.Idle, sessA),
			evLine(at(20, 0), events.TurnStart, sessA),
			evLine(at(20, 20), events.Done, sessA),
			// B: a compaction in the middle of a turn of 60 s.
			evLine(at(30, 0), events.TurnStart, sessB),
			evLine(at(30, 30), events.SessionStart, sessB, reason("compact")),
			evLine(at(31, 0), events.Done, sessB),
			// B: a second prompt while it works: the turn counts from the later one.
			evLine(at(40, 0), events.TurnStart, sessB),
			evLine(at(40, 5), events.TurnStart, sessB),
			evLine(at(40, 15), events.Done, sessB),
			// A: a done with no start recorded.
			evLine(at(50, 0), events.Done, sessA),
			// B: a failed turn. C: a prompt and then the session ends.
			evLine(at(55, 0), events.TurnStart, sessB),
			evLine(at(55, 5), events.TurnFailed, sessB),
			evLine(at(58, 0), events.TurnStart, sessC),
			evLine(at(58, 30), events.SessionEnd, sessC),
			// A: a turn that ends after the window.
			evLine(at(59, 50), events.TurnStart, sessA),
			evLine(at(60, 10), events.Done, sessA),
			// Requests that waited on the human.
			evLine(at(5, 0).Add(500*time.Millisecond), events.NeedsYou, sessA, request("permission")),
			evLine(at(6, 0).Add(200*time.Millisecond), events.NeedsYou, sessB, request("question")),
			evLine(at(8, 0), events.NeedsYou, sessB, request("permission")),
			evLine(at(15, 0), events.NeedsYou, sessA, request("plan")),
			evLine(at(16, 0), events.NeedsYou, sessA),
			// Noise a reader must step over.
			"not json",
			`{"v":1,"kind":"done"}`,
			`{"v":1,"time":"2026-01-10T10:`,
		),
		Hooks: write(t, t.TempDir(), "hooks.log",
			hookLine(at(5, 0), "PostToolUse", sessA), // the second of the request, before it: not its answer
			hookLine(at(5, 10), "Notification", sessA),
			hookLine(at(5, 40), "PostToolUse", sessA),
			hookLine(at(7, 0), "PostToolUse", sessB),
			hookLine(at(17, 0), "PostToolUse", sessA),
			"short line",
		),
		Chat: write(t, t.TempDir(), "orchestrator.jsonl",
			`{"kind":"you","at":"2026-01-10T10:05:00Z","text":"MARKER-TEXT"}`,
			`{"kind":"you","at":"2026-01-10T10:06:00Z","text":"MARKER-TEXT","mission":"m1"}`,
			`{"kind":"you","at":"2026-01-10T11:06:00Z","text":"after the window"}`,
			`{"kind":"app","at":"2026-01-10T10:07:00Z","text":"MARKER-TEXT","app":true}`,
			`{"kind":"orchestrator","at":"2026-01-10T10:08:00Z","text":"MARKER-TEXT"}`,
			`{"kind":"card","at":"2026-01-10T10:09:00Z","card":{"id":"c1","brief":"MARKER-TEXT"}}`,
			`{"kind":"you","at":`,
		),
	}
	p.Missions = filepath.Join(t.TempDir(), "missions.jsonl")
	st := mission.New(p.Missions)
	m := &mission.Mission{ID: "m1", Name: "MARKER-NAME"}
	m.AddMerge(mission.Merge{Tip: "t1", Branch: "MARKER-BRANCH", At: at(30, 0), Since: at(30, 0).Add(-2 * time.Hour)})
	m.AddMerge(mission.Merge{Tip: "t2", At: at(40, 0), Since: at(40, 0).Add(-30 * time.Minute)})
	st.Save(m)
	// A mission is saved whole each time, and the last line wins.
	m.AddMerge(mission.Merge{Tip: "t3", At: base.Add(2 * time.Hour)})
	st.Save(m)
	p.Metrics = filepath.Join(t.TempDir(), "metrics.jsonl")
	if err := metrics.Append(p.Metrics,
		metrics.Record{Kind: metrics.Daily, At: at(1, 0), Mission: "m1", Merged: 1, USD: 1, Overlaps: 1},
		metrics.Record{Kind: metrics.Daily, At: at(45, 0), Mission: "m1", Merged: 2, USD: 5, Overlaps: 3, Nudges: 1},
		metrics.Record{Kind: metrics.Daily, At: at(46, 0), Mission: "m3", USD: 1},
		metrics.Record{Kind: metrics.Daily, At: base.Add(-time.Hour), Mission: "m2", Merged: 9, USD: 90},
	); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTurnsAndConcurrency(t *testing.T) {
	r := Compute(fixture(t), base, base.Add(time.Hour))
	tn := r.Turns
	if tn == nil {
		t.Fatal("no turns section")
	}
	if tn.Finished != 6 || tn.Failed != 1 || tn.Prompts != 10 || tn.Agents != 3 {
		t.Errorf("counts: %+v", *tn)
	}
	if tn.Measured != 5 || tn.WorkingSeconds != 180 || f64(tn.MedianSeconds) != 30 || f64(tn.LongestSeconds) != 60 {
		t.Errorf("durations: %+v median %v longest %v", *tn, f64(tn.MedianSeconds), f64(tn.LongestSeconds))
	}
	if tn.PeakAgents != 2 || tn.AverageAgents != 0.05 {
		t.Errorf("peak %d average %v", tn.PeakAgents, tn.AverageAgents)
	}
	var src Source
	for _, s := range r.Sources {
		if s.File == "events.jsonl" {
			src = s
		}
	}
	if src.Skipped != 3 {
		t.Errorf("events.jsonl: %d lines skipped, want the 3 that are not events", src.Skipped)
	}
}

func TestWaits(t *testing.T) {
	r := Compute(fixture(t), base, base.Add(time.Hour))
	w := r.Waits
	if w == nil {
		t.Fatal("no waits section")
	}
	if w.Requests != 5 || w.Permission != 2 || w.Plan != 1 || w.Question != 1 || w.Other != 1 {
		t.Errorf("requests: %+v", *w)
	}
	// 39.5 s (A's permission, answered by the run at :40), 59.8 s (B's
	// question) and 60 s (A's last request); B's permission has no later run
	// and A's plan has none before A's next request.
	if w.Measured != 3 || f64(w.MedianSeconds) != 59.8 || f64(w.LongestSeconds) != 60 {
		t.Errorf("waits: measured %d median %v longest %v", w.Measured, f64(w.MedianSeconds), f64(w.LongestSeconds))
	}
}

func TestWaitsWithoutHooksLog(t *testing.T) {
	p := fixture(t)
	p.Hooks = filepath.Join(t.TempDir(), "hooks.log")
	w := Compute(p, base, base.Add(time.Hour)).Waits
	if w.Requests != 5 || w.Measured != 0 || w.MedianSeconds != nil {
		t.Errorf("without hooks.log: %+v", *w)
	}
}

func TestMessagesKeepYouApartFromDensOwnLines(t *testing.T) {
	r := Compute(fixture(t), base, base.Add(time.Hour))
	if m := r.Messages; m == nil || m.FromYou != 1 || m.MissionLines != 1 || m.FromApps != 1 {
		t.Errorf("messages: %+v", m)
	}
}

func TestMissionsAndMetrics(t *testing.T) {
	r := Compute(fixture(t), base, base.Add(time.Hour))
	g := r.Merges
	if g == nil || g.Count != 2 || f64(g.BranchHoursMedian) != 1.25 || f64(g.BranchHoursLongest) != 2 {
		t.Errorf("merges: %+v", g)
	}
	m := r.Metrics
	if m == nil || m.Missions != 2 || m.Merged != 2 || m.Overlaps != 3 || m.Nudges != 1 || f64(m.CostPerMergeUSD) != 3 {
		t.Errorf("metrics: %+v", m)
	}
}

func TestNothingRecorded(t *testing.T) {
	r := Compute(Paths{}, base, base.Add(time.Hour))
	if r.Turns != nil || r.Waits != nil || r.Messages != nil || r.Merges != nil || r.Metrics != nil {
		t.Errorf("sections from no files: %+v", r)
	}
	var out bytes.Buffer
	r.WriteText(&out)
	if n := strings.Count(out.String(), notRecorded); n < 8 {
		t.Errorf("only %d mentions of %q:\n%s", n, notRecorded, out.String())
	}
}

func TestNoneMerged(t *testing.T) {
	p := fixture(t)
	r := Compute(p, base.Add(3*time.Hour), base.Add(4*time.Hour))
	if r.Merges == nil || r.Merges.Count != 0 || r.Merges.BranchHoursMedian != nil {
		t.Errorf("merges: %+v", r.Merges)
	}
	if r.Metrics == nil || r.Metrics.CostPerMergeUSD != nil {
		t.Errorf("metrics: %+v", r.Metrics)
	}
	var out bytes.Buffer
	r.WriteText(&out)
	for _, want := range []string{"branch lifetime       not recorded", "cost per merge        not recorded (no merged change)", "merged pull requests  not recorded"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("text lacks %q:\n%s", want, out.String())
		}
	}
}

func TestReportHoldsNoText(t *testing.T) {
	p := fixture(t)
	r := Compute(p, base, base.Add(time.Hour))
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	r.WriteText(&out)
	all := string(raw) + out.String()
	for _, secret := range []string{"MARKER-", "Marker Critter", sessA, sessB, sessC, sessA[:8], filepath.Dir(p.Events), filepath.Dir(p.Chat), os.TempDir()} {
		if strings.Contains(all, secret) {
			t.Errorf("the report holds %q", secret)
		}
	}
}

func TestComputeWritesNothing(t *testing.T) {
	p := fixture(t)
	snapshot := func() string {
		var b strings.Builder
		for _, path := range []string{p.Events, p.Hooks, p.Chat, p.Missions, p.Metrics} {
			fi, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := os.ReadFile(path)
			entries, _ := os.ReadDir(filepath.Dir(path))
			fmt.Fprintf(&b, "%s %v %v %d %s %d\n", filepath.Base(path), fi.Mode(), fi.ModTime().UnixNano(), fi.Size(), raw, len(entries))
		}
		return b.String()
	}
	before := snapshot()
	Compute(p, base, base.Add(time.Hour))
	if snapshot() != before {
		t.Error("Compute changed a state file or its folder")
	}
}

func TestReadsAreBounded(t *testing.T) {
	dir := t.TempDir()
	var lines []string
	for i := range 50 {
		lines = append(lines, evLine(at(i, 0), events.TurnStart, sessA))
	}
	path := write(t, dir, "events.jsonl", lines...)
	fi, _ := os.Stat(path)

	defer func(old int64) { maxFileBytes = old }(maxFileBytes)
	maxFileBytes = fi.Size() / 4
	src := Source{File: "events.jsonl"}
	n := 0
	src.read(path, func(line []byte) (time.Time, bool) {
		var ev events.Event
		if json.Unmarshal(line, &ev) != nil {
			return time.Time{}, false
		}
		n++
		return ev.Time, true
	})
	if !src.Truncated || src.Skipped != 0 || n == 0 || n >= 50 {
		t.Errorf("truncated %v, %d lines skipped, %d events read of 50", src.Truncated, src.Skipped, n)
	}
	if src.Last == nil || !src.Last.Equal(at(49, 0)) {
		t.Errorf("the newest part was not read: last %v", src.Last)
	}

	// A line over the limit is skipped and the lines after it still read.
	maxFileBytes = 256 << 20
	long := write(t, dir, "long.jsonl", `{"pad":"`+strings.Repeat("x", maxLineBytes)+`"}`, evLine(at(1, 0), events.Done, sessA))
	src = Source{File: "long"}
	n = 0
	src.read(long, func(line []byte) (time.Time, bool) { n++; return time.Time{}, true })
	if src.Skipped != 1 || n != 1 || src.Lines != 1 {
		t.Errorf("long line: %d skipped, %d read", src.Skipped, n)
	}
}

func TestOnlyRegularFilesAreRead(t *testing.T) {
	dir := t.TempDir()
	real := write(t, dir, "real.jsonl", evLine(at(1, 0), events.Done, sessA))
	link := filepath.Join(dir, "link.jsonl")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("no symlinks:", err)
	}
	fifo := filepath.Join(dir, "fifo.jsonl")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("no fifo:", err)
	}
	for name, want := range map[string]string{"link.jsonl": "unreadable", "fifo.jsonl": "not a regular file", "": "missing", "gone.jsonl": "missing"} {
		src := Source{File: name}
		path := ""
		if name != "" {
			path = filepath.Join(dir, name)
		}
		src.read(path, func(line []byte) (time.Time, bool) { t.Errorf("%s: a line was read", name); return time.Time{}, true })
		if src.Problem != want || src.ok() {
			t.Errorf("%q: problem %q, want %q", name, src.Problem, want)
		}
	}
	src := Source{File: "real"}
	src.read(real, func(line []byte) (time.Time, bool) { return time.Time{}, true })
	if !src.ok() || src.Lines != 1 {
		t.Errorf("a regular file: %+v", src)
	}
}

func TestOldEventsAreReadFirst(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "events.jsonl", evLine(at(2, 0), events.Done, sessA))
	write(t, dir, "events.jsonl.1", evLine(at(1, 0), events.TurnStart, sessA))
	r := Compute(Paths{Events: path}, base, base.Add(time.Hour))
	if r.Turns == nil || r.Turns.Measured != 1 || r.Turns.WorkingSeconds != 60 {
		t.Errorf("a turn across the rotation: %+v", r.Turns)
	}
}

func TestParseWindow(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 500, time.UTC)
	ok := []struct {
		since, until string
		days         int
		from, to     time.Time
	}{
		{"", "", 0, now.Truncate(time.Second).Add(-7 * 24 * time.Hour), now.Truncate(time.Second)},
		{"", "", 2, now.Truncate(time.Second).Add(-48 * time.Hour), now.Truncate(time.Second)},
		{"2026-02-01T00:00:00Z", "2026-02-02T06:30:00Z", 0, time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 2, 2, 6, 30, 0, 0, time.UTC)},
		{"2026-02-01T00:00:00+02:00", "", 0, time.Date(2026, 1, 31, 22, 0, 0, 0, time.UTC), now.Truncate(time.Second)},
		{"2026-02-01", "2026-02-03 08:15", 0, time.Date(2026, 2, 1, 0, 0, 0, 0, time.Local), time.Date(2026, 2, 3, 8, 15, 0, 0, time.Local)},
	}
	for _, c := range ok {
		from, to, err := ParseWindow(c.since, c.until, c.days, now)
		if err != nil || !from.Equal(c.from) || !to.Equal(c.to) {
			t.Errorf("%+v: %v to %v, %v", c, from, to, err)
		}
	}
	for _, c := range []struct {
		since, until string
		days         int
	}{
		{"2026-02-01", "", 3}, {"", "2026-02-01", 3}, {"", "2026-02-01", 0}, {"", "", -1}, {"", "", 40000},
		{"yesterday", "", 0}, {"2026-02-01", "later", 0}, {"2026-02-02", "2026-02-01", 0}, {"2026-02-02", "2026-02-02", 0},
	} {
		if _, _, err := ParseWindow(c.since, c.until, c.days, now); err == nil {
			t.Errorf("%+v: accepted", c)
		}
	}
}
