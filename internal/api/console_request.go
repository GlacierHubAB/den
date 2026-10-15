package api

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/GlacierHubAB/den/internal/sanitize"
	"github.com/GlacierHubAB/den/internal/sysd"
)

// Pairing the console with no code (console.request): the program asking
// must be the main process of the console's user service, run by the
// user's own systemd as Den checks it (sysd). Den then asks the human on a
// card in its own window, with the pid, the start and both keys'
// fingerprints for them to match with what the dock shows. What Den read
// of the service is kept with the pairing, so that a restart of the same
// service, on the same build, can re-pair (console.reconnect).
//
// All of it is friction, not a boundary: the user's manager socket is
// the user's own, and any program of the user can serve one.

// ConsoleUnit is the console's user service.
const ConsoleUnit = sysd.ConsoleUnit

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

// consoleBusy is why a console card cannot be raised now, "" when it can:
// one console card at a time, and none in the cooldown after a deny.
// Called with s.con.mu held.
func (s *Server) consoleBusy(now time.Time) string {
	st := &s.con
	switch {
	case now.Before(st.cooldown):
		return ClaimCooldown
	case st.pending != nil:
		return ClaimBusy
	}
	return ""
}

// consoleRequest is console.request: pair the console's service with no
// code, on the human's ^Y in Den.
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
	busy := s.consoleBusy(time.Now())
	st.mu.Unlock()
	if busy != "" {
		c.fail(req.ID, CodeUnavailable, busy)
		return
	}
	v, err := readUnit(sysd.ProcID{PID: c.peer.PID, Start: c.peer.Start})
	if err != nil {
		c.fail(req.ID, CodeForbidden, "not "+ConsoleUnit+": "+sanitize.Label(err.Error(), 300)+"; pair with den pair")
		return
	}
	pub, _ := hex.DecodeString(pubHex)
	now := time.Now()
	rec := &consoleRec{
		Console:  Console{ID: randHex(), Exe: c.peer.Exe, PID: c.peer.PID, Since: now, PagesSHA: pages},
		identity: identity, start: c.peer.Start, pub: ed25519.PublicKey(pub), pubHex: pubHex,
		confirm: make(chan bool, 1), verifier: &v, trusted: true,
	}
	ask := ConsoleAsk{ID: rec.ID, Exe: c.peer.Exe, PID: c.peer.PID, Codeless: true, Unit: ConsoleUnit,
		Program: v.Exe.Path, ProgramSHA: v.Exe.SHA256, AppFP: Fingerprint(pubHex), DenFP: Fingerprint(s.ckey.pubHex)}
	ask.Started, ask.Parent, ask.Cmdline = procInfo(c.peer.PID)
	rec.Started = ask.Started
	confirm := s.raiseConsoleCard(c, req, rec, &ask)
	if confirm == nil {
		return
	}
	s.awaitConsole(c, req, rec, confirm, func() {}, func() map[string]any {
		return map[string]any{"pairing": rec.ID, "den_public_key": s.ckey.pubHex, "console": true, "powers": true}
	})
}

// raiseConsoleCard makes rec the claim waiting for the human and asks on
// a card, unless another waits or the cooldown runs: then it refuses, and
// returns nil. It returns rec's answer channel, taken under s.con.mu with
// the claim, for awaitConsole.
func (s *Server) raiseConsoleCard(c *conn, req Request, rec *consoleRec, ask *ConsoleAsk) chan bool {
	st := &s.con
	st.mu.Lock()
	if busy := s.consoleBusy(time.Now()); busy != "" {
		st.mu.Unlock()
		c.fail(req.ID, CodeUnavailable, busy)
		return nil
	}
	if st.cur != nil && ask.Replaces == nil && st.cur.ID != rec.ID {
		old := st.cur.Console
		ask.Replaces = &old
	}
	st.pending = rec
	confirm := rec.confirm
	st.mu.Unlock()
	if !s.act(Act{Console: ask}) {
		st.mu.Lock()
		if st.pending == rec {
			st.pending = nil
		}
		st.mu.Unlock()
		c.fail(req.ID, CodeUnavailable, ClaimBusy)
		return nil
	}
	return confirm
}
