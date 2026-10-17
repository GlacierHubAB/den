package api

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

// provedConsole pairs this process as the console and proves a fresh
// connection, subscribed to its events.
func provedConsole(t *testing.T) (*harness, *client) {
	t.Helper()
	h, _ := consoleHarness(t)
	h.s.Publish(Project(Snapshot{Taken: time.Now(), View: ViewAgent}))
	d := newDock(t, h)
	p := d.pair(h.s.StartConsoleCode())
	c := h.dial()
	c.hello()
	if l, _, _, _ := d.auth(c, p); !l.OK || !strings.Contains(string(l.Result), `"view":"agent"`) {
		t.Fatalf("%+v %s", l.Error, l.Result)
	}
	c.call("console.events", nil)
	skipSnapshot(t, h.s, c)
	return h, c
}

// skipSnapshot reads what Den says first on console.events, where each
// stateful kind stands (the console, each waiting card, its powers, God
// mode, then the mode and the view while Den has published them, and the
// desktop), checks
// its order, and returns the first, console event and the cards.
func skipSnapshot(t *testing.T, s *Server, c *client) (ConsoleEvent, []CardEvent) {
	t.Helper()
	want := []string{EventConsole}
	if v := s.view.Load(); v != nil {
		for range v.Cards {
			want = append(want, EventCard)
		}
	}
	want = append(want, EventConsole, EventGod)
	if v := s.view.Load(); v != nil && v.Mode.Global != "" {
		want = append(want, EventMode)
	}
	if _, ok := s.viewNow(); ok {
		want = append(want, EventView)
	}
	want = append(want, EventDesktop, EventMirror)
	var first ConsoleEvent
	var cards []CardEvent
	for i, kind := range want {
		l := c.next()
		if l.Event != kind {
			t.Fatalf("snapshot event %d is %q, want %q: %s", i, l.Event, kind, l.Data)
		}
		switch {
		case i == 0:
			json.Unmarshal(l.Data, &first)
		case kind == EventCard:
			var ev CardEvent
			json.Unmarshal(l.Data, &ev)
			cards = append(cards, ev)
		}
	}
	return first, cards
}

func card(id string, lines int, dock bool) Card {
	body := make([]string, lines)
	for i := range body {
		body[i] = "line " + strconv.Itoa(i)
	}
	return Card{ID: id, Kind: "permission", Agent: "Otter", Title: "Bash", Body: strings.Join(body, "\n"), Verbs: []string{"allow", "deny", "rm"}, DockAnswers: dock}
}

type got struct {
	Card
	Nonce string `json:"nonce"`
}

func getCard(t *testing.T, c *client, id string) got {
	t.Helper()
	l := c.call("card.get", map[string]string{"id": id})
	var g got
	if !l.OK || json.Unmarshal(l.Result, &g) != nil {
		t.Fatalf("card.get %s: %+v", id, l.Error)
	}
	return g
}

func answerCard(c *client, g got, verb string) int64 {
	return c.send("card.answer", map[string]string{"id": g.ID, "text_sha256": g.Hash, "nonce": g.Nonce, "verb": verb})
}

// Read whole, answered once: the hash covers what the console shows, the
// answer reaches Den, and Den's outcome comes back.
func TestCardGetAndAnswer(t *testing.T) {
	h, c := provedConsole(t)
	h.s.Publish(Project(Snapshot{Taken: time.Now(), Cards: []Card{card("i-1", 2, true)}}))
	var ev CardEvent
	if e := c.next(); e.Event != EventCard || json.Unmarshal(e.Data, &ev) != nil || ev.ID != "i-1" || ev.Change != "new" {
		t.Fatalf("card event %+v", e)
	}
	g := getCard(t, c, "i-1")
	if g.Hash != CardHash("permission", "Otter", "", "Bash", "", "line 0\nline 1") || ev.Hash != g.Hash || !isHex(g.Nonce, 64) {
		t.Errorf("hash %s, event %s, nonce %q", g.Hash, ev.Hash, g.Nonce)
	}
	if len(g.Verbs) != 2 {
		t.Errorf("verbs outside the closed set reached the console: %v", g.Verbs)
	}
	id := answerCard(c, g, "allow")
	a := ctlAct(t, h.s, func(a Act) bool { return a.CardAnswer != nil }).CardAnswer
	if a.ID != "i-1" || a.Verb != "allow" || a.Hash != g.Hash {
		t.Fatalf("answer %+v", a)
	}
	a.Reply <- CardResult{Result: "allowed", Note: "Allowed from test: Otter · Bash"}
	if l := c.replyTo(id); !l.OK || !strings.Contains(string(l.Result), `"result":"allowed"`) {
		t.Errorf("answer reply %+v %s", l.Error, l.Result)
	}
	// The nonce is spent.
	if l := c.call("card.answer", map[string]string{"id": g.ID, "text_sha256": g.Hash, "nonce": g.Nonce, "verb": "allow"}); code(l) != CodeNonceExpired {
		t.Errorf("a nonce used twice: %+v", l)
	}
	h.s.Publish(Project(Snapshot{Taken: time.Now(), Settled: []Settled{{ID: "i-1", Verb: "allow", By: "console"}}}))
	if e := c.next(); json.Unmarshal(e.Data, &ev) != nil || ev.Change != "settled" || ev.Result != "allowed" || ev.By != "console" {
		t.Errorf("settled event %+v", e)
	}
}

// The answer's reply comes before its card's settled event, on the
// connection that answered, even when Den settles the card first.
func TestCardAnswerReplyBeforeSettled(t *testing.T) {
	h, c := provedConsole(t)
	h.s.Publish(Project(Snapshot{Taken: time.Now(), Cards: []Card{card("i-1", 1, true)}}))
	c.next() // new
	g := getCard(t, c, "i-1")
	id := answerCard(c, g, "deny")
	a := ctlAct(t, h.s, func(a Act) bool { return a.CardAnswer != nil }).CardAnswer
	// As Den does: the card settles (a published view), then the reply.
	h.s.Publish(Project(Snapshot{Taken: time.Now(), Settled: []Settled{{ID: "i-1", Verb: "deny", By: "console"}}}))
	a.Reply <- CardResult{Result: "denied", Note: "Denied from test"}
	if l := c.next(); l.ID == nil || *l.ID != id || !l.OK {
		t.Fatalf("first line after the answer: %+v", l)
	}
	var ev CardEvent
	if e := c.next(); e.Event != EventCard || json.Unmarshal(e.Data, &ev) != nil || ev.Change != "settled" || ev.By != "console" {
		t.Errorf("then the settled event: %+v", e)
	}
}

// The console's process may open one connection past its six proved ones,
// to hear console_in_use; another process stays at six.
func TestConsoleConnectionPastTheCap(t *testing.T) {
	h, _ := consoleHarness(t)
	d := newDock(t, h)
	// The claim's connection stays open: it is the first proved one.
	l, first := d.claim(h.s.StartConsoleCode(), true)
	var r struct {
		Pairing string `json:"pairing"`
		DenKey  string `json:"den_public_key"`
	}
	if !l.OK || json.Unmarshal(l.Result, &r) != nil {
		t.Fatalf("console.claim: %+v", l.Error)
	}
	p := consolePaired{ID: r.Pairing, DenKey: r.DenKey}
	time.Sleep(challengeEvery)
	for i := 0; i < consoleConnsMax; i++ {
		c := first
		if i > 0 {
			c = h.dial()
			c.hello()
		}
		if l, _, _, _ := d.auth(c, p); !l.OK {
			t.Fatalf("proof %d: %+v", i+1, l.Error)
		}
	}
	c := h.dial()
	c.hello()
	if l, _, _, _ := d.auth(c, p); code(l) != CodeConsoleInUse {
		t.Errorf("proof %d: %+v", consoleConnsMax+1, l)
	}
}

// A console that subscribes gets the cards already waiting, as new.
func TestConsoleEventsReplayPendingCards(t *testing.T) {
	h, _ := consoleHarness(t)
	h.s.Publish(Project(Snapshot{Taken: time.Now(), Cards: []Card{card("w-1", 1, true)}}))
	d := newDock(t, h)
	p := d.pair(h.s.StartConsoleCode())
	c := h.dial()
	c.hello()
	d.auth(c, p)
	if l := c.call("console.events", nil); !l.OK || string(l.Result) != `{"cards":["w-1"],"ok":true}` {
		t.Errorf("console.events reply %+v %s", l.Error, l.Result)
	}
	_, cards := skipSnapshot(t, h.s, c)
	if len(cards) != 1 || cards[0].ID != "w-1" || cards[0].Change != "new" || cards[0].NeedsDenScreen {
		t.Errorf("no replay of the waiting card: %+v", cards)
	}
}

// A card the console may not answer says it needs Den's own screen.
func TestCardEventNeedsDenScreen(t *testing.T) {
	h, c := provedConsole(t)
	h.s.Publish(Project(Snapshot{Taken: time.Now(), Cards: []Card{card("i-1", 1, false)}}))
	var ev CardEvent
	if e := c.next(); json.Unmarshal(e.Data, &ev) != nil || ev.ID != "i-1" || !ev.NeedsDenScreen {
		t.Errorf("%+v", e)
	}
	if !strings.Contains(string(mustJSON(t, CardEvent{ID: "x", NeedsDenScreen: true})), `"needs_den_screen":true`) {
		t.Error("the field's name")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// What a console may not do with a card.
func TestCardAnswerRefusals(t *testing.T) {
	h, c := provedConsole(t)
	h.s.Publish(Project(Snapshot{Taken: time.Now(), Cards: []Card{card("i-1", 2, true), card("i-2", 2, false), card("i-3", 12, true)}}))
	g := getCard(t, c, "i-1")
	// Changed since it was read.
	h.s.Publish(Project(Snapshot{Taken: time.Now(), Cards: []Card{func() Card { x := card("i-1", 3, true); return x }(), card("i-2", 2, false), card("i-3", 12, true)}}))
	if l := c.replyTo(answerCard(c, g, "allow")); code(l) != CodeCardChanged {
		t.Errorf("a changed card: %+v", l)
	}
	if l := c.replyTo(answerCard(c, getCard(t, c, "i-2"), "allow")); code(l) != CodeRefusedFromDock {
		t.Errorf("a card the console may not answer: %+v", l)
	}
	if l := c.replyTo(answerCard(c, getCard(t, c, "i-1"), "rm")); code(l) != CodeRefusedFromDock {
		t.Errorf("a verb outside the card's: %+v", l)
	}
	long := getCard(t, c, "i-3")
	if l := c.replyTo(answerCard(c, long, "allow")); code(l) != CodeReadItFirst {
		t.Errorf("a long card answered at once: %+v", l)
	}
	time.Sleep(cardReadAtLeast)
	// The timer counts from the first read, not the re-fetch.
	again := answerCard(c, getCard(t, c, "i-3"), "deny")
	a := ctlAct(t, h.s, func(a Act) bool { return a.CardAnswer != nil }).CardAnswer
	a.Reply <- CardResult{Result: "denied"}
	if l := c.replyTo(again); !l.OK {
		t.Errorf("after reading it: %+v", l.Error)
	}
	// Without a proof, nothing.
	other := h.dial()
	other.hello()
	if l := other.call("card.get", map[string]string{"id": "i-1"}); code(l) != CodeForbidden {
		t.Errorf("card.get without a proof: %+v", l)
	}
}

// Answers faster than anyone could read unpair the console.
func TestCardBurstUnpairs(t *testing.T) {
	h, c := provedConsole(t)
	var cards []Card
	for i := 0; i < cardBurst; i++ {
		cards = append(cards, card("b-"+strconv.Itoa(i), 1, true))
	}
	h.s.Publish(Project(Snapshot{Taken: time.Now(), Cards: cards}))
	for range cards {
		c.next() // new
	}
	for i := 0; i < cardBurst; i++ {
		id := answerCard(c, getCard(t, c, "b-"+strconv.Itoa(i)), "allow")
		if i < cardBurst-1 {
			ctlAct(t, h.s, func(a Act) bool { return a.CardAnswer != nil }).CardAnswer.Reply <- CardResult{Result: "allowed"}
			c.replyTo(id)
			continue
		}
		if l := c.replyTo(id); code(l) != CodeForbidden {
			t.Errorf("the burst's last answer: %+v", l)
		}
	}
	if _, ok := h.s.CurrentConsole(); ok {
		t.Error("still paired after a burst")
	}
}
