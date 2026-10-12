package ui

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/skills"
)

func skillsItem(m Model) *inbox.Item {
	for _, it := range m.store.Items() {
		if it.Kind == inbox.Skills {
			return it
		}
	}
	return nil
}

// fakeInstall records the approval instead of downloading anything.
func fakeInstall(t *testing.T) *[]skills.Approval {
	t.Helper()
	var got []skills.Approval
	skillsInstall = func(_ context.Context, a skills.Approval) []skills.Outcome {
		got = append(got, a)
		var out []skills.Outcome
		for _, s := range a.Skills {
			out = append(out, skills.Outcome{Name: s.Name})
		}
		return out
	}
	t.Cleanup(func() {
		skillsInstall = func(ctx context.Context, a skills.Approval) []skills.Outcome {
			return skills.Install(ctx, a, skills.ProvenancePath())
		}
	})
	return &got
}

func skillsRequest(in bridge.SkillsInput) *bridge.Request {
	payload, _ := json.Marshal(in)
	return bridge.TestRequest(bridge.Envelope{Event: bridge.SkillsEvent, Payload: payload})
}

func TestSkillsOfferListsRepoCommitLicenseAndWaitsForYes(t *testing.T) {
	got := fakeInstall(t)
	m := closeModel(t)
	proj := t.TempDir()
	req := skillsRequest(bridge.SkillsInput{Role: "ui designer", Scope: "project", Dir: proj, AskedBy: "orchestrator", Reason: "new design work"})
	m.handleRequest(req)
	it := skillsItem(m)
	if it == nil || !it.Answerable() {
		t.Fatal("no skills question in the inbox")
	}
	if _, done := req.Decided(); done {
		t.Fatal("answered before the human did")
	}
	cat, _ := skills.Load()
	want, _ := cat.Skill("frontend-design")
	body := strings.Join(it.Body, "\n")
	for _, s := range []string{"frontend-design · anthropics/skills @ " + want.Commit + " · Apache-2.0", want.Reason, "for this project: " + filepath.Join(proj, ".claude", "skills"), "asked by orchestrator · new design work"} {
		if !strings.Contains(body, s) {
			t.Errorf("body lacks %q:\n%s", s, body)
		}
	}
	m.tab = tabInbox
	m.selectItem(it)
	mm, cmd := m.answer("allow")
	m = mm.(Model)
	var done skillsDoneMsg
	for _, msg := range runBatch(cmd) {
		if d, ok := msg.(skillsDoneMsg); ok {
			done = d
		}
	}
	m.finishSkills(done)
	if len(*got) != 1 || (*got)[0].Target.Root != proj || (*got)[0].ApprovedBy == "" || (*got)[0].AskedBy != "orchestrator" {
		t.Fatalf("approval = %+v", *got)
	}
	d, ok := req.Decided()
	if !ok || d.Behavior != "allow" || d.Message != "installed frontend-design" {
		t.Errorf("the asker heard %+v %v", d, ok)
	}
}

func TestSkillsOfferDeclinedInstallsNothing(t *testing.T) {
	got := fakeInstall(t)
	m := closeModel(t)
	req := skillsRequest(bridge.SkillsInput{Role: "coding agent", Scope: "project", Dir: t.TempDir(), AskedBy: "orchestrator"})
	m.handleRequest(req)
	mm, _ := m.answer("deny")
	m = mm.(Model)
	if len(*got) != 0 {
		t.Error("installed after ^N")
	}
	if d, ok := req.Decided(); !ok || d.Behavior != "deny" || !strings.Contains(d.Message, "declined") {
		t.Errorf("decline = %+v %v", d, ok)
	}
}

func TestSkillsOfferRefusedAtOnce(t *testing.T) {
	fakeInstall(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cases := map[string]bridge.SkillsInput{
		"no match":                 {Role: "assistant", Scope: "project", Dir: t.TempDir()},
		"not in catalogue":         {Role: "coding agent", Skills: []string{"evil"}, Scope: "project", Dir: t.TempDir()},
		"bad dir":                  {Role: "coding agent", Scope: "project", Dir: "/nonexistent/x"},
		"agent root outside state": {Role: "coding agent", Scope: "agent", Dir: t.TempDir(), Peer: "x", Root: "/home/someone/.claude"},
		"agent root climbs":        {Role: "coding agent", Scope: "agent", Dir: t.TempDir(), Peer: "x", Root: filepath.Join(skills.StateDir(), "skills", "agents", "x-1", "..", "..", "..")},
	}
	for what, in := range cases {
		m := closeModel(t)
		req := skillsRequest(in)
		m.handleRequest(req)
		if skillsItem(m) != nil {
			t.Errorf("%s: an offer reached the inbox", what)
		}
		if d, ok := req.Decided(); !ok || d.Behavior != "deny" {
			t.Errorf("%s: not refused at once: %+v", what, d)
		}
	}
	// A proper agent folder is accepted.
	m := closeModel(t)
	root := skills.AgentRoot("ui-dev", closeNow())
	m.handleRequest(skillsRequest(bridge.SkillsInput{Role: "ui designer", Scope: "agent", Dir: t.TempDir(), Peer: "ui-dev", Root: root, AskedBy: "orchestrator"}))
	it := skillsItem(m)
	if it == nil || !strings.HasPrefix(it.Title, "Start ui-dev (ui designer) with 1 skill?") {
		t.Fatalf("agent offer: %+v", it)
	}
	if !strings.Contains(strings.Join(it.Body, "\n"), "for this agent only") {
		t.Errorf("body: %q", it.Body)
	}
}

// runBatch runs a command and the commands of any batch it returns, and
// collects every message that is not a batch.
func runBatch(c tea.Cmd) []tea.Msg {
	if c == nil {
		return nil
	}
	switch v := c().(type) {
	case tea.BatchMsg:
		var out []tea.Msg
		for _, x := range v {
			out = append(out, runBatch(x)...)
		}
		return out
	case nil:
		return nil
	default:
		return []tea.Msg{v}
	}
}
