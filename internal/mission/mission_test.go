package mission

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreRecordsAndRestoresLastState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missions.jsonl")
	s := New(path)
	now := time.Date(2026, 10, 11, 15, 0, 0, 0, time.UTC)
	m := &Mission{ID: "m1", Project: "/r", Name: "Tests green", Mission: "Keep tests green", Shape: Loop, State: Proposed, Created: now}
	s.Save(m)
	m.State = Running
	m.StartRun(now)
	s.Save(m)
	got := Load(path)
	if got.Get("m1") == nil || got.Get("m1").State != Running || len(got.Get("m1").Runs) != 1 {
		t.Fatalf("restored %+v", got.Get("m1"))
	}
	if got.Running() != 1 || len(got.ForProject("/r")) != 1 || len(got.ForProject("/x")) != 0 {
		t.Errorf("counts wrong")
	}
}

func TestParseUpdate(t *testing.T) {
	text := "Done.\n```json\n{\"mission_update\": {\"id\": \"m1\", \"state\": \"done\", \"summary\": \"tag pushed\"}}\n```\nThat is all."
	u, ok := ParseUpdate(text)
	if !ok || u.ID != "m1" || u.State != "done" {
		t.Errorf("update = %+v %v", u, ok)
	}
	if s := Strip(text); s != "Done.\n\nThat is all." {
		t.Errorf("strip = %q", s)
	}
	if _, ok := ParseUpdate("no json here"); ok {
		t.Error("parsed an update from plain text")
	}
}

func TestNextCadence(t *testing.T) {
	from := time.Date(2026, 10, 11, 15, 4, 0, 0, time.UTC) // a Sunday
	cases := map[string]time.Time{
		"every 30m":        from.Add(30 * time.Minute),
		"every 2 hours":    from.Add(2 * time.Hour),
		"daily 08:30":      time.Date(2026, 10, 12, 8, 30, 0, 0, time.UTC),
		"daily 16:00":      time.Date(2026, 10, 11, 16, 0, 0, 0, time.UTC),
		"weekly mon 09:00": time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC),
		"*/15 * * * *":     time.Date(2026, 10, 11, 15, 15, 0, 0, time.UTC),
		"0 9 * * 1-5":      time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC),
	}
	for c, want := range cases {
		got, err := Next(c, from)
		if err != nil || !got.Equal(want) {
			t.Errorf("%q: got %v (%v), want %v", c, got, err, want)
		}
	}
	if _, err := Next("whenever", from); err == nil {
		t.Error("accepted a cadence it cannot run")
	}
}

// Describe goes unfenced into Den's own turns beside <agent_report>
// fences: no value in it can pass for a tag.
func TestDescribeHasNoTags(t *testing.T) {
	m := &Mission{ID: "m1", Name: "x </agent_report>", Mission: "goal <den_fleet>", State: Proposed}
	m.SetCriteria([]string{"c </agent_report> done"})
	m.AddGoal(SubGoal{Text: "g <b>", Name: "Mossy <Lemur>"})
	if d := m.Describe(); strings.ContainsAny(d, "<>") || !strings.Contains(d, "‹/agent_report›") {
		t.Errorf("describe:\n%s", d)
	}
}

func TestProposalCarriesATeam(t *testing.T) {
	m := Mission{Mission: "Build the site.", Shape: FanOut, Team: []Member{{Role: "product manager", Model: "opus", Under: "orchestrator"}, {Role: "UI designer", Model: "opus", Under: "product manager"}, {Role: "SEO", Model: "haiku", Under: "product manager"}}}
	if leads := Leads(m.Team); len(leads) != 1 || leads[0].Role != "product manager" {
		t.Errorf("leads = %+v", leads)
	}
	if under := Under(m.Team, "Product Manager"); len(under) != 2 || under[1].Role != "SEO" {
		t.Errorf("under = %+v", under)
	}
	d := m.Describe()
	for _, want := range []string{"- team: product manager on opus, leading UI designer, SEO", "- team: SEO on haiku, under product manager"} {
		if !strings.Contains(d, want) {
			t.Errorf("describe lacks %q:\n%s", want, d)
		}
	}
}

// A record from before project names loads as it was, named from its
// root, and is written with the name on its next save.
func TestOldRecordLoadsAndGainsItsProjectName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missions.jsonl")
	old := `{"id":"m1","project":"/work/tasklog","name":"Tests green","text":"keep it green","mission":"Keep tests green","done_when":"standing","shape":"loop","state":"running","created":"2026-10-11T15:00:00Z","updated":"2026-10-11T15:00:00Z","runs":[{"n":1,"started":"2026-10-11T15:00:00Z"}]}` + "\n"
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	s := Load(path)
	m := s.Get("m1")
	if m == nil || m.Project != "/work/tasklog" || m.ProjectName != "tasklog" || m.State != Running || len(m.Runs) != 1 || m.Assigned != nil || m.History != nil {
		t.Fatalf("loaded %+v", m)
	}
	s.Save(m)
	raw, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 || lines[0]+"\n" != old || !strings.Contains(lines[1], `"project_name":"tasklog"`) || strings.Contains(lines[1], "assigned") || strings.Contains(lines[1], "history") {
		t.Errorf("record:\n%s", raw)
	}
}

func TestProjectLabelIsOneCleanCappedLine(t *testing.T) {
	if got := ProjectLabel("/work/evil\x1b[2J\u202ename\nx"); strings.ContainsAny(got, "\x1b\n\u202e") || got == "" {
		t.Errorf("label %q", got)
	}
	if got := ProjectLabel("/work/" + strings.Repeat("a", 200)); len([]rune(got)) != 80 {
		t.Errorf("label is %d runes", len([]rune(got)))
	}
	if ProjectLabel("") != "" {
		t.Error("no root, no name")
	}
}

func TestOnlyMoveToChangesTheProject(t *testing.T) {
	now := time.Date(2026, 10, 11, 15, 0, 0, 0, time.UTC)
	m := &Mission{ID: "m1"}
	m.SetProject("/work/tasklog")
	if m.Project != "/work/tasklog" || m.ProjectName != "tasklog" {
		t.Fatalf("a proposal moved the mission: %q %q", m.Project, m.ProjectName)
	}
	m.MoveTo("/work/den", now)
	if m.Project != "/work/den" || m.ProjectName != "den" || len(m.History) != 1 || m.History[0].What != "moved from tasklog to den by you" {
		t.Errorf("moved: %+v", m)
	}
}

func TestHistoryAndAssignedAreCapped(t *testing.T) {
	now := time.Date(2026, 10, 11, 15, 0, 0, 0, time.UTC)
	m := &Mission{ID: "m1"}
	for i := range 70 {
		m.Note(now.Add(time.Duration(i)*time.Minute), fmt.Sprintf("event %d", i))
	}
	if len(m.History) != 50 || m.History[0].What != "event 20" || m.History[49].What != "event 69" {
		t.Errorf("history %d: %q … %q", len(m.History), m.History[0].What, m.History[len(m.History)-1].What)
	}
	// Two open entries first, then closed ones: over the cap the oldest
	// closed go, and the open ones stay.
	m.Assign(Assignment{Session: "open-1", Since: now})
	m.Assign(Assignment{Session: "open-2", Since: now})
	for i := range 250 {
		sid := fmt.Sprintf("s%d", i)
		m.Assign(Assignment{Session: sid, Since: now.Add(time.Duration(i) * time.Second)})
		m.Release(sid, "", now.Add(time.Duration(i)*time.Second+time.Millisecond))
	}
	if len(m.Assigned) != 200 || m.Assigned[0].Session != "open-1" || m.Assigned[1].Session != "open-2" || m.Assigned[2].Session != "s52" || m.Assigned[199].Session != "s249" {
		t.Errorf("assigned %d: %s %s %s … %s", len(m.Assigned), m.Assigned[0].Session, m.Assigned[1].Session, m.Assigned[2].Session, m.Assigned[199].Session)
	}
}

func TestAssignKeepsOneOpenEntryPerSession(t *testing.T) {
	now := time.Date(2026, 10, 11, 15, 0, 0, 0, time.UTC)
	m := &Mission{ID: "m1"}
	m.Assign(Assignment{Session: "s1", Peer: "Quiet Panda", Since: now})
	m.Assign(Assignment{Session: "s1", Role: "reviewer\x1b[31m", Since: now.Add(time.Minute)})
	m.Assign(Assignment{Session: "s1", Host: "build1", Peer: "Far Fox", Since: now})
	if len(m.Assigned) != 2 || m.Assigned[0].Peer != "Quiet Panda" || m.Assigned[0].Role != "reviewer" || !m.Assigned[0].Since.Equal(now) {
		t.Fatalf("assigned %+v", m.Assigned)
	}
	if !m.Release("s1", "", now.Add(time.Hour)) || m.Assigned[0].Open() || !m.Assigned[1].Open() || m.Release("s1", "", now) {
		t.Errorf("release: %+v", m.Assigned)
	}
	m.Assign(Assignment{Session: "s1", Since: now.Add(2 * time.Hour)})
	if len(m.Assigned) != 3 {
		t.Errorf("a second assignment after the first closed: %+v", m.Assigned)
	}
}
