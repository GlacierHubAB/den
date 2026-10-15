package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// viewConsole is a server with a console paired and proved on one
// connection, Den able to switch views as view says ("" for unable).
func viewConsole(t *testing.T, view string) (h *harness, d *dock, p consolePaired, ctl *client) {
	t.Helper()
	h, _ = consoleHarness(t)
	snap := snapshot()
	snap.View = view
	h.s.Publish(Project(snap))
	d = newDock(t, h)
	p = d.pair(h.s.StartConsoleCode())
	ctl = h.dial()
	ctl.hello()
	if l, _, _, _ := d.auth(ctl, p); !l.OK {
		t.Fatalf("console.auth: %+v", l.Error)
	}
	return h, d, p, ctl
}

func nextActView(t *testing.T, s *Server) Act {
	t.Helper()
	return ctlAct(t, s, func(a Act) bool { return a.View != nil })
}

func noViewAct(t *testing.T, s *Server, why string) {
	t.Helper()
	select {
	case a := <-s.Acts():
		if a.View != nil {
			t.Fatalf("%s reached the UI: %+v", why, a)
		}
	default:
	}
}

// TestViewVerbsNeedTheConsoleProof: only the console, proved on the
// connection, moves windows. An unproved connection and a D1 app pairing,
// which is chat only, are both refused; nothing reaches the UI.
func TestViewVerbsNeedTheConsoleProof(t *testing.T) {
	h, _, _, ctl := viewConsole(t, ViewAgent)
	plain := h.dial()
	plain.hello()
	app, _, _ := paired(t, h)
	for name, c := range map[string]*client{"an unproved connection": plain, "a D1 app pairing": app} {
		for _, op := range []string{"view.show", "view.hide"} {
			l := c.call(op, nil)
			if code(l) != CodeForbidden || l.Error.Message != "prove first" {
				t.Errorf("%s, %s: %+v", name, op, l.Error)
			}
		}
	}
	noViewAct(t, h.s, "a refused call")
	id := ctl.send("view.hide", nil)
	a := nextActView(t, h.s)
	a.View.Reply <- ViewReply{Result: ViewResult{View: ViewUser}}
	if l := ctl.replyTo(id); !l.OK {
		t.Errorf("the console: %+v", l.Error)
	}
}

// TestViewVerbsTakeNoParams.
func TestViewVerbsTakeNoParams(t *testing.T) {
	h, _, _, ctl := viewConsole(t, ViewAgent)
	for _, p := range []any{map[string]string{"view": "user"}, []int{1}, "hide"} {
		if l := ctl.call("view.hide", p); code(l) != CodeBadRequest {
			t.Errorf("params %v: %+v", p, l.Error)
		}
	}
	noViewAct(t, h.s, "a refused call")
	id := ctl.send("view.hide", map[string]string{})
	a := nextActView(t, h.s)
	a.View.Reply <- ViewReply{Result: ViewResult{View: ViewUser, Total: 0}}
	if l := ctl.replyTo(id); !l.OK {
		t.Errorf("empty params: %+v", l.Error)
	}
}

// TestHelloAuthAndFleetCarryTheView: hello, the console's proof and the
// fleet say which view Den is in, and name the workspace; a Den that cannot
// switch says none of it and refuses the verbs.
func TestHelloAuthAndFleetCarryTheView(t *testing.T) {
	h, d, p, ctl := viewConsole(t, ViewUser)
	c := h.dial()
	l := c.call("hello", map[string]any{"v": 1, "client": "test", "version": "0"})
	var in Info
	if !l.OK || json.Unmarshal(l.Result, &in) != nil || in.View != ViewUser || len(in.ViewWorkspaces) != 1 || in.ViewWorkspaces[0] != "special:den" {
		t.Fatalf("hello: %s", l.Result)
	}
	var f Fleet
	if l := c.call("fleet.snapshot", nil); json.Unmarshal(l.Result, &f) != nil || f.View != ViewUser {
		t.Errorf("fleet: %s", l.Result)
	}
	proved := func() map[string]any {
		cc := h.dial()
		cc.hello()
		l, _, _, _ := d.auth(cc, p)
		var r map[string]any
		if !l.OK || json.Unmarshal(l.Result, &r) != nil {
			t.Fatalf("console.auth: %+v", l.Error)
		}
		return r
	}
	if r := proved(); r["view"] != "user" {
		t.Errorf("console.auth says view %v, Den is in user", r["view"])
	}

	// No view: no field at all.
	h.s.Publish(Project(snapshot()))
	l = h.dial().call("hello", map[string]any{"v": 1, "client": "test", "version": "0"})
	if strings.Contains(string(l.Result), `"view`) {
		t.Errorf("hello without a view: %s", l.Result)
	}
	if l := c.call("fleet.snapshot", nil); strings.Contains(string(l.Result), `"view"`) {
		t.Errorf("fleet without a view: %s", l.Result)
	}
	if r := proved(); r["view"] != nil {
		t.Errorf("console.auth without a view: %v", r["view"])
	}
	for _, op := range []string{"view.show", "view.hide"} {
		if l := ctl.call(op, nil); code(l) != CodeUnavailable {
			t.Errorf("%s when Den cannot switch: %+v", op, l.Error)
		}
	}
}

// TestViewSwitchAnswersAndAnnounces: the UI's result is the reply, field
// for field, and the console's events and the connections that follow the
// fleet get a view event with the counts, never the reason.
func TestViewSwitchAnswersAndAnnounces(t *testing.T) {
	h, _, _, ctl := viewConsole(t, ViewAgent)
	ctl.call("console.events", nil)
	f := h.dial()
	f.hello()
	f.call("fleet.events", nil)

	id := ctl.send("view.hide", nil)
	a := nextActView(t, h.s)
	if a.View.Want != ViewUser {
		t.Fatalf("want %q", a.View.Want)
	}
	a.View.Reply <- ViewReply{Result: ViewResult{View: ViewUser, Shown: 1, Total: 4, Reason: "1 of 4 not hidden"}}
	l := ctl.replyTo(id)
	var got map[string]any
	if !l.OK || json.Unmarshal(l.Result, &got) != nil || len(got) != 4 || got["view"] != "user" || got["shown"] != 1.0 || got["total"] != 4.0 || got["reason"] != "1 of 4 not hidden" {
		t.Fatalf("reply %s", l.Result)
	}
	viewEvent := func(c *client) map[string]any {
		t.Helper()
		for {
			ev := c.next()
			if ev.Event != EventView {
				continue
			}
			var ch map[string]any
			if json.Unmarshal(ev.Data, &ch) != nil {
				t.Fatalf("event %s", ev.Data)
			}
			return ch
		}
	}
	for name, c := range map[string]*client{"the console": ctl, "a fleet follower": f} {
		if ch := viewEvent(c); len(ch) != 3 || ch["view"] != "user" || ch["shown"] != 1.0 || ch["total"] != 4.0 {
			t.Errorf("%s: event %v", name, ch)
		}
	}

	// show is the other verb, and a result without a reason has none.
	id = ctl.send("view.show", nil)
	a = nextActView(t, h.s)
	if a.View.Want != ViewAgent {
		t.Fatalf("want %q", a.View.Want)
	}
	a.View.Reply <- ViewReply{Result: ViewResult{View: ViewAgent, Shown: 4, Total: 4}}
	if l := ctl.replyTo(id); !l.OK || strings.Contains(string(l.Result), "reason") {
		t.Errorf("show: %s", l.Result)
	}
}

// TestViewSwitchErrorsFromTheUI: what the UI could not do comes back with
// its code.
func TestViewSwitchErrorsFromTheUI(t *testing.T) {
	h, _, _, ctl := viewConsole(t, ViewAgent)
	id := ctl.send("view.hide", nil)
	a := nextActView(t, h.s)
	a.View.Reply <- ViewReply{Code: CodeBusy, Msg: "a close is running"}
	l := ctl.replyTo(id)
	if code(l) != CodeBusy || l.Error.Message != "a close is running" {
		t.Errorf("%+v", l.Error)
	}
}

// TestViewSwitchOneAtATimeAndThreeAMinute: a second call while one runs
// answers busy and is not counted; past three a minute it is refused. The
// limit is the console's, across its connections.
func TestViewSwitchOneAtATimeAndThreeAMinute(t *testing.T) {
	h, d, p, ctl := viewConsole(t, ViewAgent)
	other := h.dial()
	other.hello()
	if l, _, _, _ := d.auth(other, p); !l.OK {
		t.Fatalf("console.auth: %+v", l.Error)
	}

	id := ctl.send("view.hide", nil)
	first := nextActView(t, h.s)
	if l := other.call("view.show", nil); code(l) != CodeBusy {
		t.Fatalf("second call while one runs: %+v", l.Error)
	}
	noViewAct(t, h.s, "a busy call")
	first.View.Reply <- ViewReply{Result: ViewResult{View: ViewUser}}
	if l := ctl.replyTo(id); !l.OK {
		t.Fatalf("first: %+v", l.Error)
	}
	// Two more answered (3 in all: the busy one did not count), then the
	// fourth is refused.
	for i := 0; i < 2; i++ {
		id := other.send("view.show", nil)
		a := nextActView(t, h.s)
		a.View.Reply <- ViewReply{Result: ViewResult{View: ViewAgent}}
		if l := other.replyTo(id); !l.OK {
			t.Fatalf("call %d: %+v", i+2, l.Error)
		}
	}
	l := ctl.call("view.hide", nil)
	if code(l) != CodeUnavailable || l.Error.Message != "too many view switches" {
		t.Errorf("fourth in a minute: %+v", l.Error)
	}
	noViewAct(t, h.s, "a refused call")
}

// TestViewSwitchFreesItsSlotBeforeItsAnswer: a console that calls again the
// moment it reads an answer is never told busy. While the slot cannot be
// freed (its lock is held here), the answer does not go out; once it is,
// the slot is free by the time the answer is read.
func TestViewSwitchFreesItsSlotBeforeItsAnswer(t *testing.T) {
	h, _, _, ctl := viewConsole(t, ViewAgent)
	id := ctl.send("view.hide", nil)
	a := nextActView(t, h.s)
	h.s.viewSwitch.mu.Lock()
	a.View.Reply <- ViewReply{Result: ViewResult{View: ViewUser}}
	got := make(chan line, 1)
	go func() { got <- ctl.replyTo(id) }()
	select {
	case l := <-got:
		h.s.viewSwitch.mu.Unlock()
		t.Fatalf("the answer went out while the slot was still taken: %+v", l)
	case <-time.After(300 * time.Millisecond):
	}
	h.s.viewSwitch.mu.Unlock()
	if l := <-got; !l.OK {
		t.Fatalf("answer: %+v", l.Error)
	}
	// On reading it, the next call is accepted at once.
	id = ctl.send("view.show", nil)
	a = nextActView(t, h.s)
	a.View.Reply <- ViewReply{Result: ViewResult{View: ViewAgent}}
	if l := ctl.replyTo(id); !l.OK {
		t.Fatalf("the call right after an answer: %+v", l.Error)
	}
}

// TestViewSwitchEndsWhenTheUIDoesNotAnswer: the call is answered in time
// for the dock's own timeout, and the slot is free again.
func TestViewSwitchEndsWhenTheUIDoesNotAnswer(t *testing.T) {
	was := viewWait
	viewWait = 150 * time.Millisecond
	t.Cleanup(func() { viewWait = was })
	h, _, _, ctl := viewConsole(t, ViewAgent)
	start := time.Now()
	l := ctl.call("view.hide", nil)
	if code(l) != CodeUnavailable || time.Since(start) > 3*time.Second {
		t.Errorf("%+v after %v", l.Error, time.Since(start))
	}
	nextActView(t, h.s)
	deadline := time.Now().Add(2 * time.Second)
	for {
		h.s.viewSwitch.mu.Lock()
		running := h.s.viewSwitch.running
		h.s.viewSwitch.mu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the switch slot stayed taken")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestFleetAgentsCarryTheirHooksState: on when the session has reported
// since Den started, off when Den's hooks are not installed, unknown when
// they are but it has not reported; none for an agent on another machine.
func TestFleetAgentsCarryTheirHooksState(t *testing.T) {
	state := func(installed bool, seen ...string) map[string]string {
		snap := snapshot()
		snap.HooksInstalled = installed
		snap.HookSeen = map[string]bool{}
		for _, id := range seen {
			snap.HookSeen[id] = true
		}
		out := map[string]string{}
		for _, a := range Project(snap).Fleet.Agents {
			out[a.ID] = a.Hooks
		}
		return out
	}
	if got := state(true, "s-1"); got["s-1"] != HooksOn || got["s-2"] != HooksUnknown || got["s-3"] != "" {
		t.Errorf("hooks installed: %v", got)
	}
	if got := state(false, "s-1"); got["s-1"] != HooksOn || got["s-2"] != HooksOff || got["s-3"] != "" {
		t.Errorf("hooks not installed: %v", got)
	}
	// On the wire, an agent on another machine has no hooks field.
	h := newHarness(t, 2)
	snap := snapshot()
	snap.HooksInstalled = true
	h.s.Publish(Project(snap))
	c := h.dial()
	c.hello()
	var f Fleet
	if l := c.call("fleet.snapshot", nil); json.Unmarshal(l.Result, &f) != nil || len(f.Agents) != 3 {
		t.Fatalf("fleet: %s", l.Result)
	}
	for _, a := range f.Agents {
		if (a.Host != "") != (a.Hooks == "") {
			t.Errorf("%s (host %q) hooks %q", a.Name, a.Host, a.Hooks)
		}
	}
}

// TestConsoleUpFollowsTheConsoleConnection: the dock that can call
// view.show is up only while the console has a proved connection open; a D1
// app pairing is not it.
func TestConsoleUpFollowsTheConsoleConnection(t *testing.T) {
	h, d, p, ctl := viewConsole(t, ViewAgent)
	if !h.s.ConsoleUp() {
		t.Fatal("the proved console connection is not a dock")
	}
	ctl.nc.Close()
	waitDown := func(why string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for h.s.ConsoleUp() {
			if time.Now().After(deadline) {
				t.Fatalf("%s still counts", why)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitDown("a closed connection")
	// A D1 app pairing is not a dock.
	app, _, _ := paired(t, h)
	_ = app
	if h.s.ConsoleUp() {
		t.Error("a D1 app pairing is a dock")
	}
	// The console connects again.
	again := h.dial()
	again.hello()
	if l, _, _, _ := d.auth(again, p); !l.OK || !h.s.ConsoleUp() {
		t.Errorf("a new connection: %+v", l.Error)
	}
	// Unpaired: no console, so no dock.
	h.s.UnpairConsole(CauseDen, "test")
	waitDown("an unpaired console")
}
