package api

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/sysd"
)

// claimAsk sends d's console.claim with words and returns the connection,
// the request's id and the card Den raised.
func (d *dock) claimAsk(words []string) (*client, int64, *ConsoleAsk) {
	d.t.Helper()
	c := d.h.dial()
	c.hello()
	ch := d.challenge(c)
	id := c.send("console.claim", map[string]string{"public_key": d.pub, "proof": proof(words, ch.ClaimSalt, d.pub)})
	return c, id, ctlAct(d.t, d.h.s, func(a Act) bool { return a.Console != nil }).Console
}

// A claim by code whose process reads clean as the console's service
// keeps that read: the card says the dock may rejoin, and after a restart
// it re-pairs with no code and no powers, as one paired with none.
func TestClaimReadCleanMayRejoin(t *testing.T) {
	h, _ := consoleHarness(t)
	asked := fakeUnit(t, testVerifier(), nil)
	d := newDock(t, h)
	c, id, ask := d.claimAsk(h.s.StartConsoleCode())
	if !ask.Rejoins || ask.Unit != ConsoleUnit || ask.Codeless {
		t.Errorf("card %+v", ask)
	}
	start, _, _ := procStat(os.Getpid())
	if len(*asked) != 1 || (*asked)[0] != (sysd.ProcID{PID: os.Getpid(), Start: start}) {
		t.Errorf("read for %v", *asked)
	}
	h.s.ConsoleAnswered(ask.ID, true)
	if l := c.replyTo(id); !l.OK {
		t.Fatalf("claim: %+v", l.Error)
	}
	d2 := d.restarted()
	rc, rid := reconnect(t, h, d2, "")
	l := rc.replyTo(rid)
	var r reconnectReply
	if !l.OK || json.Unmarshal(l.Result, &r) != nil || !r.verified(h, d2) || r.Powers {
		t.Fatalf("reconnect %+v %s", l.Error, l.Result)
	}
	ctlAct(t, h.s, func(a Act) bool { return a.ConsolePowers != nil })
}

// A claim whose process does not read clean pairs as a code always did:
// no word of it on the card, no strike, and a restart pairs again.
func TestClaimNotCleanPairsAsBefore(t *testing.T) {
	h, _ := consoleHarness(t)
	fakeUnit(t, testVerifier(), sysd.Problems{`its Type is "forking", not simple or exec`})
	d := newDock(t, h)
	c, id, ask := d.claimAsk(h.s.StartConsoleCode())
	if ask.Rejoins || ask.Unit != "" {
		t.Errorf("card %+v", ask)
	}
	h.s.ConsoleAnswered(ask.ID, true)
	if l := c.replyTo(id); !l.OK {
		t.Fatalf("claim: %+v", l.Error)
	}
	h.s.con.mu.Lock()
	strikes, verifier := len(h.s.con.cur.strikes), h.s.con.cur.verifier
	h.s.con.mu.Unlock()
	if strikes != 0 || verifier != nil {
		t.Errorf("strikes %d, verifier %v", strikes, verifier)
	}
	rc, rid := reconnect(t, h, d.restarted(), "")
	if l := rc.replyTo(rid); code(l) != CodeForbidden || l.Error.Message != "pair again: there is no pairing to reconnect to" {
		t.Errorf("reconnect %+v", l.Error)
	}
}

// A clean read is kept only with the human's ^Y: a claim denied in Den,
// or one whose card is not answered in time, leaves no read in memory or
// in the record.
func TestClaimReadKeptOnlyWithTheYes(t *testing.T) {
	defer func(d time.Duration) { pairConfirmLife = d }(pairConfirmLife)
	pairConfirmLife = 300 * time.Millisecond
	for _, answer := range []string{"denied", "unanswered"} {
		store := filepath.Join(t.TempDir(), "consoles.json")
		_, priv, _ := ed25519.GenerateKey(rand.Reader)
		h := storeHarness(t, store, priv)
		fakeUnit(t, testVerifier(), nil)
		d := newDock(t, h)
		c, id, ask := d.claimAsk(h.s.StartConsoleCode())
		if !ask.Rejoins {
			t.Fatalf("%s: card %+v", answer, ask)
		}
		if answer == "denied" {
			h.s.ConsoleAnswered(ask.ID, false)
		}
		if l := c.replyTo(id); l.OK {
			t.Fatalf("%s: paired", answer)
		}
		h.s.con.mu.Lock()
		cur, pending := h.s.con.cur, h.s.con.pending
		h.s.con.mu.Unlock()
		if cur != nil || pending != nil {
			t.Errorf("%s: console %v, waiting %v", answer, cur, pending)
		}
		if raw, err := os.ReadFile(store); err == nil && strings.Contains(string(raw), "verifier") {
			t.Errorf("%s: the record keeps a read: %s", answer, raw)
		}
	}
}

// Den reads nothing for a claim that does not know the code.
func TestClaimWrongCodeReadsNothing(t *testing.T) {
	h, _ := consoleHarness(t)
	asked := fakeUnit(t, testVerifier(), nil)
	d := newDock(t, h)
	h.s.StartConsoleCode()
	c := h.dial()
	c.hello()
	ch := d.challenge(c)
	l := c.call("console.claim", map[string]string{"public_key": d.pub, "proof": proof([]string{"not", "the", "code"}, ch.ClaimSalt, d.pub)})
	if code(l) != CodeForbidden || l.Error.Message != ClaimWrong {
		t.Errorf("claim %+v", l.Error)
	}
	if len(*asked) != 0 {
		t.Errorf("read for %v", *asked)
	}
}

// A code that runs out while Den reads the service raises no card.
func TestClaimCodeRunsOutDuringTheRead(t *testing.T) {
	h, _ := consoleHarness(t)
	defer func(d time.Duration) { pairCodeLife = d }(pairCodeLife)
	pairCodeLife = 300 * time.Millisecond
	old := readUnit
	readUnit = func(p sysd.ProcID) (sysd.Verifier, error) {
		time.Sleep(500 * time.Millisecond)
		v := testVerifier()
		v.Main = p
		return v, nil
	}
	t.Cleanup(func() { readUnit = old })
	d := newDock(t, h)
	words := h.s.StartConsoleCode()
	c := h.dial()
	c.hello()
	ch := d.challenge(c)
	l := c.call("console.claim", map[string]string{"public_key": d.pub, "proof": proof(words, ch.ClaimSalt, d.pub)})
	if code(l) != CodeForbidden || l.Error.Message != ClaimNoCode {
		t.Errorf("claim %+v", l.Error)
	}
	for {
		select {
		case a := <-h.s.Acts():
			if a.Console != nil {
				t.Fatalf("a card: %+v", a.Console)
			}
			continue
		case <-time.After(200 * time.Millisecond):
		}
		break
	}
	h.s.con.mu.Lock()
	defer h.s.con.mu.Unlock()
	if h.s.con.pending != nil || h.s.con.cur != nil {
		t.Error("a claim waits or paired")
	}
}
