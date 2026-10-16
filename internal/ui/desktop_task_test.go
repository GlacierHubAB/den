package ui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/desktop"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/notify"
	"github.com/GlacierHubAB/den/internal/spawn"
)

// taskDesk is what the compositor shows: Den's window (pid 300, a process
// above Den) with whatever title Den's terminal last set, the agent view
// once it is up, the human's shell (focused) and an app.
type taskDesk struct {
	title    string // Den's window's title, as the terminal shows it
	noView   bool   // the agent view was closed
	noDen    bool   // Den's window is gone
	stranger bool   // a window of another process shows Den's title too
	viewPin  bool   // the agent view floats, pinned
}

func (d *taskDesk) snap() desktop.Snapshot {
	s := desktop.Snapshot{
		Monitors: []desktop.Monitor{{Name: "DP-1", W: 1920, H: 1080, Workspace: 1}},
		Windows: []desktop.Window{
			{Address: "0xh", Class: "ghostty", Title: "~", PID: 300, Workspace: 1, At: [2]int{960, 0}, Size: [2]int{960, 540}, Mapped: true},
			{Address: "0xa", Class: "zen", Title: "Inbox", PID: 900, Workspace: 1, At: [2]int{960, 540}, Size: [2]int{960, 540}, Mapped: true},
		},
		Active: "0xh",
	}
	if !d.noDen {
		s.Windows = append(s.Windows, desktop.Window{Address: "0xden", Class: "ghostty", Title: d.title, PID: 300, Workspace: 1, At: [2]int{0, 0}, Size: [2]int{960, 1080}, Mapped: true})
	}
	if !d.noView {
		s.Windows = append(s.Windows, desktop.Window{Address: "0xview", Class: "den-desktop", Title: "den · desktop", PID: 300, Workspace: 1, At: [2]int{1500, 0}, Size: [2]int{400, 300}, Mapped: true, Floating: d.viewPin, Pinned: d.viewPin})
	}
	if d.stranger {
		s.Windows = append(s.Windows, desktop.Window{Address: "0xfake", Class: "foot", Title: d.title, PID: 999, Workspace: 1, Mapped: true})
	}
	return s
}

// taskRig is missionRig for task missions: the desktop above, the agent
// view as the window spawn reports, limits of 5 minutes and 3 actions,
// and the stop, pinning and notices recorded.
func taskRig(t *testing.T) (*Model, *taskDesk, *[]spawn.Request, *[]desktop.Targets, *[][2]bool) {
	t.Helper()
	m, spawned, _ := missionRig(t, false)
	d := &taskDesk{}
	deskSnapshot = func(context.Context) (desktop.Snapshot, error) { return d.snap(), nil }
	old := []any{deskLimits, deskAncestors, deskKeepOnTop, deskOwnWait, deskSettle, deskTaskEvery, deskStopFn, deskNotify, deskStart}
	t.Cleanup(func() {
		deskLimits = old[0].(func() (int, int))
		deskAncestors = old[1].(func() []int)
		deskKeepOnTop = old[2].(func(string, bool, bool) error)
		deskOwnWait = old[3].(time.Duration)
		deskSettle = old[4].(time.Duration)
		deskTaskEvery = old[5].(time.Duration)
		deskStopFn = old[6].(func(desktop.Targets, string) desktop.StopReport)
		deskNotify = old[7].(func(notify.Note))
		deskStart = old[8].(func(int) string)
	})
	deskLimits = func() (int, int) { return 5, 3 }
	deskAncestors = func() []int { return []int{300, 1} }
	var pins [][2]bool
	deskKeepOnTop = func(addr string, floating, pinned bool) error {
		if addr != "0xview" {
			t.Errorf("pinned %s, not the agent view", addr)
		}
		pins = append(pins, [2]bool{floating, pinned})
		return nil
	}
	deskOwnWait, deskSettle, deskTaskEvery = 30*time.Millisecond, time.Millisecond, 0
	var stops []desktop.Targets
	deskStopFn = func(tg desktop.Targets, _ string) desktop.StopReport {
		stops = append(stops, tg)
		return desktop.StopReport{Stopped: []string{"hypruse", "the agent"}}
	}
	deskNotify = func(notify.Note) {}
	start := deskStart
	deskStart = func(pid int) string {
		if pid == os.Getpid() {
			return "4242"
		}
		return start(pid)
	}
	deskSpawn = func(_ context.Context, req spawn.Request) (spawn.Result, error) {
		*spawned = append(*spawned, req)
		return spawn.Result{Name: "desktop", Window: "0xview", Registered: true}, nil
	}
	return m, d, spawned, &stops, &pins
}

func taskReq(task string, apps ...string) *bridge.Request {
	payload, _ := json.Marshal(bridge.DesktopMissionInput{Task: task, Apps: apps, Dir: "/", AskedBy: "den-orchestrator"})
	return bridge.TestRequest(bridge.Envelope{Event: bridge.DesktopMissionEvent, Payload: payload})
}

// runTask answers the task's card and runs the handshake, the prepare and
// the spawn as the event loop would; the fake terminal shows whatever
// title Den's view sets, as a real one would.
func runTask(t *testing.T, m *Model, d *taskDesk, verb string) {
	t.Helper()
	it := desktopCard(*m, inbox.DesktopMission)
	if it == nil {
		t.Fatal("no task card")
	}
	m.tab = tabInbox
	m.selectItem(it)
	mm, cmd := m.answer(verb)
	*m = mm.(Model)
	d.title = m.View().WindowTitle
	for _, msg := range runBatch(cmd) {
		own, ok := msg.(deskOwnMsg)
		if !ok {
			continue
		}
		next := m.finishOwn(own)
		d.title = m.View().WindowTitle
		for _, msg := range runBatch(next) {
			if p, ok := msg.(deskPrepared); ok {
				for _, msg := range runBatch(m.finishPrepare(p)) {
					if s, ok := msg.(deskSpawned); ok {
						m.finishSpawn(s)
					}
				}
			}
		}
	}
}

func taskLive(t *testing.T, m Model) *deskMission {
	t.Helper()
	dm := m.deskLive()
	if dm == nil || !dm.Task {
		t.Fatalf("no live task: %+v", dm)
	}
	return dm
}

// The card shows the whole task in Den's fixed sentence, what the agent
// may do while it runs, and the human's limits; nothing starts before ^Y,
// and the dock never answers it.
func TestDesktopTaskCardAndStart(t *testing.T) {
	m, d, spawned, _, _ := taskRig(t)
	task := "open the release notes in the browser and summarise the three biggest changes for me in a new note"
	r := taskReq(task)
	runBatch(m.handleRequest(r))
	it := desktopCard(*m, inbox.DesktopMission)
	if it == nil {
		t.Fatal("no card")
	}
	if !strings.HasPrefix(it.Title, desktop.TaskSentence) {
		t.Errorf("title %q", it.Title)
	}
	body := strings.Join(it.Body, "\n")
	for _, want := range []string{
		desktop.TaskSentence + task,
		"anything you can at the keyboard", "run commands as you", "other agents' terminals and their permission prompts", "clipboard", "signed in",
		"5 minutes or 3 actions", "/desktop stop", "if Den quits", "its own terminal window",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("card lacks %q:\n%s", want, body)
		}
	}
	if dockMayAnswer(it) {
		t.Error("the dock may never answer a task card")
	}
	if _, done := r.Decided(); done || len(*spawned) != 0 {
		t.Fatal("started before the human answered")
	}
	// The whole task shows on an open card, wrapped, even when narrow.
	card := strings.Join(m.inboxBlock(it, true, 50), "\n")
	if !strings.Contains(strings.Join(strings.Fields(ansi.Strip(card)), " "), "three biggest changes for me in a new note") {
		t.Errorf("the open card cut the task:\n%s", ansi.Strip(card))
	}

	runTask(t, m, d, "allow")
	dv, done := r.Decided()
	if !done || dv.Behavior != "allow" || !strings.Contains(dv.Message, "on the task") {
		t.Fatalf("asker got %+v", dv)
	}
	dm := taskLive(t, *m)
	if !dm.Protects("0xden") || !dm.Protects("0xview") || len(dm.Protected) != 2 || !dm.placed {
		t.Fatalf("protected %v placed %v", dm.Protected, dm.placed)
	}
	if dm.Workspace != 0 || dm.limitActions != 3 || time.Until(dm.deadline) > 5*time.Minute || time.Until(dm.deadline) < 4*time.Minute {
		t.Fatalf("scope or limits: ws %d actions %d deadline in %v", dm.Workspace, dm.limitActions, time.Until(dm.deadline))
	}
	if got := m.View().WindowTitle; got != restingTitle {
		t.Errorf("Den's title after the handshake: %q", got)
	}
	if len(*spawned) != 1 {
		t.Fatalf("spawns %d", len(*spawned))
	}
	argv := (*spawned)[0].Custom("desktop")
	joined := strings.Join(argv, " ")
	for _, want := range []string{"--den " + itoa(os.Getpid()) + ":4242", "mcp__hypruse__use_bind", "mcp__hypruse__launch", task} {
		if !strings.Contains(joined, want) {
			t.Errorf("the agent's launch lacks %q", want)
		}
	}
	if slices.Contains(argv, "--tools") {
		i := slices.Index(argv, "--tools")
		if strings.Contains(argv[i+1], "clipboard") || strings.Contains(argv[i+1], "sequence") {
			t.Errorf("tools %s", argv[i+1])
		}
	}
	// The token was only ever the title: never in the chat or the flash.
	for _, e := range m.orch.chat {
		if strings.Contains(e.text, ownTitlePrefix) {
			t.Errorf("the token reached the chat: %q", e.text)
		}
	}
	if strings.Contains(m.flash, ownTitlePrefix) {
		t.Errorf("the token reached the flash: %q", m.flash)
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// During the handshake the title is a fresh token; it is new per task.
func TestDesktopTaskHandshakeToken(t *testing.T) {
	m, _, _, _, _ := taskRig(t)
	seen := map[string]bool{}
	for range 2 {
		mm, _ := m.startTask("x", deskOffer{task: "t"})
		title := mm.(Model).View().WindowTitle
		if !strings.HasPrefix(title, ownTitlePrefix) || len(title) != len(ownTitlePrefix)+32 || seen[title] {
			t.Fatalf("token %q", title)
		}
		seen[title] = true
	}
}

// Den refuses to start when it cannot tell its own window for sure: no
// window shows its title (another tab in front, a multiplexer), one of
// another process does, or two do (H1, H3). The title goes back either
// way, and no agent starts.
func TestDesktopTaskOwnWindowRefusals(t *testing.T) {
	for _, c := range []struct {
		name string
		set  func(*taskDesk)
		want string
	}{
		{"no window shows the title", nil, "switch to its tab"},
		{"two windows show it", func(d *taskDesk) { d.stranger = true }, "more than one window"},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, d, spawned, _, _ := taskRig(t)
			r := taskReq("tidy my downloads folder")
			runBatch(m.handleRequest(r))
			if c.set != nil {
				c.set(d)
				runTask(t, m, d, "allow")
			} else {
				// The terminal never shows Den's title.
				it := desktopCard(*m, inbox.DesktopMission)
				m.tab = tabInbox
				m.selectItem(it)
				mm, cmd := m.answer("allow")
				*m = mm.(Model)
				for _, msg := range runBatch(cmd) {
					if own, ok := msg.(deskOwnMsg); ok {
						runBatch(m.finishOwn(own))
					}
				}
			}
			dv, done := r.Decided()
			if !done || dv.Behavior != "deny" || !strings.Contains(dv.Message, c.want) {
				t.Fatalf("asker got %+v", dv)
			}
			if len(*spawned) != 0 || m.deskLive() != nil {
				t.Fatal("an agent started without Den's window placed")
			}
			if got := m.View().WindowTitle; got != restingTitle {
				t.Errorf("title left as %q", got)
			}
		})
	}
	// The title on a window of a process that is not above Den.
	m, d, spawned, _, _ := taskRig(t)
	deskAncestors = func() []int { return []int{1234} }
	r := taskReq("tidy my downloads folder")
	runBatch(m.handleRequest(r))
	runTask(t, m, d, "allow")
	if dv, _ := r.Decided(); dv.Behavior != "deny" || !strings.Contains(dv.Message, "not Den's terminal") || len(*spawned) != 0 {
		t.Fatalf("a window of another process: %+v", dv)
	}
}

// What the card cannot show whole, or would show differently from what the
// agent gets, never reaches it; a task is never mixed with apps.
func TestDesktopTaskRefusedRequests(t *testing.T) {
	m, _, _, _, _ := taskRig(t)
	for _, c := range []struct {
		r    *bridge.Request
		want string
	}{
		{taskReq(strings.Repeat("click ", 100)), "task too long"},
		{taskReq("open the file" + string(rune(0x202e)) + "txt.exe"), "hidden character"},
		{taskReq("write a note", "editor"), "not both"},
	} {
		runBatch(m.handleRequest(c.r))
		if dv, done := c.r.Decided(); !done || dv.Behavior != "deny" || !strings.Contains(dv.Message, c.want) {
			t.Errorf("want %q, got %+v", c.want, dv)
		}
	}
	if desktopCard(*m, inbox.DesktopMission) != nil {
		t.Fatal("a refused task reached the inbox")
	}
}

// taskMission is a live, placed task mission "m1" whose server started,
// for the call tests.
func taskMission(t *testing.T) (*Model, *taskDesk, *[]desktop.Targets) {
	t.Helper()
	m, d, _, stops, _ := taskRig(t)
	d.title = restingTitle
	// The server is Den's binary (the rig's) started by the agent.
	deskChildOf = func(pid, parent int, start, _ string) bool {
		return parent == 500 && start == "5000" && (pid == 600 || pid == 650)
	}
	m.addDeskMission(&deskMission{
		Mission:   desktop.Mission{ID: "m1", Task: true, Protected: map[string]bool{"0xden": true, "0xview": true}, Windows: map[string]bool{}, PIDs: map[int]bool{}},
		SessionID: "sess-task", AgentPID: 500, AgentStart: "5000", Dir: t.TempDir(),
		view: "0xview", placed: true, limitActions: 3, deadline: time.Now().Add(time.Hour),
	})
	r := deskReq(600, bridge.DesktopInput{Kind: "start"})
	deskDo(m, r)
	dv, done := r.Decided()
	if !done || dv.Behavior != "allow" {
		t.Fatalf("start: %+v", dv)
	}
	var info desktop.StartInfo
	json.Unmarshal(dv.Data, &info)
	if !info.Task || strings.Contains(strings.Join(info.Env, " "), "HYPRUSE_CONFINE") {
		t.Fatalf("a task's server: task %v env %v", info.Task, info.Env)
	}
	r = deskReq(600, bridge.DesktopInput{Kind: "child", ChildPID: 700, ChildStart: "7000"})
	deskDo(m, r)
	if dv, _ := r.Decided(); dv.Behavior != "allow" {
		t.Fatalf("child: %+v", dv)
	}
	return m, d, stops
}

// Den counts every forwarded call, typed pieces included, and ends the
// task at the human's limit: the agent and hypruse are stopped and the
// no-focus rule lifted (F3, P7).
func TestDesktopTaskActionLimit(t *testing.T) {
	m, _, stops := taskMission(t)
	dm := m.deskMission("m1")
	dm.limitActions = 4
	if dv, _ := deskCall(m, "desktop", `{}`); dv.Behavior != "allow" {
		t.Fatalf("desktop: %+v", dv)
	}
	dv, _ := deskCall(m, "keyboard", `{"action":"type","text":"a\nb","window":"0xh"}`)
	if dv.Behavior != "allow" {
		t.Fatalf("type: %+v", dv)
	}
	var ans desktop.Answer
	json.Unmarshal(dv.Data, &ans)
	if ans.Token == "" {
		t.Fatal("no token for the rest of the text")
	}
	piece := func(args string) bridge.Decision {
		r := deskReq(600, bridge.DesktopInput{Kind: "call", Tool: "keyboard", Args: json.RawMessage(args), Token: ans.Token})
		deskDo(m, r)
		d, _ := r.Decided()
		return d
	}
	if dv := piece(`{"action":"key","keys":"Return","window":"0xh"}`); dv.Behavior != "allow" {
		t.Fatalf("second piece: %+v", dv)
	}
	if dm.actions != 3 {
		t.Fatalf("counted %d, want 3", dm.actions)
	}
	if dv := piece(`{"action":"type","text":"b","window":"0xh"}`); dv.Behavior != "allow" {
		t.Fatalf("fourth call: %+v", dv)
	}
	noFocusCalls = nil
	dv, _ = deskCall(m, "pointer", `{"action":"click","x":1200,"y":800}`)
	if dv.Behavior != "deny" || !strings.Contains(dv.Message, "all 4 actions") {
		t.Fatalf("past the limit: %+v", dv)
	}
	if dm.Ended == "" || len(*stops) != 1 || (*stops)[0].Agent.PID != 500 || (*stops)[0].Child.PID != 700 {
		t.Fatalf("ended %q, stops %+v", dm.Ended, *stops)
	}
	if !slices.Contains(noFocusCalls, "") {
		t.Error("the no-focus rule outlived the task")
	}
	if dv, _ := deskCall(m, "desktop", `{}`); dv.Behavior != "deny" {
		t.Fatalf("a call after the end: %+v", dv)
	}
}

// The task's minutes end it, on a call and on the tick alike.
func TestDesktopTaskTimeLimit(t *testing.T) {
	m, _, stops := taskMission(t)
	dm := m.deskMission("m1")
	dm.deadline = time.Now().Add(-time.Second)
	if dv, _ := deskCall(m, "desktop", `{}`); dv.Behavior != "deny" || !strings.Contains(dv.Message, "time ran out") {
		t.Fatalf("a call past the time: %+v", dv)
	}
	if dm.Ended == "" || len(*stops) != 1 {
		t.Fatalf("ended %q stops %d", dm.Ended, len(*stops))
	}

	m, _, stops = taskMission(t)
	dm = m.deskMission("m1")
	dm.deadline = time.Now().Add(-time.Second)
	m.deskOnTick()
	if dm.Ended == "" || len(*stops) != 1 {
		t.Fatalf("on the tick: ended %q stops %d", dm.Ended, len(*stops))
	}
}

// No call is answered before the agent view is placed, and a call that
// finds one of Den's surfaces gone ends the task.
func TestDesktopTaskSurfaces(t *testing.T) {
	m, d, stops := taskMission(t)
	dm := m.deskMission("m1")
	dm.placed = false
	if dv, _ := deskCall(m, "desktop", `{}`); dv.Behavior != "deny" || !strings.Contains(dv.Message, "still placing") || dm.actions != 0 {
		t.Fatalf("before the view is placed: %+v", dv)
	}
	dm.placed = true
	d.noView = true
	if dv, _ := deskCall(m, "desktop", `{}`); dv.Behavior != "deny" || !strings.Contains(dv.Message, "agent view was closed") {
		t.Fatalf("view gone: %+v", dv)
	}
	if dm.Ended == "" || len(*stops) != 1 {
		t.Fatalf("ended %q stops %d", dm.Ended, len(*stops))
	}
}

// Each check pins the agent view again when it is not floating and pinned
// (P5), and ends the task when Den's window is gone; a pin that fails is
// said once.
func TestDesktopTaskCheck(t *testing.T) {
	m, d, stops := taskMission(t)
	dm := m.deskMission("m1")
	run := func() {
		for _, msg := range runBatch(m.deskTaskCheck(dm)) {
			if c, ok := msg.(deskTaskCheckMsg); ok {
				runBatch(m.finishTaskCheck(c))
			}
		}
	}
	var got [][2]bool
	deskKeepOnTop = func(addr string, floating, pinned bool) error {
		got = append(got, [2]bool{floating, pinned})
		return errors.New("hyprctl pin: no")
	}
	run()
	run()
	if len(got) != 2 || got[0] != [2]bool{false, false} {
		t.Fatalf("pins %v", got)
	}
	notes := 0
	for _, e := range m.orch.chat {
		if strings.Contains(e.text, "could not keep the agent view on top") {
			notes++
		}
	}
	if notes != 1 {
		t.Errorf("%d notes about the pin, want 1", notes)
	}
	d.viewPin = true
	run()
	if got[2] != [2]bool{true, true} {
		t.Fatalf("a pinned view was told it was not: %v", got[2])
	}
	d.noDen = true
	run()
	if dm.Ended == "" || !strings.Contains(dm.Ended, "Den's own window is gone") || len(*stops) != 1 {
		t.Fatalf("Den's window gone: ended %q stops %d", dm.Ended, len(*stops))
	}
}

// Ending at the human's step stops the agent too (F3).
func TestDesktopYourTurnEndStopsTheAgent(t *testing.T) {
	m, _, stops := taskMission(t)
	r := deskReq(600, bridge.DesktopInput{Kind: "call", Tool: "your_turn", Args: json.RawMessage(`{"step":"sign in"}`)})
	deskDo(m, r)
	it := desktopCard(*m, inbox.YourTurn)
	if it == nil {
		t.Fatal("no your-turn card")
	}
	*m = answerDeskCard(t, *m, it, "deny")
	if dm := m.deskMission("m1"); dm.Ended == "" || len(*stops) != 1 || (*stops)[0].Agent.PID != 500 {
		t.Fatalf("ended %q stops %+v", dm.Ended, *stops)
	}
}
