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

	"github.com/GlacierHubAB/den/internal/sanitize"
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
	Cost      *RunCost  `json:"cost,omitempty"`  // what the run used, recorded when it ended
}

// Mission is one standing goal on one project.
type Mission struct {
	ID      string `json:"id"`
	Project string `json:"project"` // repository root
	// ProjectName is the project's name as Den shows it, set from the
	// root when the mission is made. Only the human's /mission move
	// changes Project and ProjectName; the orchestrator never does.
	ProjectName string    `json:"project_name,omitempty"`
	Name        string    `json:"name"`    // a few words, for the row
	Text        string    `json:"text"`    // what the human typed
	Mission     string    `json:"mission"` // the purpose in one sentence
	DoneWhen    string    `json:"done_when"`
	Constraints []string  `json:"constraints,omitempty"`
	Shape       Shape     `json:"shape"`
	Cadence     string    `json:"cadence,omitempty"` // for Schedule
	Reason      string    `json:"reason,omitempty"`  // why that shape, one line
	Agents      []string  `json:"agents,omitempty"`  // critter names the orchestrator means to use
	Team        []Member  `json:"team,omitempty"`    // roles to start, leads before their people
	State       State     `json:"state"`
	Created     time.Time `json:"created"`
	Updated     time.Time `json:"updated"`
	NextRun     time.Time `json:"next_run,omitempty"`
	Runs        []Run     `json:"runs,omitempty"`
	Budget      Budget    `json:"budget,omitzero"`

	Template string `json:"template,omitempty"` // the template it was seeded from

	// V is the record's version: 2 has the criteria and sub-goals.
	V int `json:"v,omitempty"`
	// Criteria are the definition of done, frozen once the human starts the
	// mission; Goals are the smaller goals with their owners.
	Criteria []Criterion `json:"criteria,omitempty"`
	Goals    []SubGoal   `json:"goals,omitempty"`
	// Assigned are the sessions Den put on the mission, oldest first;
	// History is what happened to the mission itself, the last few.
	Assigned []Assignment `json:"assigned,omitempty"`
	History  []Event      `json:"history,omitempty"`
}

// Assignment is one session Den put on a mission: a dispatch to it, or a
// spawn for it. Den writes these, never the orchestrator or an agent.
type Assignment struct {
	Session string    `json:"session"`
	Host    string    `json:"host,omitempty"` // Den's name for the remote host, "" for this machine
	Peer    string    `json:"peer,omitempty"` // the agent's name when it was assigned
	Role    string    `json:"role,omitempty"`
	Since   time.Time `json:"since"`
	Until   time.Time `json:"until,omitzero"` // when its card ended or the session went
}

// Open reports whether the assignment still holds.
func (a Assignment) Open() bool { return a.Until.IsZero() }

// Event is one line of a mission's history.
type Event struct {
	At   time.Time `json:"at"`
	What string    `json:"what"`
}

// Caps on what a mission keeps: its record is rewritten whole on each save.
const (
	maxAssigned = 200
	maxHistory  = 50
	maxLabel    = 80
)

// ProjectLabel is the name a project at root goes by: its folder's name,
// one clean line of at most 80 characters.
func ProjectLabel(root string) string {
	if root == "" {
		return ""
	}
	return sanitize.Label(filepath.Base(root), maxLabel)
}

// SetProject puts a new mission in the project at root.
func (m *Mission) SetProject(root string) {
	m.Project, m.ProjectName = root, ProjectLabel(root)
}

// Note adds a line to the history, keeping the last maxHistory.
func (m *Mission) Note(at time.Time, what string) {
	m.History = append(m.History, Event{At: at, What: sanitize.Line(what)})
	if n := len(m.History) - maxHistory; n > 0 {
		m.History = append([]Event(nil), m.History[n:]...)
	}
}

// MoveTo moves the mission to the project at root and notes it. Only the
// human's /mission move calls it.
func (m *Mission) MoveTo(root string, at time.Time) {
	from := m.ProjectName
	m.SetProject(root)
	m.Updated = at
	m.Note(at, "moved from "+orNone(from)+" to "+orNone(m.ProjectName)+" by you")
}

func orNone(s string) string {
	if s == "" {
		return "no project"
	}
	return s
}

// Assign records that Den put a session on the mission. A session already
// on it keeps its entry, with the name and role brought up to date.
func (m *Mission) Assign(a Assignment) {
	a.Peer, a.Role = sanitize.Label(a.Peer, maxLabel), sanitize.Label(a.Role, maxLabel)
	a.Host = sanitize.Label(a.Host, maxLabel)
	a.Until = time.Time{}
	for i := range m.Assigned {
		if o := &m.Assigned[i]; o.Open() && o.Session == a.Session && o.Host == a.Host {
			if a.Peer != "" {
				o.Peer = a.Peer
			}
			if a.Role != "" {
				o.Role = a.Role
			}
			return
		}
	}
	m.Assigned = append(m.Assigned, a)
	// Over the cap, the oldest closed entries go first, then the oldest.
	for len(m.Assigned) > maxAssigned {
		drop := 0
		for i, o := range m.Assigned {
			if !o.Open() {
				drop = i
				break
			}
		}
		m.Assigned = append(m.Assigned[:drop:drop], m.Assigned[drop+1:]...)
	}
}

// Release closes the session's open assignment; it reports whether there
// was one.
func (m *Mission) Release(session, host string, at time.Time) bool {
	done := false
	for i := range m.Assigned {
		if o := &m.Assigned[i]; o.Open() && o.Session == session && o.Host == host {
			o.Until, done = at, true
		}
	}
	return done
}

// Member is a role the orchestrator means to start for the mission.
type Member struct {
	Role  string `json:"role"`
	Model string `json:"model,omitempty"`
	Under string `json:"under,omitempty"` // the role it reports to, "" for the orchestrator
}

// Leads lists the members who report to the orchestrator, in order.
func Leads(team []Member) []Member { return Under(team, "") }

// Under lists the members who report to the given role, in order.
func Under(team []Member, role string) []Member {
	var out []Member
	for _, m := range team {
		u := strings.ToLower(strings.TrimSpace(m.Under))
		if u == "orchestrator" || u == "den" {
			u = ""
		}
		if strings.EqualFold(u, role) {
			out = append(out, m)
		}
	}
	return out
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

// StartRun opens a new run. A schedule mission's criteria say what each
// run must achieve, so they open again.
func (m *Mission) StartRun(now time.Time) *Run {
	if m.Shape == Schedule && len(m.Runs) > 0 {
		m.ResetCriteria()
	}
	m.Runs = append(m.Runs, Run{N: len(m.Runs) + 1, StartedAt: now})
	m.Updated = now
	return &m.Runs[len(m.Runs)-1]
}

// recordVersion is what a record written now carries.
const recordVersion = 2

// Store keeps the missions and the record on disk.
type Store struct {
	path  string
	items []*Mission
}

// New returns an empty store that records to path ("" keeps it in memory).
func New(path string) *Store { return &Store{path: path} }

// Compaction: a record over compactSize holding more than compactRatio
// lines per mission is rewritten on load with one line per mission.
const (
	compactSize  = 256 << 10
	compactRatio = 4
)

// Load replays the record; the last line for an id wins. The file is made
// private if it was not, and rewritten whole when it has grown long.
func Load(path string) *Store {
	s := New(path)
	f, err := os.Open(path)
	if err != nil {
		return s
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Mode().Perm()&0o077 != 0 {
		_ = f.Chmod(0o600)
	}
	lines := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		lines++
		var m Mission
		if json.Unmarshal(sc.Bytes(), &m) != nil || m.ID == "" {
			continue
		}
		if m.ProjectName == "" {
			// A record from before project names: named from its root,
			// written with the name on its next save.
			m.ProjectName = ProjectLabel(m.Project)
		}
		m.resanitize()
		m.migrate()
		s.put(&m)
	}
	if fi, err := f.Stat(); err == nil && fi.Size() > compactSize && lines > compactRatio*len(s.items) {
		_ = s.compact()
	}
	return s
}

// compact rewrites the record with one line per mission: a temporary file
// beside it, synced, then renamed over it.
func (s *Store) compact() error {
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".missions-*.jsonl")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	w := bufio.NewWriter(tmp)
	for _, m := range s.items {
		raw, err := json.Marshal(m)
		if err != nil {
			tmp.Close()
			return err
		}
		w.Write(append(raw, '\n'))
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
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
	m.V = recordVersion
	m.clip()
	s.put(m)
	if s.path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
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

// Update is the orchestrator's word on a running mission.
type Update struct {
	ID      string `json:"id"`
	State   string `json:"state"` // done, continue, waiting, failed
	Summary string `json:"summary"`
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

// Describe is the mission as the orchestrator is told it.
func (m *Mission) Describe() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Mission %s (%q) on project %s\n", m.ID, m.Name, m.Project)
	fmt.Fprintf(&b, "- goal: %s\n", m.Mission)
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
	// The criteria and sub-goals as the human and the orchestrator set them:
	// never a sub-goal's note or a claim's evidence, which may carry what an
	// agent said, since this goes unfenced into Den's own turns.
	for _, c := range m.Criteria {
		fmt.Fprintf(&b, "- done criterion %s: %s (%s)\n", c.ID, c.Text, c.State)
	}
	for _, g := range m.Goals {
		owner := "unassigned"
		switch {
		case g.Name != "":
			owner = "owner " + g.Name
		case g.Role != "":
			owner = "role to start " + g.Role
		}
		fmt.Fprintf(&b, "- sub-goal %s: %s · %s · %s\n", g.ID, g.Text, owner, g.State)
	}
	for _, t := range m.Team {
		line := "- team: " + t.Role
		if t.Model != "" {
			line += " on " + t.Model
		}
		if u := Under(m.Team, t.Role); len(u) > 0 {
			var roles []string
			for _, x := range u {
				roles = append(roles, x.Role)
			}
			line += ", leading " + strings.Join(roles, ", ")
		}
		if t.Under != "" && !strings.EqualFold(t.Under, "orchestrator") {
			line += ", under " + t.Under
		}
		b.WriteString(line + "\n")
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
