package api

import (
	"bytes"
	"encoding/json"
	"strconv"
	"sync"
	"time"

	perm "github.com/GlacierHubAB/den/internal/grants"
	"github.com/GlacierHubAB/den/internal/sanitize"
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
	// caps are the classes a console lowered for this Den process, by
	// class; a re-pair keeps them (they only lower).
	caps map[string]perm.Session
}

// Refusals of grants.lower besides the shared ones.
const (
	CodeStale    = "stale"
	CodeNotLower = perm.NotLower
)

// GrantsLowered tells Den a console lowered a class, for its chat: the
// class, its label and the level, all Den's own words, and the console's
// label as Den shows it.
type GrantsLowered struct {
	Class, Label, Console string
	Level                 int
}

// grantsInput is the server's own record the read is worked out from: the
// console paired now and its powers, the human's /api lock, God mode.
// s.perms.mu is held.
func (s *Server) grantsInput() perm.State {
	s.con.mu.Lock()
	st := perm.State{Powers: s.con.cur != nil && s.con.cur.powers, Locked: s.con.locked}
	s.con.mu.Unlock()
	st.God = s.god.get().On
	st.Sessions = map[string]perm.Session{}
	for k, v := range s.perms.caps {
		st.Sessions[k] = v
	}
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
	s.perms.mu.Lock()
	defer s.perms.mu.Unlock()
	// The read is the console paired now's: another console's connection,
	// still open after a re-pair, does not read levels that are not its
	// own. Checked under the lock the read is taken under.
	if cur, ok := s.CurrentConsole(); !ok || cur.ID != id {
		c.fail(req.ID, CodeForbidden, "not the console paired now")
		return
	}
	g := s.grantsLocked(c)
	c.mu.Lock()
	c.subs[EventGrants] = true
	c.mu.Unlock()
	c.reply(req.ID, g)
}

// grantsLower is grants.lower {class, level, seq}: the console paired now
// lowers a class for this Den process. Its powers are not needed: a
// lowering is always allowed. In one critical section under perms.mu, in
// this order: forbidden (not the console paired now), bad_request (a class
// or a level outside the closed sets, or not a JSON integer), stale (seq
// is not the read's now), not_lower (not strictly below the class's cap),
// then the apply. The kept form is unsupported until kept exists. The
// console's seq is only compared, never kept. The chat's note goes to Den
// after the lock is let go.
func (s *Server) grantsLower(c *conn, req Request) {
	s.perms.mu.Lock()
	g, code, msg, lowered := s.grantsLowerLocked(c, req.Params)
	s.perms.mu.Unlock()
	if code != "" {
		c.fail(req.ID, code, msg)
		return
	}
	c.reply(req.ID, g)
	s.act(Act{GrantsLowered: lowered})
}

// grantsLowerLocked is grantsLower's checks and apply; s.perms.mu is held.
func (s *Server) grantsLowerLocked(c *conn, params json.RawMessage) (perm.Grants, string, string, *GrantsLowered) {
	id := s.consoleOf(c)
	cur, ok := s.CurrentConsole()
	if id == "" || !ok || cur.ID != id {
		return perm.Grants{}, CodeForbidden, "not the console paired now", nil
	}
	var p map[string]json.RawMessage
	if json.Unmarshal(params, &p) != nil {
		return perm.Grants{}, CodeBadRequest, "class, level and seq", nil
	}
	if _, kept := p["kept"]; kept {
		return perm.Grants{}, CodeUnsupported, "kept comes in a later Den", nil
	}
	var class string
	level, okLevel := jsonInt(p["level"])
	seq, okSeq := jsonInt(p["seq"])
	if json.Unmarshal(p["class"], &class) != nil || !okLevel || !okSeq || seq < 0 || !perm.Valid(class, int(level)) {
		return perm.Grants{}, CodeBadRequest, "class, level and seq", nil
	}
	now := s.grantsLocked(nil) // the read as it stands: its seq is the one to match
	if uint64(seq) != now.Seq {
		return perm.Grants{}, CodeStale, "the levels changed: read them again", nil
	}
	label := ConsoleLabel
	if cur.Exe != "" {
		label = sanitize.Label(ExeLabel(cur.Exe), 40)
	}
	session, refused := perm.Lower(s.grantsInput(), class, int(level), time.Now().UTC().Format(time.RFC3339), label)
	if refused != "" {
		return perm.Grants{}, refused, "only a lower level than the class has now", nil
	}
	if s.perms.caps == nil {
		s.perms.caps = map[string]perm.Session{}
	}
	s.perms.caps[class] = session
	return s.grantsLocked(c), "", "", &GrantsLowered{Class: class, Label: perm.Label(class), Level: session.Level, Console: label}
}

// jsonInt is raw as an integer, if it is one written plainly: no string,
// no fraction, no exponent, no null.
func jsonInt(raw json.RawMessage) (int64, bool) {
	s := string(bytes.TrimSpace(raw))
	if s == "" || s == "-" {
		return 0, false
	}
	for i, r := range s {
		if !(r >= '0' && r <= '9' || i == 0 && r == '-') {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}
