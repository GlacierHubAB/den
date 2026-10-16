package mission

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)

func TestCriteriaAreSetForAProposalAndFrozenAfterTheStart(t *testing.T) {
	m := &Mission{ID: "m1", State: Proposed}
	if err := m.SetCriteria([]string{"  ", ""}); err == nil {
		t.Fatal("no criteria accepted")
	}
	many := make([]string, MaxCriteria+1)
	for i := range many {
		many[i] = fmt.Sprintf("criterion %d", i)
	}
	if err := m.SetCriteria(many); err == nil {
		t.Fatal("more than the cap accepted")
	}
	if err := m.SetCriteria([]string{"Tests pass", "Docs\u202e updated\x1b[31m"}); err != nil {
		t.Fatal(err)
	}
	if len(m.Criteria) != 2 || m.Criteria[1].ID != "c2" || m.Criteria[1].Text != "Docs updated" {
		t.Fatalf("criteria not sanitized: %+v", m.Criteria)
	}
	if m.DoneWhen != "Tests pass; Docs updated" {
		t.Errorf("done-when not kept: %q", m.DoneWhen)
	}
	m.State = Running
	if err := m.SetCriteria([]string{"weaker"}); err == nil || m.Criteria[0].Text != "Tests pass" {
		t.Fatal("criteria changed after the start")
	}
}

func TestClaimsDecideNothingAndTheHumanConfirms(t *testing.T) {
	m := &Mission{ID: "m1", State: Proposed}
	m.SetCriteria([]string{"a", "b"})
	m.State = Running
	if err := m.Claim("c1", "", t0); err == nil {
		t.Fatal("claim without evidence accepted")
	}
	if err := m.Claim("c9", "x", t0); err == nil {
		t.Fatal("claim of an unknown criterion accepted")
	}
	if err := m.Claim("C1", "PR #97 merged", t0); err != nil {
		t.Fatal(err)
	}
	if m.AllClaimed() {
		t.Fatal("all claimed with c2 open")
	}
	m.Claim("c2", "tests green", t0)
	if !m.AllClaimed() || m.State != Running {
		t.Fatalf("all claimed = %v, state %s", m.AllClaimed(), m.State)
	}
	if met, claimed, total := m.CriteriaCounts(); met != 0 || claimed != 2 || total != 2 {
		t.Errorf("counts %d %d %d", met, claimed, total)
	}
	m.MeetAll(t0)
	if met, _, _ := m.CriteriaCounts(); met != 2 || m.AllClaimed() {
		t.Errorf("after confirm: %+v", m.Criteria)
	}
	if err := m.Claim("c1", "again", t0); err == nil {
		t.Error("claim of a met criterion accepted")
	}
	m.SetMet("c1", false, t0)
	if c := m.Criterion("c1"); c.State != CritOpen || c.Evidence != "" {
		t.Errorf("unmet: %+v", c)
	}
}

func TestSubGoalsAreCappedAndKeepTheirIDs(t *testing.T) {
	m := &Mission{ID: "m1"}
	for i := 0; i < MaxGoals; i++ {
		if _, err := m.AddGoal(SubGoal{Text: fmt.Sprintf("goal %d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.AddGoal(SubGoal{Text: "one more"}); err == nil {
		t.Fatal("more sub-goals than the cap")
	}
	if _, err := (&Mission{}).AddGoal(SubGoal{Text: " \x1b "}); err == nil {
		t.Fatal("empty sub-goal accepted")
	}
	m.Goals = append(m.Goals[:1], m.Goals[2:]...) // g2 gone
	m.Goals = m.Goals[:len(m.Goals)-1]
	g, err := m.AddGoal(SubGoal{Text: "new", State: "bogus"})
	if err != nil || g.ID != "g12" || g.State != GoalTodo {
		t.Fatalf("new goal %+v %v", g, err)
	}
	bad := "later"
	if err := m.UpdateGoal("g1", GoalPatch{State: &bad}); err == nil {
		t.Error("unknown state accepted")
	}
	st, note := GoalBlocked, "waits for #97"
	if err := m.UpdateGoal("g1", GoalPatch{State: &st, Note: &note}); err != nil || m.Goal("g1").State != GoalBlocked {
		t.Errorf("update: %v %+v", err, m.Goal("g1"))
	}
	m.SetOwner("g1", "sid-1", "", "Zippy Wombat", "coding agent")
	if g := m.Goal("g1"); !g.Assigned() || g.Name != "Zippy Wombat" {
		t.Errorf("owner %+v", g)
	}
}

func TestScheduleRunsOpenTheCriteriaAgain(t *testing.T) {
	m := &Mission{ID: "m1", Shape: Schedule, State: Proposed}
	m.SetCriteria([]string{"report sent"})
	m.State = Running
	m.StartRun(t0)
	m.Claim("c1", "sent at 08:30", t0)
	m.StartRun(t0.Add(24 * time.Hour))
	if m.Criteria[0].State != CritOpen {
		t.Errorf("criterion after the next run: %+v", m.Criteria[0])
	}
}

func TestOldRecordsMigrate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missions.jsonl")
	recs := []string{
		// running with a done-when, a team and an open assignment
		`{"id":"m1","project":"/r","name":"A","mission":"a","done_when":"The cause is explained","shape":"fanout","state":"running","team":[{"role":"lead"}],"assigned":[{"session":"s1","peer":"Plucky Puffin","since":"2026-10-15T10:00:00Z"}],"created":"2026-10-15T10:00:00Z","updated":"2026-10-15T10:00:00Z"}`,
		// done
		`{"id":"m2","project":"/r","name":"B","mission":"b","done_when":"Shipped","shape":"once","state":"done","assigned":[{"session":"s2","peer":"Snug Capybara","since":"2026-10-12T10:00:00Z","until":"2026-10-12T11:00:00Z"}],"created":"2026-10-12T10:00:00Z","updated":"2026-10-12T12:00:00Z"}`,
		// standing, no criteria
		`{"id":"m3","project":"/r","name":"C","mission":"c","done_when":"standing","shape":"loop","state":"running","created":"2026-10-12T10:00:00Z","updated":"2026-10-12T10:00:00Z"}`,
		// a proposal the orchestrator never answered
		`{"id":"m4","project":"/r","text":"hm","state":"proposed","created":"2026-10-12T10:00:00Z","updated":"2026-10-12T10:00:00Z"}`,
	}
	os.WriteFile(path, []byte(strings.Join(recs, "\n")+"\n"), 0o600)
	s := Load(path)
	m1, m2, m3, m4 := s.Get("m1"), s.Get("m2"), s.Get("m3"), s.Get("m4")
	if len(m1.Criteria) != 1 || m1.Criteria[0].Text != "The cause is explained" || m1.Criteria[0].State != CritOpen {
		t.Errorf("m1 criteria %+v", m1.Criteria)
	}
	if len(m1.Goals) != 2 || m1.Goals[0].Role != "lead" || m1.Goals[1].Owner != "s1" || m1.Goals[1].Name != "Plucky Puffin" || m1.Goals[1].State != GoalDoing {
		t.Errorf("m1 goals %+v", m1.Goals)
	}
	if m2.Criteria[0].State != CritMet || m2.Criteria[0].Evidence == "" || len(m2.Goals) != 1 || m2.Goals[0].State != GoalDone {
		t.Errorf("m2 %+v %+v", m2.Criteria, m2.Goals)
	}
	if len(m3.Criteria) != 0 || len(m4.Criteria) != 0 || m4.State != Proposed {
		t.Errorf("m3 %+v m4 %+v", m3.Criteria, m4)
	}
	if m1.V != recordVersion {
		t.Errorf("version %d", m1.V)
	}
	// A migrated record keeps its sub-goals after a save and a reload:
	// migration does not run twice.
	m1.Goals[0].State = GoalDone
	s.Save(m1)
	if g := Load(path).Get("m1").Goals; len(g) != 2 || g[0].State != GoalDone {
		t.Errorf("after reload %+v", g)
	}
}

func TestRecordIsPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "den")
	path := filepath.Join(dir, "missions.jsonl")
	New(path).Save(&Mission{ID: "m1", State: Proposed})
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("new record mode %v %v", fi.Mode(), err)
	}
	if di, _ := os.Stat(dir); di.Mode().Perm() != 0o700 {
		t.Errorf("new dir mode %v", di.Mode())
	}
	os.Chmod(path, 0o644)
	Load(path)
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("an old record was left at %v", fi.Mode())
	}
}

func TestLongRecordIsCompactedOnLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missions.jsonl")
	s := New(path)
	m := &Mission{ID: "m1", State: Running, Mission: strings.Repeat("x", 2000)}
	o := &Mission{ID: "m2", State: Done}
	s.Save(o)
	for i := 0; i < 200; i++ {
		m.Name = fmt.Sprintf("v%d", i)
		s.Save(m)
	}
	if fi, _ := os.Stat(path); fi.Size() <= compactSize {
		t.Fatalf("record too small for the test: %d", fi.Size())
	}
	got := Load(path)
	raw, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("%d lines after compaction", len(lines))
	}
	if got.Get("m1").Name != "v199" || got.Get("m2") == nil || !strings.Contains(string(raw), `"name":"v199"`) {
		t.Errorf("lost the last state: %s", raw[:200])
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("compacted mode %v", fi.Mode())
	}
	if again := Load(path); again.Get("m1").Name != "v199" {
		t.Error("compacted record does not load")
	}
	ents, _ := os.ReadDir(filepath.Dir(path))
	if len(ents) != 1 {
		t.Errorf("left files behind: %v", ents)
	}
}

func TestRunSummariesAreCappedOnSave(t *testing.T) {
	m := &Mission{ID: "m1", Runs: []Run{{N: 1, Summary: strings.Repeat("word ", 1000)}}}
	New("").Save(m)
	if n := len([]rune(m.Runs[0].Summary)); n > maxRunSummary {
		t.Errorf("summary %d runes", n)
	}
}

func TestRunsAreCappedKeepingTheLast(t *testing.T) {
	m := &Mission{ID: "m1", Shape: Schedule, State: Running}
	for i := 0; i < maxKeptRuns+30; i++ {
		r := m.StartRun(t0.Add(time.Duration(i) * time.Hour))
		r.EndedAt = r.StartedAt.Add(time.Minute)
	}
	New("").Save(m)
	if len(m.Runs) != maxKeptRuns || m.Runs[0].N != 31 || m.Runs[len(m.Runs)-1].N != maxKeptRuns+30 {
		t.Errorf("%d runs, first %d, last %d", len(m.Runs), m.Runs[0].N, m.Runs[len(m.Runs)-1].N)
	}
}

// The record is writable by any process of the same user: what comes back
// from it is cleaned and capped again before Den draws it or tells the
// orchestrator.
func TestRecordIsCleanedAgainOnLoad(t *testing.T) {
	rlo, lri, esc, bel := string(rune(0x202e)), string(rune(0x2066)), string(rune(0x1b)), string(rune(0x07))
	path := filepath.Join(t.TempDir(), "missions.jsonl")
	m := Mission{ID: "m1", V: recordVersion, State: Running, Name: "ok" + rlo + "name" + esc + "]8;;http://x" + bel,
		Criteria: []Criterion{{ID: "zz", Text: "c" + lri + "one", State: "bogus", Evidence: "e" + esc + "[2Jv"}},
		Goals:    []SubGoal{{ID: "g1", Text: "a"}, {ID: "g1", Text: "dup"}, {ID: "g9999", Text: "big", State: "weird", Note: strings.Repeat("n", 1000)}},
	}
	for i := 0; i < MaxCriteria+3; i++ {
		m.Criteria = append(m.Criteria, Criterion{Text: "more"})
	}
	for i := 0; i < maxListed+5; i++ {
		m.Constraints = append(m.Constraints, "x")
	}
	raw, _ := json.Marshal(m)
	os.WriteFile(path, append(raw, '\n'), 0o600)
	got := Load(path).Get("m1")
	if strings.ContainsAny(got.Name, rlo+esc+bel) || strings.Contains(got.Name, "http") {
		t.Errorf("name %q", got.Name)
	}
	c := got.Criteria[0]
	if len(got.Criteria) != MaxCriteria || c.ID != "c1" || c.State != CritOpen || strings.ContainsAny(c.Text+c.Evidence, lri+esc) {
		t.Errorf("criteria %d %+v", len(got.Criteria), c)
	}
	ids := map[string]bool{}
	for _, g := range got.Goals {
		if ids[g.ID] || !goalID.MatchString(g.ID) {
			t.Errorf("goal id %q", g.ID)
		}
		ids[g.ID] = true
	}
	if g := got.Goals[2]; g.State != GoalTodo || len([]rune(g.Note)) > maxGoalNote {
		t.Errorf("goal %+v", g)
	}
	if len(got.Constraints) != maxListed {
		t.Errorf("%d constraints", len(got.Constraints))
	}
}
