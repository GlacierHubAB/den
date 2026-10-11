// Package mission holds Den's standing goals: a purpose in one sentence
// with constraints and a definition of done, run by the orchestrator in
// the shape it picks (once, loop, fan-out or schedule), with a log of runs.
package mission

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Shape is how the orchestrator runs a mission.
type Shape string

const (
	Once     Shape = "once"     // one dispatch, done when it reports back
	Loop     Shape = "loop"     // one agent iterates until the goal is met
	FanOut   Shape = "fanout"   // a lead with children in parallel, repeated until done
	Schedule Shape = "schedule" // Den fires a run on a cadence
)

// Glyph is the one-character mark of a shape.
func (s Shape) Glyph() string {
	switch s {
	case Loop:
		return "↻"
	case FanOut:
		return "⇶"
	case Schedule:
		return "⏲"
	}
	return "▶"
}

// Label is the shape's name as the UI prints it.
func (s Shape) Label() string {
	if s == FanOut {
		return "fan-out"
	}
	if s == "" {
		return "once"
	}
	return string(s)
}

// State is where a mission is.
type State string

const (
	Proposed State = "proposed" // the orchestrator's card, waiting for ^Y
	Running  State = "running"
	Waiting  State = "waiting" // a run needs the human
	Paused   State = "paused"  // no new run starts until resumed
	Done     State = "done"
	Failed   State = "failed"
	Dropped  State = "dropped" // a proposal the human declined
)

// Run is one pass of a mission: the orchestrator's dispatch for it.
type Run struct {
	N         int       `json:"n"`
	StartedAt time.Time `json:"started"`
	EndedAt   time.Time `json:"ended,omitempty"`
	Summary   string    `json:"summary,omitempty"`
	Cards     []string  `json:"cards,omitempty"` // dispatch card ids
}

// Mission is one standing goal on one project.
type Mission struct {
	ID          string    `json:"id"`
	Project     string    `json:"project"` // repository root
	Name        string    `json:"name"`    // a few words, for the row
	Text        string    `json:"text"`    // what the human typed
	Mission     string    `json:"mission"` // the purpose in one sentence
	DoneWhen    string    `json:"done_when"`
	Constraints []string  `json:"constraints,omitempty"`
	Shape       Shape     `json:"shape"`
	Cadence     string    `json:"cadence,omitempty"` // for Schedule
	Reason      string    `json:"reason,omitempty"`  // why that shape, one line
	Agents      []string  `json:"agents,omitempty"`  // critter names the orchestrator means to use
	State       State     `json:"state"`
	Created     time.Time `json:"created"`
	Updated     time.Time `json:"updated"`
	NextRun     time.Time `json:"next_run,omitempty"`
	Runs        []Run     `json:"runs,omitempty"`
}

// Live reports whether the mission still has work ahead of it.
func (m *Mission) Live() bool {
	return m.State == Running || m.State == Waiting || m.State == Paused
}

// Standing reports whether the mission never ends on its own.
func (m *Mission) Standing() bool {
	return m.Shape == Schedule || strings.EqualFold(strings.TrimSpace(m.DoneWhen), "standing") || strings.HasPrefix(strings.ToLower(m.DoneWhen), "never")
}

// CurrentRun is the run still going, if any.
func (m *Mission) CurrentRun() *Run {
	if n := len(m.Runs); n > 0 && m.Runs[n-1].EndedAt.IsZero() {
		return &m.Runs[n-1]
	}
	return nil
}

// StartRun opens a new run.
func (m *Mission) StartRun(now time.Time) *Run {
	m.Runs = append(m.Runs, Run{N: len(m.Runs) + 1, StartedAt: now})
	m.Updated = now
	return &m.Runs[len(m.Runs)-1]
}

// Store keeps the missions and the record on disk.
type Store struct {
	path  string
	items []*Mission
}

// New returns an empty store that records to path ("" keeps it in memory).
func New(path string) *Store { return &Store{path: path} }

// Load replays the record; the last line for an id wins.
func Load(path string) *Store {
	s := New(path)
	f, err := os.Open(path)
	if err != nil {
		return s
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var m Mission
		if json.Unmarshal(sc.Bytes(), &m) != nil || m.ID == "" {
			continue
		}
		s.put(&m)
	}
	return s
}

func (s *Store) put(m *Mission) {
	for i, o := range s.items {
		if o.ID == m.ID {
			s.items[i] = m
			return
		}
	}
	s.items = append(s.items, m)
}

// Save records the mission's current state. Best effort on disk.
func (s *Store) Save(m *Mission) {
	if s == nil {
		return
	}
	s.put(m)
	if s.path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	raw, err := json.Marshal(m)
	if err != nil {
		return
	}
	f.Write(append(raw, '\n'))
}

// Get finds a mission by id.
func (s *Store) Get(id string) *Mission {
	if s == nil {
		return nil
	}
	for _, m := range s.items {
		if m.ID == id {
			return m
		}
	}
	return nil
}

// All lists every mission, newest first.
func (s *Store) All() []*Mission {
	if s == nil {
		return nil
	}
	out := append([]*Mission(nil), s.items...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

// ForProject lists a project's missions, live ones first, then newest first.
func (s *Store) ForProject(root string) []*Mission {
	var out []*Mission
	for _, m := range s.All() {
		if m.Project == root && m.State != Dropped {
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		li, lj := out[i].Live(), out[j].Live()
		if li != lj {
			return li
		}
		return false
	})
	return out
}

// Running counts the missions in flight across projects.
func (s *Store) Running() int {
	if s == nil {
		return 0
	}
	n := 0
	for _, m := range s.items {
		if m.State == Running || m.State == Waiting {
			n++
		}
	}
	return n
}

// Path is the record on disk.
func Path() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "den", "missions.jsonl")
}

// NewID is a short id from the clock.
func NewID(now time.Time) string {
	return "m" + strconv.FormatInt(now.UnixMilli(), 36)
}

// ---- what the orchestrator says ----

var fence = regexp.MustCompile("(?s)```(?:json)?\\s*(\\{.*?\\})\\s*```")

// Proposal is the orchestrator's answer to "propose a mission".
type Proposal struct {
	Name        string   `json:"name"`
	Mission     string   `json:"mission"`
	DoneWhen    string   `json:"done_when"`
	Constraints []string `json:"constraints"`
	Shape       Shape    `json:"shape"`
	Cadence     string   `json:"cadence"`
	Reason      string   `json:"reason"`
	Agents      []string `json:"agents"`
}

// Update is the orchestrator's word on a running mission.
type Update struct {
	ID      string `json:"id"`
	State   string `json:"state"` // done, continue, waiting, failed
	Summary string `json:"summary"`
}

// ParseProposal finds a {"mission": {...}} block in the orchestrator's text.
func ParseProposal(text string) (Proposal, bool) {
	for _, m := range fence.FindAllStringSubmatch(text, -1) {
		var wrap struct {
			Mission *Proposal `json:"mission"`
		}
		if json.Unmarshal([]byte(m[1]), &wrap) == nil && wrap.Mission != nil && wrap.Mission.Mission != "" {
			p := *wrap.Mission
			p.Shape = Shape(strings.ToLower(strings.ReplaceAll(string(p.Shape), "-", "")))
			switch p.Shape {
			case Once, Loop, FanOut, Schedule:
			default:
				p.Shape = Once
			}
			return p, true
		}
	}
	return Proposal{}, false
}

// ParseUpdate finds a {"mission_update": {...}} block.
func ParseUpdate(text string) (Update, bool) {
	for _, m := range fence.FindAllStringSubmatch(text, -1) {
		var wrap struct {
			Update *Update `json:"mission_update"`
		}
		if json.Unmarshal([]byte(m[1]), &wrap) == nil && wrap.Update != nil && wrap.Update.ID != "" {
			return *wrap.Update, true
		}
	}
	return Update{}, false
}

// Strip removes the JSON blocks from the orchestrator's text so the chat
// shows only its words.
func Strip(text string) string {
	return strings.TrimSpace(fence.ReplaceAllString(text, ""))
}

// Apply fills the mission from a proposal.
func (m *Mission) Apply(p Proposal) {
	if p.Name != "" {
		m.Name = p.Name
	}
	m.Mission, m.DoneWhen, m.Constraints = p.Mission, p.DoneWhen, p.Constraints
	m.Shape, m.Cadence, m.Reason, m.Agents = p.Shape, p.Cadence, p.Reason, p.Agents
	if m.Name == "" {
		m.Name = shortName(m.Mission)
	}
}

func shortName(s string) string {
	words := strings.Fields(s)
	if len(words) > 6 {
		words = words[:6]
	}
	return strings.TrimRight(strings.Join(words, " "), ".,;:")
}

// Describe is the mission as the orchestrator is told it.
func (m *Mission) Describe() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Mission %s (%q) on project %s\n", m.ID, m.Name, m.Project)
	fmt.Fprintf(&b, "- mission: %s\n", m.Mission)
	if m.DoneWhen != "" {
		fmt.Fprintf(&b, "- done when: %s\n", m.DoneWhen)
	}
	if len(m.Constraints) > 0 {
		fmt.Fprintf(&b, "- constraints: %s\n", strings.Join(m.Constraints, "; "))
	}
	fmt.Fprintf(&b, "- shape: %s", m.Shape.Label())
	if m.Cadence != "" {
		fmt.Fprintf(&b, " (%s)", m.Cadence)
	}
	b.WriteString("\n")
	if len(m.Agents) > 0 {
		fmt.Fprintf(&b, "- agents: %s\n", strings.Join(m.Agents, ", "))
	}
	return b.String()
}

// ---- cadences ----

var (
	everyRe  = regexp.MustCompile(`(?i)^every\s+(\d+)\s*(m|min|minutes?|h|hours?|d|days?)$`)
	dailyRe  = regexp.MustCompile(`(?i)^daily(?:\s+at)?\s+(\d{1,2}):(\d{2})$`)
	weeklyRe = regexp.MustCompile(`(?i)^weekly(?:\s+on)?\s+(mon|tue|wed|thu|fri|sat|sun)[a-z]*(?:\s+at)?\s+(\d{1,2}):(\d{2})$`)
)

// Next is when a cadence fires after from. Forms: "every 30m", "every 2h",
// "every 1d", "daily 08:30", "weekly mon 09:00", or a five-field cron line
// with numbers, * and */n.
func Next(cadence string, from time.Time) (time.Time, error) {
	c := strings.TrimSpace(cadence)
	if m := everyRe.FindStringSubmatch(c); m != nil {
		n, _ := strconv.Atoi(m[1])
		if n <= 0 {
			return time.Time{}, fmt.Errorf("cadence %q: zero interval", cadence)
		}
		unit := time.Minute
		switch strings.ToLower(m[2][:1]) {
		case "h":
			unit = time.Hour
		case "d":
			unit = 24 * time.Hour
		}
		return from.Add(time.Duration(n) * unit), nil
	}
	if m := dailyRe.FindStringSubmatch(c); m != nil {
		h, _ := strconv.Atoi(m[1])
		mi, _ := strconv.Atoi(m[2])
		t := time.Date(from.Year(), from.Month(), from.Day(), h, mi, 0, 0, from.Location())
		if !t.After(from) {
			t = t.Add(24 * time.Hour)
		}
		return t, nil
	}
	if m := weeklyRe.FindStringSubmatch(c); m != nil {
		days := map[string]time.Weekday{"mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6, "sun": 0}
		wd := days[strings.ToLower(m[1])]
		h, _ := strconv.Atoi(m[2])
		mi, _ := strconv.Atoi(m[3])
		t := time.Date(from.Year(), from.Month(), from.Day(), h, mi, 0, 0, from.Location())
		for t.Weekday() != wd || !t.After(from) {
			t = t.Add(24 * time.Hour)
		}
		return t, nil
	}
	if fields := strings.Fields(c); len(fields) == 5 {
		return nextCron(fields, from)
	}
	return time.Time{}, fmt.Errorf("cadence %q: use \"every 30m\", \"daily 08:30\", \"weekly mon 09:00\" or a cron line", cadence)
}

// nextCron walks forward minute by minute, at most a year, to the first
// time every field matches.
func nextCron(fields []string, from time.Time) (time.Time, error) {
	match := func(field string, v int) (bool, error) {
		for _, part := range strings.Split(field, ",") {
			switch {
			case part == "*":
				return true, nil
			case strings.HasPrefix(part, "*/"):
				n, err := strconv.Atoi(part[2:])
				if err != nil || n <= 0 {
					return false, fmt.Errorf("bad cron step %q", part)
				}
				if v%n == 0 {
					return true, nil
				}
			default:
				lo, hi, ok := strings.Cut(part, "-")
				a, err := strconv.Atoi(lo)
				if err != nil {
					return false, fmt.Errorf("bad cron field %q", part)
				}
				b := a
				if ok {
					if b, err = strconv.Atoi(hi); err != nil {
						return false, fmt.Errorf("bad cron field %q", part)
					}
				}
				if v >= a && v <= b {
					return true, nil
				}
			}
		}
		return false, nil
	}
	t := from.Truncate(time.Minute).Add(time.Minute)
	for i := 0; i < 366*24*60; i++ {
		ok := true
		for j, v := range []int{t.Minute(), t.Hour(), t.Day(), int(t.Month()), int(t.Weekday())} {
			hit, err := match(fields[j], v)
			if err != nil {
				return time.Time{}, err
			}
			if !hit {
				ok = false
				break
			}
		}
		if ok {
			return t, nil
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}, fmt.Errorf("cron line never fires within a year")
}
