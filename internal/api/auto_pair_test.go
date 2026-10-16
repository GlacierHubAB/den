package api

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// requestLine sends d's console.request from a new connection and returns
// the reply.
func (d *dock) requestLine() line {
	d.t.Helper()
	c := d.h.dial()
	c.hello()
	return c.call("console.request", map[string]string{"public_key": d.pub})
}

// requestPowers pairs d by console.request and returns whether it came
// with its powers.
func (d *dock) requestPowers() bool {
	d.t.Helper()
	l := d.requestLine()
	var r struct {
		Powers bool `json:"powers"`
	}
	if !l.OK || json.Unmarshal(l.Result, &r) != nil {
		d.t.Fatalf("request: %+v", l.Error)
	}
	return r.Powers
}

// A2: an unpair by the human, the console itself, another program's
// unpair of all, Den's guard or a changed record stops automatic pairing
// until the human starts a pairing in Den: a console code (/api console,
// den pair) or an app's (/api pair).
func TestAutoPairOffAfterAnUnpair(t *testing.T) {
	for _, cause := range []string{CauseDen, CauseSelf, CauseOther, CauseGuard, CauseRecord} {
		for _, start := range []string{"console", "app"} {
			t.Run(cause+"/"+start, func(t *testing.T) {
				h, _ := consoleHarness(t)
				fakeUnit(t, testVerifier(), nil)
				d := newDock(t, h)
				requestPaired(t, h, d, "")
				h.s.UnpairConsole(cause, "")
				if l := d.requestLine(); code(l) != CodeForbidden || l.Error.Message != AutoPairOff {
					t.Fatalf("request after the unpair: %+v", l.Error)
				}
				if start == "console" {
					h.s.StartConsoleCode()
				} else {
					h.s.StartPairing()
				}
				if !d.requestPowers() {
					t.Error("paired without its powers")
				}
			})
		}
	}
}

// A replaced console does not stop automatic pairing.
func TestAutoPairOnAfterAReplace(t *testing.T) {
	h, _ := consoleHarness(t)
	fakeUnit(t, testVerifier(), nil)
	requestPaired(t, h, newDock(t, h), "")
	requestPaired(t, h, newDock(t, h), "")
	if l := newDock(t, h).requestLine(); !l.OK {
		t.Errorf("a third: %+v", l.Error)
	}
}

// A3: the human's /api lock outlasts every re-pair, by request, by
// reconnect, by code and after Den restarts; only the ^Y on Den's powers
// card lifts it.
func TestLockOutlastsRepairs(t *testing.T) {
	store := filepath.Join(t.TempDir(), "consoles.json")
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	h := storeHarness(t, store, priv)
	fakeUnit(t, testVerifier(), nil)
	d := newDock(t, h)
	requestPaired(t, h, d, "")
	h.s.LockConsole("you locked them in Den")
	ctlAct(t, h.s, func(a Act) bool { return a.ConsolePowers != nil })
	// By request.
	if d.requestPowers() {
		t.Error("a request after the lock has powers")
	}
	ctlAct(t, h.s, func(a Act) bool { return a.ConsolePowers != nil })
	// By reconnect.
	c, id := reconnect(t, h, d.restarted(), "")
	var r reconnectReply
	if l := c.replyTo(id); !l.OK || json.Unmarshal(l.Result, &r) != nil || r.Powers {
		t.Fatalf("reconnect after the lock: %+v %s", l.Error, l.Result)
	}
	ctlAct(t, h.s, func(a Act) bool { return a.ConsolePowers != nil })
	// By code.
	cc, cid, ask := d.claimAsk(h.s.StartConsoleCode())
	h.s.ConsoleAnswered(ask.ID, true)
	if l := cc.replyTo(cid); !l.OK {
		t.Fatalf("claim: %+v", l.Error)
	}
	if cur, _ := h.s.CurrentConsole(); cur.Powers {
		t.Error("a claim after the lock has powers")
	}
	ctlAct(t, h.s, func(a Act) bool { return a.ConsolePowers != nil })
	// After Den restarts.
	requestPaired(t, h, d, "")
	cleanExit(h.s)
	h2 := storeHarness(t, store, priv)
	if pw := ctlAct(t, h2.s, func(a Act) bool { return a.ConsolePowers != nil }).ConsolePowers; !strings.Contains(pw.Why, "you locked") {
		t.Errorf("powers card after the restart: %+v", pw)
	}
	cur, ok := h2.s.CurrentConsole()
	if !ok || cur.Powers {
		t.Fatalf("restored %+v %v", cur, ok)
	}
	// The ^Y lifts it, for the console now and every re-pair after.
	h2.s.ConsolePowersAnswered(cur.ID, true)
	if cur, _ := h2.s.CurrentConsole(); !cur.Powers {
		t.Error("no powers after the ^Y")
	}
	d.h = h2
	if !d.requestPowers() {
		t.Error("a request after the ^Y has no powers")
	}
}

// A4: three tries from other programs, wrong proofs or a burst stop
// automatic pairing for autoPairHold, whatever the human starts; pairing
// by code still works meanwhile.
func TestAutoPairHeldAfterStrikes(t *testing.T) {
	for _, cause := range []string{CauseIntruders, CauseProofs, CauseBurst} {
		t.Run(cause, func(t *testing.T) {
			h, _ := consoleHarness(t)
			fakeUnit(t, testVerifier(), nil)
			d := newDock(t, h)
			requestPaired(t, h, d, "")
			h.s.UnpairConsole(cause, "")
			h.s.StartConsoleCode()
			if l := d.requestLine(); code(l) != CodeUnavailable || l.Error.Message != AutoPairHeld {
				t.Fatalf("request in the hold: %+v", l.Error)
			}
			cc, cid, ask := d.claimAsk(h.s.StartConsoleCode())
			h.s.ConsoleAnswered(ask.ID, true)
			if l := cc.replyTo(cid); !l.OK {
				t.Fatalf("a claim by code in the hold: %+v", l.Error)
			}
			h.s.con.mu.Lock()
			if h.s.con.autoHold.Sub(time.Now()) < autoPairHold-time.Minute {
				t.Errorf("held until %v", h.s.con.autoHold)
			}
			h.s.con.autoHold = time.Now().Add(-time.Second)
			h.s.con.mu.Unlock()
			if l := d.requestLine(); !l.OK {
				t.Errorf("request after the hold: %+v", l.Error)
			}
		})
	}
}

// A5: while God mode is on, a request pairs without the powers, and asks
// for them on Den's card.
func TestAutoPairInGodMode(t *testing.T) {
	h, _ := consoleHarness(t)
	fakeUnit(t, testVerifier(), nil)
	h.s.SetGodMode(func() bool { return true })
	if newDock(t, h).requestPowers() {
		t.Error("powers in God mode")
	}
	if pw := ctlAct(t, h.s, func(a Act) bool { return a.ConsolePowers != nil }).ConsolePowers; pw.Why != "God mode is on" {
		t.Errorf("powers card %+v", pw)
	}
}

// A6: every automatic pairing leaves a note in Den's chat and an event for
// the console's first subscribed connection: the unit, pid and start, the
// program and its SHA-256, and what changed since Den last read the
// service.
func TestAutoPairSaysWhatItPaired(t *testing.T) {
	h, _ := consoleHarness(t)
	fakeUnit(t, testVerifier(), nil)
	d := newDock(t, h)
	p := requestPaired(t, h, d, "")
	n := noteWith(t, h.s, "console paired automatically: "+ConsoleUnit)
	for _, want := range []string{"/home/u/.local/bin/console-host", testVerifier().Exe.SHA256, "pid "} {
		if !strings.Contains(n, want) {
			t.Errorf("note lacks %q: %s", want, n)
		}
	}
	c := h.dial()
	c.hello()
	if l, _, _, _ := d.auth(c, p); !l.OK {
		t.Fatalf("auth %+v", l.Error)
	}
	c.call("console.events", nil)
	c.next() // connected
	var ev ConsoleEvent
	if e := c.next(); json.Unmarshal(e.Data, &ev) != nil || ev.Kind != "paired" || ev.Reason != "paired automatically" || ev.Unit != ConsoleUnit ||
		ev.Program != testVerifier().Exe.Path || ev.ProgramSHA != testVerifier().Exe.SHA256 || ev.PID == 0 || ev.StartTicks == 0 {
		t.Errorf("event %+v", ev)
	}
	// A new build: named.
	changed := testVerifier()
	changed.Exe.SHA256 = strings.Repeat("99", 32)
	fakeUnit(t, changed, nil)
	requestPaired(t, h, d.restarted(), "")
	if n := noteWith(t, h.s, "console paired automatically"); !strings.Contains(n, "the program changed") {
		t.Errorf("note %s", n)
	}
}
