package ui

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/desktop"
	"github.com/GlacierHubAB/den/internal/inbox"
)

// The test desktop: the mission on workspace 9 holds an editor (0xe) and a
// browser (0xb, outward); the human's terminal (0xh) is on workspace 1.
// The agent is pid 500, its proxy 600, hypruse 700.
func deskRig(t *testing.T) Model {
	t.Helper()
	old := []any{deskSnapshot, deskVerify, deskExe, deskChildOf, deskStart, deskParent, deskGrab}
	oldWait := deskCardWait
	deskCardWait = time.Millisecond // runBatch runs the card's timer; deskDo drops its message
	t.Cleanup(func() {
		deskCardWait = oldWait
		deskSnapshot = old[0].(func(context.Context) (desktop.Snapshot, error))
		deskVerify = old[1].(func() error)
		deskExe = old[2].(func() string)
		deskChildOf = old[3].(func(int, int, string, string) bool)
		deskStart = old[4].(func(int) string)
		deskParent = old[5].(func(int) (int, string, bool))
		deskGrab = old[6].(func(context.Context, desktop.Window, string) error)
	})
	deskSnapshot = func(context.Context) (desktop.Snapshot, error) {
		return desktop.Snapshot{
			Monitors: []desktop.Monitor{{Name: "DP-1", W: 1920, H: 1080, Workspace: 9}, {Name: "DP-2", X: 1920, W: 1920, H: 1080, Workspace: 1}},
			Windows: []desktop.Window{
				{Address: "0xe", Class: "foot-den", Title: "note", PID: 200, Workspace: 9, At: [2]int{960, 0}, Size: [2]int{960, 1080}, Mapped: true},
				{Address: "0xb", Class: "zen", Title: "Inbox", PID: 100, Workspace: 9, At: [2]int{0, 0}, Size: [2]int{960, 1080}, Mapped: true},
				{Address: "0xh", Class: "ghostty", Title: "~", PID: 400, Workspace: 1, At: [2]int{1920, 0}, Size: [2]int{1920, 1080}, Mapped: true},
			},
			Active: "0xe",
		}, nil
	}
	deskVerify = func() error { return nil }
	deskExe = func() string { return "/usr/bin/den" }
	deskChildOf = func(pid, parent int, start, exe string) bool {
		return parent == 500 && start == "5000" && exe == "/usr/bin/den" && (pid == 600 || pid == 650)
	}
	deskStart = func(pid int) string {
		return map[int]string{500: "5000", 600: "6000", 650: "6500", 700: "7000"}[pid]
	}
	deskParent = func(pid int) (int, string, bool) {
		if pid == 700 {
			return 600, "python", true
		}
		return 1, "x", true
	}
	deskGrab = func(context.Context, desktop.Window, string) error { return nil }
	m := closeModel(t)
	m.addDeskMission(&deskMission{
		Mission: desktop.Mission{
			ID: "m1", Workspace: 9,
			Apps:    []desktop.App{{Name: "editor", Class: "foot-den"}, {Name: "browser", Class: "zen", Outward: desktop.OutwardAsk}},
			Windows: map[string]bool{"0xe": true, "0xb": true},
			PIDs:    map[int]bool{100: true, 200: true},
		},
		SessionID: "sess-desk", AgentPID: 500, AgentStart: "5000", Dir: t.TempDir(),
	})
	return m
}

func deskReq(pid int, in bridge.DesktopInput) *bridge.Request {
	in.Mission = "m1"
	payload, _ := json.Marshal(in)
	r := bridge.TestRequest(bridge.Envelope{Event: bridge.DesktopEvent, Payload: payload})
	r.PeerPID = pid
	return r
}

// deskDo runs a request through Den as the event loop would.
func deskDo(m *Model, r *bridge.Request) {
	for _, msg := range runBatch(m.handleRequest(r)) {
		switch msg := msg.(type) {
		case deskSnapMsg:
			runBatch(m.decideDesktopCall(msg))
		}
	}
}

func deskStarted(t *testing.T, m *Model) {
	t.Helper()
	r := deskReq(600, bridge.DesktopInput{Kind: "start"})
	deskDo(m, r)
	d, done := r.Decided()
	if !done || d.Behavior != "allow" {
		t.Fatalf("start: %+v", d)
	}
	var info desktop.StartInfo
	json.Unmarshal(d.Data, &info)
	env := strings.Join(info.Env, " ")
	for _, want := range []string{"HYPRUSE_CONFINE=workspace:9", "HYPRUSE_STRICT=1", "HYPRUSE_JOURNAL=", "PYTHONDONTWRITEBYTECODE=1", "HYPRUSE_SCREENSHOT_MODE=image"} {
		if !strings.Contains(env, want) {
			t.Errorf("hypruse env lacks %s: %s", want, env)
		}
	}
	if strings.Contains(env, "CLIPBOARD") {
		t.Error("the clipboard tool must never be enabled")
	}
	if got := strings.Join(info.Argv, " "); !strings.HasSuffix(got, "bin/python -I -B -m hypruse") {
		t.Errorf("argv %s", got)
	}
	r = deskReq(600, bridge.DesktopInput{Kind: "child", ChildPID: 700, ChildStart: "7000"})
	deskDo(m, r)
	if d, _ := r.Decided(); d.Behavior != "allow" {
		t.Fatalf("child: %+v", d)
	}
}

func deskCall(m *Model, tool, args string) (bridge.Decision, bool) {
	r := deskReq(600, bridge.DesktopInput{Kind: "call", Tool: tool, Args: json.RawMessage(args)})
	deskDo(m, r)
	return r.Decided()
}

func TestDesktopStartOnlyFromTheAgentsServer(t *testing.T) {
	m := deskRig(t)
	r := deskReq(999, bridge.DesktopInput{Kind: "start"})
	deskDo(&m, r)
	if d, _ := r.Decided(); d.Behavior != "deny" {
		t.Fatalf("a stranger started the mission: %+v", d)
	}
	deskVerify = func() error { return errors.New("venv changed") }
	r = deskReq(600, bridge.DesktopInput{Kind: "start"})
	deskDo(&m, r)
	if d, _ := r.Decided(); d.Behavior != "deny" || !strings.Contains(d.Message, "venv changed") {
		t.Fatalf("a changed venv started: %+v", d)
	}
	deskVerify = func() error { return nil }
	deskStarted(t, &m)
	// A second server while the first runs is refused.
	r = deskReq(650, bridge.DesktopInput{Kind: "start"})
	deskDo(&m, r)
	if d, _ := r.Decided(); d.Behavior != "deny" {
		t.Fatalf("a second server started: %+v", d)
	}
	// Calls from anyone but the server are refused.
	r = deskReq(650, bridge.DesktopInput{Kind: "call", Tool: "desktop", Args: json.RawMessage(`{}`)})
	deskDo(&m, r)
	if d, _ := r.Decided(); d.Behavior != "deny" {
		t.Fatalf("a call from another process: %+v", d)
	}
}

func TestDesktopCallsAllowDenyAndScope(t *testing.T) {
	m := deskRig(t)
	deskStarted(t, &m)
	d, _ := deskCall(&m, "desktop", `{}`)
	var a desktop.Answer
	json.Unmarshal(d.Data, &a)
	if d.Behavior != "allow" || a.Scope == nil || strings.Join(a.Scope.Windows, ",") != "0xe,0xb" {
		t.Fatalf("desktop: %+v %+v", d, a.Scope)
	}
	d, _ = deskCall(&m, "keyboard", `{"action":"type","text":"rm -rf ~","window":"0xh"}`)
	if d.Behavior != "deny" {
		t.Fatalf("typing into the human's terminal: %+v", d)
	}
	d, _ = deskCall(&m, "keyboard", `{"action":"type","text":"`+strings.Repeat("a", 40)+`"}`)
	json.Unmarshal(d.Data, &a)
	if d.Behavior != "allow" || a.Token == "" {
		t.Fatalf("typing in the editor: %+v", d)
	}
	var args map[string]any
	json.Unmarshal(a.Args, &args)
	if args["window"] != "0xe" {
		t.Fatalf("forwarded %v", args)
	}
	// The next piece rides on the token, once.
	r := deskReq(600, bridge.DesktopInput{Kind: "call", Tool: "keyboard", Token: a.Token, Args: json.RawMessage(`{"action":"type","text":"aaaaaaaa","window":"0xe"}`)})
	deskDo(&m, r)
	if d, _ := r.Decided(); d.Behavior != "allow" {
		t.Fatalf("second piece: %+v", d)
	}
	r = deskReq(600, bridge.DesktopInput{Kind: "call", Tool: "keyboard", Token: a.Token, Args: json.RawMessage(`{"action":"type","text":"more","window":"0xe"}`)})
	deskDo(&m, r)
	if d, _ := r.Decided(); d.Behavior != "deny" {
		t.Fatalf("a spent token: %+v", d)
	}
}

func desktopCard(m Model, kind inbox.Kind) *inbox.Item {
	for _, it := range m.store.Items() {
		if it.Kind == kind && it.State == inbox.Waiting {
			return it
		}
	}
	return nil
}

func answerDeskCard(t *testing.T, m Model, it *inbox.Item, verb string) Model {
	t.Helper()
	m.tab = tabInbox
	m.selectItem(it)
	mm, cmd := m.answer(verb)
	runBatch(cmd)
	return mm.(Model)
}

func TestDesktopAskWaitsForTheHuman(t *testing.T) {
	m := deskRig(t)
	deskStarted(t, &m)
	r := deskReq(600, bridge.DesktopInput{Kind: "call", Tool: "keyboard", Args: json.RawMessage(`{"action":"type","text":"hello\nthere","window":"0xb"}`)})
	var cmds []tea.Cmd
	for _, msg := range runBatch(m.handleRequest(r)) {
		if s, ok := msg.(deskSnapMsg); ok {
			cmds = append(cmds, m.decideDesktopCall(s))
		}
	}
	if _, done := r.Decided(); done {
		t.Fatal("an outward call was answered before the human")
	}
	it := desktopCard(m, inbox.DesktopAct)
	if it == nil {
		t.Fatal("no card")
	}
	body := strings.Join(it.Body, "\n")
	for _, want := range []string{"why Den asks", "zen", "hello\u23cethere", "limits:"} {
		if !strings.Contains(body, want) {
			t.Errorf("card lacks %q:\n%s", want, body)
		}
	}
	if dockMayAnswer(it) {
		t.Error("the dock may never answer a desktop card")
	}
	m = answerDeskCard(t, m, it, "allow")
	d, done := r.Decided()
	var a desktop.Answer
	json.Unmarshal(d.Data, &a)
	if !done || d.Behavior != "allow" || a.Token == "" {
		t.Fatalf("after ^Y: %+v", d)
	}

	// ^N denies with a message the agent understands.
	r = deskReq(600, bridge.DesktopInput{Kind: "call", Tool: "click_ui", Args: json.RawMessage(`{"name":"Send","window":"0xe"}`)})
	deskDo(&m, r)
	m = answerDeskCard(t, m, desktopCard(m, inbox.DesktopAct), "deny")
	if d, _ := r.Decided(); d.Behavior != "deny" || !strings.Contains(d.Message, "said no") {
		t.Fatalf("after ^N: %+v", d)
	}
}

func TestDesktopCardTimesOutToDeny(t *testing.T) {
	m := deskRig(t)
	deskStarted(t, &m)
	r := deskReq(600, bridge.DesktopInput{Kind: "call", Tool: "request_approval", Args: json.RawMessage(`{"intent":"send the mail to the team"}`)})
	deskDo(&m, r)
	it := desktopCard(m, inbox.DesktopAct)
	if it == nil || !strings.Contains(strings.Join(it.Body, "\n"), "the agent says: \u201csend the mail to the team\u201d") {
		t.Fatalf("approval card: %+v", it)
	}
	m.timeoutDesktopCard(deskCardTimeoutMsg{itemID: it.ID})
	if d, _ := r.Decided(); d.Behavior != "deny" || !strings.Contains(d.Message, "in time") {
		t.Fatalf("after the timeout: %+v", d)
	}
}

func TestDesktopYourTurnPausesEverything(t *testing.T) {
	m := deskRig(t)
	deskStarted(t, &m)
	turn := deskReq(600, bridge.DesktopInput{Kind: "call", Tool: "your_turn", Args: json.RawMessage(`{"step":"sign in to Gmail"}`)})
	deskDo(&m, turn)
	it := desktopCard(m, inbox.YourTurn)
	if it == nil {
		t.Fatal("no your-turn card")
	}
	if dockMayAnswer(it) {
		t.Error("the dock may never answer a your-turn card")
	}
	if d, _ := deskCall(&m, "screenshot", `{"window":"0xe"}`); d.Behavior != "deny" || !strings.Contains(d.Message, "paused") {
		t.Fatalf("observing during the human's step: %+v", d)
	}
	m = answerDeskCard(t, m, it, "allow")
	if d, _ := turn.Decided(); d.Behavior != "allow" || !strings.Contains(d.Message, "sign in to Gmail") {
		t.Fatalf("your_turn after ^Y: %+v", d)
	}
	if d, _ := deskCall(&m, "screenshot", `{"window":"0xe"}`); d.Behavior != "allow" {
		t.Fatalf("after the step: %+v", d)
	}
	// ^N on a your-turn card ends the mission.
	turn = deskReq(600, bridge.DesktopInput{Kind: "call", Tool: "your_turn", Args: json.RawMessage(`{"step":"enter the code"}`)})
	deskDo(&m, turn)
	m = answerDeskCard(t, m, desktopCard(m, inbox.YourTurn), "deny")
	if d, _ := deskCall(&m, "desktop", `{}`); d.Behavior != "deny" || !strings.Contains(d.Message, "ended") {
		t.Fatalf("after ending: %+v", d)
	}
}

func TestDesktopGuardBrokenDeniesAll(t *testing.T) {
	m := deskRig(t)
	deskStarted(t, &m)
	m.deskMission("m1").Guard = func() string { return "its settings file changed" }
	if d, _ := deskCall(&m, "desktop", `{}`); d.Behavior != "deny" || !strings.Contains(d.Message, "settings file changed") {
		t.Fatalf("with the guard broken: %+v", d)
	}
}

func TestDesktopGateHook(t *testing.T) {
	m := deskRig(t)
	ask := func(pid int, session, tool string) bridge.Decision {
		payload, _ := json.Marshal(map[string]any{"session_id": session, "hook_event_name": "PreToolUse", "tool_name": tool})
		r := bridge.TestRequest(bridge.Envelope{Event: bridge.DesktopGateEvent, SessionID: session, Payload: payload})
		r.PeerPID = pid
		runBatch(m.handleRequest(r))
		d, _ := r.Decided()
		return d
	}
	if d := ask(650, "sess-desk", "mcp__hypruse__pointer"); d.Behavior != "allow" {
		t.Fatalf("the agent's own hook, an allowed tool: %+v", d)
	}
	for _, tool := range []string{"Bash", "mcp__hypruse__launch", "mcp__den__spawn_agent", "SendMessage"} {
		if d := ask(650, "sess-desk", tool); d.Behavior != "deny" {
			t.Errorf("%s: %+v", tool, d)
		}
	}
	if d := ask(999, "sess-desk", "mcp__hypruse__pointer"); d.Behavior != "deny" {
		t.Fatalf("a hook from another process: %+v", d)
	}
	if d := ask(650, "other", "mcp__hypruse__pointer"); d.Behavior != "deny" {
		t.Fatalf("another session: %+v", d)
	}
}
