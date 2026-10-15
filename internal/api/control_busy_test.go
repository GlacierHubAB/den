package api

import (
	"strings"
	"testing"
)

// A claim Den is too busy to put to the human did not happen: the code is
// still live and unspent, and the same claim may be sent again.
func TestBusyClaimDoesNotCount(t *testing.T) {
	h := newHarness(t, 2)
	words := h.s.StartPairing()
	for len(h.s.acts) < cap(h.s.acts) {
		h.s.acts <- Act{}
	}
	c := h.dial()
	c.hello()
	if l := c.call("pair.claim", map[string]string{"code": strings.Join(words, " ")}); code(l) != CodeUnavailable || l.Error.Message != ClaimBusy {
		t.Fatalf("a claim with Den's queue full: %+v", l)
	}
	if _, live := h.s.CodeLive(); !live || len(h.s.Pairings()) != 0 {
		t.Fatalf("live %v, pairings %+v", live, h.s.Pairings())
	}
	for len(h.s.acts) > 0 {
		<-h.s.acts
	}
	id := c.send("pair.claim", map[string]string{"code": strings.Join(words, " ")})
	ask := ctlAct(t, h.s, func(a Act) bool { return a.Pair != nil }).Pair
	h.s.PairAnswered(ask.ID, true)
	if l := c.replyTo(id); !l.OK {
		t.Errorf("the same claim again: %+v", l.Error)
	}
}
