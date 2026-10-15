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

// If another claim revoked the pairing while Den was too busy to show it,
// the rollback does not bring the code back: two programs had it.
func TestBusyClaimAfterARaceStaysDead(t *testing.T) {
	h := newHarness(t, 2)
	words := h.s.StartPairing()
	for len(h.s.acts) < cap(h.s.acts) {
		h.s.acts <- Act{}
	}
	defer func(f func(string)) { beforePairAct = f }(beforePairAct)
	beforePairAct = func(id string) { h.s.Unpair(id, "") } // the second claim's revoke
	c := h.dial()
	c.hello()
	if l := c.call("pair.claim", map[string]string{"code": strings.Join(words, " ")}); code(l) != CodeForbidden || l.Error.Message != ClaimRevoked {
		t.Fatalf("claim %+v", l)
	}
	if _, live := h.s.CodeLive(); live {
		t.Error("a raced code came back live")
	}
	h.s.ctl.mu.Lock()
	n := len(h.s.ctl.spent)
	h.s.ctl.mu.Unlock()
	if n != 1 {
		t.Errorf("spent codes %d, want the raced one kept", n)
	}
}

// The same race when the second claim has marked the code dead but not yet
// revoked the pairing: the rollback still leaves the code dead.
func TestBusyClaimAfterAMarkStaysDead(t *testing.T) {
	h := newHarness(t, 2)
	words := h.s.StartPairing()
	for len(h.s.acts) < cap(h.s.acts) {
		h.s.acts <- Act{}
	}
	defer func(f func(string)) { beforePairAct = f }(beforePairAct)
	beforePairAct = func(string) {
		h.s.ctl.mu.Lock()
		for _, sp := range h.s.ctl.spent {
			sp.dead = true // what the second claim does under the lock
		}
		h.s.ctl.mu.Unlock()
	}
	c := h.dial()
	c.hello()
	if l := c.call("pair.claim", map[string]string{"code": strings.Join(words, " ")}); code(l) != CodeForbidden || l.Error.Message != ClaimRevoked {
		t.Fatalf("claim %+v", l)
	}
	if _, live := h.s.CodeLive(); live {
		t.Error("a code marked dead came back live")
	}
}

// Any path that took the code out of the spent list meanwhile (a leak,
// say) leaves it dead, flag or not.
func TestBusyClaimAfterARemovalStaysDead(t *testing.T) {
	h := newHarness(t, 2)
	words := h.s.StartPairing()
	for len(h.s.acts) < cap(h.s.acts) {
		h.s.acts <- Act{}
	}
	defer func(f func(string)) { beforePairAct = f }(beforePairAct)
	beforePairAct = func(string) {
		h.s.ctl.mu.Lock()
		h.s.ctl.spent = nil // removed without the flag
		h.s.ctl.mu.Unlock()
	}
	c := h.dial()
	c.hello()
	if l := c.call("pair.claim", map[string]string{"code": strings.Join(words, " ")}); code(l) != CodeForbidden || l.Error.Message != ClaimRevoked {
		t.Fatalf("claim %+v", l)
	}
	if _, live := h.s.CodeLive(); live {
		t.Error("a code taken out of the list came back live")
	}
}
