package ui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/fleet"
	denpeer "github.com/GlacierHubAB/den/internal/peer"
	"github.com/GlacierHubAB/den/internal/process"
)

// teamAgent is a live local agent in a git checkout.
func teamAgent(name, sid, peer, cwd string) fleet.Agent {
	return fleet.Agent{Name: name, Status: fleet.Working, HasGit: true,
		Session: discover.Session{SessionID: sid, Name: peer, CWD: cwd, Alive: true, Socket: "/sock/" + sid}}
}

// Two agents whose branches change the same files hear it from Den, once
// each, with the files quoted; the orchestrator's roster shows it; churn
// does not repeat it, nor does a pair that stops and starts again on the
// same files; a file new to them is told, alone, once the per-agent wait
// is over.
func TestOverlapWarnings(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
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
	// Den's own check, framed as Den's: never as the human's words.
	for _, f := range append(peer.framed["/sock/s1"], peer.framed["/sock/s2"]...) {
		if !strings.HasPrefix(f, denpeer.DenHeader) || strings.Contains(f, "typed by your human") {
			t.Errorf("overlap framed as %q", f)
		}
	}
	for _, want := range []string{"Den, an automatic check (not the human)", "Snug Capybara's", `"den-b"`, `"a.go", "b.go"`, "the orchestrator, which decides", "overlap_ack"} {
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
	if len(peer.got["/sock/s1"]) != 1 {
		t.Errorf("told again about the same file: %v", peer.got["/sock/s1"])
	}
	// A file new to them: told, and only that file.
	works[0].Files = append(works[0].Files, "new.go")
	works[1].Files = []string{"a.go", "new.go"}
	scan(now.Add(teamWarnEvery + 2*time.Second))
	if got := peer.got["/sock/s1"]; len(got) != 2 || !strings.Contains(got[1], `"new.go"`) || strings.Contains(got[1], `"a.go"`) {
		t.Errorf("the new file's warning: %v", got)
	}
}

// What Den told a pair is kept in its state dir: a restarted Den, and a
// branch that flaps (its checkout on the trunk a while, then back), do not
// warn them again; a pair is forgotten when an agent is gone or a branch
// lands, and then told again.
func TestOverlapWarnedOnceAcrossRestart(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	peer := useFakePeer(t)
	agents := []fleet.Agent{teamAgent("Mossy Lemur", "s1", "den-a", "/w/one"), teamAgent("Snug Capybara", "s2", "den-b", "/w/two")}
	works := []process.Work{
		{Key: "s1", Repo: "r", Tree: "/w/one", Files: []string{"a.go"}},
		{Key: "s2", Repo: "r", Tree: "/w/two", Files: []string{"a.go"}},
	}
	now := time.Now()
	scan := func(m *Model, at time.Time) {
		t.Helper()
		runCmds(m.applyTeamScan(teamScanMsg{works: works}, at))
	}
	fresh := func() *Model {
		m := testModel(120, 40)
		m.agents = append([]fleet.Agent(nil), agents...)
		return &m
	}
	m := fresh()
	scan(m, now)
	if len(peer.got["/sock/s1"]) != 1 {
		t.Fatalf("first warning %v", peer.got)
	}
	info, err := os.Stat(teamWarnedPath())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("record %v %v", info, err)
	}
	// A restart, well past the wait.
	m = fresh()
	scan(m, now.Add(teamWarnEvery+time.Second))
	// Flapping: s2's checkout on the trunk with nothing changed, then back.
	works[1] = process.Work{Key: "s2", Repo: "r", Tree: "/w/two", Trunk: true}
	scan(m, now.Add(teamWarnEvery+2*time.Second))
	works[1] = process.Work{Key: "s2", Repo: "r", Tree: "/w/two", Files: []string{"a.go"}}
	scan(m, now.Add(2*teamWarnEvery))
	if len(peer.got["/sock/s1"]) != 1 {
		t.Fatalf("warned again: %v", peer.got["/sock/s1"])
	}
	// A Den that does not know s2 yet keeps the pair.
	m.agents = agents[:1]
	scan(m, now.Add(2*teamWarnEvery+time.Second))
	if len(m.warnedPairs()) != 1 {
		t.Fatalf("an unknown agent's pair went at once: %v", m.warnedPairs())
	}
	m.agents = append([]fleet.Agent(nil), agents...)
	// s1's branch lands: the pair is forgotten, and a later overlap told.
	// As process.Read has it, a landed branch's files are only what it
	// has not committed: none here.
	works[0].Landed, works[0].Files = true, nil
	scan(m, now.Add(3*teamWarnEvery))
	if len(m.warnedPairs()) != 0 {
		t.Fatalf("pairs after a landing: %v", m.warnedPairs())
	}
	works[0].Landed, works[0].Files = false, []string{"a.go"}
	scan(m, now.Add(4*teamWarnEvery))
	if len(peer.got["/sock/s1"]) != 2 {
		t.Errorf("not told after the landing: %v", peer.got["/sock/s1"])
	}
	// Gone: forgotten.
	m.agents[1].Status = fleet.Gone
	scan(m, now.Add(5*teamWarnEvery))
	if len(m.warnedPairs()) != 0 {
		t.Errorf("pairs after an agent went: %v", m.warnedPairs())
	}
	// An unknown agent's pair goes once stale.
	m.agents[1].Status = fleet.Working
	m.noteWarned("s1", "s9", []string{"x.go"}, "", now)
	scan(m, now.Add(warnedStale+time.Hour))
	if _, ok := m.warnedPairs()[pairKey("s1", "s9")]; ok {
		t.Errorf("a stale unknown pair stayed")
	}
}

// overlap_ack: an agent names the other, or the orchestrator both; Den
// records the files they overlap on now, says so in its chat, and warns
// them again only for a new file. Only an agent Den started, or the
// orchestrator, may say so, and only of a pair that overlaps now.
func TestOverlapAck(t *testing.T) {
	m, _ := memModel(t)
	peer := useFakePeer(t)
	works := []process.Work{
		{Key: "s1", Repo: "r", Tree: "/w/one", Files: []string{"a.go"}},
		{Key: "s2", Repo: "r", Tree: "/w/two", Files: []string{"a.go"}},
	}
	ack := func(pid int, in bridge.OverlapAckInput) bridge.Decision {
		t.Helper()
		raw, _ := json.Marshal(in)
		r := bridge.TestRequest(bridge.Envelope{Event: bridge.OverlapAckEvent, Payload: raw})
		r.PeerPID = pid
		m.handleRequest(r)
		d, ok := r.Decided()
		if !ok {
			t.Fatal("no answer")
		}
		return d
	}
	if d := ack(100, bridge.OverlapAckInput{With: "Snug Capybara"}); d.Behavior != "deny" || !strings.Contains(d.Message, "no files") {
		t.Errorf("no overlap yet: %+v", d)
	}
	// Agreed before Den warned: Den never warns them about a.go.
	m.team.overlaps = map[string][]process.Overlap{"s1": process.Overlaps(works)}
	for _, c := range []struct {
		pid int
		in  bridge.OverlapAckInput
	}{
		{4242, bridge.OverlapAckInput{With: "Snug Capybara"}},             // not an agent Den started
		{100, bridge.OverlapAckInput{With: "Nobody Here"}},                // no such agent
		{100, bridge.OverlapAckInput{With: "Mossy Lemur"}},                // itself
		{100, bridge.OverlapAckInput{Agents: []string{"den-a", "den-b"}}}, // an agent naming two
		{999, bridge.OverlapAckInput{With: "Snug Capybara"}},              // the orchestrator naming one
	} {
		if d := ack(c.pid, c.in); d.Behavior != "deny" {
			t.Errorf("%d %+v allowed: %+v", c.pid, c.in, d)
		}
	}
	if len(m.warnedPairs()) != 0 {
		t.Fatalf("a refused ack recorded %v", m.warnedPairs())
	}
	if d := ack(999, bridge.OverlapAckInput{Agents: []string{"den-a", "Snug Capybara"}}); d.Behavior != "allow" {
		t.Fatalf("orchestrator's ack: %+v", d)
	}
	p := m.warnedPairs()[pairKey("s1", "s2")]
	if p == nil || p.Acked != "The orchestrator" || len(p.Files) != 1 || p.Files[0] != "a.go" {
		t.Fatalf("record %+v", p)
	}
	if last := m.orch.chat[len(m.orch.chat)-1].text; !strings.Contains(last, "Mossy Lemur and Snug Capybara agreed on 1 file") {
		t.Errorf("note %q", last)
	}
	runCmds(m.applyTeamScan(teamScanMsg{works: works}, time.Now()))
	if len(peer.got["/sock/s1"]) != 0 {
		t.Errorf("warned after the ack: %v", peer.got)
	}
	if d := ack(200, bridge.OverlapAckInput{With: "mossy lemur"}); d.Behavior != "allow" || !strings.Contains(d.Message, "Snug Capybara and Mossy Lemur") {
		t.Errorf("agent's ack: %+v", d)
	}
}

// A God-mode guarded agent's hook blocks what Den does not mediate: it is
// not sent the overlap message, the other agent is, and Den's note says the
// orchestrator tells the guarded one.
func TestOverlapSkipsGuardedAgents(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	peer := useFakePeer(t)
	m := testModel(120, 40)
	m.agents = []fleet.Agent{teamAgent("Mossy Lemur", "s1", "den-a", "/w/one"), teamAgent("Snug Capybara", "s2", "den-b", "/w/two")}
	m.med.guards = map[string]*agentGuard{"s2": {}}
	works := []process.Work{
		{Key: "s1", Repo: "r", Tree: "/w/one", Files: []string{"a.go"}},
		{Key: "s2", Repo: "r", Tree: "/w/two", Files: []string{"a.go"}},
	}
	runCmds(m.applyTeamScan(teamScanMsg{works: works}, time.Now()))
	if len(peer.got["/sock/s1"]) != 1 || len(peer.got["/sock/s2"]) != 0 {
		t.Errorf("messages %v", peer.got)
	}
	if last := m.orch.chat[len(m.orch.chat)-1].text; !strings.Contains(last, "Snug Capybara runs guarded in God mode, so the orchestrator tells it instead") {
		t.Errorf("note %q", last)
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
	// The asking processes: 555 under Mossy Lemur's (pid 100), 777 under
	// nothing Den knows, 999 the orchestrator's den mcp.
	m.agents[0].Session.PID = 100
	oldAnc, oldOrc := teamAncestors, fromOrchestrator
	teamAncestors = func(pid int) []int { return map[int][]int{555: {555, 100, 1}, 777: {777, 1}}[pid] }
	fromOrchestrator = func(_ *Model, pid int) bool { return pid == 999 }
	t.Cleanup(func() { teamAncestors, fromOrchestrator = oldAnc, oldOrc })
	askFrom := func(pid int, dir, reason, lead string) bridge.Decision {
		t.Helper()
		payload, _ := json.Marshal(bridge.TeamQueryInput{Dir: dir, Reason: reason, Lead: lead})
		r := bridge.TestRequest(bridge.Envelope{Event: bridge.TeamEvent, Payload: payload})
		r.PeerPID = pid
		if cmd := m.handleTeamQuery(r); cmd != nil {
			m.applyTeamCap(cmd().(teamCapMsg), time.Now())
		}
		d, ok := r.Decided()
		if !ok {
			t.Fatal("no answer")
		}
		return d
	}
	ask := func(dir, reason, lead string) bridge.Decision { t.Helper(); return askFrom(999, dir, reason, lead) }
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
	// An agent's den mcp, by its process: allowed, and noted, quoted, with
	// the name Den read, whatever lead the payload claims.
	if d := askFrom(555, wt1, "two reviews \x1b[31min parallel", "den-b"); d.Behavior != "allow" {
		t.Fatalf("with a reason: %+v", d)
	}
	last := m.orch.chat[len(m.orch.chat)-1].text
	if !strings.Contains(last, `Mossy Lemur starts one more agent in "proj", over the team cap of 3, saying: "two reviews in parallel"`) {
		t.Errorf("note %q", last)
	}
	if d := ask(proj, "a third reviewer", ""); d.Behavior != "allow" || !strings.HasPrefix(m.orch.chat[len(m.orch.chat)-1].text, "The orchestrator starts one more agent") {
		t.Errorf("the orchestrator's: %+v %q", d, m.orch.chat[len(m.orch.chat)-1].text)
	}
	// Any other process: its answer, and nothing in Den's chat.
	n := len(m.orch.chat)
	if d := askFrom(777, proj, "trust me", "den-a"); d.Behavior != "allow" || len(m.orch.chat) != n {
		t.Errorf("a stranger: %+v, %d notes added", d, len(m.orch.chat)-n)
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
// with none left, nothing is read at all.
func TestTeamTickTargets(t *testing.T) {
	m := testModel(120, 40)
	remote := teamAgent("Far Fox", "s9", "den-far", "/w/far")
	remote.Host = "box"
	nogit := teamAgent("Plain Owl", "s8", "den-plain", "/w/plain")
	nogit.HasGit = false
	orc := teamAgent("Den Orchestrator", "s0", OrchestratorName, "/w/one")
	m.agents = []fleet.Agent{remote, nogit, orc}
	if cmd := m.teamTick(time.Now()); cmd != nil || m.team.scanning {
		t.Error("read with no agent to read")
	}
	var read []teamTarget
	old := teamRead
	teamRead = func(targets []teamTarget) []process.Work { read = targets; return nil }
	t.Cleanup(func() { teamRead = old })
	m.agents = append(m.agents, teamAgent("Mossy Lemur", "s1", "den-a", "/w/one"))
	m.team.next = time.Time{}
	cmd := m.teamTick(time.Now())
	if cmd == nil {
		t.Fatal("one agent's branch not read")
	}
	cmd()
	if len(read) != 1 || read[0].key != "s1" {
		t.Errorf("read %+v", read)
	}
}

// A branch older than the threshold, or too far behind, is nudged once a
// day while it stays over: the agent hears it from Den, Den notes it in its
// chat, and the roster carries it; a guarded agent is not messaged.
func TestBranchNudges(t *testing.T) {
	peer := useFakePeer(t)
	m := testModel(120, 40)
	m.cfg.Team.BranchHours, m.cfg.Team.BehindMax = 24, 30
	m.agents = []fleet.Agent{teamAgent("Mossy Lemur", "s1", "den-a", "/w/one"), teamAgent("Snug Capybara", "s2", "den-b", "/w/two"), teamAgent("Fuzzy Sprite", "s3", "den-c", "/w/three")}
	m.med.guards = map[string]*agentGuard{"s3": {}}
	now := time.Now() // the roster line reads the real clock
	works := []process.Work{
		{Key: "s1", Branch: "old\x1b[31m-one", Base: "refs/remotes/origin/dev", Since: now.Add(-50 * time.Hour), Behind: 3},
		{Key: "s2", Branch: "fresh", Base: "refs/heads/main", Since: now.Add(-2 * time.Hour), Behind: 41},
		{Key: "s3", Branch: "guarded", Base: "refs/heads/main", Since: now.Add(-30 * time.Hour)},
	}
	runCmds(m.applyTeamScan(teamScanMsg{works: works}, now))
	if got := peer.got["/sock/s1"]; len(got) != 1 || !strings.Contains(got[0], `your branch "old-one" is 2 days old.`) || !strings.Contains(got[0], "rebase it onto origin/dev") {
		t.Errorf("to s1: %v", got)
	}
	if got := peer.got["/sock/s2"]; len(got) != 1 || !strings.Contains(got[0], `"fresh" is 41 commits behind main`) {
		t.Errorf("to s2: %v", got)
	}
	if len(peer.got["/sock/s3"]) != 0 {
		t.Errorf("a guarded agent was messaged: %v", peer.got["/sock/s3"])
	}
	// Den's own check, framed as Den's: never as the human's words.
	for _, f := range peer.framed["/sock/s1"] {
		if !strings.HasPrefix(f, denpeer.DenHeader) || strings.Contains(f, "typed by your human") {
			t.Errorf("nudge framed as %q", f)
		}
	}
	var notes []string
	for _, e := range m.orch.chat {
		notes = append(notes, e.text)
	}
	all := strings.Join(notes, "\n")
	for _, want := range []string{`Den nudged Mossy Lemur: its branch "old-one" is 2 days old. You can tell it yourself (@Mossy Lemur merge or rebase now), or ask the orchestrator to split the work.`, `Den did not message Fuzzy Sprite, which runs guarded in God mode: its branch "guarded" is 30 hours old.`} {
		if !strings.Contains(all, want) {
			t.Errorf("notes lack %q:\n%s", want, all)
		}
	}
	for _, r := range m.roster() {
		if r.Critter == "Snug Capybara" && r.Branch != `"fresh" is 41 commits behind main` {
			t.Errorf("roster %q", r.Branch)
		}
	}
	// Again within the day: nothing; after it: again.
	runCmds(m.applyTeamScan(teamScanMsg{works: works}, now.Add(time.Hour)))
	if len(peer.got["/sock/s1"]) != 1 {
		t.Errorf("nudged twice in a day: %v", peer.got["/sock/s1"])
	}
	runCmds(m.applyTeamScan(teamScanMsg{works: works}, now.Add(teamNudgeEvery+time.Minute)))
	if len(peer.got["/sock/s1"]) != 2 {
		t.Errorf("not nudged the next day: %v", peer.got["/sock/s1"])
	}
	// Thresholds off: nothing.
	m.cfg.Team.BranchHours, m.cfg.Team.BehindMax = 0, 0
	if why := branchOver(works[0], now, 0, 0); why != "" {
		t.Errorf("off: %q", why)
	}
}

// Only a feature branch with work of its own is ever nudged: never the
// trunk (a checkout of main five days behind), or one with no commits
// beyond its base.
func TestNudgeOnlyFeatureBranches(t *testing.T) {
	now := time.Now()
	old := now.Add(-5 * 24 * time.Hour)
	for name, w := range map[string]process.Work{
		"the trunk":        {Branch: "main", Trunk: true, Since: old, Behind: 400},
		"no own commits":   {Branch: "idle", Behind: 400},
		"landed (squash)":  {Branch: "done", Since: old, Landed: true, Behind: 400},
		"detached, landed": {Branch: "HEAD", Since: old, Landed: true},
	} {
		if why := branchOver(w, now, 24, 30); why != "" {
			t.Errorf("%s nudged: %q", name, why)
		}
	}
	if why := branchOver(process.Work{Branch: "feature", Since: old, Behind: 3}, now, 24, 30); why == "" {
		t.Error("an old feature branch was not nudged")
	}
}

// The warned record is Den's alone: a FIFO or a symlink planted at it is
// never read (the FIFO cannot block Den), and a symlink planted at its
// .tmp is removed, never written through.
func TestWarnedRecordRefusesPlantedFiles(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	path := teamWarnedPath()
	os.MkdirAll(filepath.Dir(path), 0o700)
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	m := testModel(120, 40)
	if len(m.warnedPairs()) != 0 {
		t.Fatal("read a FIFO")
	}
	os.Remove(path)
	elsewhere := filepath.Join(t.TempDir(), "elsewhere.json")
	os.WriteFile(elsewhere, []byte(`{"v":1,"pairs":{"x":{"a":"s1","b":"s2","files":["a.go"]}}}`), 0o600)
	os.Symlink(elsewhere, path)
	m = testModel(120, 40)
	if len(m.warnedPairs()) != 0 {
		t.Fatal("followed a symlink")
	}
	os.Remove(path)
	target := filepath.Join(t.TempDir(), "target")
	os.WriteFile(target, []byte("keep"), 0o600)
	os.Symlink(target, path+".tmp")
	m.noteWarned("s1", "s2", []string{"a.go"}, "", time.Now())
	m.saveWarned()
	if raw, _ := os.ReadFile(target); string(raw) != "keep" {
		t.Errorf("wrote through the .tmp symlink: %q", raw)
	}
	if fi, err := os.Lstat(path); err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		t.Errorf("record %v %v", fi, err)
	}
}
