package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// provedConsoleV2 is provedConsole on a connection that chose card hash v2
// in hello.
func provedConsoleV2(t *testing.T) (*harness, *client) {
	t.Helper()
	h, _ := consoleHarness(t)
	h.s.Publish(Project(Snapshot{Taken: time.Now(), View: ViewAgent}))
	d := newDock(t, h)
	p := d.pair(h.s.StartConsoleCode())
	c := h.dial()
	if l := c.call("hello", map[string]any{"v": 1, "client": "test", "version": "0", "card_hash": 2}); !l.OK {
		t.Fatalf("hello: %+v", l.Error)
	}
	if l, _, _, _ := d.auth(c, p); !l.OK {
		t.Fatalf("%+v %s", l.Error, l.Result)
	}
	c.call("console.events", nil)
	skipSnapshot(t, h.s, c)
	return h, c
}

// classed is a card Den classed code 1.
func classed(id string) Card {
	c := card(id, 2, true)
	c.Class, c.Step = "code", 1
	return c
}

// A v2 connection reads v2 hashes everywhere (the event, card.get), with
// the class and step; an answer with the card's v1 hash is refused
// hash_version and spends the nonce; one with its v2 hash reaches Den.
func TestCardHashV2Connection(t *testing.T) {
	h, c := provedConsoleV2(t)
	h.s.Publish(Project(Snapshot{Taken: time.Now(), Cards: []Card{classed("i-1")}}))
	want, err := CardHashV2("permission", "Otter", "", "Bash", "", "code", 1, "", nil, "line 0\nline 1")
	if err != nil {
		t.Fatal(err)
	}
	v1 := CardHash("permission", "Otter", "", "Bash", "", "line 0\nline 1")
	var ev CardEvent
	if e := c.next(); e.Event != EventCard || json.Unmarshal(e.Data, &ev) != nil || ev.Hash != want {
		t.Fatalf("card event %s, want hash %s", e.Data, want)
	}
	l := c.call("card.get", map[string]string{"id": "i-1"})
	if !l.OK || !strings.Contains(string(l.Result), `"class":"code"`) || !strings.Contains(string(l.Result), `"step":1`) {
		t.Fatalf("card.get %+v %s", l.Error, l.Result)
	}
	var g got
	json.Unmarshal(l.Result, &g)
	if g.Hash != want {
		t.Fatalf("card.get hash %s, want v2 %s", g.Hash, want)
	}
	if l := c.call("card.answer", map[string]string{"id": "i-1", "text_sha256": v1, "nonce": g.Nonce, "verb": "allow"}); code(l) != CodeHashVersion || !strings.Contains(l.Error.Message, "v2") {
		t.Fatalf("an answer with the v1 hash: %+v", l)
	}
	if l := c.call("card.answer", map[string]string{"id": "i-1", "text_sha256": want, "nonce": g.Nonce, "verb": "allow"}); code(l) != CodeNonceExpired {
		t.Errorf("the nonce after hash_version: %+v", l)
	}
	g = getCard(t, c, "i-1")
	id := answerCard(c, g, "allow")
	a := ctlAct(t, h.s, func(a Act) bool { return a.CardAnswer != nil }).CardAnswer
	if a.Hash != want || a.Version != 2 {
		t.Fatalf("answer %+v", a)
	}
	a.Reply <- CardResult{Result: "allowed"}
	if l := c.replyTo(id); !l.OK {
		t.Errorf("answer reply %+v", l.Error)
	}
}

// provedConsoleV1 is provedConsole on a connection that kept card hash v1
// (no card_hash in hello).
func provedConsoleV1(t *testing.T) (*harness, *client) {
	t.Helper()
	h, _ := consoleHarness(t)
	h.s.Publish(Project(Snapshot{Taken: time.Now(), View: ViewAgent}))
	d := newDock(t, h)
	p := d.pair(h.s.StartConsoleCode())
	c := h.dial()
	c.hello()
	if l, _, _, _ := d.auth(c, p); !l.OK {
		t.Fatalf("%+v %s", l.Error, l.Result)
	}
	c.call("console.events", nil)
	skipSnapshot(t, h.s, c)
	return h, c
}

// A v1 connection reads v1 as before, but every answer on it, with the
// card's v1 hash or its v2 one, is refused hash_version and spends the
// nonce: the levels judge the class and step inside the v2 hash only.
func TestCardHashV1AnswersAreRefused(t *testing.T) {
	h, c := provedConsoleV1(t)
	h.s.Publish(Project(Snapshot{Taken: time.Now(), Cards: []Card{classed("i-1")}}))
	c.next() // the card event
	v1 := CardHash("permission", "Otter", "", "Bash", "", "line 0\nline 1")
	v2, _ := CardHashV2("permission", "Otter", "", "Bash", "", "code", 1, "", nil, "line 0\nline 1")
	for _, hash := range []string{v1, v2} {
		g := getCard(t, c, "i-1")
		if g.Hash != v1 {
			t.Fatalf("a v1 connection read %s", g.Hash)
		}
		if l := c.call("card.answer", map[string]string{"id": "i-1", "text_sha256": hash, "nonce": g.Nonce, "verb": "allow"}); code(l) != CodeHashVersion {
			t.Errorf("an answer on a v1 connection: %+v", l)
		}
		if l := c.call("card.answer", map[string]string{"id": "i-1", "text_sha256": hash, "nonce": g.Nonce, "verb": "allow"}); code(l) != CodeNonceExpired && code(l) != CodeHashVersion {
			t.Errorf("the nonce again: %+v", l)
		}
	}
	select {
	case a := <-h.s.Acts():
		if a.CardAnswer != nil {
			t.Errorf("an answer on a v1 connection reached Den: %+v", a.CardAnswer)
		}
	default:
	}
}

// A card whose class or step changes, its text the same, is changed for
// a v2 connection (its v2 hash moved), and a v2 answer with the old hash
// is card_changed.
func TestCardClassChangeIsAChangeInV2(t *testing.T) {
	h, c := provedConsoleV2(t)
	h.s.Publish(Project(Snapshot{Taken: time.Now(), Cards: []Card{classed("i-1")}}))
	c.next()
	g := getCard(t, c, "i-1")
	moved := classed("i-1")
	moved.Class, moved.Step = "den_only", 0
	h.s.Publish(Project(Snapshot{Taken: time.Now(), Cards: []Card{moved}}))
	var ev CardEvent
	if e := c.next(); json.Unmarshal(e.Data, &ev) != nil || ev.Change != "changed" || ev.Hash == g.Hash {
		t.Fatalf("after the class moved: %s", e.Data)
	}
	if l := c.call("card.answer", map[string]string{"id": "i-1", "text_sha256": g.Hash, "nonce": g.Nonce, "verb": "allow"}); code(l) != CodeCardChanged {
		t.Errorf("an answer with the hash read before: %+v", l)
	}
}

// card_hash is the integer 1 or 2, or absent, chosen once per connection:
// a string, 0, null or any other value is refused, never read as v1. A
// refused hello chooses nothing, so the same connection may still choose.
func TestHelloCardHash(t *testing.T) {
	h := newHarness(t, 0)
	c := h.dial()
	for _, bad := range []any{3, -1, 0, "2", "1", 2.5, nil, true} {
		if l := c.call("hello", map[string]any{"v": 1, "client": "test", "version": "0", "card_hash": bad}); code(l) != CodeBadRequest {
			t.Errorf("card_hash %v: %+v", bad, l)
		}
	}
	if l := c.call("hello", map[string]any{"v": 1, "client": "test", "version": "0", "card_hash": 2}); !l.OK {
		t.Fatalf("card_hash 2: %+v", l.Error)
	}
	if l := c.call("hello", map[string]any{"v": 1, "client": "test", "version": "0", "card_hash": 2}); !l.OK {
		t.Errorf("the same card_hash again: %+v", l.Error)
	}
	if l := c.call("hello", map[string]any{"v": 1, "client": "test", "version": "0"}); code(l) != CodeBadRequest {
		t.Errorf("a second hello back to v1: %+v", l)
	}
}
