package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/mission"
	"github.com/GlacierHubAB/den/internal/orch"
	"github.com/GlacierHubAB/den/internal/templates"
)

func templateModel(t *testing.T) Model {
	t.Setenv("DEN_CONFIG", filepath.Join(t.TempDir(), "config.toml"))
	m := missionModel()
	m.orch.starting = true // no process: prompts queue
	return m
}

func run(t *testing.T, m Model, cmd string) Model {
	t.Helper()
	mm, _ := m.command(cmd)
	return mm.(Model)
}

func TestTemplateListShowsBuiltinsAndUserOnes(t *testing.T) {
	m := templateModel(t)
	templates.Save(templates.Path(), templates.Template{Name: "nightly", About: "run the suite", Mission: "Run it", Shape: mission.Schedule, Cadence: "daily 02:00"})
	m = run(t, m, "/template")
	out := ansi.Strip(m.View().Content)
	for _, want := range []string{"Templates", "code-review", "bugfix", "research", "docs-pass", "nightly", "yours", "built in", "/template save <name>"} {
		if !strings.Contains(out, want) {
			t.Errorf("list lacks %q:\n%s", want, out)
		}
	}
	mm, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if mm.(Model).ov.listing {
		t.Error("Esc did not close the list")
	}
}

func TestTemplateSeedsAProposalAndStartsNothing(t *testing.T) {
	m := templateModel(t)
	dir := t.TempDir()
	m = run(t, m, "/template code-review "+dir+" and its tests")
	if m.ov.proposal == "" || m.tab != tabOverview {
		t.Fatalf("no proposal: %+v", m.ov)
	}
	ms := m.missions.Get(m.ov.proposal)
	if ms.State != mission.Proposed || len(ms.Runs) != 0 || ms.Template != "code-review" || ms.Shape != mission.FanOut || len(ms.Team) != 3 {
		t.Fatalf("mission = %+v", ms)
	}
	if !strings.Contains(ms.Mission, dir+" and its tests") || strings.Contains(ms.Mission, "{target}") {
		t.Errorf("purpose = %q", ms.Mission)
	}
	if len(m.orch.pending) != 1 {
		t.Fatalf("pending = %q", m.orch.pending)
	}
	p := m.orch.pending[0]
	for _, want := range []string{"from a template", `Template "code-review"`, "team: security auditor on sonnet, under review lead", "propose_mission and id " + ms.ID} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q:\n%s", want, p)
		}
	}
	// The picked folder became the project.
	if m.proj.cur == "/home/x/tasklog" {
		t.Errorf("project did not follow the target folder: %q", m.proj.cur)
	}
	out := ansi.Strip(m.View().Content)
	for _, want := range []string{"Mission proposal", "PROPOSAL", "review lead", "security auditor", "template", "code-review", "^Y"} {
		if !strings.Contains(out, want) {
			t.Errorf("card lacks %q:\n%s", want, out)
		}
	}
	// Den seeded the criteria and the roles' sub-goals from the template.
	if len(ms.Criteria) != 1 || len(ms.Goals) != 3 || ms.Goals[0].Role != "review lead" {
		t.Errorf("seeded criteria %+v goals %+v", ms.Criteria, ms.Goals)
	}
	// The orchestrator adapts it through its tool, by the id; the template
	// mark stays.
	proc, _ := liveOrchestrator(t)
	m.orch.proc, m.orch.origins = proc, []string{originDen}
	d := callDen(t, &m, bridge.ProposeEvent, bridge.ProposeInput{ID: ms.ID, Project: ms.Project, Name: "Review demo", Goal: "Review demo.",
		Criteria: []string{"one report"}, Goals: []bridge.GoalInput{{Text: "Lead the review", Owner: "review lead"}}, Shape: "fanout",
		Team: []bridge.TeamInput{{Role: "review lead", Model: "opus"}}}, orchChildPID)
	m.orch.proc, m.orch.origins = nil, nil
	ms = m.missions.Get(ms.ID)
	if d.Behavior != "allow" || ms.Name != "Review demo" || len(ms.Team) != 1 || ms.Template != "code-review" {
		t.Errorf("after the refinement %+v: %+v", d, ms)
	}
	m.startMission()
	if ms.State != mission.Running || len(ms.Runs) != 1 {
		t.Errorf("^Y did not start it: %+v", ms)
	}
}

func TestTemplateCommandErrors(t *testing.T) {
	m := templateModel(t)
	for cmd, want := range map[string]string{
		"/template nope x":        "No template called nope",
		"/template bugfix":        "What is it for?",
		"/template save":          "Name it",
		"/template save Bad Name": "Name it",
		"/template save fine":     "Open or select a mission",
	} {
		got := run(t, m, cmd)
		if !strings.Contains(got.flash, want) || !got.flashErr {
			t.Errorf("%q: flash = %q", cmd, got.flash)
		}
	}
	if len(m.missions.All()) != 0 {
		t.Error("an error left a mission behind")
	}
}

func TestTemplateSaveKeepsShapeAndTeam(t *testing.T) {
	m := templateModel(t)
	ms := &mission.Mission{ID: "m1", Project: m.proj.cur, Name: "Keep green", Mission: "Keep the tests green.", DoneWhen: "standing", Shape: mission.Loop,
		State: mission.Running, Team: []mission.Member{{Role: "coding agent", Model: "sonnet"}}}
	m.missions.Save(ms)
	m.ov.open = ms.ID
	m = run(t, m, "/template save keep-green")
	if !strings.Contains(m.flash, "Saved template keep-green") || m.flashErr {
		t.Fatalf("flash = %q", m.flash)
	}
	list, err := templateList()
	got, ok := templates.Find(list, "keep-green")
	if err != nil || !ok || got.Shape != mission.Loop || got.Team[0].Role != "coding agent" || got.Builtin {
		t.Fatalf("saved = %+v err=%v", got, err)
	}
	if raw, _ := os.ReadFile(templates.Path()); !strings.Contains(string(raw), `"coding agent"`) {
		t.Errorf("file = %s", raw)
	}
	if m = run(t, m, "/template save bugfix"); !m.flashErr || !strings.Contains(m.flash, "built in") {
		t.Errorf("overwrote a built-in: %q", m.flash)
	}
}

func TestOrchestratorHearsTheTemplateNames(t *testing.T) {
	m := templateModel(t)
	out := orch.ComposeWith(nil, m.templateInfos(), "start a code review of ~/code/demo-app")
	for _, want := range []string{"<den_templates>", "- code-review:", "- bugfix:", "</den_templates>"} {
		if !strings.Contains(out, want) {
			t.Errorf("turn lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(orch.Compose(nil, "hi"), "den_templates") {
		t.Error("Compose without templates still lists them")
	}
}
