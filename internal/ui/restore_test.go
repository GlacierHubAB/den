package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/closer"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/mission"
	"github.com/GlacierHubAB/den/internal/roster"
	"github.com/GlacierHubAB/den/internal/spawn"
	"github.com/GlacierHubAB/den/internal/theme"
	"github.com/GlacierHubAB/den/internal/tree"
)

var restoreT0 = time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)

// fakeTranscripts gives each session a transcript in a scratch Claude Code
// directory, its last turn at the given context size.
func fakeTranscripts(t *testing.T, tokens map[string]int) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	proj := filepath.Join(dir, "projects", "-home-u-code-demo-app")
	os.MkdirAll(proj, 0o755)
	for id, n := range tokens {
		model := "claude-sonnet-5-5"
		if id == "mossy-lemur" {
			model = "claude-haiku-4-5" // as the roster remembers it
		}
		line := fmt.Sprintf(`{"type":"assistant","message":{"model":%q,"usage":{"input_tokens":10,"cache_read_input_tokens":%d,"cache_creation_input_tokens":0}}}`, model, n-10)
		os.WriteFile(filepath.Join(proj, id+".jsonl"), []byte(line+"\n"), 0o644)
	}
}

// fakeRestoreRun records what would open and answers as if each session
// registered under the id it had (or newID for a fork).
type fakeRestoreRun struct {
	reqs  []spawn.Request
	newID string
}

func (f *fakeRestoreRun) run(_ context.Context, req spawn.Request) (spawn.Result, error) {
	f.reqs = append(f.reqs, req)
	id := req.Resume
	if req.Fork {
		id = f.newID
	}
	return spawn.Result{Dir: req.Dir, Name: req.Name, SessionID: id, Registered: true}, nil
}

// restoreModel is a Den just started after a reboot: the roster remembers
// Snug Capybara leading Mossy Lemur on a mission, Comet Newt closed through
// Den and Maple Owl that ended on its own an hour before Den stopped. Only
// Fuzzy Sprite runs now.
func restoreModel(t *testing.T) (Model, *fakeRestoreRun) {
	t.Helper()
	restoreNow = func() time.Time { return restoreT0 }
	fake := &fakeRestoreRun{}
	restoreRun = fake.run
	t.Cleanup(func() { restoreNow, restoreRun = time.Now, spawn.Run })
	fakeTranscripts(t, map[string]int{"snug-capybara": 123510, "mossy-lemur": 40000, "comet-newt": 1000, "maple-owl": 1000})

	r := &roster.Roster{}
	before := restoreT0.Add(-3 * time.Hour)
	r.Observe([]roster.Agent{
		{SessionID: "snug-capybara", Peer: "release-lead", Critter: "Snug Capybara", Role: "release lead", Model: "claude-sonnet-5-5", CWD: "/home/u/code/demo-app", Parent: tree.Orchestrator, Brief: "lead the release", Mission: "m1", Spawned: true},
		{SessionID: "mossy-lemur", Peer: "notes", Critter: "Mossy Lemur", Role: "coding agent", Model: "claude-haiku-4-5", CWD: "/home/u/code/demo-app", Parent: "snug-capybara", Brief: "check the notes", SkillsDir: t.TempDir()},
		{SessionID: "comet-newt", Peer: "comet", Critter: "Comet Newt", CWD: "/home/u/code/demo-app"},
		{SessionID: "maple-owl", Peer: "maple", Critter: "Maple Owl", CWD: "/home/u/code/demo-app"},
	}, before, before)
	r.MarkClosed("comet-newt", before)
	r.Observe([]roster.Agent{
		{SessionID: "snug-capybara", Peer: "release-lead", Critter: "Snug Capybara", Role: "release lead", Model: "claude-sonnet-5-5", CWD: "/home/u/code/demo-app", Parent: tree.Orchestrator, Brief: "lead the release", Mission: "m1", Spawned: true},
		{SessionID: "mossy-lemur", Peer: "notes", Critter: "Mossy Lemur", Role: "coding agent", Model: "claude-haiku-4-5", CWD: "/home/u/code/demo-app", Parent: "snug-capybara", Brief: "check the notes"},
	}, before.Add(time.Minute), before)
	r.Saved = before.Add(time.Hour)

	m := Model{st: newStyles(theme.Deepharness), store: inbox.New(), width: 120, height: 40, tree: newTreeState(), tab: tabFleet}
	m.cfg.Restore.Ask = true
	m.cfg.Orchestrator.Spawn = true
	m.cfg.Orchestrator.Name = "den-orchestrator"
	m.missions = mission.New("")
	m.missions.Save(&mission.Mission{ID: "m1", Name: "ship 2.0", State: mission.Running})
	m.restore = restoreState{r: r, since: restoreT0, plans: map[string]restorePlan{}}
	m.input = newInput(m.st)
	m.base = []fleet.Agent{sample()[2]} // Fuzzy Sprite, idle
	m.base[0].Session.SessionID = "fuzzy-sprite"
	m.refresh()
	return m, fake
}

func restoreItems(m Model) []*inbox.Item {
	var out []*inbox.Item
	for _, it := range m.store.Items() {
		if it.Kind == inbox.Restore {
			out = append(out, it)
		}
	}
	return out
}

// runRestore answers allow and feeds the run's result back.
func runRestore(t *testing.T, m Model) Model {
	t.Helper()
	if it := restoreItems(m); len(it) > 0 {
		m.tab = tabInbox
		m.selectItem(it[0])
	}
	mm, cmd := m.answer("allow")
	m = mm.(Model)
	for _, msg := range runCmds(cmd) {
		if done, ok := msg.(restoreDoneMsg); ok {
			mm, _ = m.Update(done)
			return mm.(Model)
		}
	}
	t.Fatal("allow did not run the restore")
	return m
}

func TestRestartAsksOnceAboutTheGoneAgents(t *testing.T) {
	m, fake := restoreModel(t)
	m.afterScan()
	items := restoreItems(m)
	if len(items) != 1 || items[0].Title != "Restore 2 agents?" || !items[0].Answerable() {
		t.Fatalf("items = %+v", items)
	}
	body := strings.Join(items[0].Body, "\n")
	for _, want := range []string{
		"Snug Capybara · release lead · Sonnet 5.5 · /home/u/code/demo-app",
		"under the orchestrator · mission ship 2.0 · last seen 2h ago · 123k tokens to reload, ≈ $0.309",
		"Mossy Lemur · coding agent · Haiku 4.5",
		"under Snug Capybara",
		"≈ $0.359 in all",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	for _, no := range []string{"Comet", "Maple", "Fuzzy"} {
		if strings.Contains(body, no) {
			t.Errorf("body lists %s:\n%s", no, body)
		}
	}
	// A second scan does not ask again, and nothing opened by itself.
	m.afterScan()
	if len(restoreItems(m)) != 1 || len(fake.reqs) != 0 {
		t.Fatalf("asked again or opened: %d items, %d opens", len(restoreItems(m)), len(fake.reqs))
	}
	m.tab = tabInbox
	out := ansi.Strip(m.View().Content)
	if !strings.Contains(out, "Restore 2 agents?") || !strings.Contains(out, "^Y reopen") || !strings.Contains(out, "^N leave closed") {
		t.Errorf("inbox view:\n%s", out)
	}
}

func TestAllowReopensAndRelinksTheTree(t *testing.T) {
	m, fake := restoreModel(t)
	m.afterScan()
	it := restoreItems(m)[0]
	m = runRestore(t, m)
	if len(fake.reqs) != 2 {
		t.Fatalf("opened %d terminals", len(fake.reqs))
	}
	snug, mossy := fake.reqs[0], fake.reqs[1]
	if snug.Resume != "snug-capybara" || snug.Name != "release-lead" || snug.Model != "claude-sonnet-5-5" || snug.Spawner == "" || snug.Dir != "/home/u/code/demo-app" || snug.Fork {
		t.Errorf("snug request = %+v", snug)
	}
	if mossy.Resume != "mossy-lemur" || mossy.Name != "notes" || mossy.Spawner != "" || len(mossy.AddDirs) != 1 {
		t.Errorf("mossy request = %+v", mossy)
	}
	if it.State != inbox.Resolved || it.Resolved != "reopened Snug Capybara, Mossy Lemur" {
		t.Errorf("settled: %+v", it)
	}

	// The child shows up before its parent: it waits for it.
	live := func(id, name string) fleet.Agent {
		a := sample()[2]
		a.Session.SessionID, a.Name, a.Session.Name = id, name, strings.ToLower(name)
		return a
	}
	m.base = append(m.base, live("mossy-lemur", "Mossy Lemur"))
	m.refresh()
	m.afterScan()
	if _, ok := m.tree.edges.Parent("mossy-lemur"); ok {
		t.Fatal("linked under a parent that is not back yet")
	}
	m.base = append(m.base, live("snug-capybara", "Snug Capybara"))
	m.refresh()
	m.afterScan()
	if e, _ := m.tree.edges.Parent("snug-capybara"); e.Parent != tree.Orchestrator {
		t.Errorf("snug edge = %+v", e)
	}
	if e, _ := m.tree.edges.Parent("mossy-lemur"); e.Parent != "snug-capybara" || e.Brief != "check the notes" {
		t.Errorf("mossy edge = %+v", e)
	}
	if len(m.restore.pending) != 0 {
		t.Errorf("pending = %+v", m.restore.pending)
	}
	// Seen live again, they are ordinary roster entries.
	if a, _ := m.restore.r.Get("mossy-lemur"); !a.LastSeen.Equal(restoreT0) {
		t.Errorf("roster entry = %+v", a)
	}
}

func TestDenyDismissesForLaterRestarts(t *testing.T) {
	m, fake := restoreModel(t)
	m.restore.path = filepath.Join(t.TempDir(), "roster.json")
	m.afterScan()
	it := restoreItems(m)[0]
	mm, _ := m.answer("deny")
	m = mm.(Model)
	if it.State != inbox.Resolved || len(fake.reqs) != 0 {
		t.Fatalf("deny: %+v, %d opens", it, len(fake.reqs))
	}
	// The next Den reads the saved roster and does not ask.
	m2, _ := restoreModel(t)
	m2.restore.r = roster.Load(m.restore.path)
	m2.afterScan()
	if n := len(restoreItems(m2)); n != 0 {
		t.Fatalf("asked again after a no: %d", n)
	}
}

func TestRestoreQuestionCanBeTurnedOff(t *testing.T) {
	m, _ := restoreModel(t)
	m.cfg.Restore.Ask = false
	m.afterScan()
	if n := len(restoreItems(m)); n != 0 {
		t.Fatalf("asked with restore.ask off: %d", n)
	}
}

func TestReopenOneClosedAgent(t *testing.T) {
	m, fake := restoreModel(t)
	mm, _ := m.reopenCommand([]string{"Comet", "Newt"})
	m = mm.(Model)
	items := restoreItems(m)
	if len(items) != 1 || items[0].Title != "Reopen Comet Newt?" || m.tab != tabInbox {
		t.Fatalf("items = %+v tab %v", items, m.tab)
	}
	m = runRestore(t, m)
	if len(fake.reqs) != 1 || fake.reqs[0].Resume != "comet-newt" || fake.reqs[0].Fork {
		t.Fatalf("reqs = %+v", fake.reqs)
	}

	// Unknown, running and conversation-less agents are refused.
	m, _ = restoreModel(t)
	for _, c := range []struct{ args, want string }{
		{"Nobody", "has not seen an agent called Nobody"},
		{"", "Which agent?"},
	} {
		mm, _ := m.reopenCommand(strings.Fields(c.args))
		if got := mm.(Model).flash; !strings.Contains(got, c.want) {
			t.Errorf("/reopen %s: %q", c.args, got)
		}
	}
	m.restore.r.Observe([]roster.Agent{{SessionID: "fuzzy-sprite", Critter: "Fuzzy Sprite", CWD: "/home/u"}}, restoreT0, restoreT0)
	mm, _ = m.reopenCommand([]string{"Fuzzy", "Sprite"})
	if got := mm.(Model).flash; !strings.Contains(got, "is running") {
		t.Errorf("running agent: %q", got)
	}
}

func TestReopenForkRecordsTheNewID(t *testing.T) {
	m, fake := restoreModel(t)
	fake.newID = "fork-of-mossy"
	mm, _ := m.reopenCommand([]string{"--fork", "Mossy", "Lemur"})
	m = mm.(Model)
	if it := restoreItems(m); len(it) != 1 || it[0].Title != "Fork Mossy Lemur?" {
		t.Fatalf("items = %+v", it)
	}
	m = runRestore(t, m)
	if len(fake.reqs) != 1 || !fake.reqs[0].Fork || fake.reqs[0].Resume != "mossy-lemur" {
		t.Fatalf("reqs = %+v", fake.reqs)
	}
	if m.restore.r.Forks["mossy-lemur"] != "fork-of-mossy" {
		t.Fatalf("forks = %+v", m.restore.r.Forks)
	}
	// The fork comes up beside its parent and takes the old place.
	a := sample()[2]
	a.Session.SessionID, a.Name = "snug-capybara", "Snug Capybara"
	b := sample()[2]
	b.Session.SessionID, b.Name = "fork-of-mossy", "Quiet Heron"
	m.base = append(m.base, a, b)
	m.refresh()
	m.afterScan()
	if e, _ := m.tree.edges.Parent("fork-of-mossy"); e.Parent != "snug-capybara" {
		t.Errorf("fork edge = %+v", e)
	}
	if e, _ := m.restore.r.Get("fork-of-mossy"); e.Parent != "snug-capybara" {
		t.Errorf("fork roster entry = %+v", e)
	}
}

func TestClosingThroughDenKeepsAgentsOutOfRestore(t *testing.T) {
	m := closeModel(t)
	m.ensureRestore()
	a, _ := m.agentFor("maple-owl")
	m.observeRoster()
	m.finishClose(closeDoneMsg{results: []closer.Result{{Target: m.closeTarget(a), Ended: true}}})
	e, ok := m.restore.r.Get("maple-owl")
	if !ok || e.Closed.IsZero() {
		t.Fatalf("roster entry = %+v %v", e, ok)
	}
	if gone := m.restore.r.Gone(func(string) bool { return false }); len(gone) == 0 {
		t.Fatal("the other agents are still offered")
	} else {
		for _, g := range gone {
			if g.SessionID == "maple-owl" {
				t.Fatal("a closed agent is offered")
			}
		}
	}
}

func TestRosterSkipsTheOrchestratorAndHeadlessSessions(t *testing.T) {
	m := treeModel()
	m.cfg.Orchestrator.Name = "den-orchestrator"
	m.base[0].Session.Name = "den-orchestrator"
	m.base[2].Session.Kind = "sdk-cli"
	m.refresh()
	m.observeRoster()
	for _, id := range []string{"mossy-lemur", "fuzzy-sprite"} {
		if _, ok := m.restore.r.Get(id); ok {
			t.Errorf("%s recorded", id)
		}
	}
	e, ok := m.restore.r.Get("snug-capybara")
	if !ok || e.Parent != tree.Orchestrator || e.Brief != "lead the release" || e.Critter != "Snug Capybara" {
		t.Errorf("snug entry = %+v", e)
	}
	if _, ok := m.restore.r.Get("comet-newt"); ok {
		t.Error("a gone agent is not recorded as live")
	}
}

var _ tea.Msg = restoreDoneMsg{}

// An agent closed while in plan mode, which Claude Code may run on another
// model, is reopened with the model it runs outside plan mode, as its
// transcript records it; the roster saw only the plan-mode one.
func TestRestoreUsesTheModelOutsidePlanMode(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	proj := filepath.Join(dir, "projects", "-home-u-code-demo-app")
	os.MkdirAll(proj, 0o755)
	lines := []string{
		`{"type":"attachment","attachment":{"type":"model","identity":{"modelId":"claude-haiku-4-5-20251001"}}}`,
		`{"type":"assistant","message":{"model":"claude-haiku-4-5-20251001","usage":{"input_tokens":10}}}`,
		`{"type":"attachment","attachment":{"type":"model","identity":{"modelId":"claude-sonnet-5-5"}}}`,
		`{"type":"attachment","attachment":{"type":"plan_mode"}}`,
		`{"type":"assistant","message":{"model":"claude-sonnet-5-5","usage":{"input_tokens":10}}}`,
	}
	os.WriteFile(filepath.Join(proj, "brave-ferret.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	a := roster.Agent{SessionID: "brave-ferret", Critter: "Brave Ferret", Model: "claude-sonnet-5-5", CWD: "/home/u/code/demo-app"}
	m := Model{st: newStyles(theme.Deepharness), store: inbox.New(), width: 120, height: 40, tree: newTreeState()}
	m.ensureRestore()
	reqs := m.restoreRequests(restorePlan{agents: []roster.Agent{a}})
	if len(reqs) != 1 || reqs[0].Model != "claude-haiku-4-5-20251001" {
		t.Fatalf("requests = %+v", reqs)
	}
	if head, _, _ := m.restoreLines(a); !strings.Contains(strings.Join(head, "\n"), "Haiku 4.5") {
		t.Errorf("restore lines = %q", head)
	}
	// Started in plan mode, its default model is not recorded: the model
	// the roster saw, as before.
	os.WriteFile(filepath.Join(proj, "brave-ferret.jsonl"), []byte(strings.Join(lines[2:], "\n")+"\n"), 0o644)
	if reqs := m.restoreRequests(restorePlan{agents: []roster.Agent{a}}); reqs[0].Model != "claude-sonnet-5-5" {
		t.Errorf("unrecorded: %+v", reqs[0])
	}
}
