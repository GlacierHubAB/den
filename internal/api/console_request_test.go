package api

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/sysd"
)

// fakeUnit makes the console's service read as v for the test's own
// process, or fail with err.
func fakeUnit(t *testing.T, v sysd.Verifier, err error) *[]sysd.ProcID {
	t.Helper()
	var asked []sysd.ProcID
	old := readUnit
	readUnit = func(p sysd.ProcID) (sysd.Verifier, error) {
		asked = append(asked, p)
		v.Main = p
		return v, err
	}
	t.Cleanup(func() { readUnit = old })
	return &asked
}

func testVerifier() sysd.Verifier {
	return sysd.Verifier{Unit: ConsoleUnit, Boot: "b", UID: 1000, Exe: sysd.FileID{Path: "/home/u/.local/bin/drey-host", SHA256: strings.Repeat("ab", 32)}, Props: strings.Repeat("cd", 32), Pins: map[string]string{"GTK_MODULES": ""}}
}

// request sends console.request from a new connection and returns its id
// and the card Den raised.
func (d *dock) request(pages string) (*client, int64, *ConsoleAsk) {
	d.t.Helper()
	c := d.h.dial()
	c.hello()
	p := map[string]string{"public_key": d.pub}
	if pages != "" {
		p["pages_sha256"] = pages
	}
	id := c.send("console.request", p)
	ask := ctlAct(d.t, d.h.s, func(a Act) bool { return a.Console != nil }).Console
	return c, id, ask
}

// A code-less request from the console's service raises a card with both
// fingerprints; the human's ^Y pairs it with its powers, records what Den
// read, and the new console proves as any other.
func TestConsoleRequestPairs(t *testing.T) {
	h, _ := consoleHarness(t)
	asked := fakeUnit(t, testVerifier(), nil)
	d := newDock(t, h)
	c, id, ask := d.request(strings.Repeat("ef", 32))
	if !ask.Codeless || ask.Unit != ConsoleUnit || ask.AppFP != Fingerprint(d.pub) || ask.DenFP != Fingerprint(h.s.ckey.pubHex) || ask.Program == "" {
		t.Fatalf("card %+v", ask)
	}
	if len(*asked) != 1 || (*asked)[0].PID != os.Getpid() {
		t.Errorf("the unit was read for %+v", *asked)
	}
	h.s.ConsoleAnswered(ask.ID, true)
	l := c.replyTo(id)
	var r struct {
		Pairing string `json:"pairing"`
		DenKey  string `json:"den_public_key"`
		Console bool   `json:"console"`
		Powers  bool   `json:"powers"`
	}
	if !l.OK || json.Unmarshal(l.Result, &r) != nil || !isHex(r.Pairing, 32) || r.DenKey != h.s.ckey.pubHex || !r.Console || !r.Powers {
		t.Fatalf("reply %+v %s", l.Error, l.Result)
	}
	h.s.con.mu.Lock()
	cur := h.s.con.cur
	h.s.con.mu.Unlock()
	if cur == nil || cur.verifier == nil || !cur.trusted || !cur.powers || cur.PagesSHA != strings.Repeat("ef", 32) {
		t.Fatalf("console %+v", cur)
	}
	pc := h.dial()
	pc.hello()
	if l, _, _, _ := d.auth(pc, consolePaired{ID: r.Pairing, DenKey: r.DenKey}); !l.OK {
		t.Errorf("the requested console does not prove: %+v", l.Error)
	}
}

// Not the console's service, or one that fails a check: refused before
// any card, with what failed.
func TestConsoleRequestRefusedForAnotherProgram(t *testing.T) {
	h, _ := consoleHarness(t)
	fakeUnit(t, testVerifier(), sysd.Problems{"its environment sets LD_PRELOAD"})
	d := newDock(t, h)
	c := h.dial()
	c.hello()
	l := c.call("console.request", map[string]string{"public_key": d.pub})
	if code(l) != CodeForbidden || !strings.Contains(l.Error.Message, "not "+ConsoleUnit) || !strings.Contains(l.Error.Message, "LD_PRELOAD") {
		t.Errorf("reply %+v", l.Error)
	}
	select {
	case a := <-h.s.Acts():
		if a.Console != nil {
			t.Error("a card was raised")
		}
	case <-time.After(50 * time.Millisecond):
	}
	fakeUnit(t, testVerifier(), errors.New("the systemd user manager: no socket"))
	time.Sleep(requestEvery)
	if l := c.call("console.request", map[string]string{"public_key": d.pub}); code(l) != CodeForbidden {
		t.Errorf("an unreadable manager: %+v", l.Error)
	}
}

// One console card at a time, one request a second per connection, a
// cooldown after a deny, and bad params refused.
func TestConsoleRequestLimits(t *testing.T) {
	h, _ := consoleHarness(t)
	fakeUnit(t, testVerifier(), nil)
	d := newDock(t, h)
	_, _, ask := d.request("")
	other := h.dial()
	other.hello()
	if l := other.call("console.request", map[string]string{"public_key": d.pub}); code(l) != CodeUnavailable || l.Error.Message != ClaimBusy {
		t.Errorf("a second card: %+v", l.Error)
	}
	if l := other.call("console.request", map[string]string{"public_key": d.pub}); code(l) != CodeUnavailable || !strings.Contains(l.Error.Message, "a second") {
		t.Errorf("two a second: %+v", l.Error)
	}
	h.s.ConsoleAnswered(ask.ID, false)
	time.Sleep(requestEvery + 50*time.Millisecond)
	if l := other.call("console.request", map[string]string{"public_key": d.pub}); code(l) != CodeUnavailable || l.Error.Message != ClaimCooldown {
		t.Errorf("after a deny: %+v", l.Error)
	}
	for _, bad := range []any{map[string]string{"public_key": "xyz"}, map[string]string{"public_key": d.pub, "pages_sha256": "1"}, "nope"} {
		time.Sleep(requestEvery)
		if l := other.call("console.request", bad); code(l) != CodeBadRequest {
			t.Errorf("%v: %+v", bad, l.Error)
		}
	}
}

// What Den read of the service is kept with the pairing; after a Den
// start it comes back, but never trusted for a silent re-pair.
func TestConsoleVerifierKeptButNotTrustedAfterRestart(t *testing.T) {
	store := filepath.Join(t.TempDir(), "consoles.json")
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	h := storeHarness(t, store, priv)
	fakeUnit(t, testVerifier(), nil)
	d := newDock(t, h)
	c, id, ask := d.request("")
	h.s.ConsoleAnswered(ask.ID, true)
	if l := c.replyTo(id); !l.OK {
		t.Fatalf("reply %+v", l.Error)
	}
	cleanExit(h.s)
	h2 := storeHarness(t, store, priv)
	h2.s.con.mu.Lock()
	cur := h2.s.con.cur
	h2.s.con.mu.Unlock()
	if cur == nil || cur.verifier == nil || cur.verifier.Exe.SHA256 != testVerifier().Exe.SHA256 || cur.trusted {
		t.Fatalf("restored %+v", cur)
	}
}

// The human's answer, an unpair and a code typed into a chat each clear a
// waiting claim's answer channel under the console lock, from another
// goroutine than the one that waits on it: run under -race, a claim and a
// request (a reconnect's card goes the request's way) are answered while
// their waits start.
func TestConsoleAnswerRacesTheWait(t *testing.T) {
	for i := range 8 {
		h, _ := consoleHarness(t)
		fakeUnit(t, testVerifier(), nil)
		d := newDock(t, h)
		answer := func(id string) {
			switch i % 3 {
			case 0:
				go h.s.ConsoleAnswered(id, true)
			case 1:
				go h.s.ConsoleAnswered(id, false)
			default:
				go h.s.UnpairConsole("a test")
			}
		}
		// A claim by code.
		c := h.dial()
		c.hello()
		ch := d.challenge(c)
		words := h.s.StartConsoleCode()
		id := c.send("console.claim", map[string]string{"public_key": d.pub, "proof": proof(words, ch.ClaimSalt, d.pub)})
		answer(ctlAct(t, h.s, func(a Act) bool { return a.Console != nil }).Console.ID)
		c.replyTo(id)
		// A request, with a deny's cooldown cleared.
		h.s.con.mu.Lock()
		h.s.con.cooldown = time.Time{}
		h.s.con.mu.Unlock()
		rc := h.dial()
		rc.hello()
		rid := rc.send("console.request", map[string]string{"public_key": d.pub})
		answer(ctlAct(t, h.s, func(a Act) bool { return a.Console != nil }).Console.ID)
		rc.replyTo(rid)
	}
}

// hello carries Den's console key, so the dock can show its fingerprint
// while the card waits.
func TestHelloCarriesConsoleKey(t *testing.T) {
	h, _ := consoleHarness(t)
	c := h.dial()
	l := c.call("hello", map[string]any{"v": 1})
	var info Info
	if json.Unmarshal(l.Result, &info) != nil || info.ConsolePublicKey != h.s.ckey.pubHex {
		t.Errorf("hello %s", l.Result)
	}
}

func TestFingerprint(t *testing.T) {
	fp := Fingerprint(strings.Repeat("00", 32))
	if len(fp) != 19 || strings.Count(fp, " ") != 3 || fp != Fingerprint(strings.Repeat("00", 32)) {
		t.Errorf("%q", fp)
	}
	if Fingerprint("zz") != "" || Fingerprint(strings.Repeat("00", 31)) != "" {
		t.Error("a bad key has a fingerprint")
	}
}
