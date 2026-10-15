package api

import (
	"strings"
	"testing"
	"time"
)

// A new /api pair does not forget the code claimed before it: that code
// again revokes the pairing it made, while the new code pairs as usual.
func TestSpentCodeOutlivesARepair(t *testing.T) {
	h := newHarness(t, 2)
	words := h.s.StartPairing()
	c := h.dial()
	c.hello()
	id := c.send("pair.claim", map[string]string{"code": strings.Join(words, " ")})
	first := ctlAct(t, h.s, func(a Act) bool { return a.Pair != nil }).Pair
	h.s.PairAnswered(first.ID, true)
	if l := c.replyTo(id); !l.OK {
		t.Fatalf("claim %+v", l.Error)
	}
	fresh := h.s.StartPairing()
	ok := h.dial()
	ok.hello()
	id2 := ok.send("pair.claim", map[string]string{"code": strings.Join(fresh, " ")})
	second := ctlAct(t, h.s, func(a Act) bool { return a.Pair != nil }).Pair
	h.s.PairAnswered(second.ID, true)
	if l := ok.replyTo(id2); !l.OK {
		t.Fatalf("the new code: %+v", l.Error)
	}
	thief := h.dial()
	thief.hello()
	if l := thief.call("pair.claim", map[string]string{"code": strings.Join(words, " ")}); code(l) != CodeForbidden || l.Error.Message != ClaimSpent {
		t.Errorf("the old code again: %+v", l)
	}
	left := map[string]bool{}
	for _, p := range h.s.Pairings() {
		left[p.ID] = true
	}
	if left[first.ID] {
		t.Error("the pairing the old code made survived its reuse")
	}
}

// The code that made a pairing, typed into a chat, revokes that pairing.
func TestSpentCodeLeakRevokes(t *testing.T) {
	h := newHarness(t, 2)
	words := h.s.StartPairing()
	c := h.dial()
	c.hello()
	id := c.send("pair.claim", map[string]string{"code": strings.Join(words, " ")})
	ask := ctlAct(t, h.s, func(a Act) bool { return a.Pair != nil }).Pair
	h.s.PairAnswered(ask.ID, true)
	c.replyTo(id)
	if l := c.call("ask", map[string]string{"text": strings.Join(words[2:], " ")}); code(l) != CodeBadRequest {
		t.Errorf("ask %+v", l)
	}
	if h.s.Paired() {
		t.Error("the pairing survived its code's leak")
	}
}

// A claim waiting for the human is answered at once when its pairing is
// revoked, and says so.
func TestPendingClaimWokenOnRevoke(t *testing.T) {
	h := newHarness(t, 2)
	words := h.s.StartPairing()
	c := h.dial()
	c.hello()
	id := c.send("pair.claim", map[string]string{"code": strings.Join(words, " ")})
	ctlAct(t, h.s, func(a Act) bool { return a.Pair != nil })
	thief := h.dial()
	thief.hello()
	thief.call("pair.claim", map[string]string{"code": "anything"})
	start := time.Now()
	if l := c.replyTo(id); code(l) != CodeForbidden || l.Error.Message != ClaimRevoked {
		t.Errorf("the waiting claim: %+v", l)
	}
	if time.Since(start) > time.Second {
		t.Error("the waiting claim was not woken")
	}
}
