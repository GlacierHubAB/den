package ui

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/mission"
	"github.com/GlacierHubAB/den/internal/theme"
	"github.com/GlacierHubAB/den/internal/tree"
)

// projectsOf maps each agent to the roots of the projects that list it.
func projectsOf(m Model) map[string][]string {
	out := map[string][]string{}
	for _, p := range m.proj.list {
		for _, sid := range p.Agents {
			out[sid] = append(out[sid], p.Root)
		}
	}
	return out
}

// TestAgentShowsUnderOneProject: the live mission Den last put it on (or
// its lead's), else the mission Den last put it on, else its checkout.
func TestAgentShowsUnderOneProject(t *testing.T) {
	home, other, past, clone := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	for _, r := range []string{home, other, past, clone} {
		gitRun(t, r, "init", "-q", "-b", "main")
	}
	m := missionModel()
	m.base = nil
	now := time.Now()
	for _, a := range []struct {
		id  string
		git bool
	}{{"on-mission", true}, {"on-two", true}, {"done-before", true}, {"plain", true}, {"no-git", false}, {"its-child", true}} {
		var ag fleet.Agent
		ag.Name = a.id
		ag.Session = discover.Session{SessionID: a.id, CWD: clone, StartedAt: now.UnixMilli(), Alive: true}
		ag.HasGit = a.git
		if !a.git {
			ag.Session.CWD = t.TempDir()
		}
		m.base = append(m.base, ag)
	}
	newMission := func(id, root string, st mission.State) *mission.Mission {
		ms := &mission.Mission{ID: id, Name: id, Shape: mission.Once, State: st, Created: now.Add(-time.Hour), Updated: now}
		ms.SetProject(root)
		m.missions.Save(ms)
		return ms
	}
	homeM, otherM, pastM := newMission("m-home", home, mission.Running), newMission("m-other", other, mission.Running), newMission("m-past", past, mission.Done)
	m.refresh()
	card := func(id, sid string, ms *mission.Mission) *dispatch {
		d := &dispatch{ID: id, SessionID: sid, Agent: sid, State: cardWorking, SentAt: now}
		m.attachCardTo(d, ms.ID)
		m.orch.chat = append(m.orch.chat, chatEntry{role: "card", card: d, at: now})
		return d
	}
	first := card("c1", "on-mission", homeM)
	card("c2", "on-two", otherM)
	card("c3", "on-two", homeM)
	// on-two went to the other project's mission first, then to home's.
	otherM.Assigned[0].Since, homeM.Assigned[1].Since = now.Add(-30*time.Minute), now.Add(-10*time.Minute)
	pastM.Assign(mission.Assignment{Session: "done-before", Since: now.Add(-2 * time.Hour)})
	pastM.Release("done-before", "", now.Add(-time.Hour))
	m.learnEdge(tree.Edge{Parent: "on-mission", Child: "its-child", Brief: "help", Source: "spawn"})
	m.refresh()

	want := map[string]string{"on-mission": home, "on-two": home, "done-before": past, "plain": clone, "its-child": home}
	got := projectsOf(m)
	for sid, root := range want {
		if len(got[sid]) != 1 || got[sid][0] != root {
			t.Errorf("%s is under %v, want only %s", sid, got[sid], root)
		}
	}
	if len(got["no-git"]) != 0 || m.proj.loose != 1 {
		t.Errorf("no-git is under %v, loose %d", got["no-git"], m.proj.loose)
	}
	if a := homeM.Assigned[0]; a.Session != "on-mission" || a.Peer != "on-mission" || !a.Open() {
		t.Errorf("assignment %+v", a)
	}

	// Its card ends: the assignment closes, and the agent stays with the
	// mission Den last put it on.
	first.State = cardDone
	m.refresh()
	if homeM.Assigned[0].Open() {
		t.Error("the card ended and the assignment is still open")
	}
	if got := projectsOf(m)["on-mission"]; len(got) != 1 || got[0] != home {
		t.Errorf("after its card: %v", got)
	}
}

// TestMissionMoveIsAskedAndHumanOnly: /mission move only asks; ^N keeps the
// mission where it is, ^Y moves it and notes it. The question shows both
// names cleaned, and nothing the orchestrator says moves a mission.
func TestMissionMoveIsAskedAndHumanOnly(t *testing.T) {
	m := missionModel()
	m.proj.path = filepath.Join(t.TempDir(), "overview.json")
	base := t.TempDir()
	home, evil := filepath.Join(base, "home"), filepath.Join(base, "evil\x1b[31mred")
	for _, d := range []string{home, evil} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := m.pinProject(d); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	ms := &mission.Mission{ID: "m1", Name: "Ship", Shape: mission.Once, State: mission.Running, Created: now, Updated: now}
	ms.SetProject(home)
	m.missions.Save(ms)
	m.ov.open = ms.ID

	m.applyUpdate(mission.Update{ID: "m1", State: "continue", Summary: "move to " + evil})
	if ms.Project != home {
		t.Fatal("an update from the orchestrator moved the mission")
	}

	move := func() *inbox.Item {
		t.Helper()
		mm, _ := m.command("/mission move " + evil)
		m = mm.(Model)
		var it *inbox.Item
		for _, x := range m.store.Items() {
			if x.Kind == inbox.MissionMove && x.State == inbox.Waiting {
				it = x
			}
		}
		if it == nil {
			t.Fatalf("no move question (flash %q)", m.flash)
		}
		if ms.Project != home {
			t.Fatal("moved before the human answered")
		}
		text := it.Title + "\n" + strings.Join(it.Body, "\n")
		if strings.Contains(text, "\x1b") || !strings.Contains(text, "from home") || !strings.Contains(text, "to   evil") {
			t.Errorf("question:\n%q", text)
		}
		return it
	}
	m.tab, m.inboxSel = tabInbox, move().ID
	mm, _ := m.answer("deny")
	m = mm.(Model)
	if ms.Project != home || len(ms.History) != 0 {
		t.Fatalf("^N moved it: %q (flash %q)", ms.Project, m.flash)
	}
	m.tab, m.inboxSel = tabInbox, move().ID
	mm, _ = m.answer("allow")
	m = mm.(Model)
	if ms.Project != evil || strings.Contains(ms.ProjectName, "\x1b") || len(ms.History) != 1 || !strings.Contains(ms.History[0].What, "moved from home to evil") {
		t.Fatalf("^Y: %q %q %+v (flash %q)", ms.Project, ms.ProjectName, ms.History, m.flash)
	}
}

// TestFleetGroupingIsRemembered: /fleet project and /fleet flat, kept in
// overview.json with the pick.
func TestFleetGroupingIsRemembered(t *testing.T) {
	m := missionModel()
	m.proj.path = filepath.Join(t.TempDir(), "overview.json")
	mm, _ := m.command("/fleet project")
	m = mm.(Model)
	if !m.proj.byProject || m.tab != tabFleet {
		t.Fatal("not by project")
	}
	m2 := missionModel()
	m2.proj.path = m.proj.path
	m2.loadProjectPick()
	if !m2.proj.byProject {
		raw, _ := os.ReadFile(m.proj.path)
		t.Fatalf("not remembered: %s", raw)
	}
	mm, _ = m.command("/fleet flat")
	m = mm.(Model)
	m2.loadProjectPick()
	if m.proj.byProject || m2.proj.byProject {
		t.Error("/fleet flat not remembered")
	}
}

// TestFleetByProjectGolden: the Fleet under project headers, trees whole
// under their root's project, the orchestrator's a level up, and a "no
// project" group last.
func TestFleetByProjectGolden(t *testing.T) {
	volatile := regexp.MustCompile(`\d{2}:\d{2}|(\b|m)\d+[smhd]\b`)
	m := Model{st: newStyles(theme.Deepharness), base: sample(), store: inbox.New(), width: 120, height: 40, tab: tabFleet, hooksReady: true, tree: newTreeState()}
	m.input = newInput(m.st)
	m.colors = map[string]string{}
	for i := range m.base {
		m.base[i].Session.SessionID = strings.ToLower(strings.ReplaceAll(m.base[i].Name, " ", "-"))
	}
	m.missions = mission.New("")
	// The view's ages read the real clock: three hours ago reads "3h" on
	// any day, where a fixed date grew a digit on the tenth day (#138).
	at := time.Now().Add(-3 * time.Hour)
	for _, x := range []struct{ id, root, sid string }{{"m1", "/home/x/tasklog", "mossy-lemur"}, {"m2", "/home/x/den", "fuzzy-sprite"}} {
		ms := &mission.Mission{ID: x.id, Name: x.id, Shape: mission.Once, State: mission.Running, Created: at, Updated: at}
		ms.SetProject(x.root)
		ms.StartRun(at)
		m.missions.Save(ms)
		d := &dispatch{ID: "c-" + x.id, SessionID: x.sid, Agent: x.sid, State: cardWorking, SentAt: at}
		m.attachCardTo(d, ms.ID)
		m.orch.chat = append(m.orch.chat, chatEntry{role: "card", card: d, at: at})
	}
	m.learnEdge(tree.Edge{Parent: "mossy-lemur", Child: "snug-capybara", Brief: "mockup", Source: "spawn", At: at})
	m.learnEdge(tree.Edge{Parent: tree.Orchestrator, Child: "fuzzy-sprite", Brief: "den work", Source: "dispatch", At: at})
	m.proj.byProject = true
	m.refresh()
	got := volatile.ReplaceAllStringFunc(m.View().Content, func(v string) string {
		if strings.HasPrefix(v, "m") {
			return "m#"
		}
		return "#"
	})
	plain := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(got, "")
	at1, at2, at3, snug := strings.Index(plain, "TASKLOG · 2"), strings.Index(plain, "DEN · 1"), strings.Index(plain, "NO PROJECT · 2"), strings.Index(plain, "Snug Capybara")
	if at1 < 0 || at2 < at1 || at3 < at2 || snug < at1 || snug > at2 {
		t.Errorf("want TASKLOG · 2 (Snug Capybara under its lead), DEN · 1, NO PROJECT · 2 in that order:\n%s", plain)
	}
	if p, ok := m.projectByRoot("/home/x/tasklog"); !ok || len(p.Agents) != 2 {
		t.Errorf("tasklog lists %+v: Snug Capybara works for its lead's mission", p.Agents)
	}
	path := filepath.Join("testdata", "fleet_by_project.golden")
	if *updateGolden {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("the Fleet by project changed\n--- got\n%s\n--- want\n%s", got, want)
	}
}
