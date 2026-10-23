package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/badge"
	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/names"
	"github.com/GlacierHubAB/den/internal/orch"
	"github.com/GlacierHubAB/den/internal/roster"
	"github.com/GlacierHubAB/den/internal/theme"
	"github.com/charmbracelet/x/ansi"
)

// namedFleet is sample() with two agents named by their role, keeping their
// critter names as aliases.
func namedFleet() []fleet.Agent {
	as := sample()
	// sample() gives every agent one session id; these need their own.
	for i := range as {
		as[i].Session.SessionID = fmt.Sprintf("5e55%012d", i)
	}
	as[0].Alias, as[0].Name, as[0].Labels.Role = as[0].Name, "backend-engineer", "backend engineer"
	as[1].Alias, as[1].Name, as[1].Labels.Role = as[1].Name, "backend-engineer2", "backend engineer"
	as[1].Session.Name = "user-ab"
	return as
}

func TestNameDisplay(t *testing.T) {
	as := namedFleet()
	if got := shownName(as[0]); got != "@backend-engineer" {
		t.Errorf("shown %q", got)
	}
	if got := shownName(as[2]); strings.HasPrefix(got, "@") {
		t.Errorf("a critter name is shown as is: %q", got)
	}
	for in, want := range map[string]string{
		"@technical-writer2": "@technical-wri…2",
		"@backend-engineer":  "@backend-engine…",
		"@short2":            "@short2",
	} {
		if got := cutName(in, 16); got != want || ansi.StringWidth(got) > 16 {
			t.Errorf("cutName(%q) = %q, want %q", in, got, want)
		}
	}
	if roleShown(as[0]) {
		t.Error("a role name repeats its role")
	}
	as[0].Labels.Role = "release lead"
	if !roleShown(as[0]) {
		t.Error("a title name hides a different role")
	}
}

// Typing either the name or the critter name finds the agent; a role name
// colours only as "@name", never as a bare word, while the critter name
// keeps its bare rule.
func TestNamesInMentions(t *testing.T) {
	as := namedFleet()
	alias := as[1].Alias
	if got := completions("@"+strings.ToLower(alias[:3]), as); len(got) == 0 || got[0].Name != "backend-engineer2" {
		t.Errorf("completion by alias: %+v", got)
	}
	if mt := resolveMention("@"+alias+" rebase", as); mt.target == nil || mt.target.Name != "backend-engineer2" || mt.body != "rebase" {
		t.Errorf("mention by alias: %+v", mt)
	}
	ms := buildMentions(as, func(fleet.Agent) bool { return false })
	if _, _, ok := ms.match("backend-engineer2 rebase", 0); ok {
		t.Error("a bare role name coloured")
	}
	if n, _, ok := ms.match("@backend-engineer2 rebase", 0); !ok || n != len("@backend-engineer2") {
		t.Error("an @role name did not colour")
	}
	if _, _, ok := ms.match(alias+" again", 0); !ok {
		t.Error("the bare critter alias lost its rule")
	}
}

// The role chip beside the name is whole or not there.
func TestHeaderChipWholeOrNot(t *testing.T) {
	name, chip := "@site-reliability-infrastructure2", "   SITE RELIABILITY INFRASTRUCTURE"
	if got := headerLine(name, chip, 40); got != name {
		t.Errorf("a chip that does not fit: %q", got)
	}
	if got := headerLine(name, chip, 80); got != name+chip {
		t.Errorf("a chip that fits: %q", got)
	}
}

// The details pane names the agent, says its critter name too, and says
// "no role" for an agent that has none.
func TestDetailsShowAlias(t *testing.T) {
	as := namedFleet()
	as[2].Labels.Role = ""
	m := Model{st: newStyles(theme.Deepharness), agents: as, store: inbox.New(), width: 120, height: 40}
	m.input = newInput(m.st)
	pane := func(i int) string {
		m.cursor = i
		lines, _, _ := m.detailLines(80)
		return ansi.Strip(strings.Join(lines, "\n"))
	}
	if got := pane(1); !strings.Contains(got, "@backend-engineer2") || !strings.Contains(got, "also "+as[1].Alias) {
		t.Errorf("details of a named agent:\n%s", got)
	}
	if got := pane(2); !strings.Contains(got, "no role") {
		t.Errorf("details of an agent with no role:\n%s", got)
	}
}

// TestNamesShots writes the frames the designer reviews, as plain text,
// into $DEN_NAMES_SHOTS; without it the test does nothing.
func TestNamesShots(t *testing.T) {
	dir := os.Getenv("DEN_NAMES_SHOTS")
	if dir == "" {
		t.Skip("set DEN_NAMES_SHOTS to write the frames")
	}
	as := namedFleet()
	as[3].Alias, as[3].Name, as[3].Labels.Role = as[3].Name, "site-reliability-infrastructure2", "site reliability infrastructure"
	frame := func(name string, w, h int, prep func(*Model)) {
		m := Model{st: newStyles(theme.Deepharness), agents: as, store: inbox.New(), width: w, height: h, tab: tabFleet}
		m.input = newInput(m.st)
		m.colors = map[string]string{}
		if prep != nil {
			prep(&m)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".txt"), []byte(ansi.Strip(m.View().Content)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	frame("fleet-two-of-a-role", 120, 30, nil)
	frame("picker-by-alias", 120, 30, func(m *Model) { m.input.SetValue("@" + strings.ToLower(as[1].Alias[:4])) })
	frame("details-with-alias", 120, 40, func(m *Model) { m.cursor = 1 })
	frame("cut-name-keeps-number", 44, 30, func(m *Model) { m.cursor = 3 })
	// Below 72 columns the fleet shows no details pane: its lines at the
	// same width, as Den draws them where there is room.
	m := Model{st: newStyles(theme.Deepharness), agents: as, store: inbox.New(), width: 44, height: 40, tab: tabFleet, cursor: 3}
	m.input = newInput(m.st)
	m.colors = map[string]string{}
	lines, _, _ := m.detailLines(42)
	if err := os.WriteFile(filepath.Join(dir, "cut-name-details.txt"), []byte(ansi.Strip(strings.Join(lines, "\n"))), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Den names a session only from a spawn it saw itself: the orchestrator's
// spawn_agent call in its own stream, or a lead's spawn_agent result through
// its hook, the record the spawned-by edge comes from. A session that
// started before the spawn, or is already named, keeps its name; no
// request can name one; and nothing written to names.json changes what the
// fleet shows.
func TestDenNamesOnlyWhatItSawSpawn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "names.json")
	old := fleet.GivenNames
	t.Cleanup(func() { fleet.GivenNames = old })
	m := Model{st: newStyles(theme.Deepharness), store: inbox.New(), tree: newTreeState()}
	m.useNames(names.OpenLive(path))
	m.base = sample()[:4]
	now := time.Now()
	for i := range m.base {
		m.base[i].Session.SessionID = fmt.Sprintf("0000000%d-1111-4222-8333-444444444444", i)
		m.base[i].Session.Name = fmt.Sprintf("peer-%d", i)
		m.base[i].Session.Alive = true
		m.base[i].Session.StartedAt = now.UnixMilli()
		m.base[i].Status = fleet.Working
	}
	m.base[1].Session.Name = "backend-engineer" // another session's peer handle
	m.base[2].Session.StartedAt = now.Add(-time.Hour).UnixMilli()
	m.refresh()
	given := func(i int) string { return fleet.GivenNames()[m.base[i].Session.SessionID].Name() }
	started := func(peer string) string { return `Started X (peer "` + peer + `") in ~/x.` }

	// The orchestrator's own spawn: named, past the peer handle in use.
	raw, _ := json.Marshal(orch.SpawnInput{Dir: "/x", Brief: "b", Role: "backend engineer"})
	m.handleOrchEvent(orch.Event{Kind: orch.ToolUse, Tool: orch.SpawnAgentTool, ToolUseID: "t1", Input: raw})
	m.handleOrchEvent(orch.Event{Kind: orch.ToolResult, ToolUseID: "t1", Text: started("peer-0")})
	if got := given(0); got != "backend-engineer2" {
		t.Fatalf("the orchestrator's spawn: %q", got)
	}
	if a, _ := m.agentFor(m.base[0].Session.SessionID); a.Name != "backend-engineer2" || a.Alias == "" {
		t.Errorf("the fleet does not show the name yet: %+v", a)
	}
	// A lead's spawn, through its PostToolUse hook.
	hook := func(lead, peer, role string) {
		payload, _ := json.Marshal(map[string]any{
			"hook_event_name": "PostToolUse", "session_id": lead, "tool_name": orch.SpawnAgentTool,
			"tool_input":    map[string]string{"dir": "/x", "brief": "b", "role": role},
			"tool_response": started(peer),
		})
		m.handleRequest(bridge.TestRequest(bridge.Envelope{Event: "PostToolUse", SessionID: lead, Payload: payload}))
	}
	// A lead's hook alone names nothing: anything in its tree can send one.
	hook(m.base[1].Session.SessionID, "peer-3", "technical writer")
	if got := given(3); got != "" {
		t.Fatalf("a hook alone named a session: %q", got)
	}
	// The lead's own den mcp reports the spawn (pid 777 here), and a plain
	// process may not.
	oldSpawner := spawnerOf
	t.Cleanup(func() { spawnerOf = oldSpawner })
	spawnerOf = func(_ *Model, pid int) (string, bool) { return m.base[1].Session.SessionID, pid == 777 }
	report := func(pid int, peer string) bridge.Decision {
		payload, _ := json.Marshal(bridge.SpawnedInput{Peer: peer})
		r := bridge.TestRequest(bridge.Envelope{Event: bridge.SpawnedEvent, Payload: payload})
		r.PeerPID = pid
		m.handleRequest(r)
		d, _ := r.Decided()
		return d
	}
	if d := report(4242, "peer-3"); d.Behavior != "deny" {
		t.Errorf("a plain process reported a spawn: %+v", d)
	}
	hook(m.base[1].Session.SessionID, "peer-3", "technical writer")
	if got := given(3); got != "" {
		t.Fatalf("a refused report counted: %q", got)
	}
	if d := report(777, "peer-3"); d.Behavior != "allow" {
		t.Fatalf("the lead's den mcp: %+v", d)
	}
	hook(m.base[1].Session.SessionID, "peer-3", "technical writer")
	if got := given(3); got != "technical-writer" {
		t.Errorf("a lead's reported spawn: %q", got)
	}
	report(777, "peer-2")
	report(777, "peer-0")
	// A session that started before the spawn is not the spawn's.
	hook(m.base[1].Session.SessionID, "peer-2", "security reviewer")
	if got := given(2); got != "" {
		t.Errorf("an old session was named: %q", got)
	}
	// A named session keeps its name.
	hook(m.base[1].Session.SessionID, "peer-0", "designer")
	if got := given(0); got != "backend-engineer2" {
		t.Errorf("a named session was renamed: %q", got)
	}
	// No request names a session.
	payload, _ := json.Marshal(map[string]string{"session_id": m.base[2].Session.SessionID, "role": "lead"})
	m.handleRequest(bridge.TestRequest(bridge.Envelope{Event: "Name", Payload: payload}))
	if got := given(2); got != "" {
		t.Errorf("a request named a session: %q", got)
	}
	os.WriteFile(path, []byte(`{"entries":[{"session":"`+m.base[0].Session.SessionID+`","base":"impostor","n":1}]}`), 0o600)
	if got := given(0); got != "backend-engineer2" {
		t.Errorf("a name changed with the file: %q", got)
	}
}

// The spawner check compares the file a process runs, never its path: a
// copy of a binary, even one named "<path> (deleted)", is another file.
func TestRunsFileIsTheFileNotThePath(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep")
	}
	real, _ := filepath.EvalSymlinks(sleep)
	start := func(path string) *exec.Cmd {
		t.Helper()
		c := exec.Command(path, "5")
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Process.Kill(); c.Wait() })
		return c
	}
	orig := start(real)
	if !runsFile(orig.Process.Pid, real) {
		t.Fatal("the binary itself is refused")
	}
	raw, err := os.ReadFile(real)
	if err != nil {
		t.Skip(err)
	}
	copyPath := filepath.Join(t.TempDir(), filepath.Base(real)+" (deleted)")
	if err := os.WriteFile(copyPath, raw, 0o755); err != nil {
		t.Fatal(err)
	}
	cp := start(copyPath)
	if runsFile(cp.Process.Pid, real) {
		t.Error("a copy named like the deleted binary passed")
	}
	if runsFile(cp.Process.Pid, "", filepath.Join(t.TempDir(), "missing")) {
		t.Error("an unreadable file matched")
	}
}

// renameModel is a model holding names, with three live local agents.
func renameModel(t *testing.T) *Model {
	t.Helper()
	path := filepath.Join(t.TempDir(), "names.json")
	old := fleet.GivenNames
	t.Cleanup(func() { fleet.GivenNames = old })
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	m := &Model{st: newStyles(theme.Deepharness), store: inbox.New(), tree: newTreeState()}
	m.useNames(names.OpenLive(path))
	m.base = sample()[:3]
	for i := range m.base {
		m.base[i].Session.SessionID = fmt.Sprintf("0000000%d-1111-4222-8333-444444444444", i)
		m.base[i].Session.Name = fmt.Sprintf("peer-%d", i)
		m.base[i].Session.Alive = true
		m.base[i].Status = fleet.Idle
		m.base[i].Name = badge.Name(m.base[i].Session.SessionID)
	}
	m.base[0].Labels.Role = "backend engineer"
	m.base[1].Labels.Role = "coding agent" // too vague: no usable name
	m.base[2].Labels.Role = "security reviewer"
	m.refresh()
	return m
}

// /rename <agent> <name> renames one agent of this machine through Den's
// checks; the name it had stays its own, still resolving, and no other
// agent can take it while it lives. Nothing renames an agent by itself.
func TestRenameOne(t *testing.T) {
	m := renameModel(t)
	critter := m.base[0].Name
	mm, _ := m.renameCommand([]string{critter, "Security", "Reviewer"})
	*m = mm.(Model)
	if a, _ := m.agentFor(m.base[0].Session.SessionID); a.Name != "security-reviewer" || a.Alias != critter {
		t.Fatalf("renamed %+v; flash %q", a, m.flash)
	}
	mm, _ = m.renameCommand([]string{"security-reviewer", "backend", "engineer"})
	*m = mm.(Model)
	a, _ := m.agentFor(m.base[0].Session.SessionID)
	if a.Name != "backend-engineer" || len(a.Former) != 1 || a.Former[0] != "security-reviewer" {
		t.Fatalf("second rename %+v", a)
	}
	if got, why := m.closeNamed("security-reviewer"); why != "" || got.Session.SessionID != a.Session.SessionID {
		t.Errorf("a former name does not reach the same agent: %+v %q", got, why)
	}
	// Another agent asking for the former name gets a number.
	mm, _ = m.renameCommand([]string{m.base[2].Name, "security", "reviewer"})
	*m = mm.(Model)
	if b, _ := m.agentFor(m.base[2].Session.SessionID); b.Name != "security-reviewer2" {
		t.Errorf("a former name was taken: %+v", b)
	}
	for _, bad := range []string{"orchestrator", "den bot", "close"} {
		mm, _ = m.renameCommand([]string{m.base[1].Name, bad})
		if g := mm.(Model); !g.flashErr {
			t.Errorf("renamed to %q", bad)
		}
	}
	n := 0
	for _, e := range m.orch.chat {
		if e.role == "note" && strings.HasPrefix(e.text, "Renamed ") && strings.Contains(e.text, "(by you)") {
			n++
		}
	}
	if n != 3 {
		t.Errorf("%d rename notes, want 3", n)
	}
}

// /rename alone puts a TTY-only card: each agent with the name Den would
// apply from its own label, quoted, the label beneath; an unusable label
// cannot be picked; only the picked are renamed.
func TestRenameCard(t *testing.T) {
	m := renameModel(t)
	mm, _ := m.renameCommand(nil)
	*m = mm.(Model)
	var it *inbox.Item
	for _, x := range m.store.Items() {
		if x.Kind == inbox.RenamePick {
			it = x
		}
	}
	if it == nil {
		t.Fatalf("no card; flash %q", m.flash)
	}
	body := strings.Join(it.Body, "\n")
	for _, want := range []string{`"@backend-engineer"`, "no usable name", `the agent's own label: "coding agent"`, `"@security-reviewer"`} {
		if !strings.Contains(body, want) {
			t.Errorf("card lacks %q:\n%s", want, body)
		}
	}
	if dockMayAnswer(it) || !godTTYKinds[it.Kind] {
		t.Error("the rename card is not Den's window only")
	}
	if !m.toggleRename(it, 2) || strings.Contains(strings.Join(it.Body, "\n"), "[x] 2.") {
		t.Error("an unusable label was picked")
	}
	m.toggleRename(it, 3)
	mm, _ = m.decideRename(it, "allow")
	*m = mm.(Model)
	if a, _ := m.agentFor(m.base[2].Session.SessionID); a.Name != "security-reviewer" {
		t.Errorf("the picked agent: %+v", a)
	}
	if a, _ := m.agentFor(m.base[0].Session.SessionID); a.Alias != "" {
		t.Errorf("an agent not picked was renamed: %+v", a)
	}
}

// At start, a name whose two records disagree (names.json and the roster's
// Given), or that one has and the other lacks, is dropped, with a note.
func TestNamesCheckedAgainstTheRoster(t *testing.T) {
	m := renameModel(t)
	for _, i := range []int{0, 2} {
		mm, _ := m.renameCommand([]string{m.base[i].Name, "reviewer"})
		*m = mm.(Model)
	}
	m.ensureRestore()
	m.restore.r.Agents = []roster.Agent{
		{SessionID: m.base[0].Session.SessionID, Critter: badge.Name(m.base[0].Session.SessionID), Given: "reviewer2"}, // swapped
		{SessionID: m.base[2].Session.SessionID, Critter: badge.Name(m.base[2].Session.SessionID), Given: "reviewer2"},
		{SessionID: m.base[1].Session.SessionID, Critter: badge.Name(m.base[1].Session.SessionID), Given: "builder"}, // names.json lacks it
	}
	m.checkNames()
	if a, _ := m.agentFor(m.base[0].Session.SessionID); a.Alias != "" {
		t.Errorf("a disagreeing name was kept: %+v", a)
	}
	if a, _ := m.agentFor(m.base[2].Session.SessionID); a.Name != "reviewer2" {
		t.Errorf("an agreeing name was dropped: %+v", a)
	}
	n := 0
	for _, e := range m.orch.chat {
		if e.role == "note" && strings.HasSuffix(e.text, ": its records disagree.") {
			n++
		}
	}
	if n != 2 {
		t.Errorf("%d disagree notes, want 2", n)
	}
}

// Before renaming, Den copies names.json and roster.json aside, once, and
// keeps the first copy.
func TestBackupBeforeRoles(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "names.json"), filepath.Join(dir, "roster.json")
	os.WriteFile(a, []byte("names-before"), 0o600)
	os.WriteFile(b, []byte("roster-before"), 0o600)
	if got := backupBeforeRoles(a, b, filepath.Join(dir, "missing.json")); len(got) != 2 {
		t.Fatalf("copies %v", got)
	}
	os.WriteFile(a, []byte("names-after"), 0o600)
	backupBeforeRoles(a, b)
	// A link planted where the copy goes is never written through.
	c := filepath.Join(dir, "other.json")
	os.WriteFile(c, []byte("other"), 0o600)
	target := filepath.Join(dir, "target")
	os.Symlink(target, c+backupSuffix)
	backupBeforeRoles(c)
	if _, err := os.Stat(target); err == nil {
		t.Error("a copy was written through a link")
	}
	if raw, _ := os.ReadFile(a + backupSuffix); string(raw) != "names-before" {
		t.Errorf("the first copy was replaced: %q", raw)
	}
	if fi, err := os.Stat(b + backupSuffix); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("copy mode %v %v", fi, err)
	}
}
