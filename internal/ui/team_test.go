package ui

import (
	"strings"
	"testing"
	"time"

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
