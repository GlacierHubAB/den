package api

import (
	"bytes"
	"encoding/json"
	"sync"

	perm "github.com/GlacierHubAB/den/internal/grants"
)

// EventGrants carries the permission manager's read (perm.Grants) on
// each change, to the console's connections that asked for it with
// grants.get: the reply is where it stands, the events what changed.
const EventGrants = "grants"

// permsState is the last read Den sent, and its seq: Den's own number,
// risen on every change, never reset while Den runs, for ordering only.
// mu also orders the pushes: one read at a time, in seq order.
//
// Lock order: mu first, then s.con.mu, s.god.mu and s.mu (consoleConns),
// each taken and let go inside. No caller holds any of those when it takes
// mu. Under mu a push only queues (conn.push never blocks).
type permsState struct {
	mu   sync.Mutex
	seq  uint64
	last []byte
}

// grantsInput is the server's own record the read is worked out from: the
// console paired now and its powers, the human's /api lock, God mode.
func (s *Server) grantsInput() perm.State {
	s.con.mu.Lock()
	st := perm.State{Powers: s.con.cur != nil && s.con.cur.powers, Locked: s.con.locked}
	s.con.mu.Unlock()
	st.God = s.god.get().On
	return st
}

// grantsLocked is the read now, with seq risen if it changed since the
// last; when it did, every subscribed connection of the console but skip
// hears it. s.perms.mu is held.
func (s *Server) grantsLocked(skip *conn) perm.Grants {
	g := perm.Read(s.grantsInput())
	raw, _ := json.Marshal(g)
	if bytes.Equal(raw, s.perms.last) {
		g.Seq = s.perms.seq
		return g
	}
	s.perms.seq++
	s.perms.last = raw
	g.Seq = s.perms.seq
	if cur, ok := s.CurrentConsole(); ok {
		for _, c := range s.consoleConns(cur.ID) {
			if c != skip && c.subscribed(EventGrants) {
				c.push(Event{Event: EventGrants, Data: g}, true)
			}
		}
	}
	return g
}

// grantsChanged pushes the read to the console's subscribed connections
// when it changed: after a lock, God mode, the powers, and on each
// publish, which catches a pairing or an unpairing.
func (s *Server) grantsChanged() {
	s.perms.mu.Lock()
	s.grantsLocked(nil)
	s.perms.mu.Unlock()
}

// grantsGet is grants.get: the read, for a proved console, which from
// then on hears each change on this connection as a grants event. It
// changes nothing else. The reply and the subscription are taken under the
// lock the pushes take, so a change after the reply reaches the connection
// after it, and none before it.
func (s *Server) grantsGet(c *conn, req Request) {
	id := s.consoleOf(c)
	if id == "" {
		c.fail(req.ID, CodeForbidden, "prove first")
		return
	}
	// The read is the console paired now's: another console's connection,
	// still open after a re-pair, does not read levels that are not its own.
	if cur, ok := s.CurrentConsole(); !ok || cur.ID != id {
		c.fail(req.ID, CodeForbidden, "not the console paired now")
		return
	}
	s.perms.mu.Lock()
	defer s.perms.mu.Unlock()
	g := s.grantsLocked(c)
	c.mu.Lock()
	c.subs[EventGrants] = true
	c.mu.Unlock()
	c.reply(req.ID, g)
}
