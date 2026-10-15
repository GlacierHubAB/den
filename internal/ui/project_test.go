package ui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/mission"
)

// A folder's name is anyone's to choose: the project strip and the
// Overview's titles show it as one clean line.
func TestProjectNamesAreShownClean(t *testing.T) {
	m := missionModel()
	m.proj.path = filepath.Join(t.TempDir(), "overview.json")
	m.width, m.height, m.tab = 120, 30, tabOverview
	dir := filepath.Join(t.TempDir(), "evil\x1b[2J\x1b]0;owned\x07name")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := m.pinProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(p.Name, "\x1b\x07") || !strings.Contains(p.Name, "name") {
		t.Errorf("listed as %q", p.Name)
	}
	frame := m.View().Content
	if strings.Contains(frame, "\x1b[2J") || strings.Contains(frame, "\x1b]0;") || strings.Contains(frame, "\x07") {
		t.Errorf("the frame carries the folder's escapes: %q", frame)
	}
	if !strings.Contains(frame, "name") {
		t.Error("the project is not shown")
	}
}

func TestPinnedProjectIsListedWithNobodyOnIt(t *testing.T) {
	m := missionModel()
	m.proj.path = filepath.Join(t.TempDir(), "overview.json")
	dir := t.TempDir()
	p, err := m.pinProject(dir)
	if err != nil || p.Root != dir || len(p.Agents) != 0 {
		t.Fatalf("pin: %+v %v", p, err)
	}
	if m.proj.cur != dir {
		t.Errorf("pick = %q", m.proj.cur)
	}
	raw, _ := os.ReadFile(m.proj.path)
	if !strings.Contains(string(raw), `"pinned":["`+dir+`"]`) {
		t.Errorf("saved %s", raw)
	}
	m2 := missionModel()
	m2.proj.path = m.proj.path
	m2.loadProjectPick()
	m2.buildProjects()
	if _, ok := m2.projectByRoot(dir); !ok || m2.proj.cur != dir {
		t.Errorf("pin not restored: %+v cur %q", m2.proj.list, m2.proj.cur)
	}
	if !m.unpinProject() {
		t.Fatal("unpin")
	}
	if _, ok := m.projectByRoot(dir); ok || m.proj.cur == dir {
		t.Errorf("still listed after unpin: %+v", m.proj.list)
	}
	if _, err := m.pinProject(filepath.Join(dir, "missing")); err == nil {
		t.Error("a missing folder should fail")
	}
}

// An edit in another repository lists the agent there only while it is
// recent, or while it is the agent's latest edit.
func TestOldSideEditDoesNotKeepAgentOnProject(t *testing.T) {
	side, main := t.TempDir(), t.TempDir()
	for _, r := range []string{side, main} {
		gitRun(t, r, "init", "-q", "-b", "main")
	}
	now := time.Date(2026, 10, 11, 15, 30, 0, 0, time.UTC)
	edit := func(id, file string, at time.Time) []string {
		ts := at.Format(time.RFC3339)
		return []string{
			`{"type":"assistant","timestamp":"` + ts + `","message":{"content":[{"type":"tool_use","id":"` + id + `","name":"Write","input":{"file_path":"` + file + `"}}]}}`,
			`{"type":"user","timestamp":"` + ts + `","message":{"content":[{"type":"tool_result","tool_use_id":"` + id + `","content":"ok"}]},"toolUseResult":{"filePath":"` + file + `","structuredPatch":[{"oldStart":1,"newStart":1,"lines":["+x"]}]}}`,
		}
	}
	p := filepath.Join(t.TempDir(), "s.jsonl")
	lines := append(edit("t1", filepath.Join(side, "SKILL.md"), now.Add(-45*time.Minute)), edit("t2", filepath.Join(main, "doc.md"), now.Add(-40*time.Minute))...)
	os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
	a := fleet.Agent{Path: p}
	a.Session.StartedAt = now.Add(-2 * time.Hour).UnixMilli()

	if got := rootsAt(a, now); len(got) != 1 || got[0] != main {
		t.Errorf("45 minutes on = %v, want only the latest edit's repo %s", got, main)
	}
	if got := rootsAt(a, now.Add(-30*time.Minute)); len(got) != 2 {
		t.Errorf("15 minutes on = %v, want both repos", got)
	}
}

// An agent working in a checkout of its own (a scratch clone outside the
// usual projects) shows on the project of the live mission it serves, and
// there only; with its mission done, on its own checkout's.
func TestMissionAgentInOwnCheckoutShowsOnMissionProject(t *testing.T) {
	home, clone := t.TempDir(), t.TempDir()
	for _, r := range []string{home, clone} {
		gitRun(t, r, "init", "-q", "-b", "main")
	}
	m := missionModel()
	now := time.Now()
	live := &mission.Mission{ID: "m-land", Project: home, Name: "Land", Shape: mission.Once, State: mission.Running, Created: now, Updated: now}
	done := &mission.Mission{ID: "m-old", Project: home, Name: "Old", Shape: mission.Once, State: mission.Done, Created: now, Updated: now}
	m.missions.Save(live)
	m.missions.Save(done)
	for _, a := range []struct{ id, mission string }{{"quiet-panda", "m-land"}, {"tiny-moth", "m-old"}} {
		var ag fleet.Agent
		ag.Session = discover.Session{SessionID: a.id, CWD: clone, StartedAt: now.UnixMilli(), Alive: true}
		ag.HasGit = true
		m.base = append(m.base, ag)
		m.orch.chat = append(m.orch.chat, chatEntry{role: "card", card: &dispatch{ID: "c-" + a.id, SessionID: a.id, MissionID: a.mission, State: cardWorking}, at: now})
	}
	m.refresh()

	on := func(root string) []string {
		m.proj.cur = root
		var ids []string
		for _, a := range m.projectAgents() {
			ids = append(ids, a.Session.SessionID)
		}
		return ids
	}
	if got := on(home); !slices.Contains(got, "quiet-panda") || slices.Contains(got, "tiny-moth") {
		t.Errorf("mission project %s lists %v, want quiet-panda (live mission) and not tiny-moth (done)", home, got)
	}
	if got := on(clone); slices.Contains(got, "quiet-panda") || !slices.Contains(got, "tiny-moth") {
		t.Errorf("own checkout %s lists %v, want tiny-moth only", clone, got)
	}
}
