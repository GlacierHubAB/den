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
// A restart of the same service, on the same build, run the same way,
// while this Den has held what it read at pairing in memory, re-pairs
// with no question: reading, chat and status at once, and the powers
// (answering cards, running commands) after one ^Y in Den's own window.
// Anything else that is still the service's main process asks on a card
// that names what changed, and that ^Y gives the powers too: after a Den
// start (the record on disk may have been rewritten while Den was down),
// in God mode, or when anything Den read differs.

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

// SetGodMode tells the API how to know whether God mode is on: no silent
// re-pair while it is. God mode turning on calls LockConsolePowers.
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
	given := ok && cur != nil && cur.ID == id && !cur.powers
	if given {
		cur.powers = true
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
	busy := s.consoleBusy(time.Now())
	st.mu.Unlock()
	if cur == nil || cur.verifier == nil {
		c.fail(req.ID, CodeForbidden, "pair again: there is no pairing to reconnect to")
		return
	}
	if busy != "" {
		c.fail(req.ID, CodeUnavailable, busy)
		return
	}
	was := *cur.verifier
	v, err := readUnit(sysd.ProcID{PID: c.peer.PID, Start: c.peer.Start})
	if err == nil && v.Boot != was.Boot {
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
	var changes []string
	for _, d := range sysd.Compare(was, v) {
		changes = append(changes, string(d))
	}
	if pages != cur.PagesSHA {
		changes = append(changes, "the dock's pages changed")
	}
	god := s.godMode()
	silent := len(changes) == 0 && cur.trusted && !god
	switch {
	case !cur.trusted:
		changes = append([]string{"Den has restarted since you paired it, so it cannot tell whether its record was changed while it was down"}, changes...)
	case god:
		changes = append([]string{"God mode is on: no console re-pairs without your ^Y"}, changes...)
	}
	pub, _ := hex.DecodeString(pubHex)
	now := time.Now()
	rec := &consoleRec{
		Console:  Console{ID: randHex(), Exe: c.peer.Exe, PID: c.peer.PID, Since: now, PagesSHA: pages},
		identity: identity, start: c.peer.Start, pub: ed25519.PublicKey(pub), pubHex: pubHex,
		confirm: make(chan bool, 1), verifier: &v, trusted: true,
	}
	rec.Started, _, _ = procInfo(c.peer.PID)
	if silent {
		s.repairSilently(c, req, cur, rec, signed(rec.ID))
		return
	}
	ask := ConsoleAsk{ID: rec.ID, Exe: c.peer.Exe, PID: c.peer.PID, Codeless: true, Reconnect: true, Unit: ConsoleUnit,
		Program: v.Exe.Path, ProgramSHA: v.Exe.SHA256, AppFP: Fingerprint(pubHex), DenFP: Fingerprint(s.ckey.pubHex), Changes: changes}
	ask.Started, ask.Parent, ask.Cmdline = procInfo(c.peer.PID)
	old := cur.Console
	ask.Replaces = &old
	confirm := s.raiseConsoleCard(c, req, rec, &ask)
	if confirm == nil {
		return
	}
	s.awaitConsole(c, req, rec, confirm, func() {}, func() map[string]any {
		return map[string]any{"pairing": rec.ID, "den_sig": signed(rec.ID), "powers": true}
	})
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

// repairSilently makes rec the console in place of was, with no powers
// until the human's ^Y, and says so: a note, the console's event, and the
// powers card.
func (s *Server) repairSilently(c *conn, req Request, was, rec *consoleRec, sig string) {
	st := &s.con
	st.mu.Lock()
	if st.cur != was || st.pending != nil {
		st.mu.Unlock()
		c.fail(req.ID, CodeUnavailable, ClaimBusy)
		return
	}
	rec.reconnected = true
	st.cur = rec
	s.saveConsoleLocked()
	st.mu.Unlock()
	s.closeConsole(was.ID, ConsoleEvent{Kind: "unpaired", PID: was.PID, StartTicks: was.start, Started: hhmmss(was.Started), Reason: "it reconnected", Cause: CauseReplaced})
	s.note("console reconnected: " + ConsoleUnit + " pid " + itoa(rec.PID) + ", started " + hhmmss(rec.Started) + ". It answers cards and runs commands again once you press Ctrl+Y on its card.")
	s.act(Act{ConsoleChanged: true})
	s.askPowers(rec, "it reconnected after a restart")
	c.reply(req.ID, map[string]any{"pairing": rec.ID, "den_sig": sig, "powers": false})
}

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
		s.UnpairConsole(CauseIntruders, "three other programs tried your console in ten minutes: it is unpaired; pair again with den pair")
		return
	}
	s.LockConsolePowers("another program tried your console")
}
