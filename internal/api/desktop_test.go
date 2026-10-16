package api

import (
	"encoding/json"
	"testing"
	"time"
)

func desktopEvent(t *testing.T, e line) Desktop {
	t.Helper()
	if e.Event != EventDesktop {
		t.Fatalf("event %q, want desktop: %s", e.Event, e.Data)
	}
	var d Desktop
	if json.Unmarshal(e.Data, &d) != nil {
		t.Fatalf("desktop event %s", e.Data)
	}
	return d
}

// nextDesktop reads up to the next desktop event, past the snapshot's
// other kinds and other connections' notices.
func nextDesktop(t *testing.T, c *client) Desktop {
	t.Helper()
	for i := 0; i < 12; i++ {
		if e := c.next(); e.Event == EventDesktop {
			return desktopEvent(t, e)
		}
	}
	t.Fatal("no desktop event")
	return Desktop{}
}

// A desktop mission's state reaches the console's subscribed connections
// on each change, and console.events' snapshot ends with it, idle or
// active. The same state twice says nothing new.
func TestDesktopEventOnTheWire(t *testing.T) {
	h, _ := consoleHarness(t)
	h.s.Publish(Project(Snapshot{Taken: time.Now(), View: ViewAgent}))
	d := newDock(t, h)
	p := d.pair(h.s.StartConsoleCode())
	sub := func() *client {
		c := h.dial()
		c.hello()
		if l, _, _, _ := d.auth(c, p); !l.OK {
			t.Fatalf("%+v", l.Error)
		}
		c.call("console.events", nil)
		return c
	}
	c := sub()
	if h.s.ConsoleProcs() == nil {
		t.Fatal("no process for the console's proved connection")
	}
	if got := nextDesktop(t, c); got != (Desktop{State: DesktopIdle}) {
		t.Errorf("snapshot with none running: %+v", got)
	}
	h.s.SetDesktop("m1")
	if got := nextDesktop(t, c); got != (Desktop{State: DesktopActive, Mission: "m1"}) {
		t.Errorf("start: %+v", got)
	}
	h.s.SetDesktop("m1")
	// A second connection, subscribing while m1 runs.
	c2 := sub()
	if got := nextDesktop(t, c2); got != (Desktop{State: DesktopActive, Mission: "m1"}) {
		t.Errorf("snapshot: %+v", got)
	}
	h.s.SetDesktop("")
	for _, x := range []*client{c, c2} {
		// The next desktop event is the end: m1 set again said nothing.
		if got := nextDesktop(t, x); got != (Desktop{State: DesktopIdle, Mission: "m1"}) {
			t.Errorf("end: %+v", got)
		}
	}
	// Another mission takes over from a running one: the first ends.
	h.s.SetDesktop("m2")
	nextDesktop(t, c)
	h.s.SetDesktop("m3")
	if a, b := nextDesktop(t, c), nextDesktop(t, c); a != (Desktop{State: DesktopIdle, Mission: "m2"}) || b != (Desktop{State: DesktopActive, Mission: "m3"}) {
		t.Errorf("take over: %+v then %+v", a, b)
	}
	// Unpaired, the console's processes are no longer let through.
	h.s.UnpairConsole(CauseSelf, "")
	if procs := h.s.ConsoleProcs(); procs != nil {
		t.Errorf("console processes after unpair: %v", procs)
	}
}
