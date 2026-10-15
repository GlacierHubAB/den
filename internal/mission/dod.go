package mission

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/GlacierHubAB/den/internal/sanitize"
)

// A mission is a goal made of smaller goals, each owned by an agent, and
// done when its criteria are met. The orchestrator sets the criteria and
// the owners when it proposes; the criteria freeze when the human starts
// the mission. From then on the orchestrator only claims a criterion, with
// its evidence; the human confirms, and only the human changes the
// criteria's text. An owner grants nothing: it says who does what.

// Criterion states.
const (
	CritOpen    = "open"
	CritClaimed = "claimed" // the orchestrator says it is met, with evidence
	CritMet     = "met"     // the human confirmed it
)

// Sub-goal states as stored. "Needs you" is not one of them: the UI
// derives it from the owner's open inbox items.
const (
	GoalTodo    = "todo"
	GoalDoing   = "doing"
	GoalBlocked = "blocked"
	GoalDone    = "done"
)

// Caps on the definition of done and the sub-goals.
const (
	MaxCriteria   = 8
	MaxGoals      = 12
	maxCritText   = 300
	maxGoalText   = 200
	maxGoalNote   = 200
	maxEvidence   = 300
	maxRunSummary = 600
)

// Criterion is one line of the definition of done.
type Criterion struct {
	ID        string    `json:"id"` // c1..c8, Den's
	Text      string    `json:"text"`
	State     string    `json:"state"`
	Evidence  string    `json:"evidence,omitempty"` // the orchestrator's one line when it claimed it
	ClaimedAt time.Time `json:"claimed_at,omitzero"`
	MetAt     time.Time `json:"met_at,omitzero"`
}

// SubGoal is one of the smaller goals and who owns it.
type SubGoal struct {
	ID   string `json:"id"` // g1..g12, Den's
	Text string `json:"text"`
	// Owner is the owning session ("" while unresolved) on Host ("" for
	// this machine); Name is its critter name when assigned, kept for after
	// it has gone. Role is the team role to start when the owner is not a
	// running agent yet.
	Owner string `json:"owner,omitempty"`
	Host  string `json:"host,omitempty"`
	Name  string `json:"name,omitempty"`
	Role  string `json:"role,omitempty"`
	State string `json:"state"`
	Note  string `json:"note,omitempty"` // blocked on what, or what it produced
}

// Assigned reports whether the sub-goal has an owning session.
func (g SubGoal) Assigned() bool { return g.Owner != "" }

// Frozen reports whether the criteria's text is fixed: from the human's
// start on.
func (m *Mission) Frozen() bool { return m.State != Proposed && m.State != Dropped }

// SetCriteria sets the definition of done from texts, for a proposal only.
func (m *Mission) SetCriteria(texts []string) error {
	if m.Frozen() {
		return errors.New("the done criteria are fixed once the mission has started")
	}
	var out []Criterion
	for _, t := range texts {
		t = sanitize.Label(t, maxCritText)
		if t == "" {
			continue
		}
		if len(out) == MaxCriteria {
			return fmt.Errorf("at most %d done criteria", MaxCriteria)
		}
		out = append(out, Criterion{ID: "c" + strconv.Itoa(len(out)+1), Text: t, State: CritOpen})
	}
	if len(out) == 0 {
		return errors.New("set at least one done criterion")
	}
	m.Criteria = out
	m.syncDoneWhen()
	return nil
}

// syncDoneWhen keeps the old one-line field for older readers and the API.
func (m *Mission) syncDoneWhen() {
	var parts []string
	for _, c := range m.Criteria {
		parts = append(parts, c.Text)
	}
	if len(parts) > 0 {
		m.DoneWhen = strings.Join(parts, "; ")
	}
}

// Criterion finds a criterion by id.
func (m *Mission) Criterion(id string) *Criterion {
	id = strings.ToLower(strings.TrimSpace(id))
	for i := range m.Criteria {
		if m.Criteria[i].ID == id {
			return &m.Criteria[i]
		}
	}
	return nil
}

// Claim records the orchestrator's word that a criterion is met. A claim
// decides nothing: the human confirms.
func (m *Mission) Claim(id, evidence string, at time.Time) error {
	c := m.Criterion(id)
	switch {
	case c == nil:
		return fmt.Errorf("no criterion %q", sanitize.Label(id, 8))
	case c.State == CritMet:
		return fmt.Errorf("%s is already met", c.ID)
	}
	evidence = sanitize.Label(evidence, maxEvidence)
	if evidence == "" {
		return fmt.Errorf("claim %s with its evidence", c.ID)
	}
	c.State, c.Evidence, c.ClaimedAt = CritClaimed, evidence, at
	return nil
}

// SetMet is the human's word on one criterion: met or open again.
func (m *Mission) SetMet(id string, met bool, at time.Time) error {
	c := m.Criterion(id)
	if c == nil {
		return fmt.Errorf("no criterion %q", sanitize.Label(id, 8))
	}
	if met {
		c.State, c.MetAt = CritMet, at
		return nil
	}
	c.State, c.MetAt, c.Evidence, c.ClaimedAt = CritOpen, time.Time{}, "", time.Time{}
	return nil
}

// MeetAll is the human confirming the mission done: every criterion met.
func (m *Mission) MeetAll(at time.Time) {
	for i := range m.Criteria {
		if c := &m.Criteria[i]; c.State != CritMet {
			c.State, c.MetAt = CritMet, at
		}
	}
}

// ResetCriteria opens every criterion again: a schedule mission's next run.
func (m *Mission) ResetCriteria() {
	for i := range m.Criteria {
		c := &m.Criteria[i]
		c.State, c.Evidence, c.ClaimedAt, c.MetAt = CritOpen, "", time.Time{}, time.Time{}
	}
}

// ChangeCriterion sets a criterion's text: only after the human's ^Y on
// the orchestrator's request, or for a proposal.
func (m *Mission) ChangeCriterion(id, text string) error {
	c := m.Criterion(id)
	if c == nil {
		return fmt.Errorf("no criterion %q", sanitize.Label(id, 8))
	}
	text = sanitize.Label(text, maxCritText)
	if text == "" {
		return fmt.Errorf("%s needs its text", c.ID)
	}
	c.Text = text
	m.syncDoneWhen()
	return nil
}

// AllClaimed reports whether every criterion is claimed or met, with at
// least one claimed: the moment Den asks the human.
func (m *Mission) AllClaimed() bool {
	if len(m.Criteria) == 0 {
		return false
	}
	claimed := false
	for _, c := range m.Criteria {
		switch c.State {
		case CritClaimed:
			claimed = true
		case CritMet:
		default:
			return false
		}
	}
	return claimed
}

// CriteriaCounts is how many criteria are met, claimed and in all.
func (m *Mission) CriteriaCounts() (met, claimed, total int) {
	for _, c := range m.Criteria {
		switch c.State {
		case CritMet:
			met++
		case CritClaimed:
			claimed++
		}
	}
	return met, claimed, len(m.Criteria)
}

// Goal finds a sub-goal by id.
func (m *Mission) Goal(id string) *SubGoal {
	id = strings.ToLower(strings.TrimSpace(id))
	for i := range m.Goals {
		if m.Goals[i].ID == id {
			return &m.Goals[i]
		}
	}
	return nil
}

// AddGoal adds a sub-goal; owner fields are set by the caller, who
// resolves them.
func (m *Mission) AddGoal(g SubGoal) (*SubGoal, error) {
	if len(m.Goals) >= MaxGoals {
		return nil, fmt.Errorf("at most %d sub-goals", MaxGoals)
	}
	g.Text = sanitize.Label(g.Text, maxGoalText)
	if g.Text == "" {
		return nil, errors.New("a sub-goal needs its text")
	}
	g.ID = "g" + strconv.Itoa(m.nextGoal())
	g.Name, g.Role, g.Host = sanitize.Label(g.Name, maxLabel), sanitize.Label(g.Role, maxLabel), sanitize.Label(g.Host, maxLabel)
	g.Note = sanitize.Label(g.Note, maxGoalNote)
	if !validGoalState(g.State) {
		g.State = GoalTodo
	}
	m.Goals = append(m.Goals, g)
	return &m.Goals[len(m.Goals)-1], nil
}

// nextGoal is the next free sub-goal number: ids are never reused.
func (m *Mission) nextGoal() int {
	n := 0
	for _, g := range m.Goals {
		if k, err := strconv.Atoi(strings.TrimPrefix(g.ID, "g")); err == nil && k > n {
			n = k
		}
	}
	return n + 1
}

// GoalPatch is a change to a sub-goal; nil fields stay as they are.
type GoalPatch struct {
	Text, State, Note *string
}

// UpdateGoal applies a patch to a sub-goal.
func (m *Mission) UpdateGoal(id string, p GoalPatch) error {
	g := m.Goal(id)
	if g == nil {
		return fmt.Errorf("no sub-goal %q", sanitize.Label(id, 8))
	}
	if p.State != nil && !validGoalState(*p.State) {
		return fmt.Errorf("a sub-goal's state is todo, doing, blocked or done")
	}
	if p.Text != nil {
		t := sanitize.Label(*p.Text, maxGoalText)
		if t == "" {
			return fmt.Errorf("%s needs its text", g.ID)
		}
		g.Text = t
	}
	if p.State != nil {
		g.State = *p.State
	}
	if p.Note != nil {
		g.Note = sanitize.Label(*p.Note, maxGoalNote)
	}
	return nil
}

// SetOwner puts a session, or a role still to start, on a sub-goal.
func (m *Mission) SetOwner(id, session, host, name, role string) error {
	g := m.Goal(id)
	if g == nil {
		return fmt.Errorf("no sub-goal %q", sanitize.Label(id, 8))
	}
	g.Owner, g.Host = session, sanitize.Label(host, maxLabel)
	g.Name, g.Role = sanitize.Label(name, maxLabel), sanitize.Label(role, maxLabel)
	return nil
}

func validGoalState(s string) bool {
	switch s {
	case GoalTodo, GoalDoing, GoalBlocked, GoalDone:
		return true
	}
	return false
}

// GoalCounts is how many sub-goals are done and in all.
func (m *Mission) GoalCounts() (done, total int) {
	for _, g := range m.Goals {
		if g.State == GoalDone {
			done++
		}
	}
	return done, len(m.Goals)
}

// migrate brings a record from before criteria and sub-goals up to date:
// its done-when becomes the first criterion, its team roles and the
// sessions Den put on it become sub-goals. Written on the next save.
func (m *Mission) migrate() {
	if m.V >= recordVersion {
		return
	}
	m.V = recordVersion
	done := m.State == Done
	if len(m.Criteria) == 0 && m.DoneWhen != "" && !m.standingText() {
		m.Criteria = []Criterion{{ID: "c1", Text: sanitize.Label(m.DoneWhen, maxCritText), State: CritOpen}}
		if done {
			m.Criteria[0].State, m.Criteria[0].Evidence, m.Criteria[0].MetAt = CritMet, "done before criteria were kept", m.Updated
		}
	}
	if len(m.Goals) > 0 {
		return
	}
	state := GoalTodo
	if done || m.State == Failed {
		state = GoalDone
	}
	for _, t := range m.Team {
		if len(m.Goals) == MaxGoals {
			break
		}
		m.Goals = append(m.Goals, SubGoal{ID: "g" + strconv.Itoa(len(m.Goals)+1), Text: sanitize.Label(t.Role+"'s part", maxGoalText), Role: sanitize.Label(t.Role, maxLabel), State: state})
	}
	for _, a := range m.Assigned {
		if len(m.Goals) == MaxGoals {
			break
		}
		if a.Open() || !m.Live() {
			st := GoalDoing
			if !m.Live() {
				st = GoalDone
			}
			m.Goals = append(m.Goals, SubGoal{ID: "g" + strconv.Itoa(len(m.Goals)+1), Text: "work on the mission", Owner: a.Session, Host: a.Host, Name: a.Peer, Role: a.Role, State: st})
		}
	}
}

// standingText reports a done-when that says the mission never ends.
func (m *Mission) standingText() bool {
	d := strings.ToLower(strings.TrimSpace(m.DoneWhen))
	return d == "standing" || strings.HasPrefix(d, "never")
}

// clip caps what grows with use before a save: run summaries are built
// from agents' results.
func (m *Mission) clip() {
	for i := range m.Runs {
		if r := &m.Runs[i]; len(r.Summary) > maxRunSummary {
			r.Summary = sanitize.Label(r.Summary, maxRunSummary)
		}
	}
}
