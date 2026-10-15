package api

import (
	"encoding/json"
	"strings"
	"sync"
	"time"
)

// Cards from the dock: a console reads a card whole (card.get), with a hash
// over the exact text it will show and a nonce bound to its connection,
// then answers it once (card.answer). Den checks the nonce, that the card
// is unchanged, that the console read it before answering, and that the
// kind and verb are ones a console may answer; then Den's own answer path
// runs, as for the human's ^Y (Act.CardAnswer).

// The card limits.
const (
	cardNonceLife   = 10 * time.Minute
	cardReadAtLeast = 300 * time.Millisecond // before answering a card of more than cardLongLines
	cardLongLines   = 10
	cardAnswersMax  = 20  // a minute, per console
	cardBurst       = 5   // answers in a row faster than one a second unpair the console
	cardReadsMax    = 256 // nonces and first reads a connection keeps
)

// Card answer refusals.
const (
	CodeCardChanged     = "card_changed"
	CodeNonceExpired    = "nonce_expired"
	CodeReadItFirst     = "read_it_first"
	CodeRefusedFromDock = "refused_from_dock"
)

// Card is one card as Den shows it, for a console. Den fills Snapshot.Cards
// with what it would draw; the text is the lines it shows, sanitised.
type Card struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Agent string `json:"agent"`
	Tool  string `json:"tool"` // the tool asked for, "" for none
	Title string `json:"title"`
	// Note is a warning Den shows with the card (two agents share the
	// name), "" for none.
	Note        string   `json:"note"`
	Body        string   `json:"body"`
	Hash        string   `json:"text_sha256"`
	Truncated   bool     `json:"truncated"`
	CutAt       int      `json:"cut_at,omitempty"`
	Verbs       []string `json:"verbs"`
	DockAnswers bool     `json:"dock_answers"`
}

// CardHash is a card's text_sha256: over kind, agent, tool, title, note
// and body, in that order, one per line (none but the body holds a
// newline).
func CardHash(kind, agent, tool, title, note, body string) string {
	return sha256Hex([]byte(kind + "\n" + agent + "\n" + tool + "\n" + title + "\n" + note + "\n" + body))
}

// CardEvent is a card event's data: a card came, changed or was settled.
type CardEvent struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Agent  string `json:"agent"`
	Change string `json:"change"` // new, changed or settled
	Hash   string `json:"text_sha256,omitempty"`
	// NeedsDenScreen is set on a new or changed card the console may not
	// answer: only the human answers it, on Den's own screen.
	NeedsDenScreen bool `json:"needs_den_screen,omitempty"`
	// On a settled card: allowed, denied, always or gone (it settled
	// without an answer: the agent moved on, or it went to its terminal),
	// and by den or console, "" for gone.
	Result string `json:"result,omitempty"`
	By     string `json:"by,omitempty"`
}

// Settled is how Den settled a card: the verb answered and who answered.
type Settled struct {
	ID   string
	Verb string // allow, deny or always
	By   string // den or console
}

var settledResult = map[string]string{"allow": "allowed", "deny": "denied", "always": "always"}

// CardAnswer is a console's answer for Den to carry out, as the human's
// would be; Den replies on Reply with what happened.
type CardAnswer struct {
	ID      string
	Hash    string
	Verb    string // allow, deny or always
	Console string // the console as Den names it
	Reply   chan CardResult
}

// CardResult is Den's outcome of a CardAnswer: Code "" for done, with
// Result and Note; else a refusal code and its reason in Note.
type CardResult struct {
	Code   string
	Result string
	Note   string
}

// cardNonce is a card.get's nonce, on one connection.
type cardNonce struct {
	id, hash string
	issued   time.Time
}

// cardReads is a connection's cards read: nonces, and when each card's
// text was first fetched (the read timer starts there, not at a re-fetch).
type cardReads struct {
	mu     sync.Mutex
	nonces map[string]cardNonce
	first  map[string]time.Time // id + "\x00" + hash
}

// answers counts a console's answers for its limits.
type answers struct {
	mu     sync.Mutex
	recent []time.Time
}

// allow reports whether one more answer is in the limits, and whether the
// burst means the console is to be unpaired.
func (a *answers) allow(now time.Time) (ok, burst bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var kept []time.Time
	for _, t := range a.recent {
		if now.Sub(t) < time.Minute {
			kept = append(kept, t)
		}
	}
	a.recent = kept
	if len(kept) >= cardAnswersMax {
		return false, false
	}
	a.recent = append(a.recent, now)
	n := len(a.recent)
	if n >= cardBurst {
		fast := true
		for i := n - cardBurst + 1; i < n; i++ {
			if a.recent[i].Sub(a.recent[i-1]) >= time.Second {
				fast = false
			}
		}
		if fast {
			return true, true
		}
	}
	return true, false
}

// consoleOf is the console c proved as, "" for none (or one since gone).
func (s *Server) consoleOf(c *conn) string {
	c.mu.Lock()
	id := c.console
	c.mu.Unlock()
	if id == "" {
		return ""
	}
	cur, ok := s.CurrentConsole()
	if !ok || cur.ID != id {
		return ""
	}
	return id
}

func (s *Server) cardOf(id string) (Card, bool) {
	v := s.view.Load()
	if v == nil {
		return Card{}, false
	}
	card, ok := v.Cards[id]
	return card, ok
}

// cardGet is card.get: the card whole, with a fresh nonce.
func (s *Server) cardGet(c *conn, req Request) {
	if s.consoleOf(c) == "" {
		c.fail(req.ID, CodeForbidden, "prove first")
		return
	}
	var p struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(req.Params, &p) != nil || p.ID == "" {
		c.fail(req.ID, CodeBadRequest, "id")
		return
	}
	card, ok := s.cardOf(p.ID)
	if !ok {
		c.fail(req.ID, CodeNotFound, "no such card")
		return
	}
	now := time.Now()
	nonce := randHex64()
	c.reads.mu.Lock()
	if c.reads.nonces == nil {
		c.reads.nonces, c.reads.first = map[string]cardNonce{}, map[string]time.Time{}
	}
	for n, cn := range c.reads.nonces {
		if now.Sub(cn.issued) >= cardNonceLife {
			delete(c.reads.nonces, n)
		}
	}
	if len(c.reads.nonces) >= cardReadsMax {
		c.reads.nonces = map[string]cardNonce{} // a fetch again is all it costs
	}
	if len(c.reads.first) >= cardReadsMax {
		// Keep the first reads of cards still there: the others are done.
		v := s.view.Load()
		for k := range c.reads.first {
			id, _, _ := strings.Cut(k, "\x00")
			if _, live := v.Cards[id]; !live {
				delete(c.reads.first, k)
			}
		}
	}
	c.reads.nonces[nonce] = cardNonce{id: card.ID, hash: card.Hash, issued: now}
	key := card.ID + "\x00" + card.Hash
	if _, seen := c.reads.first[key]; !seen {
		c.reads.first[key] = now
	}
	c.reads.mu.Unlock()
	out := struct {
		Card
		Nonce string `json:"nonce"`
	}{card, nonce}
	c.reply(req.ID, out)
}

// cardAnswer is card.answer: once per card read, by a verb its kind takes.
func (s *Server) cardAnswer(c *conn, req Request) {
	console := s.consoleOf(c)
	if console == "" {
		c.fail(req.ID, CodeForbidden, "prove first")
		return
	}
	if !s.consolePowers(c) {
		c.fail(req.ID, CodePowersLocked, PowersLocked)
		return
	}
	var p struct {
		ID    string `json:"id"`
		Hash  string `json:"text_sha256"`
		Nonce string `json:"nonce"`
		Verb  string `json:"verb"`
	}
	if json.Unmarshal(req.Params, &p) != nil || p.ID == "" || !isHex(p.Hash, 64) || !isHex(p.Nonce, 64) {
		c.fail(req.ID, CodeBadRequest, "id, text_sha256 and nonce")
		return
	}
	now := time.Now()
	c.reads.mu.Lock()
	cn, ok := c.reads.nonces[p.Nonce]
	delete(c.reads.nonces, p.Nonce) // spent, whatever follows
	first := c.reads.first[p.ID+"\x00"+p.Hash]
	c.reads.mu.Unlock()
	switch {
	case !ok || now.Sub(cn.issued) >= cardNonceLife:
		c.fail(req.ID, CodeNonceExpired, "fetch the card again")
		return
	case cn.id != p.ID || cn.hash != p.Hash:
		c.fail(req.ID, CodeCardChanged, "the nonce is for another card or text")
		return
	}
	card, ok := s.cardOf(p.ID)
	switch {
	case !ok:
		c.fail(req.ID, CodeNotFound, "no such card, or it was answered")
		return
	case card.Hash != p.Hash:
		c.fail(req.ID, CodeCardChanged, "the card changed: fetch it again")
		return
	case !card.DockAnswers || !hasVerb(card.Verbs, p.Verb):
		c.fail(req.ID, CodeRefusedFromDock, "the console may not answer this card that way")
		return
	case strings.Count(card.Body, "\n")+1 > cardLongLines && now.Sub(first) < cardReadAtLeast:
		c.fail(req.ID, CodeReadItFirst, "read the card first")
		return
	}
	ok, burst := s.cardAnswers.allow(now)
	if !ok {
		c.fail(req.ID, CodeUnavailable, "too many answers")
		return
	}
	if burst {
		// Unpaired first, then told: a console that hears it is unpaired
		// never finds it still paired. Its connections close 50 ms after,
		// so the reply still goes out.
		s.UnpairConsole("the console answered cards faster than anyone could read: it is unpaired; pair again with den pair")
		c.fail(req.ID, CodeForbidden, "answers faster than anyone could read: the console is unpaired")
		return
	}
	a := &CardAnswer{ID: p.ID, Hash: p.Hash, Verb: p.Verb, Console: ConsoleLabel, Reply: make(chan CardResult, 1)}
	// The card's settled event, if Den pushes one while it answers, waits
	// for the reply: the reply first, then the event, on this connection.
	c.mu.Lock()
	if c.answering == nil {
		c.answering = map[string][]Event{}
	}
	c.answering[p.ID] = nil
	c.mu.Unlock()
	if !s.act(Act{CardAnswer: a}) {
		c.takeAnswering(p.ID)
		c.fail(req.ID, CodeUnavailable, ClaimBusy)
		return
	}
	go func() {
		defer func() {
			for _, e := range c.takeAnswering(p.ID) {
				c.push(e, true)
			}
		}()
		select {
		case r := <-a.Reply:
			if r.Code != "" {
				c.fail(req.ID, r.Code, r.Note)
				return
			}
			c.reply(req.ID, map[string]any{"ok": true, "result": r.Result, "note": r.Note})
		case <-time.After(10 * time.Second):
			c.fail(req.ID, CodeUnavailable, "Den did not answer in time")
		case <-c.done:
		}
	}()
}

// takeAnswering ends an answer of card id on this connection, and returns
// the events held for it.
func (c *conn) takeAnswering(id string) []Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	q := c.answering[id]
	delete(c.answering, id)
	return q
}

// holdSettled holds e for after the reply when this connection is
// answering its card; false when it is not.
func (c *conn) holdSettled(e CardEvent) bool {
	if e.Change != "settled" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	q, ok := c.answering[e.ID]
	if ok {
		c.answering[e.ID] = append(q, Event{Event: EventCard, Data: e})
	}
	return ok
}

func hasVerb(verbs []string, v string) bool {
	for _, x := range verbs {
		if x == v {
			return true
		}
	}
	return false
}

// cardEvent is a new or changed card's event.
func cardEvent(c Card, change string) CardEvent {
	return CardEvent{ID: c.ID, Kind: c.Kind, Agent: c.Agent, Change: change, Hash: c.Hash, NeedsDenScreen: !c.DockAnswers}
}

// pushCards tells the console's subscribed connections what changed
// between two views' cards.
func (s *Server) pushCards(old, now map[string]Card, settled map[string]Settled) {
	var evs []CardEvent
	for id, c := range now {
		if o, ok := old[id]; !ok {
			evs = append(evs, cardEvent(c, "new"))
		} else if o.Hash != c.Hash {
			evs = append(evs, cardEvent(c, "changed"))
		}
	}
	for id, o := range old {
		if _, ok := now[id]; !ok {
			e := CardEvent{ID: id, Kind: o.Kind, Agent: o.Agent, Change: "settled", Result: "gone"}
			if st, ok := settled[id]; ok {
				e.Result, e.By = settledResult[st.Verb], st.By
			}
			evs = append(evs, e)
		}
	}
	if len(evs) == 0 {
		return
	}
	cur, ok := s.CurrentConsole()
	if !ok {
		return
	}
	for _, c := range s.consoleConns(cur.ID) {
		if !c.subscribed(EventCard) {
			continue
		}
		for _, e := range evs {
			if !c.holdSettled(e) {
				c.push(Event{Event: EventCard, Data: e}, true)
			}
		}
	}
}
