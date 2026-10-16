package api

import (
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GlacierHubAB/den/internal/sanitize"
	"github.com/GlacierHubAB/den/internal/sysd"
)

// A console is the one program the human paired, in Den's own window, as
// their console: it may answer Den's cards and send Den's commands for
// them. Pairing one is not D1's app pairing (pair.*): the code never
// crosses the socket, both sides sign every proof with Ed25519, and the
// console is bound to one process (consoleIdentity). Den keeps its side of
// the pairing; the app keeps its private key in memory only.
//
// The flow on one connection: console.challenge (a nonce and a claim
// salt), console.claim (the app's public key and a proof it knows the
// code, keyed from it), the human's ^Y on a card in Den, then Den's public
// key and a bind keyed from the code. Every later connection proves with
// console.challenge and console.auth.

// The console's limits.
const (
	consoleCooldown     = 30 * time.Second // after the human declines a console
	consoleConnsMax     = 6                // proved connections of the console at once
	consoleStrikesMax   = 3                // proofs from other programs, in consoleStrikeWindow, that unpair
	consoleStrikeWindow = 10 * time.Minute
	unpairAllEvery      = 10 * time.Second // console.unpair {"all": true}, for everyone
	// autoPairHold is how long three tries from other programs, wrong
	// proofs or a burst of answers stop automatic pairing: the strike
	// window. Pairing by code still works meanwhile.
	autoPairHold = consoleStrikeWindow
)

// EventConsole carries what happened to the console (ConsoleEvent).
const EventConsole = "console"

// CodeConsoleInUse refuses a connection past consoleConnsMax.
const CodeConsoleInUse = "console_in_use"

// ClaimCooldown refuses a console claim within consoleCooldown of a decline.
const ClaimCooldown = "wait: a pairing was just declined"

// ClaimLeaked refuses a console claim whose code was typed into a chat
// while the claim waited for the human.
const ClaimLeaked = "revoked: the code was typed into a chat"

// The labels the console's MACs and signatures start with: no value made
// for one use is ever valid for another.
const (
	consoleClaimInfo = "den-console-bind-app-v1"
	consoleBindInfo  = "den-console-bind-v1"
	consoleSigLabel  = "den-console-v1"
	dirAppToDen      = "app-to-den"
	dirDenToApp      = "den-to-app"
)

// ConsoleAsk asks the human, on a card in Den, whether the program that
// claimed the console code becomes their console.
type ConsoleAsk struct {
	ID       string
	Exe      string // the identity Den read (ExeLabel and ExeShown name it)
	PID      int
	Started  time.Time // zero when unknown
	Parent   string
	Cmdline  string   // as the program reports it: it can be spoofed
	Replaces *Console // the console it replaces, nil for none
	// Codeless is a console.request: no code was typed, and the human
	// matches the pid, start and fingerprints with the dock's. Unit,
	// Program and ProgramSHA are the service as Den read it from its
	// manager; AppFP and DenFP the two keys' fingerprints (Fingerprint).
	Codeless bool
	// Reconnect is a console.reconnect the human answers: the service
	// restarted, and Changes says why it is not re-paired silently.
	Reconnect bool
	// Rejoins is a claim by code whose process Den read clean as Unit:
	// the dock may re-pair after a restart with no code, as one paired
	// with none, and the card says so.
	Rejoins    bool
	Changes    []string
	Unit       string
	Program    string
	ProgramSHA string
	AppFP      string
	DenFP      string
}

// ConsoleIntruder is a proof for the console from another process.
type ConsoleIntruder struct {
	Exe string
	PID int
}

// Console is the console as Den shows it.
type Console struct {
	ID        string
	Exe       string
	PID       int
	Started   time.Time
	Since     time.Time
	LastProof time.Time
	PagesSHA  string
	// Powers is whether it may answer cards and run commands now, and
	// Verified whether this Den read it clean as ConsoleUnit for this
	// pairing itself (never from the record on disk); only CurrentConsole
	// fills them in.
	Powers   bool
	Verified bool
}

// ConsoleEvent is the data of a console event.
type ConsoleEvent struct {
	Kind       string `json:"kind"` // restored, connected, paired, reconnected, powers, refused or unpaired
	PID        int    `json:"pid"`
	StartTicks uint64 `json:"start_ticks"`
	Started    string `json:"started,omitempty"` // HH:MM:SS, local time
	Program    string `json:"program,omitempty"`
	Reason     string `json:"reason,omitempty"`
	On         *bool  `json:"on,omitempty"` // powers: whether the console has them now
	// Cause says why an unpaired console is unpaired, one of the Cause
	// values; reason is display text. A Den from before causes sends none.
	Cause string `json:"cause,omitempty"`
	// An automatic pairing (paired, reconnected) names the unit, the
	// program as the manager runs it and its SHA-256, and what changed
	// since Den last read the service.
	Unit       string   `json:"unit,omitempty"`
	ProgramSHA string   `json:"program_sha256,omitempty"`
	Changes    []string `json:"changes,omitempty"`
}

// The causes of an unpaired console event (ConsoleEvent.Cause). The set is
// closed: every path that unpairs the console names one.
const (
	// CauseBurst: the console answered cards faster than anyone could read.
	CauseBurst = "burst"
	// CauseProofs: three wrong proofs for the console in ten minutes.
	CauseProofs = "proofs"
	// CauseIntruders: three tries for the console from other programs in ten
	// minutes.
	CauseIntruders = "intruders"
	// CauseSelf: the console unpaired itself, with console.unpair.
	CauseSelf = "self"
	// CauseDen: unpaired in Den (/api unpair) or with den unpair in a
	// terminal, which a program run on this machine can issue too: word it
	// as "in Den", not as the human's act.
	CauseDen = "den"
	// CauseOther: another program on this machine unpaired every pairing
	// with console.unpair {all: true}, from a connection that is not the
	// console's own. Never the human's act.
	CauseOther = "other"
	// CauseReplaced: another pairing took its place: another console, or
	// this one after a console.reconnect with a fresh key.
	CauseReplaced = "replaced"
	// CauseRecord: Den found its console record changed outside Den.
	CauseRecord = "record"
	// CauseGuard: Den's own guard stopped the orchestrator, and unpaired.
	CauseGuard = "guard"
)

// UnpairCauses are the Cause values.
var UnpairCauses = []string{CauseBurst, CauseProofs, CauseIntruders, CauseSelf, CauseDen, CauseOther, CauseReplaced, CauseRecord, CauseGuard}

type consoleRec struct {
	Console
	identity string
	start    uint64
	pub      ed25519.PublicKey
	pubHex   string
	confirm  chan bool
	// words are the code a claim waiting for the human was made with, for
	// the chat leak check (leakedConsole); nil once the claim is answered.
	// leaked says the code was typed into a chat while it waited.
	words    []string
	leaked   bool
	strikes  []time.Time
	restored bool // restored from before a Den restart, not yet announced
	// verifier is what Den last read of the console's service (an
	// automatic pairing, or a claim by code that read clean), nil for any
	// other pairing by code. readHere says this Den process read it, not
	// a record from disk.
	verifier *sysd.Verifier
	readHere bool
	// powers is whether the console may answer cards and run commands:
	// given with a pairing unless the human locked them or God mode is
	// on, and by the human's ^Y on Den's powers card; taken by /api lock,
	// God mode, or a refused try for the console.
	powers bool
	// auto is an automatic pairing's event (paired, or reconnected), not
	// yet told to the console's first subscribed connection.
	auto *ConsoleEvent
}

type consoleCode struct {
	words []string
	until time.Time
}

type consoleState struct {
	mu          sync.Mutex
	code        *consoleCode
	spent       time.Time   // a claimed code's end: claims until then hear "code spent"
	cur         *consoleRec // the console, nil for none
	pending     *consoleRec // a claim waiting for the human
	cooldown    time.Time
	unpairedAll time.Time
	// recordHash is the record's hash as Den last wrote it; intruders
	// when each identity last raised an intruder card.
	recordHash string
	intruders  map[string]time.Time
	// godOn says whether God mode is on (SetGodMode), nil for never.
	godOn func() bool
	// locked is the human's /api lock: every console, re-paired or
	// restored, comes without its powers until the ^Y on Den's powers
	// card. It is kept in the record across Den's restarts.
	locked bool
	// autoOff stops automatic pairing after an unpair by the human, the
	// console itself or Den's own guards, until the human starts a pairing
	// in Den; autoHold stops it until then after three tries from other
	// programs or wrong proofs.
	autoOff  bool
	autoHold time.Time
	// last is what Den last read of the console's service, for the changes
	// an automatic pairing names. given is the read this Den process last
	// gave the powers to, itself (never one read back from disk): unless
	// ConsolePowersAlways, only the same program, unit and pins pair with
	// them.
	last  *sysd.Verifier
	given *sysd.Verifier
}

// consoleKey is Den's half: its Ed25519 key, loaded once (signing caches
// the expanded key with the slice, so it is never copied per signature),
// its public key in hex, and Den's own identity for its signatures.
type consoleKey struct {
	priv     ed25519.PrivateKey
	pubHex   string
	identity string
}

func newConsoleKey(priv ed25519.PrivateKey) *consoleKey {
	if len(priv) != ed25519.PrivateKeySize {
		return nil
	}
	k := &consoleKey{priv: priv, pubHex: hex.EncodeToString(priv.Public().(ed25519.PublicKey))}
	pid := os.Getpid()
	start, _, _ := procStat(pid)
	k.identity = "den:" + bootID() + ":" + strconv.Itoa(pid) + ":" + strconv.FormatUint(start, 10)
	return k
}

// consoleIdentity is the identity a console is bound to: one process in
// this boot, with its executable as Den read it. A readable path is
// hex-encoded, since a path may hold newlines or colons and the identity
// goes into the newline-separated signed string. False when Den cannot
// tell which process this is.
func consoleIdentity(p Peer) (string, bool) {
	if p.PID <= 0 || p.Start == 0 || bootID() == "unknown" {
		return "", false
	}
	if strings.HasPrefix(p.Exe, unreadablePrefix) {
		if strings.Contains(p.Exe, ":conn:") {
			return "", false
		}
		return p.Exe, true
	}
	return "exe:" + hex.EncodeToString([]byte(p.Exe)) + ":" + bootID() + ":" + strconv.Itoa(p.PID) + ":" + strconv.FormatUint(p.Start, 10), true
}

// randHex64 is 32 random bytes in hex.
func randHex64() string {
	var b [32]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// isHex reports whether s is n lowercase hex digits.
func isHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !(s[i] >= '0' && s[i] <= '9' || s[i] >= 'a' && s[i] <= 'f') {
			return false
		}
	}
	return true
}

// codeKey derives a MAC key from the code's words, as UTF-8 [a-z] words
// joined by single spaces.
func codeKey(code []byte, salt, info string) []byte {
	k, err := hkdf.Key(sha256.New, code, []byte(salt), info, 32)
	if err != nil {
		return nil
	}
	return k
}

func macHex(key []byte, msg string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(msg))
	return hex.EncodeToString(m.Sum(nil))
}

// codeBytes is the code as the MACs use it: its words joined by single
// spaces, built into one buffer that zero can clear. Zeroing is best
// effort: Go may have copied the words elsewhere, and the code is spent by
// the time it matters.
func codeBytes(words []string) []byte {
	n := len(words)
	for _, w := range words {
		n += len(w)
	}
	b := make([]byte, 0, n)
	for i, w := range words {
		if i > 0 {
			b = append(b, ' ')
		}
		b = append(b, w...)
	}
	return b
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// consoleSigned is what one side of a console proof signs.
func consoleSigned(dir, conn, pairing, appKey, denKey, nd, na, signer string) []byte {
	return []byte(strings.Join([]string{consoleSigLabel, dir, conn, pairing, appKey, denKey, nd, na, signer}, "\n"))
}

// StartConsoleCode makes a new console code, live for two minutes, and
// returns its words for Den's screen. An earlier one is forgotten.
func (s *Server) StartConsoleCode() []string {
	words := make([]string, 8)
	var b [8]byte
	rand.Read(b[:])
	for i := range words {
		words[i] = pairWords[b[i]]
	}
	s.con.mu.Lock()
	s.con.code = &consoleCode{words: words, until: time.Now().Add(pairCodeLife)}
	if s.con.autoOff {
		s.con.autoOff = false // the human acts in Den: the dock may pair by itself again
		s.saveConsoleLocked()
	}
	s.con.mu.Unlock()
	return append([]string(nil), words...)
}

// ConsoleCodeLive reports whether a console code waits to be claimed, and
// until when.
func (s *Server) ConsoleCodeLive() (time.Time, bool) {
	s.con.mu.Lock()
	defer s.con.mu.Unlock()
	c := s.con.code
	if c == nil || time.Now().After(c.until) {
		return time.Time{}, false
	}
	return c.until, true
}

// CurrentConsole is the console, if there is one.
func (s *Server) CurrentConsole() (Console, bool) {
	s.con.mu.Lock()
	defer s.con.mu.Unlock()
	if s.con.cur == nil {
		return Console{}, false
	}
	c := s.con.cur.Console
	c.Powers = s.con.cur.powers
	c.Verified = s.con.cur.verifier != nil && s.con.cur.readHere
	return c, true
}

// ConsoleAnswered is the human's ^Y (ok) or ^N on a console card.
func (s *Server) ConsoleAnswered(id string, ok bool) {
	s.con.mu.Lock()
	p := s.con.pending
	var ch chan bool
	if p != nil && p.ID == id && p.confirm != nil {
		ch, p.confirm = p.confirm, nil
	}
	s.con.mu.Unlock()
	if ch != nil {
		ch <- ok
	}
}

// UnpairConsole ends the console, and a claim waiting for the human, and
// closes the console's connections after telling them why; why is also
// noted in Den's chat when it is not "".
func (s *Server) UnpairConsole(cause, why string) int {
	s.con.mu.Lock()
	rec, pending := s.con.cur, s.con.pending
	s.con.cur, s.con.pending = nil, nil
	switch cause {
	case CauseReplaced:
	case CauseIntruders, CauseProofs, CauseBurst:
		s.con.autoHold = time.Now().Add(autoPairHold)
	default:
		s.con.autoOff = true
	}
	s.saveConsoleLocked() // the switch and the hold are kept across Den's restarts too
	if pending != nil && pending.confirm != nil {
		close(pending.confirm)
		pending.confirm = nil
	}
	s.con.mu.Unlock()
	if rec == nil {
		return 0
	}
	reason := why
	if len(reason) > 40 {
		reason = "unpaired in Den"
	}
	s.closeConsole(rec.ID, ConsoleEvent{Kind: "unpaired", PID: rec.PID, StartTicks: rec.start, Started: hhmmss(rec.Started), Reason: reason, Cause: cause})
	if why != "" {
		s.note(why)
	}
	// An unpair also ends God mode: both are lowerings.
	s.act(Act{ConsoleChanged: true, GodOff: "the console was unpaired"})
	return 1
}

// closeConsole tells the console's connections ev, then closes them.
func (s *Server) closeConsole(id string, ev ConsoleEvent) {
	conns := s.consoleConns(id)
	for _, c := range conns {
		if c.subscribed(EventConsole) {
			c.push(Event{Event: EventConsole, Data: ev}, false)
		}
	}
	if len(conns) > 0 {
		go func() {
			time.Sleep(50 * time.Millisecond) // what they were told goes first
			for _, c := range conns {
				c.close()
			}
		}()
	}
}

// consoleConns are the connections proved as console id.
func (s *Server) consoleConns(id string) []*conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*conn
	for c := range s.conns {
		c.mu.Lock()
		if c.console == id {
			out = append(out, c)
		}
		c.mu.Unlock()
	}
	return out
}

func hhmmss(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("15:04:05")
}

// isConsoleProcess reports whether peer is the paired console's process.
func (s *Server) isConsoleProcess(peer Peer) bool {
	s.con.mu.Lock()
	defer s.con.mu.Unlock()
	cur := s.con.cur
	return cur != nil && cur.PID == peer.PID && cur.start == peer.Start
}

// leakedConsole reports whether words hold the console code: the live
// one, or the one a claim waiting for the human was made with, at least
// pairLeakWords of its words, counted as leaked() counts them. Den then
// cancels the live code, refuses the waiting claim and notes it.
func (s *Server) leakedConsole(words []string) bool {
	st := &s.con
	st.mu.Lock()
	live := st.code != nil && time.Now().Before(st.code.until) && codeWordsIn(st.code.words, words) >= pairLeakWords
	if live {
		st.code = nil
	}
	var refuse chan bool
	if p := st.pending; p != nil && p.words != nil && codeWordsIn(p.words, words) >= pairLeakWords {
		p.leaked = true
		refuse, p.confirm = p.confirm, nil
	}
	st.mu.Unlock()
	if !live && refuse == nil {
		return false
	}
	if live {
		s.act(Act{ConsoleCodeGone: true})
	}
	if refuse != nil {
		close(refuse)
	}
	s.note("The console code was typed into a chat; pair again with den pair.")
	return true
}

// consoleChallenge is console.challenge: a fresh nonce for console.auth,
// and a claim salt for a console.claim on this connection. It reveals and
// spends nothing about any code.
func (s *Server) consoleChallenge(c *conn, req Request) {
	if s.ckey == nil {
		c.fail(req.ID, CodeUnavailable, "console pairing is not set up")
		return
	}
	now := time.Now()
	c.mu.Lock()
	if now.Sub(c.cchallenged) < challengeEvery {
		c.mu.Unlock()
		c.fail(req.ID, CodeUnavailable, "one challenge a second")
		return
	}
	c.cchallenged, c.cnonce, c.claimSalt = now, randHex64(), randHex64()
	nonce, salt, cid := c.cnonce, c.claimSalt, c.cid
	c.mu.Unlock()
	c.reply(req.ID, map[string]string{"nonce": nonce, "conn": cid, "claim_salt": salt})
}

// consoleClaim is console.claim: the app's public key and its proof that
// it knows the code, once per code. Its answer comes when the human
// answers the card.
func (s *Server) consoleClaim(c *conn, req Request) {
	if s.ckey == nil {
		c.fail(req.ID, CodeUnavailable, "console pairing is not set up")
		return
	}
	var p struct {
		PublicKey string `json:"public_key"`
		Proof     string `json:"proof"`
		Pages     string `json:"pages_sha256"`
	}
	if json.Unmarshal(req.Params, &p) != nil || !isHex(p.PublicKey, 64) || !isHex(p.Proof, 64) || (p.Pages != "" && !isHex(p.Pages, 64)) {
		c.fail(req.ID, CodeBadRequest, "public_key and proof: 64 lowercase hex digits each")
		return
	}
	identity, ok := consoleIdentity(c.peer)
	if !ok {
		c.fail(req.ID, CodeForbidden, "Den cannot tell which process this is")
		return
	}
	c.mu.Lock()
	salt := c.claimSalt
	c.claimSalt = ""
	c.mu.Unlock()
	if salt == "" {
		c.fail(req.ID, CodeBadRequest, "console.challenge first")
		return
	}
	now := time.Now()
	st := &s.con
	st.mu.Lock()
	if now.Before(st.cooldown) {
		st.mu.Unlock()
		c.fail(req.ID, CodeUnavailable, ClaimCooldown)
		return
	}
	if st.pending != nil {
		st.mu.Unlock()
		c.fail(req.ID, CodeUnavailable, ClaimBusy)
		return
	}
	code := st.code
	if code == nil || now.After(code.until) {
		why := ClaimNoCode
		if now.Before(st.spent) {
			why = ClaimSpent
		}
		st.mu.Unlock()
		c.fail(req.ID, CodeForbidden, why)
		return
	}
	st.code, st.spent = nil, code.until // one attempt per code, right or wrong
	text := codeBytes(code.words)
	words := code.words
	code.words = nil
	st.mu.Unlock()
	s.act(Act{ConsoleCodeGone: true})
	claimKey := codeKey(text, salt, consoleClaimInfo)
	want := macHex(claimKey, consoleClaimInfo+"\n"+p.PublicKey)
	zero(claimKey)
	if !hmac.Equal([]byte(want), []byte(p.Proof)) {
		zero(text)
		s.note("A console claim did not know the code: the code is spent. /api console starts another.")
		c.fail(req.ID, CodeForbidden, ClaimWrong)
		return
	}
	// Proved: the claimant is read as the console's service, as a
	// console.request is, outside the lock. A clean read is kept, so the
	// dock may re-pair after a restart as one paired with no code; any
	// other read leaves the pairing as a code makes it, with no strike.
	self := sysd.ProcID{PID: c.peer.PID, Start: c.peer.Start}
	v, err := readUnit(self)
	if time.Now().After(code.until) {
		zero(text)
		c.fail(req.ID, CodeForbidden, ClaimNoCode)
		return
	}
	pub, _ := hex.DecodeString(p.PublicKey)
	id := randHex()
	rec := &consoleRec{
		Console:  Console{ID: id, Exe: c.peer.Exe, PID: self.PID, Since: now, PagesSHA: p.Pages},
		identity: identity, start: self.Start, pub: ed25519.PublicKey(pub), pubHex: p.PublicKey,
		confirm: make(chan bool, 1), words: words,
	}
	ask := ConsoleAsk{ID: id, Exe: c.peer.Exe, PID: self.PID}
	if err == nil {
		rec.verifier, rec.readHere = &v, true
		ask.Rejoins, ask.Unit = true, ConsoleUnit
	}
	ask.Started, ask.Parent, ask.Cmdline = procInfo(self.PID)
	rec.Started = ask.Started
	st.mu.Lock()
	if time.Now().Before(st.cooldown) {
		st.mu.Unlock()
		zero(text)
		c.fail(req.ID, CodeUnavailable, ClaimCooldown)
		return
	}
	if st.pending != nil {
		st.mu.Unlock()
		zero(text)
		c.fail(req.ID, CodeUnavailable, ClaimBusy)
		return
	}
	if st.cur != nil {
		old := st.cur.Console
		ask.Replaces = &old
	}
	st.pending = rec
	// Taken with the claim, under the lock: the human's answer and a leak
	// clear the field (ConsoleAnswered, leakedConsole) under it too.
	confirm := rec.confirm
	st.mu.Unlock()
	if !s.act(Act{Console: &ask}) {
		st.mu.Lock()
		if st.pending == rec {
			st.pending = nil
		}
		st.mu.Unlock()
		zero(text)
		c.fail(req.ID, CodeUnavailable, ClaimBusy)
		return
	}
	s.awaitConsole(c, req, rec, confirm, func() { zero(text) }, func() map[string]any {
		bindKey := codeKey(text, id, consoleBindInfo)
		bind := macHex(bindKey, consoleBindInfo+"\n"+rec.pubHex+"\n"+s.ckey.pubHex+"\n"+id)
		zero(bindKey)
		return map[string]any{"pairing": id, "den_public_key": s.ckey.pubHex, "bind": bind, "console": true}
	})
}

// awaitConsole waits, off the connection's reader, for the human's answer
// to a console card in Den, then makes rec the console or refuses: the
// ^Y counts only for the claimant still connected and still the same
// process, and only while rec is still the claim waiting; a deny cools
// claims down. confirm is rec's answer channel, as the caller took it
// under s.con.mu when it made rec the claim waiting; rec.confirm itself is
// never read here, since the answer clears it under that lock. reply is
// the success answer, end runs last either way. The human's ^Y in Den's
// own window gives the console its powers.
func (s *Server) awaitConsole(c *conn, req Request, rec *consoleRec, confirm chan bool, end func(), reply func() map[string]any) {
	st := &s.con
	go func() {
		defer end()
		ok, why := false, ClaimTimeout
		select {
		case answer, open := <-confirm:
			ok, why = answer, map[bool]string{true: ClaimDenied, false: ClaimRevoked}[open]
		case <-time.After(pairConfirmLife):
		case <-c.done:
		}
		if ok {
			// The ^Y is for the claimant: still connected, still the same
			// process.
			select {
			case <-c.done:
				ok, why = false, ClaimRevoked
			default:
			}
			if start, _, err := procStatFn(rec.PID); err != nil || start != rec.start {
				ok, why = false, ClaimRevoked
			}
		}
		st.mu.Lock()
		mine := st.pending == rec
		if mine {
			st.pending = nil
		}
		rec.words = nil
		if rec.leaked {
			ok, why = false, ClaimLeaked
		}
		var old *consoleRec
		locked := st.locked
		if ok && mine {
			// The human's /api lock outlasts any pairing: only the powers
			// card's ^Y lifts it.
			rec.powers = !locked
			old, st.cur = st.cur, rec
			if rec.verifier != nil {
				// The human paired this program by code and ^Y.
				st.last, st.given = rec.verifier, rec.verifier
			}
			s.saveConsoleLocked()
		} else if why == ClaimDenied && mine {
			st.cooldown = time.Now().Add(consoleCooldown)
		}
		st.mu.Unlock()
		if !ok || !mine {
			if !mine {
				why = ClaimRevoked
			}
			s.act(Act{ConsoleDone: rec.ID})
			c.fail(req.ID, CodeForbidden, why)
			return
		}
		if old != nil && old.ID != rec.ID {
			s.closeConsole(old.ID, ConsoleEvent{Kind: "unpaired", PID: old.PID, StartTicks: old.start, Started: hhmmss(old.Started), Reason: "another console was paired", Cause: CauseReplaced})
		}
		s.act(Act{ConsoleDone: rec.ID, ConsoleChanged: true})
		if locked {
			s.askPowers(rec, "you locked the console's powers in Den")
		}
		c.reply(req.ID, reply())
	}()
}

// strike counts a proof for the console from another process, or a wrong
// signature; consoleStrikesMax in consoleStrikeWindow unpair it. Called
// with s.con.mu held; reports whether to unpair.
func (r *consoleRec) strike(now time.Time) bool {
	var recent []time.Time
	for _, t := range r.strikes {
		if now.Sub(t) < consoleStrikeWindow {
			recent = append(recent, t)
		}
	}
	r.strikes = append(recent, now)
	return len(r.strikes) >= consoleStrikesMax
}

// consoleAuth is console.auth: the console's signature over this
// connection's transcript, and Den's in answer.
func (s *Server) consoleAuth(c *conn, req Request) {
	if s.ckey == nil {
		c.fail(req.ID, CodeUnavailable, "console pairing is not set up")
		return
	}
	var p struct {
		Pairing string `json:"pairing"`
		Nonce   string `json:"nonce"`
		Sig     string `json:"sig"`
	}
	if json.Unmarshal(req.Params, &p) != nil || !isHex(p.Pairing, 32) || !isHex(p.Nonce, 64) || !isHex(p.Sig, 128) {
		c.fail(req.ID, CodeBadRequest, "pairing (32), nonce (64) and sig (128) in lowercase hex")
		return
	}
	c.mu.Lock()
	nd, cid := c.cnonce, c.cid
	c.cnonce = ""
	c.mu.Unlock()
	if nd == "" {
		c.fail(req.ID, CodeBadRequest, "console.challenge first")
		return
	}
	s.checkConsoleRecord()
	identity, known := consoleIdentity(c.peer)
	now := time.Now()
	st := &s.con
	st.mu.Lock()
	rec := st.cur
	if rec == nil || rec.ID != p.Pairing {
		st.mu.Unlock()
		c.fail(req.ID, CodeNotFound, ClaimNoPairing)
		return
	}
	if !known || identity != rec.identity {
		unpair := rec.strike(now)
		key := identity
		if !known {
			key = c.peer.Exe
		}
		if st.intruders == nil {
			st.intruders = map[string]time.Time{}
		}
		card := now.Sub(st.intruders[key]) >= consoleStrikeWindow
		if card {
			st.intruders[key] = now
		}
		st.mu.Unlock()
		if card {
			// One card per program in the strike window, across pairings.
			s.act(Act{ConsoleIntruder: &ConsoleIntruder{Exe: c.peer.Exe, PID: c.peer.PID}})
		}
		for _, cc := range s.consoleConns(rec.ID) {
			if cc.subscribed(EventConsole) {
				cc.push(Event{Event: EventConsole, Data: ConsoleEvent{Kind: "refused", PID: c.peer.PID, StartTicks: c.peer.Start, Program: sanitize.Label(ExeLabel(c.peer.Exe), 40)}}, false)
			}
		}
		// Unpaired first, then told, as for a burst.
		if unpair {
			s.UnpairConsole(CauseIntruders, "three tries for your console from other programs in ten minutes: it is unpaired; pair again with den pair")
		} else {
			s.LockConsolePowers("another program tried your console")
		}
		c.fail(req.ID, CodeForbidden, "another program holds this console")
		return
	}
	appKey, pub := rec.pubHex, rec.pub
	st.mu.Unlock()
	if len(s.consoleConns(rec.ID)) >= consoleConnsMax {
		c.fail(req.ID, CodeConsoleInUse, "the console has as many connections as it may")
		return
	}
	sig, _ := hex.DecodeString(p.Sig)
	if !ed25519.Verify(pub, consoleSigned(dirAppToDen, cid, rec.ID, appKey, s.ckey.pubHex, nd, p.Nonce, identity), sig) {
		st.mu.Lock()
		unpair := st.cur == rec && rec.strike(now)
		st.mu.Unlock()
		// Unpaired first, then told, as for a burst.
		if unpair {
			s.UnpairConsole(CauseProofs, "three wrong console proofs in ten minutes: the console is unpaired; pair again with den pair")
		} else {
			s.LockConsolePowers("a wrong proof for your console")
		}
		c.fail(req.ID, CodeForbidden, "wrong proof")
		time.Sleep(50 * time.Millisecond) // the answer first, as for too_large
		c.close()
		return
	}
	st.mu.Lock()
	if st.cur != rec {
		st.mu.Unlock()
		c.fail(req.ID, CodeNotFound, ClaimNoPairing)
		return
	}
	rec.LastProof = now
	powers := rec.powers
	st.mu.Unlock()
	others := s.consoleConns(rec.ID)
	c.mu.Lock()
	c.console = rec.ID
	c.mu.Unlock()
	for _, cc := range others {
		if cc.subscribed(EventConsole) {
			cc.push(Event{Event: EventConsole, Data: ConsoleEvent{Kind: "connected", PID: c.peer.PID, StartTicks: c.peer.Start, Started: hhmmss(rec.Started)}}, false)
		}
	}
	den := ed25519.Sign(s.ckey.priv, consoleSigned(dirDenToApp, cid, rec.ID, appKey, s.ckey.pubHex, nd, p.Nonce, s.ckey.identity))
	// God mode's state as it is now. The view is the one Den is in, and
	// absent when this Den cannot switch views.
	rep := map[string]any{"paired": true, "sig": hex.EncodeToString(den), "god": s.god.get(), "powers": powers}
	if v := s.currentView(); v != "" {
		rep["view"] = v
	}
	c.reply(req.ID, rep)
}

// consoleEvents is console.events: this proved connection of the console
// gets its console and card events. The first after a restore hears that
// it was restored; any other, that it connected.
func (s *Server) consoleEvents(c *conn, req Request) {
	c.mu.Lock()
	id := c.console
	c.mu.Unlock()
	if id == "" {
		c.fail(req.ID, CodeForbidden, "prove first")
		return
	}
	st := &s.con
	st.mu.Lock()
	rec := st.cur
	if rec == nil || rec.ID != id {
		st.mu.Unlock()
		c.fail(req.ID, CodeNotFound, ClaimNoPairing)
		return
	}
	kind := "connected"
	if rec.restored {
		kind, rec.restored = "restored", false
	}
	auto := rec.auto
	rec.auto = nil
	ev := ConsoleEvent{Kind: kind, PID: rec.PID, StartTicks: rec.start, Started: hhmmss(rec.Started)}
	st.mu.Unlock()
	c.mu.Lock()
	c.subs[EventConsole] = true
	c.subs[EventCard] = true
	c.mu.Unlock()
	// The cards already waiting: their ids in the reply, the whole set, so
	// that a card not listed is gone; then each as new, after a re-dial or
	// a reload, so the console sees what Den sees.
	var waiting []Card
	ids := []string{}
	if v := s.view.Load(); v != nil {
		for _, card := range v.Cards {
			waiting = append(waiting, card)
		}
	}
	sort.Slice(waiting, func(i, j int) bool { return waiting[i].ID < waiting[j].ID })
	for _, card := range waiting {
		ids = append(ids, card.ID)
	}
	c.reply(req.ID, map[string]any{"ok": true, "cards": ids})
	c.push(Event{Event: EventConsole, Data: ev}, true)
	for _, card := range waiting {
		c.push(Event{Event: EventCard, Data: cardEvent(card, "new")}, true)
	}
	// The mode in force, after the replay, which ends at the listed cards.
	if v := s.view.Load(); v != nil && v.Mode.Global != "" {
		c.push(Event{Event: EventMode, Data: v.Mode}, true)
	}
	// Then an automatic pairing, told to the connection that subscribes
	// first.
	if auto != nil {
		c.push(Event{Event: EventConsole, Data: *auto}, true)
	}
}

// consoleUnpair is console.unpair: {} ends this console, from its own
// proved connection; {"all": true} ends every pairing, apps' too, from any
// connection, at most one a unpairAllEvery for everyone: it only lowers,
// as den unpair all in any terminal does.
func (s *Server) consoleUnpair(c *conn, req Request) {
	var p struct {
		All bool `json:"all"`
	}
	json.Unmarshal(req.Params, &p)
	if !p.All {
		c.mu.Lock()
		id := c.console
		c.mu.Unlock()
		cur, ok := s.CurrentConsole()
		if id == "" || !ok || cur.ID != id {
			c.fail(req.ID, CodeForbidden, "prove first, or send {\"all\": true}")
			return
		}
		c.reply(req.ID, map[string]any{"ok": true, "unpaired": s.UnpairConsole(CauseSelf, "the console unpaired itself")})
		return
	}
	now := time.Now()
	s.con.mu.Lock()
	if now.Sub(s.con.unpairedAll) < unpairAllEvery {
		s.con.mu.Unlock()
		c.fail(req.ID, CodeUnavailable, "too many unpairs")
		return
	}
	s.con.unpairedAll = now
	s.con.mu.Unlock()
	n := len(s.Pairings())
	s.Unpair("", "")
	// From the console's own proved connection it is the console unpairing
	// itself (along with the rest); from any other program, that program's
	// act, which the human did not do.
	c.mu.Lock()
	id := c.console
	c.mu.Unlock()
	if cur, ok := s.CurrentConsole(); ok && id != "" && cur.ID == id {
		n += s.UnpairConsole(CauseSelf, "")
	} else {
		n += s.UnpairConsole(CauseOther, "")
	}
	s.note("every console and app pairing was unpaired by " + sanitize.Label(ExeLabel(c.peer.Exe), 40) + " pid " + itoa(c.peer.PID))
	c.reply(req.ID, map[string]any{"ok": true, "unpaired": n})
}
