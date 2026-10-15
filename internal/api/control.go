package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/GlacierHubAB/den/internal/sanitize"
)

// Control lets one paired app speak to the orchestrator the human talks
// to in Den (orch.say). Pairing: the human runs /api pair and Den shows a
// code of eight words, on its own screen only; the app sends it once
// (pair.claim); Den asks the human, on an inbox card that names the
// program that claimed it, and only their ^Y completes the pairing and
// hands the app a 128-bit key. Den and the app keep the key in memory
// only. Each connection that wants control then proves the key once
// (pair.challenge, pair.auth), and the proof goes both ways. Every tool the
// orchestrator wants in an app's turn waits for the human in Den (the
// orchestrator package and internal/ui); nothing here approves anything.

// Control's limits.
var (
	pairCodeLife    = 120 * time.Second // a code is live this long
	pairConfirmLife = 120 * time.Second // the human has this long to confirm
)

const (
	pairWrongMax   = 3                // wrong codes before the attempt ends
	pairLeakAfter  = 10 * time.Minute // a spent code is looked for in texts this long
	pairLeakWords  = 5                // this many of its words make a text hold the code
	authFailMax    = 3                // wrong proofs, in authFailWindow, that revoke
	authFailWindow = 5 * time.Minute
	challengeEvery = time.Second // one challenge a connection this often
	sayPerMinute   = 6
	// SayPrefaceMax bounds Den's preface before an app's text.
	SayPrefaceMax = 512
)

// EventOrch carries an app turn's chunks.
const EventOrch = "orch"

// PairAsk asks the human, on an inbox card, whether the program that
// claimed the pairing code may have control.
type PairAsk struct {
	ID      string
	Exe     string
	PID     int
	Started time.Time // when the process started, zero when unknown
	Parent  string    // its parent's name
	// Cmdline is its command line, sanitised and capped: under an
	// interpreter (python3, node) the executable alone names no app.
	Cmdline string
}

// Pairing is one pairing as /api lists it.
type Pairing struct {
	ID        string
	Exe       string
	PID       int
	Since     time.Time
	LastUsed  time.Time
	Confirmed bool
	Off       bool // control off for it (/api control off)
}

type pairing struct {
	Pairing
	key     []byte
	confirm chan bool // the human's answer, while unconfirmed
	fails   []time.Time
	says    []time.Time
	rules   []Rule
}

// Rule is a standing allowance the human gave a pairing with /always on a
// card: one Den tool the orchestrator may use in that app's turns without
// asking, and for send_message one session, bound by its id, pid and start
// so that another session that takes its name is asked about again. It
// lives as long as the pairing.
type Rule struct {
	Tool      string
	Peer      string // send_message: the peer name (orch.PeerName)
	SessionID string
	PID       int
	Start     uint64
	At        time.Time
	From      string // the card it was granted on
}

// RulesMax is the most standing allowances a pairing holds.
const RulesMax = 16

// ErrRules is AddRule's refusal at RulesMax.
var ErrRules = errors.New("this app has 16 standing allowances; /api disallow removes some")

// AddRule gives a confirmed pairing a standing allowance; one already there
// is not added twice.
func (s *Server) AddRule(pairing string, r Rule) error {
	s.ctl.mu.Lock()
	defer s.ctl.mu.Unlock()
	pr := s.ctl.pairings[pairing]
	if pr == nil || !pr.Confirmed {
		return errors.New("the pairing is gone")
	}
	for _, o := range pr.rules {
		if o.Tool == r.Tool && o.Peer == r.Peer && o.SessionID == r.SessionID {
			return nil
		}
	}
	if len(pr.rules) >= RulesMax {
		return ErrRules
	}
	pr.rules = append(pr.rules, r)
	return nil
}

// Rules are a pairing's standing allowances, in the order given; none
// while control is off for it.
func (s *Server) Rules(pairing string) []Rule {
	s.ctl.mu.Lock()
	defer s.ctl.mu.Unlock()
	pr := s.ctl.pairings[pairing]
	if pr == nil || !pr.Confirmed || pr.Off {
		return nil
	}
	return append([]Rule(nil), pr.rules...)
}

// AllRules lists every pairing's allowances, numbered from 1 in Pairings'
// order, for /api and /api disallow.
func (s *Server) AllRules() []NumberedRule {
	var out []NumberedRule
	for _, p := range s.Pairings() {
		s.ctl.mu.Lock()
		pr := s.ctl.pairings[p.ID]
		var rules []Rule
		if pr != nil {
			rules = append(rules, pr.rules...)
		}
		s.ctl.mu.Unlock()
		for _, r := range rules {
			out = append(out, NumberedRule{N: len(out) + 1, Pairing: p.ID, Exe: p.Exe, Rule: r})
		}
	}
	return out
}

// NumberedRule is one allowance as /api lists it.
type NumberedRule struct {
	N       int
	Pairing string
	Exe     string
	Rule
}

// Disallow removes allowance n (AllRules' numbering), or every one with
// n 0; false when there is no such allowance.
func (s *Server) Disallow(n int) bool {
	all := s.AllRules()
	s.ctl.mu.Lock()
	defer s.ctl.mu.Unlock()
	if n == 0 {
		for _, pr := range s.ctl.pairings {
			pr.rules = nil
		}
		return len(all) > 0
	}
	for _, r := range all {
		if r.N != n {
			continue
		}
		pr := s.ctl.pairings[r.Pairing]
		if pr == nil {
			return false
		}
		for i, o := range pr.rules {
			if o == r.Rule {
				pr.rules = append(pr.rules[:i], pr.rules[i+1:]...)
				return true
			}
		}
	}
	return false
}

// SetControl turns control on or off for every pairing: off, orch.say is
// refused and no allowance applies, until it is on again.
func (s *Server) SetControl(on bool) {
	s.ctl.mu.Lock()
	defer s.ctl.mu.Unlock()
	for _, pr := range s.ctl.pairings {
		pr.Off = !on
	}
}

type pairCode struct {
	words   []string
	until   time.Time
	wrong   int
	spent   time.Time // when it was claimed, zero while live
	claimed string    // the pairing that claim made
}

type control struct {
	mu   sync.Mutex
	code *pairCode // the live code, nil for none
	// spent are the codes claimed in the last pairLeakAfter, apart from
	// any newer live code: reusing or leaking one revokes the pairing it
	// made.
	spent    []*pairCode
	pairings map[string]*pairing
	turn     *Say // the app turn running, nil for none
}

// Claim refusals: pair.claim's error message is exactly one of these, so
// an app can tell the human what happened.
const (
	ClaimNoCode    = "no code waiting"
	ClaimWrong     = "wrong code"
	ClaimSpent     = "code spent"
	ClaimRevoked   = "revoked: the code was used by another program too"
	ClaimDenied    = "denied in Den"
	ClaimTimeout   = "not confirmed in time"
	ClaimBusy      = "Den is busy"
	ClaimNoPairing = "no such pairing"
)

func newControl() *control { return &control{pairings: map[string]*pairing{}} }

// StartPairing makes a new code, live for two minutes, and returns its
// words for Den's screen. An earlier code, live or spent, is forgotten.
func (s *Server) StartPairing() []string {
	words := make([]string, 8)
	var b [8]byte
	rand.Read(b[:])
	for i := range words {
		words[i] = pairWords[b[i]]
	}
	s.ctl.mu.Lock()
	s.ctl.code = &pairCode{words: words, until: time.Now().Add(pairCodeLife)}
	s.ctl.mu.Unlock()
	return append([]string(nil), words...)
}

// CodeLive reports whether a pairing code is waiting to be claimed, and
// until when.
func (s *Server) CodeLive() (time.Time, bool) {
	s.ctl.mu.Lock()
	defer s.ctl.mu.Unlock()
	c := s.ctl.code
	if c == nil || !c.spent.IsZero() || time.Now().After(c.until) {
		return time.Time{}, false
	}
	return c.until, true
}

// Pairings lists the pairings, newest first.
func (s *Server) Pairings() []Pairing {
	s.ctl.mu.Lock()
	defer s.ctl.mu.Unlock()
	var out []Pairing
	for _, p := range s.ctl.pairings {
		out = append(out, p.Pairing)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Since.After(out[j].Since) })
	return out
}

// Paired reports whether any pairing is confirmed: control is on.
func (s *Server) Paired() bool {
	s.ctl.mu.Lock()
	defer s.ctl.mu.Unlock()
	for _, p := range s.ctl.pairings {
		if p.Confirmed {
			return true
		}
	}
	return false
}

// PairAnswered is the human's ^Y (ok) or ^N on a pairing card.
func (s *Server) PairAnswered(id string, ok bool) {
	s.ctl.mu.Lock()
	p := s.ctl.pairings[id]
	var ch chan bool
	if p != nil && !p.Confirmed && p.confirm != nil {
		ch, p.confirm = p.confirm, nil
	}
	s.ctl.mu.Unlock()
	if ch != nil {
		ch <- ok
	}
}

// Unpair revokes a pairing ("" for every one) and closes its connections.
func (s *Server) Unpair(id, why string) {
	s.ctl.mu.Lock()
	var gone []string
	for pid, p := range s.ctl.pairings {
		if id == "" || pid == id {
			gone = append(gone, pid)
			if p.confirm != nil {
				close(p.confirm)
				p.confirm = nil
			}
			delete(s.ctl.pairings, pid)
		}
	}
	s.ctl.mu.Unlock()
	if len(gone) == 0 {
		return
	}
	s.mu.Lock()
	var conns []*conn
	for c := range s.conns {
		c.mu.Lock()
		for _, g := range gone {
			if c.paired == g {
				conns = append(conns, c)
			}
		}
		c.mu.Unlock()
	}
	s.mu.Unlock()
	if len(conns) > 0 {
		// What each was last told (its turn's end) goes out first.
		go func() {
			time.Sleep(50 * time.Millisecond)
			for _, c := range conns {
				c.close()
			}
		}()
	}
	if why != "" {
		s.note(why)
	}
	s.act(Act{Paired: true})
}

// act hands Den an Act, dropping it when Den is too busy to take it.
func (s *Server) act(a Act) bool {
	select {
	case s.acts <- a:
		return true
	default:
		return false
	}
}

// codeWords are the words of a code typed as text: lower case, split on
// anything but letters, after NFKC.
func codeWords(text string) []string {
	return strings.FieldsFunc(strings.ToLower(norm.NFKC.String(text)), func(r rune) bool { return !unicode.IsLetter(r) })
}

// leaked reports whether text holds the pairing code: at least
// pairLeakWords of its words, while it is live and for pairLeakAfter after
// it was spent. Den then cancels the code (it is no longer secret) and
// notes it; the text itself is never kept.
func (s *Server) leaked(text string) bool {
	words := codeWords(text)
	holds := func(c *pairCode) bool {
		want := map[string]bool{}
		for _, w := range c.words {
			want[w] = true
		}
		hit := map[string]bool{}
		for _, w := range words {
			if want[w] {
				hit[w] = true
			}
		}
		return len(hit) >= pairLeakWords
	}
	now := time.Now()
	s.ctl.mu.Lock()
	live := s.ctl.code != nil && now.Before(s.ctl.code.until) && holds(s.ctl.code)
	if live {
		s.ctl.code = nil
	}
	var revoke []string
	kept := s.ctl.spent[:0]
	for _, sp := range s.ctl.spent {
		switch {
		case now.Sub(sp.spent) >= pairLeakAfter:
		case holds(sp):
			revoke = append(revoke, sp.claimed)
		default:
			kept = append(kept, sp)
		}
	}
	s.ctl.spent = kept
	s.ctl.mu.Unlock()
	if !live && len(revoke) == 0 {
		return false
	}
	if live {
		s.act(Act{CodeGone: true})
	}
	s.note("The pairing code was typed into a chat; pair again.")
	for _, id := range revoke {
		// The code that made a pairing is out: that pairing is not to be
		// trusted either.
		s.Unpair(id, "")
	}
	return true
}

// claim is pair.claim: the code, once, from the program that shows it to
// the human for their ^Y. Its answer comes when they answer.
func (s *Server) claim(c *conn, req Request) {
	var p struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(req.Params, &p) != nil || strings.TrimSpace(p.Code) == "" {
		c.fail(req.ID, CodeBadRequest, "code")
		return
	}
	got := strings.Join(codeWords(p.Code), " ")
	now := time.Now()
	st := s.ctl
	st.mu.Lock()
	code := st.code
	live := code != nil && !now.After(code.until)
	matchesLive := live && hmac.Equal([]byte(got), []byte(strings.Join(code.words, " ")))
	if !matchesLive {
		// A code was claimed already. Another claim in its two minutes,
		// or that code again later, means another program had it too: the
		// pairing it made is not to be trusted.
		var revoke []string
		kept := st.spent[:0]
		for _, sp := range st.spent {
			switch {
			case now.Sub(sp.spent) >= pairLeakAfter:
			case now.Sub(sp.spent) < pairCodeLife || got == strings.Join(sp.words, " "):
				revoke = append(revoke, sp.claimed)
			default:
				kept = append(kept, sp)
			}
		}
		st.spent = kept
		if len(revoke) > 0 {
			st.mu.Unlock()
			for _, id := range revoke {
				s.Unpair(id, "the pairing code was used by another program ("+sanitize.Label(filepath.Base(c.peer.Exe), 40)+", pid "+itoa(c.peer.PID)+"); pair again")
			}
			c.fail(req.ID, CodeForbidden, ClaimSpent)
			return
		}
	}
	if !live {
		st.mu.Unlock()
		c.fail(req.ID, CodeForbidden, ClaimNoCode)
		return
	}
	if !matchesLive {
		code.wrong++
		ended := code.wrong >= pairWrongMax
		if ended {
			st.code = nil
		}
		st.mu.Unlock()
		if ended {
			s.note("Three wrong pairing codes: the attempt ended. /api pair starts another.")
			s.act(Act{CodeGone: true})
		}
		c.fail(req.ID, CodeForbidden, ClaimWrong)
		return
	}
	id := randHex()
	key := make([]byte, 16)
	rand.Read(key)
	pr := &pairing{Pairing: Pairing{ID: id, Exe: c.peer.Exe, PID: c.peer.PID, Since: now}, key: key, confirm: make(chan bool, 1)}
	code.spent, code.claimed = now, id
	st.code, st.spent = nil, append(st.spent, code)
	st.pairings[id] = pr
	confirm := pr.confirm
	st.mu.Unlock()
	ask := PairAsk{ID: id, Exe: c.peer.Exe, PID: c.peer.PID}
	ask.Started, ask.Parent, ask.Cmdline = procInfo(c.peer.PID)
	s.act(Act{CodeGone: true})
	if !s.act(Act{Pair: &ask}) {
		s.Unpair(id, "")
		c.fail(req.ID, CodeUnavailable, ClaimBusy)
		return
	}
	go func() {
		ok, why := false, ClaimTimeout
		select {
		case answer, open := <-confirm:
			// Closed: the pairing was revoked while it waited.
			ok, why = answer, map[bool]string{true: ClaimDenied, false: ClaimRevoked}[open]
		case <-time.After(pairConfirmLife):
		case <-c.done:
		}
		st.mu.Lock()
		pr2 := st.pairings[id]
		if ok && pr2 != nil {
			pr2.Confirmed, pr2.confirm = true, nil
		}
		st.mu.Unlock()
		if !ok || pr2 == nil {
			s.Unpair(id, "")
			s.act(Act{PairDone: id})
			c.fail(req.ID, CodeForbidden, why)
			return
		}
		s.act(Act{PairDone: id, Paired: true})
		c.reply(req.ID, map[string]any{"pairing": id, "key": hex.EncodeToString(key), "control": true})
	}()
}

// procInfo is when pid started, its parent's name and its command line,
// for the pairing card; zero and "" when /proc cannot tell.
func procInfo(pid int) (time.Time, string, string) {
	raw, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	cmdline := sanitize.Label(strings.Join(strings.Fields(strings.ReplaceAll(string(raw), "\x00", " ")), " "), 200)
	start, ppid, err := procStatFn(pid)
	if err != nil {
		return time.Time{}, "", cmdline
	}
	var at time.Time
	if boot, err := bootTime(); err == nil {
		at = boot.Add(time.Duration(start) * time.Second / clockTicks)
	}
	comm, _ := os.ReadFile("/proc/" + strconv.Itoa(ppid) + "/comm")
	return at, sanitize.Label(strings.TrimSpace(string(comm)), 40), cmdline
}

// pairMAC is a proof over a connection's nonce, for one direction:
// app-to-den (the app's) or den-to-app (Den's).
func pairMAC(key []byte, dir, conn, nonce string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("pair-auth\n" + dir + "\n" + conn + "\n" + nonce))
	return hex.EncodeToString(m.Sum(nil))
}

// challenge is pair.challenge: a single-use nonce for this connection.
func (s *Server) challenge(c *conn, req Request) {
	now := time.Now()
	c.mu.Lock()
	if now.Sub(c.challenged) < challengeEvery {
		c.mu.Unlock()
		c.fail(req.ID, CodeUnavailable, "one challenge a second")
		return
	}
	c.challenged, c.nonce = now, randHex()
	nonce, cid := c.nonce, c.cid
	c.mu.Unlock()
	c.reply(req.ID, map[string]string{"nonce": nonce, "conn": cid})
}

// auth is pair.auth: the app's proof of the key over this connection's
// nonce. A wrong proof closes the connection; three in five minutes revoke
// the pairing.
func (s *Server) auth(c *conn, req Request) {
	var p struct {
		Pairing string `json:"pairing"`
		MAC     string `json:"mac"`
	}
	if json.Unmarshal(req.Params, &p) != nil || p.Pairing == "" || p.MAC == "" {
		c.fail(req.ID, CodeBadRequest, "pairing and mac")
		return
	}
	c.mu.Lock()
	nonce, cid := c.nonce, c.cid
	c.nonce = ""
	c.mu.Unlock()
	if nonce == "" {
		c.fail(req.ID, CodeBadRequest, "pair.challenge first")
		return
	}
	st := s.ctl
	st.mu.Lock()
	pr := st.pairings[p.Pairing]
	if pr == nil || !pr.Confirmed {
		st.mu.Unlock()
		c.fail(req.ID, CodeNotFound, ClaimNoPairing)
		return
	}
	if pr.Exe != c.peer.Exe {
		st.mu.Unlock()
		c.fail(req.ID, CodeForbidden, "the pairing belongs to another program")
		return
	}
	if !hmac.Equal([]byte(strings.ToLower(p.MAC)), []byte(pairMAC(pr.key, "app-to-den", cid, nonce))) {
		now := time.Now()
		var recent []time.Time
		for _, t := range pr.fails {
			if now.Sub(t) < authFailWindow {
				recent = append(recent, t)
			}
		}
		pr.fails = append(recent, now)
		revoke := len(pr.fails) >= authFailMax
		st.mu.Unlock()
		c.fail(req.ID, CodeForbidden, "wrong proof")
		time.Sleep(50 * time.Millisecond) // the answer first, as for too_large
		c.close()
		if revoke {
			s.Unpair(p.Pairing, "three wrong pairing proofs in five minutes from "+sanitize.Label(filepath.Base(c.peer.Exe), 40)+": the pairing is revoked; pair again")
		}
		return
	}
	pr.LastUsed = time.Now()
	mac := pairMAC(pr.key, "den-to-app", cid, nonce)
	st.mu.Unlock()
	c.mu.Lock()
	c.paired = p.Pairing
	c.mu.Unlock()
	c.reply(req.ID, map[string]any{"paired": true, "control": true, "mac": mac})
}

// SayText applies orch.say's rules: NFKC; lines broken at \n, \r\n, \r,
// U+0085, U+2028 and U+2029; every C0 and C1 control removed; a line that
// starts with "/", "!" or "#" once trimmed refuses the whole text, as does
// a bidi or invisible character; < and > become ‹ and ›, so the text cannot
// open or close a block of Den's (an <agent_report>, a peer's envelope).
// The lines are joined with \n.
func SayText(text string) (string, error) {
	if len(text) > MaxAskText {
		return "", errors.New("the text is over 16 KiB")
	}
	t := norm.NFKC.String(text)
	t = strings.NewReplacer("<", "‹", ">", "›").Replace(t)
	t = strings.NewReplacer("\r\n", "\n", "\r", "\n", "\u0085", "\n", " ", "\n", " ", "\n").Replace(t)
	lines := strings.Split(t, "\n")
	for i, l := range lines {
		l = strings.Map(func(r rune) rune {
			if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
				return -1
			}
			return r
		}, l)
		if r, bad := sanitize.Hidden(l); bad {
			return "", fmt.Errorf("the text holds a bidi or invisible character (%U)", r)
		}
		if tr := strings.TrimLeftFunc(l, unicode.IsSpace); strings.HasPrefix(tr, "/") || strings.HasPrefix(tr, "!") || strings.HasPrefix(tr, "#") {
			return "", errors.New(`a line starts with "/", "!" or "#"`)
		}
		lines[i] = l
	}
	out := strings.TrimSpace(strings.Join(lines, "\n"))
	if out == "" {
		return "", errors.New("text")
	}
	return out, nil
}

// Say is an app's turn for the orchestrator, handed to Den (Act.Say); Den
// streams its chunks back through it, and exactly one Done or Fail ends it.
type Say struct {
	Turn    string
	Text    string // after SayText
	Exe     string // the program, as Den read it
	Pairing string // the pairing the connection proved

	c      *conn
	s      *Server
	mu     sync.Mutex
	seq    int
	ended  bool
	cancel chan struct{} // closed when the app's connection goes
	fin    chan struct{} // closed when the turn ends
}

func (y *Say) push(ch Chunk) bool {
	ch.Turn = y.Turn
	return y.c.push(Event{Event: EventOrch, Data: ch}, true)
}

// Delta streams a piece of the orchestrator's answer.
func (y *Say) Delta(text string) {
	y.mu.Lock()
	defer y.mu.Unlock()
	if !y.ended && text != "" {
		y.push(Chunk{Type: "text", Delta: text})
	}
}

// Status shows one of Den's fixed status lines ("" clears it).
func (y *Say) Status(text string) {
	y.mu.Lock()
	defer y.mu.Unlock()
	if y.ended {
		return
	}
	y.seq++
	y.push(Chunk{Type: "status", Seq: y.seq, Text: text})
}

// NeedsApproval says something waits for the human in Den: card is the
// inbox card's title, item its id (for focus "item:<id>").
func (y *Say) NeedsApproval(card, item string) {
	y.mu.Lock()
	defer y.mu.Unlock()
	if !y.ended {
		y.push(Chunk{Type: "needs_approval", Card: sanitize.Label(card, 200), Item: item})
	}
}

// Done ends the turn.
func (y *Say) Done() { y.end("") }

// Fail ends the turn with why.
func (y *Say) Fail(why string) { y.end(why) }

// Cancelled is closed when the app's connection goes.
func (y *Say) Cancelled() <-chan struct{} { return y.cancel }

func (y *Say) end(why string) {
	y.mu.Lock()
	if y.ended {
		y.mu.Unlock()
		return
	}
	y.ended = true
	close(y.fin)
	if why == "" {
		y.push(Chunk{Type: "done"})
	} else {
		y.push(Chunk{Type: "error", Message: why})
	}
	y.mu.Unlock()
	y.s.ctl.mu.Lock()
	if y.s.ctl.turn == y {
		y.s.ctl.turn = nil
	}
	y.s.ctl.mu.Unlock()
	y.c.mu.Lock()
	y.c.asks--
	y.c.mu.Unlock()
}

// say is orch.say: from a connection that passed pair.auth, one turn at a
// time, six a minute.
func (s *Server) say(c *conn, req Request) {
	var p struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(req.Params, &p) != nil || strings.TrimSpace(p.Text) == "" {
		c.fail(req.ID, CodeBadRequest, "text")
		return
	}
	c.mu.Lock()
	paired := c.paired
	c.mu.Unlock()
	st := s.ctl
	st.mu.Lock()
	pr := st.pairings[paired]
	if paired == "" || pr == nil || !pr.Confirmed {
		st.mu.Unlock()
		c.fail(req.ID, CodeForbidden, "pair.auth first")
		return
	}
	if pr.Off {
		st.mu.Unlock()
		c.fail(req.ID, CodeForbidden, "control is off for this pairing in Den")
		return
	}
	st.mu.Unlock()
	if s.leaked(p.Text) {
		c.fail(req.ID, CodeBadRequest, "the text holds the pairing code; Den cancelled it")
		return
	}
	text, err := SayText(p.Text)
	if err != nil {
		c.fail(req.ID, CodeBadRequest, err.Error())
		return
	}
	now := time.Now()
	st.mu.Lock()
	var recent []time.Time
	for _, t := range pr.says {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	if len(recent) >= sayPerMinute {
		st.mu.Unlock()
		c.fail(req.ID, CodeUnavailable, "six turns a minute")
		return
	}
	if st.turn != nil {
		st.mu.Unlock()
		c.fail(req.ID, CodeUnavailable, "an app's turn is running")
		return
	}
	pr.says, pr.LastUsed = append(recent, now), now
	y := &Say{Turn: "t-" + randHex(), Text: text, Exe: c.peer.Exe, Pairing: paired, c: c, s: s, cancel: make(chan struct{}), fin: make(chan struct{})}
	st.turn = y
	st.mu.Unlock()
	c.mu.Lock()
	c.asks++
	c.mu.Unlock()
	c.reply(req.ID, map[string]string{"turn": y.Turn})
	if !s.act(Act{Say: y}) {
		y.Fail("Den is busy; say it again")
		return
	}
	go func() {
		select {
		case <-c.done:
			// Closing the connection ends the turn; Den stops it.
			close(y.cancel)
			y.Fail("cancelled")
		case <-y.fin:
		}
	}()
}
