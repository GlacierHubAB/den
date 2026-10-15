package ui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/desktop"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/permmode"
	"github.com/GlacierHubAB/den/internal/spawn"
)

// missionRig fakes the apps config, the desktop, launching, moving and
// spawning. Workspaces 5 to 9 are empty except 9, so Den picks 8.
func missionRig(t *testing.T, joined bool) (*Model, *[]spawn.Request, *[]string) {
	t.Helper()
	m := deskRig(t)
	delete(m.desk.missions, "m1")
	old := []any{deskApps, deskLaunch, deskMove, deskClose, deskSpawn, deskSession, deskStateDir, deskSocket}
	t.Cleanup(func() {
		deskApps = old[0].(func() ([]desktop.App, []string))
		deskLaunch = old[1].(func(context.Context, desktop.App, string, desktop.Snapshot) (deskLaunched, error))
		deskMove = old[2].(func(string, int) error)
		deskClose = old[3].(func(string) error)
		deskSpawn = old[4].(func(context.Context, spawn.Request) (spawn.Result, error))
		deskSession = old[5].(func(string) (int, bool))
		deskStateDir = old[6].(func() string)
		deskSocket = old[7].(func() string)
	})
	state := t.TempDir()
	den := filepath.Join(state, "den")
	os.WriteFile(den, []byte("den"), 0o755)
	deskExe = func() string { return den }
	deskStateDir = func() string { return filepath.Join(state, "desktop") }
	deskSocket = func() string { return "/run/user/1/den.sock" }
	deskApps = func() ([]desktop.App, []string) {
		return []desktop.App{
			{Name: "editor", Command: []string{"foot", "nvim"}, Class: "foot-den", Outward: "no", IfRunning: "refuse"},
			{Name: "browser", Command: []string{"zen"}, Class: "zen", Outward: desktop.OutwardAsk, Profile: []string{"--profile", "{dir}"}, IfRunning: "refuse"},
		}, nil
	}
	n := 0
	deskLaunch = func(_ context.Context, a desktop.App, dir string, _ desktop.Snapshot) (deskLaunched, error) {
		n++
		return deskLaunched{App: a, Address: "0xn" + string(rune('0'+n)), PID: 1000 + n, Joined: joined && a.Name == "browser"}, nil
	}
	var moved []string
	deskMove = func(addr string, ws int) error {
		moved = append(moved, addr+"@"+string(rune('0'+ws)))
		return nil
	}
	var closed []string
	deskClose = func(addr string) error {
		closed = append(closed, addr)
		return nil
	}
	oldFocus := deskFocus
	t.Cleanup(func() { deskFocus = oldFocus })
	deskFocus = func(addr string) error {
		focusedBack = append(focusedBack, addr)
		return nil
	}
	var spawned []spawn.Request
	deskSpawn = func(_ context.Context, req spawn.Request) (spawn.Result, error) {
		spawned = append(spawned, req)
		return spawn.Result{Name: "desktop", Registered: true}, nil
	}
	deskSession = func(string) (int, bool) { return 500, true }
	oldClaude, oldSame := deskClaude, deskSameExe
	t.Cleanup(func() { deskClaude, deskSameExe = oldClaude, oldSame })
	deskClaude = func() (string, error) { return "/opt/claude/claude", nil }
	deskSameExe = func(pid int, path string) bool { return pid == 500 && path == "/opt/claude/claude" }
	_ = moved
	return &m, &spawned, &closed
}

// focusedBack records where Den gave the focus back after a launch.
var focusedBack []string

func missionReq(apps ...string) *bridge.Request {
	payload, _ := json.Marshal(bridge.DesktopMissionInput{Plan: "write a note in the editor", Apps: apps, HumanSteps: []string{"sign in to Gmail"}, Dir: "/", AskedBy: "den-orchestrator"})
	return bridge.TestRequest(bridge.Envelope{Event: bridge.DesktopMissionEvent, Payload: payload})
}

// runMission answers the card and runs the prepare and spawn steps as the
// event loop would.
func runMission(t *testing.T, m *Model, verb string) {
	t.Helper()
	it := desktopCard(*m, inbox.DesktopMission)
	if it == nil {
		t.Fatal("no mission card")
	}
	m.tab = tabInbox
	m.selectItem(it)
	mm, cmd := m.answer(verb)
	*m = mm.(Model)
	for _, msg := range runBatch(cmd) {
		if p, ok := msg.(deskPrepared); ok {
			for _, msg := range runBatch(m.finishPrepare(p)) {
				if s, ok := msg.(deskSpawned); ok {
					m.finishSpawn(s)
				}
			}
		}
	}
}

func TestDesktopMissionCardAndStart(t *testing.T) {
	m, spawned, _ := missionRig(t, false)
	r := missionReq("editor", "browser")
	runBatch(m.handleRequest(r))
	it := desktopCard(*m, inbox.DesktopMission)
	if it == nil {
		t.Fatal("no card")
	}
	body := strings.Join(it.Body, "\n")
	for _, want := range []string{"write a note", "editor: foot nvim", "on a profile of its own", "on your normal profile", "every click and keystroke there asks you", "sign in to Gmail", "never types credentials", "no shell"} {
		if !strings.Contains(body, want) {
			t.Errorf("card lacks %q:\n%s", want, body)
		}
	}
	if dockMayAnswer(it) {
		t.Error("the dock may never answer a mission card")
	}
	if _, done := r.Decided(); done || len(*spawned) != 0 {
		t.Fatal("started before the human answered")
	}
	runMission(t, m, "allow")
	d, done := r.Decided()
	if !done || d.Behavior != "allow" || !strings.Contains(d.Message, "workspace 8") {
		t.Fatalf("asker got %+v", d)
	}
	if len(*spawned) != 1 {
		t.Fatalf("spawns %d", len(*spawned))
	}
	// The human had the editor (0xe) focused: the focus goes back there,
	// so no new window keeps the keyboard.
	if len(focusedBack) == 0 || focusedBack[len(focusedBack)-1] != "0xe" {
		t.Fatalf("focus given back to %v", focusedBack)
	}
	// The rule names the agent's marker class only, and is lifted once
	// the terminal is up.
	if len(noFocusCalls) < 1 || noFocusCalls[len(noFocusCalls)-1] != "" {
		t.Fatalf("rule calls %q: not lifted after the launch", noFocusCalls)
	}
	noFocusCalls = nil
	argv := (*spawned)[0].Custom("desktop")
	if len(noFocusCalls) != 1 || noFocusCalls[0] != "io.glacierhub.den.desktop" {
		t.Fatalf("rule declared for %q", noFocusCalls)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{"--strict-mcp-config", "--permission-mode dontAsk", `"desktop-mcp","--mission"`, "--settings ", "--session-id "} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv lacks %q: %s", want, joined)
		}
	}
	var dm *deskMission
	for _, x := range m.desk.missions {
		dm = x
	}
	if dm == nil || dm.Workspace != 8 || dm.AgentPID != 500 || len(dm.Windows) != 2 || !dm.PIDs[1001] || dm.Guard == nil {
		t.Fatalf("mission %+v", dm)
	}
	if why := dm.Guard(); why != "" {
		t.Fatalf("guard: %s", why)
	}
	if st, err := os.Stat(dm.Dir); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("mission dir: %v %v", st, err)
	}
	// One at a time.
	r2 := missionReq("editor")
	runBatch(m.handleRequest(r2))
	if d, _ := r2.Decided(); d.Behavior != "deny" {
		t.Fatalf("a second mission: %+v", d)
	}
}

func TestDesktopMissionRefusals(t *testing.T) {
	m, _, _ := missionRig(t, false)
	r := missionReq("terminal")
	runBatch(m.handleRequest(r))
	if d, _ := r.Decided(); d.Behavior != "deny" || !strings.Contains(d.Message, "editor, browser") {
		t.Fatalf("unknown app: %+v", d)
	}
	deskVerify = func() error { return os.ErrNotExist }
	r = missionReq("editor")
	runBatch(m.handleRequest(r))
	if d, _ := r.Decided(); d.Behavior != "deny" || !strings.Contains(d.Message, "not ready") {
		t.Fatalf("not installed: %+v", d)
	}
}

// An app that joins an instance already running is refused, and the
// windows Den opened so far are closed; the joined one is not touched.
func TestDesktopMissionJoinedInstanceRefused(t *testing.T) {
	m, spawned, closed := missionRig(t, true)
	r := missionReq("editor", "browser")
	runBatch(m.handleRequest(r))
	runMission(t, m, "allow")
	if d, _ := r.Decided(); d.Behavior != "deny" || !strings.Contains(d.Message, "already running") {
		t.Fatalf("asker got %+v", d)
	}
	if len(*spawned) != 0 {
		t.Fatal("an agent started")
	}
	if strings.Join(*closed, ",") != "0xn1" {
		t.Fatalf("closed %v", *closed)
	}
}

func TestFreeWorkspace(t *testing.T) {
	s := desktop.Snapshot{Windows: []desktop.Window{{Workspace: 9}, {Workspace: 8}, {Workspace: 7}, {Workspace: 6}, {Workspace: 5}}}
	if ws := freeWorkspace(s); ws != 0 {
		t.Fatalf("all used: %d", ws)
	}
	s.Windows = s.Windows[1:]
	if ws := freeWorkspace(s); ws != 9 {
		t.Fatalf("9 free: %d", ws)
	}
}

func TestNewSessionID(t *testing.T) {
	id := newSessionID()
	if len(id) != 36 || id[14] != '4' || !strings.ContainsRune("89ab", rune(id[19])) {
		t.Fatalf("%s", id)
	}
}

// Only the human at Den's own window answers a mission card: not a
// console, even if the dock rule changed, and never with always.
func TestDesktopMissionTTYOnly(t *testing.T) {
	m, spawned, _ := missionRig(t, false)
	r := missionReq("editor")
	runBatch(m.handleRequest(r))
	it := desktopCard(*m, inbox.DesktopMission)
	if dockMayAnswer(it) || len(m.dockCard(it).Verbs) != 2 {
		t.Fatal("the dock must get no verbs it may use")
	}
	it.AnsweredBy = "console"
	mm, cmd := m.answerItem(it, "allow")
	*m = mm.(Model)
	runBatch(cmd)
	if it.State == inbox.Resolved || len(*spawned) != 0 {
		t.Fatal("a console answer started a mission")
	}
	it.AnsweredBy = ""
	mm, cmd = m.answerItem(it, "always")
	*m = mm.(Model)
	runBatch(cmd)
	if it.State == inbox.Resolved {
		t.Fatal("always answered a mission card")
	}
	if _, done := r.Decided(); done {
		t.Fatal("the asker was answered")
	}
}

// desktop_mission comes from the orchestrator's own den mcp, a process it
// spawned: it is handled above the orchestrator's restriction, so the card
// still comes up and the human's answer still reaches it. Den's global
// mode, however loose, never reaches the desktop agent's launch line.
func TestDesktopMissionFromOrchestratorsDenMCP(t *testing.T) {
	m, spawned, _ := missionRig(t, false)
	proc, _ := liveOrchestrator(t)
	m.orch.proc = proc
	m.mode.global = permmode.Bypass
	r := missionReq("editor")
	r.PeerPID = orchChildPID
	if !m.fromOrchestratorSession(r) {
		t.Fatal("the request is not taken for the orchestrator's")
	}
	runBatch(m.handleRequest(r))
	if d, done := r.Decided(); done {
		t.Fatalf("answered before the card: %+v", d)
	}
	if desktopCard(*m, inbox.DesktopMission) == nil {
		t.Fatal("no mission card")
	}
	runMission(t, m, "allow")
	if d, done := r.Decided(); !done || d.Behavior != "allow" {
		t.Fatalf("asker got %+v %v", d, done)
	}
	if len(*spawned) != 1 || (*spawned)[0].PermissionMode != "" {
		t.Fatalf("spawns %+v", *spawned)
	}
	joined := strings.Join((*spawned)[0].Custom("desktop"), " ")
	if strings.Count(joined, "--permission-mode") != 1 || !strings.Contains(joined, "--permission-mode dontAsk") {
		t.Errorf("argv: %s", joined)
	}
}
