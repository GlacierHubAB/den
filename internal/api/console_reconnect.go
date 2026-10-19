package api

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/GlacierHubAB/den/internal/sanitize"
	"github.com/GlacierHubAB/den/internal/sysd"
)

// Re-pairing the console's service after it restarted (console.reconnect),
// and the console's powers.
//
// The service's new main process, read clean, re-pairs at once with its
// powers, as console.request pairs, and Den names what changed in its
// chat; Den signs its reply under the key the dock pinned. The powers
// (answering cards, running commands) stay off while the human's /api
// lock or God mode holds, until the ^Y on Den's powers card.

// CodePowersLocked refuses card.answer and cmd.run while the console has
// no powers.
const CodePowersLocked = "powers_locked"

// PowersLocked is its message.
const PowersLocked = "press Ctrl+Y in Den to let the console answer cards and run commands"

// ConsolePowersAsk asks the human, on a card in Den's own window, to give
// the console its powers again.
type ConsolePowersAsk struct {
	ID      string // the console's pairing
	PID     int
	Started time.Time
	Why     string
}

// SetGodMode tells the API how to know whether God mode is on: a console
// paired automatically while it is comes without its powers. God mode
// turning on calls LockConsolePowers.
func (s *Server) SetGodMode(on func() bool) {
	s.con.mu.Lock()
	s.con.godOn = on
	s.con.mu.Unlock()
}

// godMode reports whether God mode is on, as SetGodMode said to ask.
func (s *Server) godMode() bool {
	s.con.mu.Lock()
	on := s.con.godOn
	s.con.mu.Unlock()
	return on != nil && on()
}

// consolePowers reports whether c is a proved connection of a console
// that has its powers.
func (s *Server) consolePowers(c *conn) bool {
	c.mu.Lock()
	id := c.console
	c.mu.Unlock()
	s.con.mu.Lock()
	defer s.con.mu.Unlock()
	cur := s.con.cur
	return id != "" && cur != nil && cur.ID == id && cur.powers
}

// LockConsolePowers takes the console's powers until the human's next ^Y
// in Den, and asks for them on a card.
func (s *Server) LockConsolePowers(why string) {
	s.con.mu.Lock()
	cur := s.con.cur
	locked := cur != nil && cur.powers
	if locked {
		cur.powers = false
	}
	s.con.mu.Unlock()
	if locked {
		s.powersEvent(cur, false)
		s.askPowers(cur, why)
	}
}

// LockConsole is the human's /api lock: the console's powers are taken,
// and every console paired after, automatically or by code, and one
// restored after Den restarts, comes without them, until the human's ^Y
// on Den's powers card.
func (s *Server) LockConsole(why string) {
	s.con.mu.Lock()
	s.con.locked = true
	s.saveConsoleLocked()
	s.con.mu.Unlock()
	s.LockConsolePowers(why)
}

// askPowers puts the console's powers on a card in Den.
func (s *Server) askPowers(rec *consoleRec, why string) {
	s.act(Act{ConsolePowers: &ConsolePowersAsk{ID: rec.ID, PID: rec.PID, Started: rec.Started, Why: why}})
}

// ConsolePowersAnswered is the human's ^Y (ok) or ^N on the powers card.
// ConsolePowered reports whether id is the console paired now, with its
// powers: in its turns Den runs ListAgents and send_message without a card.
// It is asked at each tool, so a lock in the middle of a turn holds for the
// rest of it.
func (s *Server) ConsolePowered(id string) bool {
	s.con.mu.Lock()
	defer s.con.mu.Unlock()
	return id != "" && s.con.cur != nil && s.con.cur.ID == id && s.con.cur.powers
}

func (s *Server) ConsolePowersAnswered(id string, ok bool) {
	s.con.mu.Lock()
	cur := s.con.cur
	mine := ok && cur != nil && cur.ID == id
	given := mine && !cur.powers
	if given {
		cur.powers = true
	}
	if mine && s.con.locked {
		s.con.locked = false // the ^Y lifts the human's lock too
		s.saveConsoleLocked()
	}
	if mine && cur.verifier != nil && cur.readHere {
		s.con.given = cur.verifier // what the human gave the powers to, as this Den read it
	}
	s.con.mu.Unlock()
	if given {
		s.powersEvent(cur, true)
		s.act(Act{ConsoleChanged: true})
	}
}

// powersEvent tells the console's subscribed connections its powers.
func (s *Server) powersEvent(rec *consoleRec, on bool) {
	for _, cc := range s.consoleConns(rec.ID) {
		if cc.subscribed(EventConsole) {
			cc.push(Event{Event: EventConsole, Data: ConsoleEvent{Kind: "powers", On: &on, PID: rec.PID, StartTicks: rec.start}}, false)
		}
	}
}

// consoleReconnect is console.reconnect: the console's service, restarted
// with a fresh key, asks to be the console again.
func (s *Server) consoleReconnect(c *conn, req Request) {
	if s.ckey == nil {
		c.fail(req.ID, CodeUnavailable, "console pairing is not set up")
		return
	}
	var np struct {
		Nonce string `json:"nonce"`
	}
	if json.Unmarshal(req.Params, &np) != nil || !isHex(np.Nonce, 64) {
		c.fail(req.ID, CodeBadRequest, "nonce: 64 lowercase hex digits, the dock's own")
		return
	}
	pubHex, pages, ok := requestParams(c, req)
	if !ok || !requestAllowed(c, req) {
		return
	}
	identity, known := consoleIdentity(c.peer)
	if !known {
		c.fail(req.ID, CodeForbidden, "Den cannot tell which process this is")
		return
	}
	// Den proves itself in the reply, under the key the dock pinned at
	// pairing, before the dock sends anything else.
	signed := func(pairing string) string {
		return hex.EncodeToString(ed25519.Sign(s.ckey.priv, reconnectSigned(np.Nonce, pairing, pubHex)))
	}
	st := &s.con
	st.mu.Lock()
	cur := st.cur
	code, msg := s.autoPairRefusal(time.Now())
	st.mu.Unlock()
	if cur == nil {
		c.fail(req.ID, CodeForbidden, "pair again: there is no pairing to reconnect to")
		return
	}
	if code != "" {
		c.fail(req.ID, code, msg)
		return
	}
	self := sysd.ProcID{PID: c.peer.PID, Start: c.peer.Start}
	v, err := readUnit(self)
	if err == nil && cur.verifier != nil && v.Boot != cur.verifier.Boot {
		c.fail(req.ID, CodeForbidden, "pair again: it was paired before this boot")
		return
	}
	if err != nil {
		// Not the service's main process, or a service that fails a check,
		// tried the console: a strike, and its powers are taken. A manager
		// Den could not read at all is only a refusal.
		var probs sysd.Problems
		if errors.As(err, &probs) {
			s.consoleIntruder(cur, c.peer)
		}
		c.fail(req.ID, CodeForbidden, "pair again: not "+ConsoleUnit+": "+sanitize.Label(err.Error(), 300))
		return
	}
	var extra []string
	if pages != cur.PagesSHA {
		extra = append(extra, "the dock's pages changed")
	}
	pub, _ := hex.DecodeString(pubHex)
	rec := &consoleRec{
		Console:  Console{ID: randHex(), Exe: c.peer.Exe, PID: self.PID, Since: time.Now(), PagesSHA: pages, Lineage: randHex()},
		identity: identity, start: self.Start, pub: ed25519.PublicKey(pub), pubHex: pubHex,
	}
	rec.Started, _, _ = procInfo(self.PID)
	powers, ok := s.pairAutomatically(c, req, cur, rec, v, "reconnected", extra)
	if !ok {
		return
	}
	c.reply(req.ID, map[string]any{"pairing": rec.ID, "den_sig": signed(rec.ID), "powers": powers})
}

// reconnectSigned is what Den signs in console.reconnect's reply: its
// label, the dock's nonce, the new pairing and the dock's new public key,
// each of fixed length (checked before), one to a line.
func reconnectSigned(nonce, pairing, appKey string) []byte {
	return []byte(consoleReconnectLabel + "\n" + nonce + "\n" + pairing + "\n" + appKey)
}

// consoleReconnectLabel starts what Den signs for a reconnect: no other
// signature of Den's is valid as one.
const consoleReconnectLabel = "den-console-reconnect-v1"

// consoleIntruder counts a try for the console from another process, or
// one that fails the service's checks: a strike (three in the window
// unpair), a card once a window, and the console's powers taken.
func (s *Server) consoleIntruder(rec *consoleRec, peer Peer) {
	now := time.Now()
	st := &s.con
	st.mu.Lock()
	if st.cur != rec {
		st.mu.Unlock()
		return
	}
	unpair := rec.strike(now)
	key := peer.Exe + "\x00" + itoa(peer.PID)
	if st.intruders == nil {
		st.intruders = map[string]time.Time{}
	}
	card := now.Sub(st.intruders[key]) >= consoleStrikeWindow
	if card {
		st.intruders[key] = now
	}
	st.mu.Unlock()
	if card {
		s.act(Act{ConsoleIntruder: &ConsoleIntruder{Exe: peer.Exe, PID: peer.PID}})
	}
	for _, cc := range s.consoleConns(rec.ID) {
		if cc.subscribed(EventConsole) {
			cc.push(Event{Event: EventConsole, Data: ConsoleEvent{Kind: "refused", PID: peer.PID, StartTicks: peer.Start, Program: sanitize.Label(ExeLabel(peer.Exe), 40)}}, false)
		}
	}
	if unpair {
		s.UnpairConsole(CauseIntruders, "three tries for your console from other programs in ten minutes: it is unpaired; pair again with den pair")
		return
	}
	s.LockConsolePowers("another program tried your console")
}
