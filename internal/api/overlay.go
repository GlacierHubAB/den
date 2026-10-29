package api

import (
	"encoding/json"
	"sync"
	"time"
)

// The console's overlay link (overlay.linked, overlay.unlinked). The
// console sends overlay.linked once it has drawn its glow for the running
// desktop mission, and overlay.unlinked when it hides it. From a
// mission's first link on, the mission is overlay-backed: Den's TUI pauses
// the agent's input whenever the link is down (OverlayLink), and only a
// fresh overlay.linked over a proved connection with its powers resumes
// it. The link lives on the connection that sent it: that connection
// closing, the console unpairing or re-pairing, or another mission
// starting takes it down. A console that never links leaves every mission
// unbacked, as before.

// overlayMax is how many overlay calls the console may make in a minute.
const overlayMax = 30

type overlayState struct {
	mu      sync.Mutex
	backed  string // the mission that has been linked at least once
	mission string // the mission linked now, "" for none
	conn    *conn  // the connection that linked it
	recent  []time.Time
}

// allow reports whether one more overlay call is in the limit; o.mu is
// held.
func (o *overlayState) allow(now time.Time) bool {
	var kept []time.Time
	for _, t := range o.recent {
		if now.Sub(t) < time.Minute {
			kept = append(kept, t)
		}
	}
	o.recent = kept
	if len(kept) >= overlayMax {
		return false
	}
	o.recent = append(o.recent, now)
	return true
}

// overlayVerb answers overlay.linked (linked) and overlay.unlinked.
func (s *Server) overlayVerb(c *conn, req Request, linked bool) {
	if s.consoleOf(c) == "" {
		c.fail(req.ID, CodeForbidden, "prove first")
		return
	}
	var p struct {
		Mission string `json:"mission"`
	}
	if json.Unmarshal(req.Params, &p) != nil || p.Mission == "" {
		c.fail(req.ID, CodeBadRequest, "mission")
		return
	}
	if linked && !s.consolePowers(c) {
		c.fail(req.ID, CodePowersLocked, PowersLocked)
		return
	}
	if !c.subscribed(EventConsole) {
		c.fail(req.ID, CodeForbidden, "subscribe to console.events first")
		return
	}
	s.desk.mu.Lock()
	running := s.desk.mission
	s.desk.mu.Unlock()
	o := &s.overlay
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.allow(time.Now()) {
		c.fail(req.ID, CodeUnavailable, "too many overlay calls")
		return
	}
	if running == "" || p.Mission != running {
		c.fail(req.ID, CodeNotFound, "no such desktop mission runs")
		return
	}
	if linked {
		o.backed, o.mission, o.conn = p.Mission, p.Mission, c
	} else if o.mission == p.Mission {
		o.mission, o.conn = "", nil
	}
	c.reply(req.ID, map[string]bool{"linked": linked})
}

// overlayEnd forgets the link when the running mission changes.
func (s *Server) overlayEnd(mission string) {
	o := &s.overlay
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.backed != mission {
		o.backed = ""
	}
	if o.mission != mission {
		o.mission, o.conn = "", nil
	}
}

// ConsoleWatching reports whether the paired console has a proved
// connection subscribed to its events: a desktop task starting now
// expects its overlay.
func (s *Server) ConsoleWatching() bool {
	cur, ok := s.CurrentConsole()
	if !ok {
		return false
	}
	for _, c := range s.consoleConns(cur.ID) {
		if c.subscribed(EventConsole) {
			return true
		}
	}
	return false
}

// OverlayLink reports whether the console has linked its overlay to the
// mission at least once (backed), and whether that link holds now: the
// connection that sent it is open, still a proved connection of the
// paired console, and subscribed to its events.
func (s *Server) OverlayLink(mission string) (backed, linked bool) {
	o := &s.overlay
	o.mu.Lock()
	backed = mission != "" && o.backed == mission
	c := o.conn
	linked = backed && o.mission == mission && c != nil
	o.mu.Unlock()
	if !linked {
		return backed, false
	}
	select {
	case <-c.done:
		return true, false
	default:
	}
	s.mu.Lock()
	open := s.conns[c]
	s.mu.Unlock()
	return true, open && s.consoleOf(c) != "" && c.subscribed(EventConsole)
}
