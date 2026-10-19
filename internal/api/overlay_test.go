package api

import (
	"testing"
	"time"
)

// linkHolds waits for OverlayLink's linked to be want: a closed
// connection leaves the server's list a moment after.
func linkHolds(t *testing.T, s *Server, mission string, want bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		backed, linked := s.OverlayLink(mission)
		if !backed {
			t.Fatalf("%s is no longer backed", mission)
		}
		if linked == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("linked %v, want %v", linked, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The console links its overlay to the running mission from a proved,
// subscribed connection with its powers. The link goes down on unlinked,
// on that connection closing, and on a lock of the powers only for a new
// link; the mission stays backed throughout, and a fresh link from a new
// connection brings it back. A new mission starts unbacked.
func TestOverlayLink(t *testing.T) {
	h, _ := consoleHarness(t)
	h.s.Publish(Project(Snapshot{Taken: time.Now(), View: ViewAgent}))
	d := newDock(t, h)
	p := d.pair(h.s.StartConsoleCode())
	proved := func(subscribe bool) *client {
		c := h.dial()
		c.hello()
		if l, _, _, _ := d.auth(c, p); !l.OK {
			t.Fatalf("%+v", l.Error)
		}
		if subscribe {
			c.call("console.events", nil)
		}
		return c
	}
	code := func(l line) string {
		if l.OK || l.Error == nil {
			return "ok"
		}
		return l.Error.Code
	}
	link := map[string]string{"mission": "m1"}

	// Nothing runs: not_found, and nothing is backed.
	c := proved(true)
	if got := code(c.call("overlay.linked", link)); got != CodeNotFound {
		t.Fatalf("link with no mission: %s", got)
	}
	h.s.SetDesktop("m1")
	if backed, _ := h.s.OverlayLink("m1"); backed {
		t.Fatal("backed before any link")
	}
	// An app's connection, an unsubscribed one, and another mission's id
	// are refused.
	app := h.dial()
	app.hello()
	if got := code(app.call("overlay.linked", link)); got != CodeForbidden {
		t.Fatalf("an app linked: %s", got)
	}
	if got := code(proved(false).call("overlay.linked", link)); got != CodeForbidden {
		t.Fatalf("an unsubscribed connection linked: %s", got)
	}
	if got := code(c.call("overlay.linked", map[string]string{"mission": "m9"})); got != CodeNotFound {
		t.Fatalf("another mission linked: %s", got)
	}
	if backed, _ := h.s.OverlayLink("m1"); backed {
		t.Fatal("backed by a refused link")
	}

	if got := code(c.call("overlay.linked", link)); got != "ok" {
		t.Fatalf("link: %s", got)
	}
	linkHolds(t, h.s, "m1", true)
	if got := code(c.call("overlay.unlinked", link)); got != "ok" {
		t.Fatalf("unlink: %s", got)
	}
	linkHolds(t, h.s, "m1", false)
	c.call("overlay.linked", link)
	linkHolds(t, h.s, "m1", true)

	// The linking connection closes: down, though another stays open.
	other := proved(true)
	c.nc.Close()
	linkHolds(t, h.s, "m1", false)
	// No powers: no new link.
	h.s.LockConsolePowers("test")
	if got := code(other.call("overlay.linked", link)); got != CodePowersLocked {
		t.Fatalf("link without powers: %s", got)
	}
	linkHolds(t, h.s, "m1", false)
	cur, _ := h.s.CurrentConsole()
	h.s.ConsolePowersAnswered(cur.ID, true)
	if got := code(other.call("overlay.linked", link)); got != "ok" {
		t.Fatalf("link with powers back: %s", got)
	}
	linkHolds(t, h.s, "m1", true)

	// Another mission: m1's link and backing are gone, m2 starts unbacked.
	h.s.SetDesktop("m2")
	if backed, linked := h.s.OverlayLink("m1"); backed || linked {
		t.Fatalf("m1 after m2 started: backed %v linked %v", backed, linked)
	}
	if backed, _ := h.s.OverlayLink("m2"); backed {
		t.Fatal("m2 backed by m1's link")
	}
}

// Overlay calls are limited: past overlayMax in a minute, unavailable.
func TestOverlayLimit(t *testing.T) {
	h, _ := consoleHarness(t)
	h.s.Publish(Project(Snapshot{Taken: time.Now(), View: ViewAgent}))
	d := newDock(t, h)
	p := d.pair(h.s.StartConsoleCode())
	c := h.dial()
	c.hello()
	if l, _, _, _ := d.auth(c, p); !l.OK {
		t.Fatalf("%+v", l.Error)
	}
	c.call("console.events", nil)
	h.s.SetDesktop("m1")
	for i := 0; i < overlayMax; i++ {
		if l := c.call("overlay.linked", map[string]string{"mission": "m1"}); !l.OK {
			t.Fatalf("call %d: %+v", i, l.Error)
		}
	}
	if l := c.call("overlay.unlinked", map[string]string{"mission": "m1"}); l.OK || l.Error.Code != CodeUnavailable {
		t.Fatalf("past the limit: %+v", l)
	}
}

// The console watches once a proved connection subscribes to its events,
// and no longer once that connection closes.
func TestConsoleWatching(t *testing.T) {
	h, _ := consoleHarness(t)
	h.s.Publish(Project(Snapshot{Taken: time.Now(), View: ViewAgent}))
	if h.s.ConsoleWatching() {
		t.Fatal("watching with no console")
	}
	d := newDock(t, h)
	p := d.pair(h.s.StartConsoleCode())
	c := h.dial()
	c.hello()
	if l, _, _, _ := d.auth(c, p); !l.OK {
		t.Fatalf("%+v", l.Error)
	}
	if h.s.ConsoleWatching() {
		t.Fatal("watching before a subscription")
	}
	c.call("console.events", nil)
	if !h.s.ConsoleWatching() {
		t.Fatal("not watching once subscribed")
	}
	c.nc.Close()
	deadline := time.Now().Add(5 * time.Second)
	for h.s.ConsoleWatching() {
		if time.Now().After(deadline) {
			t.Fatal("still watching after the connection closed")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
