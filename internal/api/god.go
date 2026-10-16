package api

import (
	"sync"
	"time"
)

// God mode is set in Den's TUI only (internal/ui's god.go); the server
// only shows it and takes it down. It has no verb that turns it on: hello
// and console.auth carry its state, a god event goes to the console's
// subscribed connections on each change, and god.off, from a proved
// console connection, asks Den to turn it off.

// EventGod carries God mode's state (God) on each change.
const EventGod = "god"

// CodeTTYOnly refuses a console's answer to a card that, while God mode
// is on, only the human answers, in Den's own window.
const CodeTTYOnly = "tty_only"

// God is God mode's state as apps see it.
type God struct {
	On    bool   `json:"on"`
	Since string `json:"since,omitempty"` // RFC 3339, UTC, while on
}

type godState struct {
	mu    sync.Mutex
	on    bool
	since time.Time
}

func (g *godState) get() God {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.valueLocked()
}

// valueLocked is God mode's state; called with g.mu held.
func (g *godState) valueLocked() God {
	if !g.on {
		return God{}
	}
	return God{On: true, Since: g.since.UTC().Format(time.RFC3339)}
}

// SetGod records God mode's state, from Den, and tells the console's
// subscribed connections.
func (s *Server) SetGod(on bool, since time.Time) {
	s.god.mu.Lock()
	changed := s.god.on != on
	s.god.on, s.god.since = on, since
	s.god.mu.Unlock()
	if !changed {
		return
	}
	cur, ok := s.CurrentConsole()
	if !ok {
		return
	}
	g := s.god.get()
	for _, c := range s.consoleConns(cur.ID) {
		if c.subscribed(EventConsole) {
			c.push(Event{Event: EventGod, Data: g}, true)
		}
	}
}

// GodNow is God mode's state as the server last heard it from Den.
func (s *Server) GodNow() God { return s.god.get() }

// godOff is god.off: a proved console asks Den to turn God mode off. It
// only lowers, so it is answered at once; Den turns it off on its loop.
func (s *Server) godOff(c *conn, req Request) {
	if s.consoleOf(c) == "" {
		c.fail(req.ID, CodeForbidden, "prove first")
		return
	}
	if !s.god.get().On {
		c.reply(req.ID, map[string]bool{"ok": true, "was_on": false})
		return
	}
	if !s.act(Act{GodOff: "the console turned it off"}) {
		c.fail(req.ID, CodeUnavailable, ClaimBusy)
		return
	}
	c.reply(req.ID, map[string]bool{"ok": true, "was_on": true})
}
