package ui

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/api"
	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/hypr"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/procs"
	"github.com/GlacierHubAB/den/internal/view"
	"github.com/GlacierHubAB/den/internal/winmark"
)

// viewWM is a window manager with a few windows; Move changes them. No real
// hyprctl.
type viewWM struct {
	mu    sync.Mutex
	cs    []hypr.Client
	moves []string
	down  bool
}

func (w *viewWM) Available() bool { return !w.down }

func (w *viewWM) Clients(context.Context) ([]hypr.Client, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]hypr.Client(nil), w.cs...), nil
}

func (w *viewWM) Move(_ context.Context, addr, ws string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.moves = append(w.moves, addr+" "+ws)
	for i := range w.cs {
		if w.cs[i].Address == addr {
			w.cs[i].Workspace.Name = ws
			id, _ := strconv.Atoi(ws)
			w.cs[i].Workspace.ID = id
		}
	}
	return nil
}

func (w *viewWM) Active(context.Context) (hypr.Workspace, bool) { return hypr.Workspace{ID: 1}, true }

func (w *viewWM) where(addr string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, c := range w.cs {
		if c.Address == addr {
			return c.Workspace.Name
		}
	}
	return ""
}

func viewClient(addr, class string, pid, ws int) hypr.Client {
	c := hypr.Client{Address: addr, PID: pid, Class: class, InitialClass: class}
	c.Workspace.ID, c.Workspace.Name = ws, strconv.Itoa(ws)
	return c
}

// viewRig is a Model with two live local agents Den spawned (a window each),
// one gone, one on a remote host, and Den's own window known.
func viewRig(t *testing.T) (*Model, *viewWM) {
	t.Helper()
	apiEnv(t)
	wm := &viewWM{}
	m := testModel(140, 30)
	ag := func(name, sid string, pid int, st fleet.Status, host string) fleet.Agent {
		return fleet.Agent{Name: name, Status: st, Host: host, Session: discover.Session{SessionID: sid, PID: pid, Alive: st != fleet.Gone}}
	}
	m.agents = []fleet.Agent{
		ag("A", "s-a", 101, fleet.Idle, ""),
		ag("B", "s-b", 102, fleet.Working, ""),
		ag("C", "s-c", 103, fleet.Gone, ""),
		ag("R", "s-r", 0, fleet.Idle, "devbox"),
	}
	chains := map[int][]procs.Proc{}
	for i, p := range []struct{ peer, sid string }{{"a", "s-a"}, {"b", "s-b"}} {
		mk := "io.glacierhub.den." + p.peer
		if err := winmark.Record(p.peer, mk); err != nil {
			t.Fatal(err)
		}
		winmark.Bind(p.peer, p.sid, func(string) bool { return true })
		pid := 101 + i
		chains[pid] = []procs.Proc{{PID: pid}, {PID: 1000 + pid}}
		wm.cs = append(wm.cs, viewClient("0x"+p.peer, mk, 1000+pid, 2+i))
	}
	wm.cs = append(wm.cs, viewClient("0xden", "com.mitchellh.ghostty", 900, 1))
	sw := &view.Switcher{
		Ops:       wm,
		Marker:    winmark.For,
		Ancestors: func(pid int) []procs.Proc { return chains[pid] },
		Own: func(all []hypr.Client) (hypr.Client, bool) {
			for _, c := range all {
				if c.Address == "0xden" {
					return c, true
				}
			}
			return hypr.Client{}, false
		},
		StatePath: filepath.Join(t.TempDir(), "view.json"),
	}
	was := viewSwitcher
	viewSwitcher = func() *view.Switcher { return sw }
	t.Cleanup(func() { viewSwitcher = was })
	return &m, wm
}

func switchView(t *testing.T, m *Model, want string) (api.ViewReply, viewDoneMsg) {
	t.Helper()
	r := &api.ViewReq{Want: want, Reply: make(chan api.ViewReply, 1)}
	cmd := m.applyAPIAct(api.Act{View: r})
	if cmd == nil {
		t.Fatal("no switch")
	}
	done, _ := cmd().(viewDoneMsg)
	return <-r.Reply, done
}

// TestViewSwitchHidesAndShowsDensWindows: the live agents on this machine
// and Den's own window go to special:den and come back; a gone agent and
// an agent on another host are not in it.
func TestViewSwitchHidesAndShowsDensWindows(t *testing.T) {
	m, wm := viewRig(t)
	rep, done := switchView(t, m, api.ViewUser)
	if rep.Code != "" || rep.Result != (api.ViewResult{View: api.ViewUser, Shown: 0, Total: 3}) {
		t.Fatalf("hide: %+v", rep)
	}
	if done.view != api.ViewUser {
		t.Errorf("view after a hide: %q", done.view)
	}
	for _, a := range []string{"0xa", "0xb", "0xden"} {
		if wm.where(a) != api.ViewWorkspace {
			t.Errorf("%s is on %q", a, wm.where(a))
		}
	}
	rep, done = switchView(t, m, api.ViewAgent)
	if rep.Result != (api.ViewResult{View: api.ViewAgent, Shown: 3, Total: 3}) || done.view != api.ViewAgent {
		t.Fatalf("show: %+v %+v", rep, done)
	}
	for a, ws := range map[string]string{"0xa": "2", "0xb": "3", "0xden": "1"} {
		if wm.where(a) != ws {
			t.Errorf("%s came back to %q, want %s", a, wm.where(a), ws)
		}
	}
}

// TestViewDoneUpdatesWhatTheAPISays: the API starts with the view Den is
// in, and after a switch hello and the fleet carry the new one.
func TestViewDoneUpdatesWhatTheAPISays(t *testing.T) {
	m, _ := viewRig(t)
	if cmd := m.startAPI(); cmd == nil {
		t.Fatal("the API did not start")
	}
	defer m.closeAPI()
	if m.api.view != api.ViewAgent {
		t.Fatalf("view at start: %q", m.api.view)
	}
	c := dialAPI(t, m)
	hello := c.call("hello", map[string]any{"v": 1, "client": "test"})["result"].(map[string]any)
	if hello["view"] != "agent" {
		t.Errorf("hello: %v", hello)
	}
	mm, _ := m.Update(viewDoneMsg{view: api.ViewUser})
	got := mm.(Model)
	if got.api.view != api.ViewUser {
		t.Fatalf("view after a switch: %q", got.api.view)
	}
	hello = c.call("hello", map[string]any{"v": 1, "client": "test"})["result"].(map[string]any)
	fleetNow := c.call("fleet.snapshot", nil)["result"].(map[string]any)
	if hello["view"] != "user" || fleetNow["view"] != "user" {
		t.Errorf("hello %v, fleet %v", hello, fleetNow)
	}
	if ws, _ := hello["view_workspaces"].([]any); len(ws) != 1 || ws[0] != "special:den" {
		t.Errorf("view_workspaces: %v", hello["view_workspaces"])
	}
}

// TestViewKeepsDensWindowWhileACardWaits: a card waiting on the human
// keeps Den's own window where it is, and the reply counts it as shown.
func TestViewKeepsDensWindowWhileACardWaits(t *testing.T) {
	m, wm := viewRig(t)
	m.store.AddClose("s-a", "Close A?", []string{"A is idle"}, nil)
	rep, _ := switchView(t, m, api.ViewUser)
	if rep.Result.Shown != 1 || rep.Result.Total != 3 || !strings.HasPrefix(rep.Result.Reason, "1 of 3 not hidden") {
		t.Fatalf("hide with a card: %+v", rep)
	}
	if wm.where("0xden") == api.ViewWorkspace {
		t.Error("Den's window was hidden with a card pending")
	}
	if wm.where("0xa") != api.ViewWorkspace || wm.where("0xb") != api.ViewWorkspace {
		t.Error("the agents' windows were not hidden")
	}
}

// TestViewSwitchErrorsKeepTheirCodes: a close in progress is busy; a Den
// with nothing to move windows with is unavailable.
func TestViewSwitchErrorsKeepTheirCodes(t *testing.T) {
	m, wm := viewRig(t)
	unlock := hypr.LockWindows()
	rep, _ := switchView(t, m, api.ViewUser)
	unlock()
	if rep.Code != api.CodeBusy {
		t.Errorf("with a close running: %+v", rep)
	}
	if len(wm.moves) != 0 {
		t.Errorf("moved during a close: %v", wm.moves)
	}
	wm.down = true
	rep, _ = switchView(t, m, api.ViewUser)
	if rep.Code != api.CodeUnavailable {
		t.Errorf("without a compositor: %+v", rep)
	}
}

// TestViewRescueIsForTheHumanAtDen: /view says the view, /view rescue brings
// back everything on special:den and says how many.
func TestViewRescueIsForTheHumanAtDen(t *testing.T) {
	m, wm := viewRig(t)
	switchView(t, m, api.ViewUser)
	wm.cs = append(wm.cs, viewClient("0xstray", "org.example.term", 7002, 5))
	wm.cs[len(wm.cs)-1].Workspace.ID, wm.cs[len(wm.cs)-1].Workspace.Name = -99, api.ViewWorkspace

	mm, cmd := m.viewCommand(nil)
	if cmd == nil || !strings.Contains(mm.(Model).flash, "View: user") {
		t.Errorf("/view: %q", mm.(Model).flash)
	}
	mm, cmd = m.viewCommand([]string{"rescue"})
	if cmd == nil {
		t.Fatal("no rescue")
	}
	msg, ok := cmd().(viewRescueMsg)
	if !ok || msg.err != nil || msg.moved != 4 || msg.left != 0 {
		t.Fatalf("rescue: %+v", msg)
	}
	mm, _ = mm.(Model).applyViewRescue(msg)
	got := mm.(Model)
	if !strings.Contains(got.flash, "Brought back 4") || got.flashErr {
		t.Errorf("flash %q", got.flash)
	}
	if got.api.view != api.ViewAgent {
		t.Errorf("view after a rescue: %q", got.api.view)
	}
	for _, c := range wm.cs {
		if c.Workspace.Name == api.ViewWorkspace {
			t.Errorf("%s is still on special:den", c.Address)
		}
	}
	// A bad argument changes nothing.
	if mm, _ := m.viewCommand([]string{"hide"}); !mm.(Model).flashErr {
		t.Error("/view hide was taken for a command")
	}
}

// TestHideNeverHidesWhatWaitsOnYou: whatever kind of card waits in Den, a
// hide leaves Den's own window up, so no card, no approval and no ^Y
// question goes out of sight, while the agents' windows are hidden as
// usual (the card is in Den, not in them).
func TestHideNeverHidesWhatWaitsOnYou(t *testing.T) {
	body := []string{"x"}
	heldRequest := func(m *Model) {
		r := bridge.TestRequest(bridge.Envelope{Event: "PermissionRequest", SessionID: "s-a",
			Payload: []byte(`{"session_id":"s-a","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"ls"}}`)})
		if it := m.store.Apply(r); it == nil || it.State != inbox.Waiting {
			t.Fatalf("not held: %+v", it)
		}
	}
	for name, add := range map[string]func(m *Model){
		"a tool permission held in Den": heldRequest,
		"close":                         func(m *Model) { m.store.AddClose("s-a", "Close?", body, nil) },
		"skills":                        func(m *Model) { m.store.AddSkills("s-a", "Install?", body, nil) },
		"budget":                        func(m *Model) { m.store.AddBudget("s-a", "Budget?", body) },
		"restore":                       func(m *Model) { m.store.AddRestore("s-a", "Reopen?", body) },
		"merge":                         func(m *Model) { m.store.AddMerge("s-a", "Merge?", body) },
		"cleanup":                       func(m *Model) { m.store.AddCleanup("s-a", "Remove?", body) },
		"install":                       func(m *Model) { m.store.AddInstall("devbox", "Copy?", body, nil) },
		"message":                       func(m *Model) { m.store.AddMessageOn("devbox", "s-r", "Send?", body, nil) },
		"guidance":                      func(m *Model) { m.store.AddGuidance("Load?", body) },
		"mission update":                func(m *Model) { m.store.AddMissionUpdate("s-a", "Apply?", body) },
		"usage resume":                  func(m *Model) { m.store.AddUsageResume("Resume?", body) },
		"mission move":                  func(m *Model) { m.store.AddMissionMove("s-a", "Move?", body) },
		"api on":                        func(m *Model) { m.store.AddAPIOn("Turn on?", body) },
		"app grant":                     func(m *Model) { m.store.AddGrant("Grant?", body) },
		"pairing":                       func(m *Model) { m.store.AddPair("Pair?", body) },
		"control ask":                   func(m *Model) { m.store.AddControlAsk("Allow?", body) },
	} {
		t.Run(name, func(t *testing.T) {
			m, wm := viewRig(t)
			add(m)
			if m.store.Waiting() == 0 {
				t.Fatal("the card does not wait on the human: the test is wrong")
			}
			rep, _ := switchView(t, m, api.ViewUser)
			if rep.Code != "" || rep.Result.Shown != 1 || rep.Result.Total != 3 {
				t.Fatalf("hide with a card: %+v", rep)
			}
			if wm.where("0xden") == api.ViewWorkspace {
				t.Error("Den's own window was hidden while a card waits")
			}
			if wm.where("0xa") != api.ViewWorkspace || wm.where("0xb") != api.ViewWorkspace {
				t.Error("the agents' windows were not hidden")
			}
		})
	}
}

func terminalPrompt(m *Model, session string) {
	p, _ := json.Marshal(map[string]any{"session_id": session, "hook_event_name": "Notification", "notification_type": "permission_prompt"})
	r := &bridge.Request{Envelope: bridge.Envelope{Event: "Notification", SessionID: session, Payload: p}}
	json.Unmarshal(p, &r.Input)
	m.store.Apply(r)
}

// TestHideKeepsTheWindowOfAnAgentAtAPrompt: an agent whose permission
// prompt only its own terminal can answer keeps its window, and says so;
// Den's own window stays too, since the prompt waits on the human.
func TestHideKeepsTheWindowOfAnAgentAtAPrompt(t *testing.T) {
	m, wm := viewRig(t)
	terminalPrompt(m, "s-a")
	if !m.store.WaitingInTerminal("s-a") {
		t.Fatal("the prompt is not waiting in the terminal")
	}
	rep, _ := switchView(t, m, api.ViewUser)
	if rep.Result.Shown != 2 || rep.Result.Total != 3 || !strings.Contains(rep.Result.Reason, "1 waits on a prompt") {
		t.Fatalf("hide: %+v", rep)
	}
	if wm.where("0xa") == api.ViewWorkspace || wm.where("0xden") == api.ViewWorkspace {
		t.Error("a window with a prompt waiting was hidden")
	}
	if wm.where("0xb") != api.ViewWorkspace {
		t.Error("the other agent's window was not hidden")
	}
}

// TestHideSaysWhenItCannotTellDensWindow: with a card waiting and no window
// Den can tell is its own, nothing of Den's own moves, and the reason says
// why.
func TestHideSaysWhenItCannotTellDensWindow(t *testing.T) {
	m, wm := viewRig(t)
	wm.mu.Lock()
	for i, c := range wm.cs {
		if c.Address == "0xden" {
			wm.cs = append(wm.cs[:i], wm.cs[i+1:]...)
			break
		}
	}
	wm.mu.Unlock()
	m.store.AddClose("s-a", "Close?", []string{"x"}, nil)
	rep, _ := switchView(t, m, api.ViewUser)
	if rep.Result.Shown != 0 || rep.Result.Total != 2 || rep.Result.Reason != "Den's own window not found" {
		t.Fatalf("hide: %+v", rep)
	}
}

// TestACardWaitingForNoDockBringsDensWindowBack: in User view with a card
// waiting and no dock connected, Den's own window comes back, alone; with a
// dock connected, or nothing waiting, it stays hidden; and it is done once.
func TestACardWaitingForNoDockBringsDensWindowBack(t *testing.T) {
	m, wm := viewRig(t)
	dock := false
	defer func(f func(*Model) bool) { consoleUp = f }(consoleUp)
	consoleUp = func(*Model) bool { return dock }
	if m.checkViewStranded() != nil {
		t.Fatal("acted while in Agent view")
	}
	_, done := switchView(t, m, api.ViewUser)
	m.api.view = done.view
	if wm.where("0xden") != api.ViewWorkspace {
		t.Fatal("Den's window was not hidden")
	}
	if m.checkViewStranded() != nil {
		t.Fatal("acted with nothing waiting")
	}

	it := m.store.AddClose("s-a", "Close?", []string{"x"}, nil)
	dock = true
	if m.checkViewStranded() != nil || wm.where("0xden") != api.ViewWorkspace {
		t.Fatal("brought Den's window back with a dock connected")
	}
	dock = false
	cmd := m.checkViewStranded()
	if cmd == nil {
		t.Fatal("a card waits behind a hidden window with no dock, and nothing was done")
	}
	if msg, ok := cmd().(viewRaisedMsg); !ok || msg.err != nil {
		t.Fatalf("raise: %+v", msg)
	}
	if wm.where("0xden") != "1" {
		t.Errorf("Den's window is on %q, want its workspace 1", wm.where("0xden"))
	}
	if wm.where("0xa") != api.ViewWorkspace || wm.where("0xb") != api.ViewWorkspace {
		t.Error("an agent's window came back")
	}
	if viewSwitcher().Current() != api.ViewUser {
		t.Error("the view changed")
	}
	if m.checkViewStranded() != nil {
		t.Error("acted twice for one card")
	}
	// The card is answered; the next one, with the window shown again by
	// the dock's hide, is handled afresh.
	m.store.Settle(it, true, "answered")
	if m.checkViewStranded() != nil {
		t.Error("acted with nothing waiting")
	}
	if _, done := switchView(t, m, api.ViewUser); done.view != api.ViewUser || wm.where("0xden") != api.ViewWorkspace {
		t.Fatalf("the second hide: %+v", done)
	}
	m.store.AddClose("s-b", "Close?", []string{"x"}, nil)
	if m.checkViewStranded() == nil {
		t.Error("a second card was not handled")
	}
	// A failed attempt is tried again on the next tick.
	mm, _ := m.Update(viewRaisedMsg{err: context.Canceled})
	if got := mm.(Model); got.api.raised {
		t.Error("a failed raise was not retried")
	}
}

// TestHooksStateIsPublished: the fleet says, per agent on this machine,
// whether Den has seen its hooks report.
func TestHooksStateIsPublished(t *testing.T) {
	m, _ := viewRig(t)
	m.hooksReady = true
	m.att.ready()
	m.att.lastHook["s-a"] = time.Now()
	if cmd := m.startAPI(); cmd == nil {
		t.Fatal("the API did not start")
	}
	defer m.closeAPI()
	c := dialAPI(t, m)
	res := c.call("fleet.snapshot", nil)["result"].(map[string]any)
	hooks := map[string]any{}
	for _, a := range res["agents"].([]any) {
		ag := a.(map[string]any)
		hooks[ag["id"].(string)] = ag["hooks"]
	}
	if hooks["s-a"] != "on" || hooks["s-b"] != "unknown" {
		t.Errorf("hooks: %v", hooks)
	}
	if v, has := hooks["s-r"]; !has || v != nil {
		t.Errorf("the remote agent: %v %v", v, has)
	}
	m.hooksReady = false
	m.publishAPI()
	res = c.call("fleet.snapshot", nil)["result"].(map[string]any)
	for _, a := range res["agents"].([]any) {
		if ag := a.(map[string]any); ag["id"] == "s-b" && ag["hooks"] != "off" {
			t.Errorf("without hooks installed: %v", ag["hooks"])
		}
	}
}
