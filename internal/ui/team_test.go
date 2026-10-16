package ui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/process"
)

// teamAgent is a live local agent in a git checkout.
func teamAgent(name, sid, peer, cwd string) fleet.Agent {
	return fleet.Agent{Name: name, Status: fleet.Working, HasGit: true,
		Session: discover.Session{SessionID: sid, Name: peer, CWD: cwd, Alive: true, Socket: "/sock/" + sid}}
}

// Two agents whose branches change the same files hear it from Den, once
// each, with the files quoted; the orchestrator's roster shows it; churn
// does not repeat it, and a pair that stops and starts again is told again
// once the per-agent wait is over.
func TestOverlapWarnings(t *testing.T) {
	peer := useFakePeer(t)
	m := testModel(120, 40)
	m.agents = []fleet.Agent{
		teamAgent("Mossy Lemur", "s1", "den-a", "/w/one"),
		teamAgent("Snug Capybara", "s2", "den-b", "/w/two"),
		teamAgent("Fuzzy Sprite", "s3", "den-c", "/w/three"),
	}
	works := []process.Work{
		{Key: "s1", Repo: "r", Tree: "/w/one", Files: []string{"a.go", "b\x1b[31m.go", "c.go"}},
		{Key: "s2", Repo: "r", Tree: "/w/two", Files: []string{"a.go", "b\x1b[31m.go"}},
		{Key: "s3", Repo: "r", Tree: "/w/three", Files: []string{"z.go"}},
	}
	old := teamRead
	teamRead = func(targets []teamTarget) []process.Work {
		if len(targets) != 3 {
			t.Errorf("targets %+v", targets)
		}
		return works
	}
	t.Cleanup(func() { teamRead = old })
	now := time.Date(2026, 10, 16, 15, 0, 0, 0, time.UTC)
	scan := func(at time.Time) {
		t.Helper()
		m.team.next = time.Time{}
		cmd := m.teamTick(at)
		if cmd == nil {
			t.Fatal("no scan")
		}
		runCmds(m.applyTeamScan(cmd().(teamScanMsg), at))
	}
	scan(now)
	toA, toB := peer.got["/sock/s1"], peer.got["/sock/s2"]
	if len(toA) != 1 || len(toB) != 1 || len(peer.got["/sock/s3"]) != 0 {
		t.Fatalf("messages %v", peer.got)
	}
	for _, want := range []string{"Den, an automatic check (not the human)", "Snug Capybara's", `"den-b"`, `"a.go", "b.go"`, "the orchestrator, which decides"} {
		if !strings.Contains(toA[0], want) {
			t.Errorf("to Mossy Lemur lacks %q: %s", want, toA[0])
		}
	}
	if strings.Contains(toA[0], "\x1b") {
		t.Errorf("an escape reached the message: %q", toA[0])
	}
	if !strings.Contains(toB[0], "Mossy Lemur's") {
		t.Errorf("to Snug Capybara: %s", toB[0])
	}
	var r1 []string
	for _, r := range m.roster() {
		if r.Critter == "Mossy Lemur" {
			r1 = r.Overlaps
		}
	}
	if len(r1) != 1 || !strings.Contains(r1[0], `with Snug Capybara, files as git lists them: "a.go", "b.go"`) {
		t.Errorf("roster overlaps %q", r1)
	}
	// The same overlap again: no new message.
	scan(now.Add(teamEvery))
	if len(peer.got["/sock/s1"]) != 1 {
		t.Errorf("told twice: %v", peer.got["/sock/s1"])
	}
	// It stops, then starts again within the wait: not yet; after it: yes.
	works[1].Files = []string{"q.go"}
	scan(now.Add(2 * teamEvery))
	if len(m.team.overlaps) != 0 {
		t.Errorf("overlaps after it stopped: %v", m.team.overlaps)
	}
	works[1].Files = []string{"a.go"}
	scan(now.Add(3 * teamEvery))
	if len(peer.got["/sock/s1"]) != 1 {
		t.Errorf("told again within the wait: %v", peer.got["/sock/s1"])
	}
	scan(now.Add(teamWarnEvery + time.Second))
	if len(peer.got["/sock/s1"]) != 2 || len(peer.got["/sock/s2"]) != 2 {
		t.Errorf("not told again after the wait: %v", peer.got)
	}
}

// The team cap: under it a spawn goes on; at it, only with a reason,
// which Den notes in its chat, quoted, with who gave it. Worktrees of one
// repository count together, other repositories and the orchestrator not
// at all, and a lead's spawn counts as the orchestrator's does.
func TestTeamCap(t *testing.T) {
	m := testModel(120, 40)
	m.cfg.Team.MaxAgents = 3
	base := t.TempDir()
	dir := func(name string) string {
		d := filepath.Join(base, name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		return d
	}
	proj, wt1, wt2, other := dir("proj"), dir("proj-wt1"), dir("proj-wt2"), dir("other")
	repoOf := map[string]string{proj: "proj-aaaaaaaaaaaa", wt1: "proj-aaaaaaaaaaaa", wt2: "proj-aaaaaaaaaaaa", other: "other-bbbbbbbbbbbb"}
	old := teamRepo
	teamRepo = func(_ context.Context, dir string) (string, string, error) {
		id, ok := repoOf[dir]
		if !ok {
			return "", "", errors.New("not a repository")
		}
		return id, id[:len(id)-13], nil
	}
	t.Cleanup(func() { teamRepo = old })
	m.agents = []fleet.Agent{
		teamAgent("Mossy Lemur", "s1", "den-a", proj),
		teamAgent("Snug Capybara", "s2", "den-b", wt1),
		teamAgent("Fuzzy Sprite", "s3", "den-c", other),
		teamAgent("Den Orchestrator", "s0", OrchestratorName, proj), // never counted
	}
	m.base = m.agents
	ask := func(dir, reason, lead string) bridge.Decision {
		t.Helper()
		payload, _ := json.Marshal(bridge.TeamQueryInput{Dir: dir, Reason: reason, Lead: lead})
		r := bridge.TestRequest(bridge.Envelope{Event: bridge.TeamEvent, Payload: payload})
		if cmd := m.handleTeamQuery(r); cmd != nil {
			m.applyTeamCap(cmd().(teamCapMsg), time.Now())
		}
		d, ok := r.Decided()
		if !ok {
			t.Fatal("no answer")
		}
		return d
	}
	// Not a repository, another repository, and proj with two agents (the
	// orchestrator not counted): room for one more.
	for _, d := range []string{base, other, proj} {
		if got := ask(d, "", ""); got.Behavior != "allow" {
			t.Errorf("%s: %+v", d, got)
		}
	}
	// A third in another worktree of proj: at the cap.
	m.agents = append(m.agents, teamAgent("Maple Owl", "s4", "den-d", wt2))
	m.base = m.agents
	d := ask(proj, "", "")
	if d.Behavior != "deny" || !strings.Contains(d.Message, `"proj" has 3 agents, Den's cap of 3 (Maple Owl, Mossy Lemur, Snug Capybara)`) || !strings.Contains(d.Message, "give reason") {
		t.Fatalf("at the cap: %+v", d)
	}
	if d := ask(proj, "   ", ""); d.Behavior != "deny" {
		t.Errorf("a blank reason: %+v", d)
	}
	// A lead's spawn with a reason: allowed, and noted, quoted, with whose.
	if d := ask(wt1, "two reviews \x1b[31min parallel", "den-a"); d.Behavior != "allow" {
		t.Fatalf("with a reason: %+v", d)
	}
	last := m.orch.chat[len(m.orch.chat)-1].text
	if !strings.Contains(last, `Mossy Lemur starts one more agent in "proj", over the team cap of 3, saying: "two reviews in parallel"`) {
		t.Errorf("note %q", last)
	}
	// God mode does not lift it.
	m.god.on = true
	if d := ask(proj, "", ""); d.Behavior != "deny" {
		t.Errorf("God mode lifted the cap: %+v", d)
	}
}

// Many files: at most overlapNames quoted, each cut, then how many more.
func TestQuotedFiles(t *testing.T) {
	var files []string
	for i := 0; i < overlapNames+3; i++ {
		files = append(files, strings.Repeat("x", 200))
	}
	q := quotedFiles(files)
	if strings.Count(q, `"`) != 2*overlapNames || !strings.HasSuffix(q, "and 3 more") {
		t.Errorf("%s", q)
	}
	if strings.Contains(q, strings.Repeat("x", overlapNameMax+1)) {
		t.Error("a name was not cut")
	}
}

// The orchestrator, remote agents and agents with no git are never read;
// with fewer than two agents to compare, nothing is read at all.
func TestTeamTickTargets(t *testing.T) {
	m := testModel(120, 40)
	remote := teamAgent("Far Fox", "s9", "den-far", "/w/far")
	remote.Host = "box"
	nogit := teamAgent("Plain Owl", "s8", "den-plain", "/w/plain")
	nogit.HasGit = false
	m.agents = []fleet.Agent{teamAgent("Mossy Lemur", "s1", "den-a", "/w/one"), remote, nogit}
	if cmd := m.teamTick(time.Now()); cmd != nil || m.team.scanning {
		t.Error("read with one agent to compare")
	}
}
