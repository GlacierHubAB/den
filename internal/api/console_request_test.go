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
	return sysd.Verifier{Unit: ConsoleUnit, Boot: "b", UID: 1000, Exe: sysd.FileID{Path: "/home/u/.local/bin/console-host", SHA256: strings.Repeat("ab", 32)}, Props: strings.Repeat("cd", 32), Pins: map[string]string{"GTK_MODULES": ""}}
}

// noAct fails if Den raises a console or powers card, or changes the
// console, within a short wait.
func noAct(t *testing.T, s *Server) {
	t.Helper()
	select {
	case a := <-s.Acts():
		if a.Console != nil || a.ConsolePowers != nil || a.ConsoleChanged {
			t.Errorf("an act: %+v", a)
		}
	case <-time.After(50 * time.Millisecond):
	}
}

// noCard fails if Den raises a console or powers card within a short
// wait; other acts pass.
func noCard(t *testing.T, s *Server) {
	t.Helper()
	deadline := time.After(100 * time.Millisecond)
	for {
		select {
		case a := <-s.Acts():
			if a.Console != nil || a.ConsolePowers != nil {
				t.Errorf("a card: %+v", a)
			}
		case <-deadline:
			return
		}
	}
}

// A request from the console's service, read clean, pairs at once with
// its powers and no card: the reply names Den's key, Den records what it
// read, and the new console proves as any other.
func TestConsoleRequestPairsAtOnce(t *testing.T) {
	h := alwaysHarness(t)
	asked := fakeUnit(t, testVerifier(), nil)
	d := newDock(t, h)
	c := h.dial()
	c.hello()
	l := c.call("console.request", map[string]string{"public_key": d.pub, "pages_sha256": strings.Repeat("ef", 32)})
	var r struct {
		Pairing string `json:"pairing"`
		DenKey  string `json:"den_public_key"`
		Console bool   `json:"console"`
		Powers  bool   `json:"powers"`
	}
	if !l.OK || json.Unmarshal(l.Result, &r) != nil || !isHex(r.Pairing, 32) || r.DenKey != h.s.ckey.pubHex || !r.Console || !r.Powers {
		t.Fatalf("reply %+v %s", l.Error, l.Result)
	}
	if len(*asked) != 1 || (*asked)[0].PID != os.Getpid() {
		t.Errorf("the unit was read for %+v", *asked)
	}
	h.s.con.mu.Lock()
	cur := h.s.con.cur
	h.s.con.mu.Unlock()
	if cur == nil || cur.ID != r.Pairing || cur.verifier == nil || !cur.powers || cur.PagesSHA != strings.Repeat("ef", 32) {
		t.Fatalf("console %+v", cur)
	}
	noCard(t, h.s)
	pc := h.dial()
	pc.hello()
	if l, _, _, _ := d.auth(pc, consolePaired{ID: r.Pairing, DenKey: r.DenKey}); !l.OK {
		t.Errorf("the requested console does not prove: %+v", l.Error)
	}
}

// Not the console's service, one that fails a check, or a manager Den
// could not read through every try: refused, with what failed, and
// nothing paired.
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
	fakeUnit(t, testVerifier(), errors.New("the systemd user manager: i/o timeout"))
	time.Sleep(requestEvery)
	if l := c.call("console.request", map[string]string{"public_key": d.pub}); code(l) != CodeForbidden {
		t.Errorf("an unreadable manager: %+v", l.Error)
	}
	if cur, ok := h.s.CurrentConsole(); ok {
		t.Errorf("paired: %+v", cur)
	}
	noAct(t, h.s)
}

// One request a second per connection, none while a claim by code waits
// for the human, and bad params refused.
func TestConsoleRequestLimits(t *testing.T) {
	h, _ := consoleHarness(t)
	fakeUnit(t, testVerifier(), nil)
	d := newDock(t, h)
	cc := h.dial()
	cc.hello()
	ch := d.challenge(cc)
	cc.send("console.claim", map[string]string{"public_key": d.pub, "proof": proof(h.s.StartConsoleCode(), ch.ClaimSalt, d.pub)})
	ctlAct(t, h.s, func(a Act) bool { return a.Console != nil })
	other := h.dial()
	other.hello()
	if l := other.call("console.request", map[string]string{"public_key": d.pub}); code(l) != CodeUnavailable || l.Error.Message != ClaimBusy {
		t.Errorf("while a claim waits: %+v", l.Error)
	}
	if l := other.call("console.request", map[string]string{"public_key": d.pub}); code(l) != CodeUnavailable || !strings.Contains(l.Error.Message, "a second") {
		t.Errorf("two a second: %+v", l.Error)
	}
	for _, bad := range []any{map[string]string{"public_key": "xyz"}, map[string]string{"public_key": d.pub, "pages_sha256": "1"}, "nope"} {
		time.Sleep(requestEvery)
		if l := other.call("console.request", bad); code(l) != CodeBadRequest {
			t.Errorf("%v: %+v", bad, l.Error)
		}
	}
}

// A console restored after a Den start never gets its powers back by
// itself, "always" or not: the record and config.toml are on disk, which
// anything of the user's may rewrite while Den is down. It asks on Den's
// powers card and has none meanwhile, though its service reads clean.
func TestConsoleRestoredAsksEvenWithAlways(t *testing.T) {
	store := filepath.Join(t.TempDir(), "consoles.json")
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	h := alwaysStore(t, store, priv)
	fakeUnit(t, testVerifier(), nil)
	d := newDock(t, h)
	requestPaired(t, h, d, "")
	cleanExit(h.s)
	h2 := alwaysStore(t, store, priv)
	pw := ctlAct(t, h2.s, func(a Act) bool { return a.ConsolePowers != nil }).ConsolePowers
	if pw.Why != "Den restarted" {
		t.Errorf("powers card: %+v", pw)
	}
	if cur, ok := h2.s.CurrentConsole(); !ok || cur.Powers {
		t.Errorf("restored %+v %v", cur, ok)
	}
}

// A restored console's first subscribed connection hears "restored", then
// where its powers stand: without them, "always" or not, until the ^Y.
func TestRestoredConsoleHearsItsPowers(t *testing.T) {
	for _, always := range []bool{true, false} {
		store := filepath.Join(t.TempDir(), "consoles.json")
		_, priv, _ := ed25519.GenerateKey(rand.Reader)
		harness := storeHarness
		if always {
			harness = alwaysStore
		}
		h := harness(t, store, priv)
		fakeUnit(t, testVerifier(), nil)
		d := newDock(t, h)
		p := requestPaired(t, h, d, "")
		cleanExit(h.s)
		h2 := harness(t, store, priv)
		ctlAct(t, h2.s, func(a Act) bool { return a.ConsolePowers != nil })
		d.h = h2
		c := h2.dial()
		c.hello()
		if l, _, _, _ := d.auth(c, p); !l.OK {
			t.Fatalf("auth %+v", l.Error)
		}
		c.call("console.events", nil)
		var ev ConsoleEvent
		if e := c.next(); json.Unmarshal(e.Data, &ev) != nil || ev.Kind != "restored" {
			t.Fatalf("first event %+v", ev)
		}
		if e := c.next(); json.Unmarshal(e.Data, &ev) != nil || ev.Kind != "powers" || ev.On == nil || *ev.On {
			t.Errorf("always %v: then %+v", always, ev)
		}
	}
}

// The human's answer, an unpair and a code typed into a chat each clear a
// waiting claim's answer channel under the console lock, from another
// goroutine than the one that waits on it: run under -race, a claim is
// answered while its wait starts.
func TestConsoleAnswerRacesTheWait(t *testing.T) {
	for i := range 8 {
		h, _ := consoleHarness(t)
		d := newDock(t, h)
		answer := func(id string) {
			switch i % 3 {
			case 0:
				go h.s.ConsoleAnswered(id, true)
			case 1:
				go h.s.ConsoleAnswered(id, false)
			default:
				go h.s.UnpairConsole(CauseDen, "a test")
			}
		}
		c := h.dial()
		c.hello()
		ch := d.challenge(c)
		words := h.s.StartConsoleCode()
		id := c.send("console.claim", map[string]string{"public_key": d.pub, "proof": proof(words, ch.ClaimSalt, d.pub)})
		answer(ctlAct(t, h.s, func(a Act) bool { return a.Console != nil }).Console.ID)
		c.replyTo(id)
	}
}

// hello carries Den's console key, so the dock can check the key a
// request's reply names.
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
