package api

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/GlacierHubAB/den/internal/sanitize"
	"github.com/GlacierHubAB/den/internal/sysd"
)

// Pairing the console with no code (console.request): the program asking
// must be the main process of the console's user service, run by the
// user's own systemd as Den checks it (sysd). It then pairs at once, with
// its powers, as the human chose: the dock is part of Den. Den says so in
// its chat, naming the program and what changed since it last read the
// service. What Den read is kept with the pairing, and a restart of the
// service re-pairs the same way (console.reconnect).
//
// All of it is friction, not a boundary: the user's manager socket is
// the user's own, and any program of the user can serve one, or change
// the service's unit or program. The human's levers are /api lock, /api
// unpair, and God mode.

// ConsoleUnit is the console's user service, and ConsoleUnitName what
// Den calls a console it read clean as that unit: Den's own words, never
// what the program says.
const (
	ConsoleUnit     = sysd.ConsoleUnit
	ConsoleUnitName = "Drey"
)

// consoleUnitWho is how Den's chat names a console it read clean as
// ConsoleUnit, in Den's own words: ConsoleUnitName, the unit in
// brackets, and the pid.
func consoleUnitWho(pid int) string {
	return ConsoleUnitName + " (" + ConsoleUnit + ") pid " + itoa(pid)
}

// readUnit reads the console's service for the process peer; tests
// replace it.
var readUnit = func(peer sysd.ProcID) (sysd.Verifier, error) { return sysd.Read(ConsoleUnit, peer) }

// requestEvery is how often one connection may ask (console.request or
// console.reconnect).
const requestEvery = time.Second

// Fingerprint is a public key as the human compares it: the first 16 hex
// digits of the SHA-256 of its 32 bytes, in four groups of four.
func Fingerprint(pubHex string) string {
	b, err := hex.DecodeString(pubHex)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return ""
	}
	sum := sha256.Sum256(b)
	h := hex.EncodeToString(sum[:8])
	return h[0:4] + " " + h[4:8] + " " + h[8:12] + " " + h[12:16]
}

// requestParams reads console.request's and console.reconnect's params.
func requestParams(c *conn, req Request) (key, pages string, ok bool) {
	var p struct {
		PublicKey string `json:"public_key"`
		Pages     string `json:"pages_sha256"`
	}
	if json.Unmarshal(req.Params, &p) != nil || !isHex(p.PublicKey, 64) || (p.Pages != "" && !isHex(p.Pages, 64)) {
		c.fail(req.ID, CodeBadRequest, "public_key: 64 lowercase hex digits; pages_sha256: 64, if sent")
		return "", "", false
	}
	return p.PublicKey, p.Pages, true
}

// requestAllowed holds one connection to one ask a second.
func requestAllowed(c *conn, req Request) bool {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if now.Sub(c.crequested) < requestEvery {
		c.fail(req.ID, CodeUnavailable, "one request a second")
		return false
	}
	c.crequested = now
	return true
}

// Automatic pairing's refusals.
const (
	// AutoPairOff: the console was unpaired by the human, by itself or by
	// Den's own guard, and the human has not started a pairing in Den since.
	AutoPairOff = "unpaired in Den: pair again there (/api console or den pair)"
	// AutoPairHeld: other programs tried the console, or proofs failed,
	// in the last autoPairHold.
	AutoPairHeld = "the console was unpaired for tries from other programs: pair by code, or wait ten minutes"
)

// autoPairRefusal is why the console's service may not pair by itself
// now, as an error code and message; "" when it may. Called with s.con.mu
// held.
func (s *Server) autoPairRefusal(now time.Time) (code, msg string) {
	st := &s.con
	switch {
	case st.autoOff:
		return CodeForbidden, AutoPairOff
	case now.Before(st.autoHold):
		return CodeUnavailable, AutoPairHeld
	case st.pending != nil:
		return CodeUnavailable, ClaimBusy
	}
	return "", ""
}

// pairAutomatically makes rec, which read clean as v, the console at once:
// in place of was when was is not nil, and only while was is still the
// console; in place of any console when it is nil. It comes with its
// powers unless the human locked them or God mode is on, and Den says so
// in its chat and, as kind, to the console's first subscribed connection,
// naming the program and what changed since Den last read the service
// (with extra). It reports the powers, and false when it refused.
func (s *Server) pairAutomatically(c *conn, req Request, was, rec *consoleRec, v sysd.Verifier, kind string, extra []string) (powers, ok bool) {
	god := s.godMode()
	st := &s.con
	st.mu.Lock()
	if code, msg := s.autoPairRefusal(time.Now()); code != "" {
		st.mu.Unlock()
		c.fail(req.ID, code, msg)
		return false, false
	}
	if was != nil && st.cur != was {
		st.mu.Unlock()
		c.fail(req.ID, CodeUnavailable, ClaimBusy)
		return false, false
	}
	locked, read := st.locked, st.last != nil
	var changes []string
	if read {
		for _, d := range sysd.Compare(*st.last, v) {
			changes = append(changes, string(d))
		}
	}
	changes = append(changes, extra...)
	// The powers come with what this Den run already gave them to, as it
	// read it itself; anything else, or anything at all when the human
	// chose "always", as said.
	known, since := s.opts.ConsolePowersAlways, ""
	if !known {
		switch g := st.given; {
		case g == nil:
			since = "Den has not given this dock its powers since Den started"
		default:
			var diff []string
			for _, d := range sysd.Compare(*g, v) {
				diff = append(diff, string(d))
			}
			known = len(diff) == 0
			if !known {
				since = "it changed since you last gave it its powers: " + strings.Join(diff, "; ")
			}
		}
	}
	rec.powers = !locked && !god && known
	rec.verifier, rec.readHere = &v, true
	if kind == "reconnected" && was != nil {
		// The same dock service, restarted on this boot, read clean as its
		// unit's main process: it continues the pairing, and keeps the
		// human's answer about the mirror. Any other pairing, or any
		// refusal above, starts a lineage of its own.
		rec.Lineage, rec.Mirror, rec.MirrorSince = was.Lineage, was.Mirror, was.MirrorSince
	}
	if rec.powers {
		st.given = &v
	}
	rec.auto = &ConsoleEvent{Kind: kind, PID: rec.PID, StartTicks: rec.start, Started: hhmmss(rec.Started), Reason: "paired automatically",
		Unit: ConsoleUnit, Program: sanitize.Label(v.Exe.Path, 160), ProgramSHA: v.Exe.SHA256, Changes: changes}
	old := st.cur
	st.cur, st.last = rec, &v
	s.saveConsoleLocked()
	st.mu.Unlock()
	if old != nil {
		reason := "another console was paired"
		if kind == "reconnected" {
			reason = "it reconnected"
		}
		s.closeConsole(old.ID, ConsoleEvent{Kind: "unpaired", PID: old.PID, StartTicks: old.start, Started: hhmmss(old.Started), Reason: reason, Cause: CauseReplaced})
	}
	note := "console paired automatically: " + consoleUnitWho(rec.PID) + ", started " + hhmmss(rec.Started) +
		", program " + sanitize.Label(v.Exe.Path, 160) + " (sha256 " + v.Exe.SHA256 + ")"
	if len(changes) > 0 {
		var said []string
		for _, ch := range changes {
			said = append(said, sanitize.Label(ch, 200))
		}
		note += "; changed since Den last read it: " + strings.Join(said, "; ")
	} else if read {
		note += "; nothing changed since Den last read it"
	}
	why := ""
	switch {
	case locked:
		why = "you locked the console's powers in Den"
	case god:
		why = "God mode is on"
	case !known:
		why = sanitize.Label(since, 300)
	}
	if why != "" {
		note += ". It answers no cards and runs no commands until you press Ctrl+Y on its card: " + why + "."
	} else {
		note += ". It may answer cards and run commands; /api lock takes that back, /api unpair ends it."
	}
	s.note(note)
	s.act(Act{ConsoleChanged: true})
	if why != "" {
		s.askPowers(rec, why)
	}
	return rec.powers, true
}

// consoleRequest is console.request: the console's service pairs with no
// code, at once.
func (s *Server) consoleRequest(c *conn, req Request) {
	if s.ckey == nil {
		c.fail(req.ID, CodeUnavailable, "console pairing is not set up")
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
	st := &s.con
	st.mu.Lock()
	code, msg := s.autoPairRefusal(time.Now())
	st.mu.Unlock()
	if code != "" {
		c.fail(req.ID, code, msg)
		return
	}
	// Only a clean read pairs: any problem, or a manager that did not
	// answer through every try, refuses, with no other way in but a code.
	self := sysd.ProcID{PID: c.peer.PID, Start: c.peer.Start}
	v, err := readUnit(self)
	if err != nil {
		c.fail(req.ID, CodeForbidden, "not "+ConsoleUnit+": "+sanitize.Label(err.Error(), 300)+"; pair with den pair")
		return
	}
	pub, _ := hex.DecodeString(pubHex)
	rec := &consoleRec{
		Console:  Console{ID: randHex(), Exe: c.peer.Exe, PID: self.PID, Since: time.Now(), PagesSHA: pages, Lineage: randHex()},
		identity: identity, start: self.Start, pub: ed25519.PublicKey(pub), pubHex: pubHex,
	}
	rec.Started, _, _ = procInfo(self.PID)
	powers, ok := s.pairAutomatically(c, req, nil, rec, v, "paired", nil)
	if !ok {
		return
	}
	c.reply(req.ID, map[string]any{"pairing": rec.ID, "den_public_key": s.ckey.pubHex, "console": true, "powers": powers})
}
