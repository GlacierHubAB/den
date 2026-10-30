package api

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
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
// mu. Under mu a push only queues (conn.push never blocks). The file's lock
// (permsFile.mu) is never held together with mu: under mu Den only changes
// the state and raises gen, and the file is written after
// (grants_store.go).
type permsState struct {
	mu   sync.Mutex
	seq  uint64
	last []byte
	// caps are the classes a console lowered for this Den process, by
	// class. A new pairing folds them into the Den levels (F3).
	caps map[string]perm.Session
	// denLevels are the Den levels as Den keeps them, by class (nil reads
	// as the defaults); lineage is the pairing lineage they belong to. A
	// console paired now under another lineage is a new pairing (F3).
	denLevels map[string]int
	lineage   string
	// confirmed is the start confirm: the human's Ctrl+Y on Den's powers
	// card since Den started. held is another program's change to the
	// stored levels while Den ran: every level reads 0 until that Ctrl+Y.
	confirmed, held bool
	// startWhy is what Den found of its stored levels at start, for the
	// powers card until the start confirm.
	startWhy string
	// gen counts the changes the file keeps; queued is the last one a
	// write was asked for (permsDo).
	gen, queued uint64
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
// console paired now, its powers and its lineage, the human's /api lock,
// God mode, and the levels Den keeps. s.perms.mu is held.
func (s *Server) grantsInput() (perm.State, string) {
	s.con.mu.Lock()
	st := perm.State{Powers: s.con.cur != nil && s.con.cur.powers, Locked: s.con.locked}
	lineage := ""
	if s.con.cur != nil {
		lineage = s.con.cur.Lineage
	}
	s.con.mu.Unlock()
	st.God = s.god.get().On
	st.Sessions = map[string]perm.Session{}
	for k, v := range s.perms.caps {
		st.Sessions[k] = v
	}
	st.DenLevels = map[string]int{}
	for k, v := range s.perms.denLevels {
		st.DenLevels[k] = v
	}
	st.Confirmed, st.Held = s.perms.confirmed, s.perms.held
	return st, lineage
}

// grantsLocked is the read now, with seq risen if it changed since the
// last; when it did, every subscribed connection of the console but skip
// hears it. A console paired now under a lineage the levels do not belong
// to is folded in first (F3), so no read shows a new pairing the levels of
// the one before. s.perms.mu is held.
func (s *Server) grantsLocked(skip *conn) perm.Grants {
	st, lineage := s.grantsInput()
	if lineage != "" && lineage != s.perms.lineage {
		s.foldLocked(st, lineage)
		st, _ = s.grantsInput()
	}
	g := perm.Read(st)
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

// foldLocked is F3, a new pairing under lineage: each class's Den level
// becomes the lowest of its default, its Den level and its session; the
// sessions, folded in, are cleared, and so is kept (when it exists); the
// levels now belong to lineage. All in this one section under perms.mu, so
// the read rises once. Den says so only when a class came down.
// s.perms.mu is held.
func (s *Server) foldLocked(st perm.State, lineage string) {
	after := st
	after.DenLevels, after.Sessions = perm.Fold(st), nil
	var down []string
	for _, id := range perm.Classes() {
		if after.Cap(id) < st.Cap(id) {
			down = append(down, id)
		}
	}
	s.perms.denLevels, s.perms.caps, s.perms.lineage = after.DenLevels, nil, lineage
	s.perms.gen++
	if len(down) == 1 {
		s.note("A new console was paired, so " + perm.Label(down[0]) + " went back to Den's default, " + strconv.Itoa(perm.Default(down[0])) + " of 5.")
	} else if len(down) > 1 {
		var said []string
		for _, id := range down {
			said = append(said, perm.Label(id)+" "+strconv.Itoa(perm.Default(id))+" of 5")
		}
		s.note("A new console was paired, so these went back to Den's defaults: " + strings.Join(said, ", ") + ".")
	}
}

// grantsChanged pushes the read to the console's subscribed connections
// when it changed: after a lock, God mode, the powers, and on each
// publish, which catches a pairing or an unpairing.
func (s *Server) grantsChanged() {
	s.permsDo(func() { s.grantsLocked(nil) })
}

// permsDo runs f under perms.mu, then, once it is let go, asks the file's
// writer for the state f left, when f changed what the file keeps.
func (s *Server) permsDo(f func()) {
	s.perms.mu.Lock()
	f()
	dirty := s.perms.gen != s.perms.queued
	s.perms.queued = s.perms.gen
	s.perms.mu.Unlock()
	if dirty {
		s.kickPerms()
	}
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
	answered := false
	s.permsDo(func() {
		// The read is the console paired now's: another console's
		// connection, still open after a re-pair, does not read levels
		// that are not its own. Checked under the lock the read is taken
		// under.
		if cur, ok := s.CurrentConsole(); !ok || cur.ID != id {
			return
		}
		g := s.grantsLocked(c)
		c.mu.Lock()
		c.subs[EventGrants] = true
		c.mu.Unlock()
		c.reply(req.ID, g)
		answered = true
	})
	if !answered {
		c.fail(req.ID, CodeForbidden, "not the console paired now")
	}
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
	var g perm.Grants
	var code, msg string
	var lowered *GrantsLowered
	s.permsDo(func() { g, code, msg, lowered = s.grantsLowerLocked(c, req.Params) })
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
	level, okLevel := perm.PlainInt(p["level"])
	seq, okSeq := perm.PlainInt(p["seq"])
	if json.Unmarshal(p["class"], &class) != nil || !okLevel || !okSeq || seq < 0 || !perm.Valid(class, int(level)) {
		return perm.Grants{}, CodeBadRequest, "class, level and seq", nil
	}
	now := s.grantsLocked(nil) // the read as it stands: its seq is the one to match
	if uint64(seq) != now.Seq {
		return perm.Grants{}, CodeStale, "the levels changed: read them again", nil
	}
	label := lowerLabel(cur)
	st, _ := s.grantsInput()
	session, refused := perm.Lower(st, class, int(level), time.Now().UTC().Format(time.RFC3339), label)
	if refused != "" {
		return perm.Grants{}, refused, "only a lower level than the class has now", nil
	}
	if s.perms.caps == nil {
		s.perms.caps = map[string]perm.Session{}
	}
	s.perms.caps[class] = session
	s.perms.gen++ // the record's notice of this session's lowerings
	return s.grantsLocked(c), "", "", &GrantsLowered{Class: class, Label: perm.Label(class), Level: session.Level, Console: label}
}

// lowerLabel is Den's label for console cur, as a lowering and the record
// keep it.
func lowerLabel(cur Console) string {
	if cur.Exe == "" {
		return ConsoleLabel
	}
	return sanitize.Label(ExeLabel(cur.Exe), 40)
}
