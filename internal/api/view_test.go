package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// viewHarness is a paired console-side connection and a plain one that
// follows the fleet, with Den able to switch views.
func viewHarness(t *testing.T, view string) (h *harness, ctl *client, pairing string, key []byte) {
	t.Helper()
	h = newHarness(t, 2)
	snap := snapshot()
	snap.View = view
	h.s.Publish(Project(snap))
	ctl, pairing, key = paired(t, h)
	return h, ctl, pairing, key
}

// TestViewVerbsNeedAProvedPairing: an unproved connection, however well
// behaved, moves no window; nothing reaches the UI.
func TestViewVerbsNeedAProvedPairing(t *testing.T) {
	h, ctl, _, _ := viewHarness(t, ViewAgent)
	c := h.dial()
	c.hello()
	for _, op := range []string{"view.show", "view.hide"} {
		l := c.call(op, nil)
		if code(l) != CodeForbidden || l.Error.Message != "prove first" {
			t.Errorf("%s unproved: %+v", op, l.Error)
		}
	}
	// Control off in Den is off for the proved connection too.
	h.s.SetControl(false)
	if l := ctl.call("view.hide", nil); code(l) != CodeForbidden {
		t.Errorf("view.hide with control off: %+v", l.Error)
	}
	select {
	case a := <-h.s.Acts():
		if a.View != nil {
			t.Fatalf("an unproved view call reached the UI: %+v", a)
		}
	default:
	}
}

// TestViewVerbsTakeNoParams.
func TestViewVerbsTakeNoParams(t *testing.T) {
	h, ctl, _, _ := viewHarness(t, ViewAgent)
	for _, p := range []any{map[string]string{"view": "user"}, []int{1}, "hide"} {
		if l := ctl.call("view.hide", p); code(l) != CodeBadRequest {
			t.Errorf("params %v: %+v", p, l.Error)
		}
	}
	select {
	case a := <-h.s.Acts():
		if a.View != nil {
			t.Fatalf("a refused call reached the UI: %+v", a)
		}
	default:
	}
	id := ctl.send("view.hide", map[string]string{})
	a := nextActView(t, h.s)
	a.View.Reply <- ViewReply{Result: ViewResult{View: ViewUser, Total: 0}}
	if l := ctl.replyTo(id); !l.OK {
		t.Errorf("empty params: %+v", l.Error)
	}
}

func nextActView(t *testing.T, s *Server) Act {
	t.Helper()
	return ctlAct(t, s, func(a Act) bool { return a.View != nil })
}

// TestHelloAndFleetCarryTheView: hello and the fleet say which view Den is
// in, and name the workspace; a Den that cannot switch says neither and
// refuses the verbs.
func TestHelloAndFleetCarryTheView(t *testing.T) {
	h, ctl, _, _ := viewHarness(t, ViewUser)
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

	// No view: no field at all.
	snap := snapshot()
	h.s.Publish(Project(snap))
	l = h.dial().call("hello", map[string]any{"v": 1, "client": "test", "version": "0"})
	if strings.Contains(string(l.Result), `"view`) {
		t.Errorf("hello without a view: %s", l.Result)
	}
	if l := c.call("fleet.snapshot", nil); strings.Contains(string(l.Result), `"view"`) {
		t.Errorf("fleet without a view: %s", l.Result)
	}
	for _, op := range []string{"view.show", "view.hide"} {
		if l := ctl.call(op, nil); code(l) != CodeUnavailable {
			t.Errorf("%s when Den cannot switch: %+v", op, l.Error)
		}
	}
}

// TestViewSwitchAnswersAndAnnounces: the UI's result is the reply, field
// for field, and the connections that follow the fleet get a view event
// with the counts, never the reason.
func TestViewSwitchAnswersAndAnnounces(t *testing.T) {
	h, ctl, _, _ := viewHarness(t, ViewAgent)
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
	var ev line
	for {
		ev = f.next()
		if ev.Event == EventView {
			break
		}
	}
	var ch map[string]any
	if json.Unmarshal(ev.Data, &ch) != nil || len(ch) != 3 || ch["view"] != "user" || ch["shown"] != 1.0 || ch["total"] != 4.0 {
		t.Errorf("event %s", ev.Data)
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
	h, ctl, _, _ := viewHarness(t, ViewAgent)
	id := ctl.send("view.hide", nil)
	a := nextActView(t, h.s)
	a.View.Reply <- ViewReply{Code: CodeBusy, Msg: "a close is running"}
	l := ctl.replyTo(id)
	if code(l) != CodeBusy || l.Error.Message != "a close is running" {
		t.Errorf("%+v", l.Error)
	}
}

// TestViewSwitchOneAtATimeAndThreeAMinute: a second call while one runs
// answers busy and is not counted; past three a minute it is refused.
func TestViewSwitchOneAtATimeAndThreeAMinute(t *testing.T) {
	h, ctl, pairing, key := viewHarness(t, ViewAgent)
	other := h.dial()
	other.hello()
	authOK(t, other, pairing, key)

	id := ctl.send("view.hide", nil)
	first := nextActView(t, h.s)
	if l := other.call("view.show", nil); code(l) != CodeBusy {
		t.Fatalf("second call while one runs: %+v", l.Error)
	}
	select {
	case a := <-h.s.Acts():
		if a.View != nil {
			t.Fatalf("a busy call reached the UI: %+v", a)
		}
	default:
	}
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
	select {
	case a := <-h.s.Acts():
		if a.View != nil {
			t.Fatalf("a refused call reached the UI: %+v", a)
		}
	default:
	}
}

// TestViewSwitchEndsWhenTheUIDoesNotAnswer: the call is answered in time
// for the dock's own timeout, and the slot is free again.
func TestViewSwitchEndsWhenTheUIDoesNotAnswer(t *testing.T) {
	was := viewWait
	viewWait = 150 * time.Millisecond
	t.Cleanup(func() { viewWait = was })
	h, ctl, _, _ := viewHarness(t, ViewAgent)
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
