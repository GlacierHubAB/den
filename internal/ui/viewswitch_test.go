package ui

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/GlacierHubAB/den/internal/api"
	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/hypr"
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
