package api

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GlacierHubAB/den/internal/inbox"
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
	// CodeHashVersion refuses an answer whose text_sha256 is the card's
	// hash in the other version than the connection's: final, the console
	// must not fetch again and retry.
	CodeHashVersion = "hash_version"
)

// Card is one card as Den shows it, for a console. Den fills Snapshot.Cards
// with what it would draw; the text is the lines it shows, sanitised.
type Card struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Agent string `json:"agent"`
	// AgentID is the agent's id, "" for a card from a host or from Den:
	// Agent is only its name. Not in the card's hash.
	AgentID string `json:"agent_id,omitempty"`
	Tool    string `json:"tool"` // the tool asked for, "" for none
	Title   string `json:"title"`
	// Note is a warning Den shows with the card (two agents share the
	// name), "" for none.
	Note string `json:"note"`
	// Class and Step are what a console's "allow" on it does, as Den
	// classed it from its own record (inbox.Classify): in card hash v2,
	// never taken from a console.
	Class string `json:"class"`
	Step  int    `json:"step"`
	Body  string `json:"body"`
	// Hash is the card's text_sha256 as the connection reads it: v1 as Den
	// keeps it, v2 for a connection that chose it in hello. HashV2 is the
	// v2 hash, "" when the hasher refused the card's fields.
	Hash        string   `json:"text_sha256"`
	HashV2      string   `json:"-"`
	Truncated   bool     `json:"truncated"`
	CutAt       int      `json:"cut_at,omitempty"`
	Verbs       []string `json:"verbs"`
	DockAnswers bool     `json:"dock_answers"`
	// TTYOnly: while God mode is on, only the human answers it, in Den's
	// own window; card.answer says tty_only rather than refused_from_dock.
	TTYOnly bool `json:"-"`
	// Started: an answer started the card's action and it runs. No one
	// answers it (no verbs, dock_answers false) and its text is as it was
	// when it started; it settles once, when the action ends. StartedBy
	// is who answered: den or console. Neither is in the card's hash.
	Started   bool   `json:"started,omitempty"`
	StartedBy string `json:"by,omitempty"`
}

// CardHash is a card's text_sha256: over kind, agent, tool, title, note
// and body, in that order, one per line (none but the body holds a
// newline).
func CardHash(kind, agent, tool, title, note, body string) string {
	return sha256Hex([]byte(kind + "\n" + agent + "\n" + tool + "\n" + title + "\n" + note + "\n" + body))
}

// Criterion is one done criterion of a "mission done" card, as card hash
// v2 binds it: its id, state, text and evidence ("" when left out).
type Criterion struct {
	ID       string `json:"id"`
	State    string `json:"state"`
	Text     string `json:"text"`
	Evidence string `json:"evidence"`
}

// errCardHashV2 is CardHashV2's refusal.
var errCardHashV2 = errors.New("card hash v2: a field breaks the rules")

// CardHashV2 is a card's text_sha256 in version 2: over kind, agent, tool,
// title, note, class, step, mission, criteria and body, in that order, one
// per line, the body last for every kind. step is a bare decimal; mission
// and criteria are "" on a card without them. Each criterion is its id,
// state, text and evidence joined by U+001F, the criteria joined by
// U+001E in card order. It refuses a class outside the set, a step that
// is not one of its class's, a newline in any field before the body
// (mission included), and either separator, or a newline, inside a
// criterion's part: no two different cards share a text.
func CardHashV2(kind, agent, tool, title, note, class string, step int, mission string, criteria []Criterion, body string) (string, error) {
	if !inbox.StepFits(inbox.Class(class), step) {
		return "", errCardHashV2
	}
	for _, f := range []string{kind, agent, tool, title, note, mission} {
		if strings.Contains(f, "\n") {
			return "", errCardHashV2
		}
	}
	rows := make([]string, len(criteria))
	for i, c := range criteria {
		for _, f := range []string{c.ID, c.State, c.Text, c.Evidence} {
			if strings.ContainsAny(f, "\n\x1e\x1f") {
				return "", errCardHashV2
			}
		}
		rows[i] = c.ID + "\x1f" + c.State + "\x1f" + c.Text + "\x1f" + c.Evidence
	}
	text := strings.Join([]string{kind, agent, tool, title, note, class, strconv.Itoa(step), mission, strings.Join(rows, "\x1e"), body}, "\n")
	return sha256Hex([]byte(text)), nil
}

// forVersion is the card as a connection on card hash version v reads it:
// its text_sha256 in that version. A card with no v2 hash is not one a v2
// connection may answer.
func (c Card) forVersion(v int) Card {
	if v == 2 {
		c.Hash = c.HashV2
		if c.Hash == "" {
			c.DockAnswers = false
		}
	}
	return c
}

// HashIn is the card's text_sha256 in card hash version v, for Den to
// check an answer against the card as it would hand it over now.
func (c Card) HashIn(v int) string { return c.forVersion(v).Hash }

// otherHash is the card's text_sha256 in the version a connection on v
// does not read, "" for none.
func (c Card) otherHash(v int) string {
	if v == 2 {
		return c.Hash
	}
	return c.HashV2
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
	// and by den, console or rule (God mode answered it), "" for gone.
	Result string `json:"result,omitempty"`
	By     string `json:"by,omitempty"`
	// Started is set on the changed event of a card whose action an
	// answer started, with By; it never goes back. Outcome is on the
	// settled event of such a card only: done, failed or lost.
	Started bool   `json:"started,omitempty"`
	Outcome string `json:"outcome,omitempty"`
}

// Settled is how Den settled a card: the verb answered and who answered,
// and for a card whose action an answer started, how it ended.
type Settled struct {
	ID      string
	Verb    string // allow, deny or always
	By      string // den or console
	Outcome string // done, failed or lost; "" for a card never started
}

var settledResult = map[string]string{"allow": "allowed", "deny": "denied", "always": "always"}

// CardAnswer is a console's answer for Den to carry out, as the human's
// would be; Den replies on Reply with what happened.
type CardAnswer struct {
	ID      string
	Hash    string
	Version int    // the card hash version Hash is in: 1 or 2
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
	// Class is below_level's: the class set below the card's step.
	Class string
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
	card = card.forVersion(c.cardVersion())
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
	if card, live := s.cardOf(p.ID); live && card.Started {
		// Answered already, and its action runs: refused before any
		// check of hash or nonce, since started is in neither.
		c.fail(req.ID, CodeNotFound, "it was answered, and its action runs")
		return
	}
	ver := c.cardVersion()
	if card, live := s.cardOf(p.ID); live && card.otherHash(ver) == p.Hash {
		c.fail(req.ID, CodeHashVersion, "this connection uses card hash v"+strconv.Itoa(ver))
		return
	}
	switch {
	case !ok || now.Sub(cn.issued) >= cardNonceLife:
		c.fail(req.ID, CodeNonceExpired, "fetch the card again")
		return
	case cn.id != p.ID || cn.hash != p.Hash:
		c.fail(req.ID, CodeCardChanged, "the nonce is for another card or text")
		return
	case ver != 2:
		// The levels judge the class and step inside card hash v2 only.
		c.fail(req.ID, CodeHashVersion, "answers need card hash v2: say card_hash 2 in hello")
		return
	}
	card, ok := s.cardOf(p.ID)
	card = card.forVersion(ver)
	switch {
	case !ok:
		c.fail(req.ID, CodeNotFound, "no such card, or it was answered")
		return
	case card.Hash != p.Hash:
		c.fail(req.ID, CodeCardChanged, "the card changed: fetch it again")
		return
	case card.TTYOnly:
		c.fail(req.ID, CodeTTYOnly, "while God mode is on, only Den's own window answers this card")
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
		s.UnpairConsole(CauseBurst, "the console answered cards faster than anyone could read: it is unpaired; pair again with den pair")
		c.fail(req.ID, CodeForbidden, "answers faster than anyone could read: the console is unpaired")
		return
	}
	a := &CardAnswer{ID: p.ID, Hash: p.Hash, Version: ver, Verb: p.Verb, Console: ConsoleLabel, Reply: make(chan CardResult, 1)}
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
			if r.Code != "" && r.Class != "" {
				c.failData(req.ID, r.Code, r.Note, map[string]string{"class": r.Class})
				return
			}
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

// settledEvent is the settled event of card o, which left the cards: by
// st when Den settled it by an answer (ok), else gone.
func settledEvent(o Card, st Settled, ok bool) CardEvent {
	e := CardEvent{ID: o.ID, Kind: o.Kind, Agent: o.Agent, Change: "settled", Result: "gone"}
	if ok {
		e.Result, e.By, e.Outcome = settledResult[st.Verb], st.By, st.Outcome
	}
	return e
}

// cardEvent is a new or changed card's event, for a connection on card
// hash version v.
func cardEvent(c Card, change string, v int) CardEvent {
	c = c.forVersion(v)
	if c.Started {
		return CardEvent{ID: c.ID, Kind: c.Kind, Agent: c.Agent, Change: change, Hash: c.Hash, Started: true, By: c.StartedBy}
	}
	return CardEvent{ID: c.ID, Kind: c.Kind, Agent: c.Agent, Change: change, Hash: c.Hash, NeedsDenScreen: !c.DockAnswers}
}

// pushCards tells the console's subscribed connections what changed
// between two views' cards, each in the card hash version it reads: a
// card is changed for it when its hash in that version changed, or what
// it lists (dock_answers and the verbs, which follow the levels: not in
// the hash) changed, so the console reads it again.
func (s *Server) pushCards(old, now map[string]Card, settled map[string]Settled) {
	events := func(v int) []CardEvent {
		var evs []CardEvent
		for id, c := range now {
			if o, ok := old[id]; !ok {
				evs = append(evs, cardEvent(c, "new", v))
			} else if o.HashIn(v) != c.HashIn(v) || c.Started && !o.Started || !sameListing(o, c) {
				evs = append(evs, cardEvent(c, "changed", v))
			}
		}
		for id, o := range old {
			if _, ok := now[id]; !ok {
				st, ok := settled[id]
				evs = append(evs, settledEvent(o, st, ok))
			}
		}
		return evs
	}
	byVersion := map[int][]CardEvent{1: events(1), 2: events(2)}
	if len(byVersion[1]) == 0 && len(byVersion[2]) == 0 {
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
		for _, e := range byVersion[c.cardVersion()] {
			if !c.holdSettled(e) {
				c.push(Event{Event: EventCard, Data: e}, true)
			}
		}
	}
}

// sameListing reports whether two views of a card list the same answers:
// whether the dock answers it, and with which verbs.
func sameListing(a, b Card) bool {
	if a.DockAnswers != b.DockAnswers || len(a.Verbs) != len(b.Verbs) {
		return false
	}
	for i := range a.Verbs {
		if a.Verbs[i] != b.Verbs[i] {
			return false
		}
	}
	return true
}
