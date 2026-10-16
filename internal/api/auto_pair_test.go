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

// alwaysHarness is consoleHarness with api.console_powers = "always";
// alwaysStore is storeHarness with it.
func alwaysHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessWith(t, 2, func(o *Options) { o.ConsoleKey, o.ConsolePowersAlways = mustKey(t), true })
}

func alwaysStore(t *testing.T, store string, priv ed25519.PrivateKey) *harness {
	t.Helper()
	return newHarnessWith(t, 2, func(o *Options) { o.ConsoleKey, o.ConsoleStore, o.ConsolePowersAlways = priv, store, true })
}

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
				if l := d.requestLine(); !l.OK {
					t.Errorf("request after the human started a pairing: %+v", l.Error)
				}
			})
		}
	}
}

// The switch and the hold outlast Den's restarts: an unpair, then a
// restart, still refuses; strikes, then a restart within the hold, still
// hold.
func TestAutoPairStopsOutlastARestart(t *testing.T) {
	for _, c := range []struct {
		cause, code, msg string
	}{
		{CauseDen, CodeForbidden, AutoPairOff},
		{CauseIntruders, CodeUnavailable, AutoPairHeld},
	} {
		t.Run(c.cause, func(t *testing.T) {
			store := filepath.Join(t.TempDir(), "consoles.json")
			_, priv, _ := ed25519.GenerateKey(rand.Reader)
			h := storeHarness(t, store, priv)
			fakeUnit(t, testVerifier(), nil)
			d := newDock(t, h)
			requestPaired(t, h, d, "")
			h.s.UnpairConsole(c.cause, "")
			cleanExit(h.s)
			d.h = storeHarness(t, store, priv)
			if l := d.requestLine(); code(l) != c.code || l.Error.Message != c.msg {
				t.Errorf("request after the restart: %+v", l.Error)
			}
		})
	}
}

// An unclean exit (a crash, a killed terminal) is not the human's unpair:
// after one, the dock pairs by itself again, unless the record keeps an
// unpair or a hold from before it.
func TestAutoPairAfterAnUncleanExit(t *testing.T) {
	for _, c := range []struct {
		name, cause, code, msg string
	}{
		{"paired", "", "", ""},
		{"unpaired", CauseDen, CodeForbidden, AutoPairOff},
		{"held", CauseIntruders, CodeUnavailable, AutoPairHeld},
	} {
		t.Run(c.name, func(t *testing.T) {
			store := filepath.Join(t.TempDir(), "consoles.json")
			priv := mustKey(t)
			h := storeHarness(t, store, priv)
			fakeUnit(t, testVerifier(), nil)
			d := newDock(t, h)
			requestPaired(t, h, d, "")
			if c.cause != "" {
				h.s.UnpairConsole(c.cause, "")
			}
			// No clean exit: no mark.
			d.h = storeHarness(t, store, priv)
			l := d.requestLine()
			if c.cause == "" {
				if !l.OK {
					t.Errorf("request after a crash: %+v", l.Error)
				}
				return
			}
			if code(l) != c.code || l.Error.Message != c.msg {
				t.Errorf("request after a crash: %+v", l.Error)
			}
		})
	}
}

// A record Den cannot trust at its start, after a clean exit, stops
// automatic pairing, as it may have held the human's unpair: changed,
// damaged, or removed. A start that never paired does not.
func TestAutoPairOffForARecordDenCannotTrust(t *testing.T) {
	for name, spoil := range map[string]func(store string){
		"changed":   func(store string) { os.WriteFile(store, []byte(`{"consoles":[]}`), 0o600) },
		"damaged":   func(store string) { os.WriteFile(store, []byte(`{`), 0o600) },
		"removed":   func(store string) { os.Remove(store) },
		"untouched": func(string) {},
	} {
		t.Run(name, func(t *testing.T) {
			store := filepath.Join(t.TempDir(), "consoles.json")
			_, priv, _ := ed25519.GenerateKey(rand.Reader)
			h := storeHarness(t, store, priv)
			fakeUnit(t, testVerifier(), nil)
			d := newDock(t, h)
			requestPaired(t, h, d, "")
			cleanExit(h.s)
			spoil(store)
			d.h = storeHarness(t, store, priv)
			l := d.requestLine()
			if name == "untouched" {
				if !l.OK {
					t.Errorf("request after a clean restart: %+v", l.Error)
				}
				return
			}
			if code(l) != CodeForbidden || l.Error.Message != AutoPairOff {
				t.Errorf("request after the restart: %+v", l.Error)
			}
		})
	}
	// Never paired: no record, no mark.
	h := storeHarness(t, filepath.Join(t.TempDir(), "consoles.json"), mustKey(t))
	fakeUnit(t, testVerifier(), nil)
	if l := newDock(t, h).requestLine(); !l.OK {
		t.Errorf("a first start: %+v", l.Error)
	}
}

func mustKey(t *testing.T) ed25519.PrivateKey {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

// By default ("unchanged"), the dock pairs by itself at once, but comes
// with its powers only as this Den run gave them: the first pairing after
// Den starts, and any change of program, unit or pins, ask for one ^Y on
// the powers card; the same build after that ^Y has them.
func TestPowersOnlyForWhatThisRunGaveThemTo(t *testing.T) {
	store := filepath.Join(t.TempDir(), "consoles.json")
	priv := mustKey(t)
	h := storeHarness(t, store, priv)
	fakeUnit(t, testVerifier(), nil)
	d := newDock(t, h)
	// The first: paired, no powers, a card that says why.
	if d.requestPowers() {
		t.Fatal("the first pairing has powers")
	}
	pw := ctlAct(t, h.s, func(a Act) bool { return a.ConsolePowers != nil }).ConsolePowers
	if !strings.Contains(pw.Why, "has not given this dock its powers") {
		t.Errorf("powers card %+v", pw)
	}
	h.s.ConsolePowersAnswered(pw.ID, true)
	// The same build restarts: powers.
	reconnectPowers := func(d *dock) bool {
		t.Helper()
		time.Sleep(requestEvery)
		c, id := reconnect(t, d.h, d, "")
		var r reconnectReply
		if l := c.replyTo(id); !l.OK || json.Unmarshal(l.Result, &r) != nil {
			t.Fatalf("reconnect %+v %s", l.Error, l.Result)
		}
		return r.Powers
	}
	d = d.restarted()
	if !reconnectPowers(d) {
		t.Error("the same build has no powers after the ^Y")
	}
	// A new build: paired, no powers, the change named.
	changed := testVerifier()
	changed.Exe.SHA256 = strings.Repeat("99", 32)
	fakeUnit(t, changed, nil)
	d = d.restarted()
	if reconnectPowers(d) {
		t.Error("a new build has its powers")
	}
	pw = ctlAct(t, h.s, func(a Act) bool { return a.ConsolePowers != nil }).ConsolePowers
	if !strings.Contains(pw.Why, "the program changed") {
		t.Errorf("powers card %+v", pw)
	}
	// Restarted again, still not given: still none.
	d = d.restarted()
	if reconnectPowers(d) {
		t.Error("a build never given the powers has them")
	}
	pw = ctlAct(t, h.s, func(a Act) bool { return a.ConsolePowers != nil }).ConsolePowers
	h.s.ConsolePowersAnswered(pw.ID, true)
	d = d.restarted()
	if !reconnectPowers(d) {
		t.Error("the new build has no powers after its ^Y")
	}
	// After Den restarts, what it read before is from disk: the restored
	// console, and the next re-pair, ask again.
	cleanExit(h.s)
	h2 := storeHarness(t, store, priv)
	if pw := ctlAct(t, h2.s, func(a Act) bool { return a.ConsolePowers != nil }).ConsolePowers; pw.Why != "Den restarted" {
		t.Errorf("restored: %+v", pw)
	}
	cur, _ := h2.s.CurrentConsole()
	h2.s.ConsolePowersAnswered(cur.ID, true) // a ^Y for what is on disk counts for nothing after
	d.h = h2
	d = d.restarted()
	if reconnectPowers(d) {
		t.Error("a re-pair after Den restarted has its powers from the record on disk")
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
	h := alwaysStore(t, store, priv)
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
	h2 := alwaysStore(t, store, priv)
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

// A console is Verified (Drey in Den's header) only when this Den read it
// clean for the pairing itself: a request is, a claim by code that did
// not read clean is not, nor is one restored from the record on disk.
func TestConsoleVerifiedOnlyFromThisRun(t *testing.T) {
	store := filepath.Join(t.TempDir(), "consoles.json")
	priv := mustKey(t)
	h := storeHarness(t, store, priv)
	fakeUnit(t, testVerifier(), nil)
	d := newDock(t, h)
	requestPaired(t, h, d, "")
	if cur, _ := h.s.CurrentConsole(); !cur.Verified {
		t.Error("a request read clean is not verified")
	}
	cleanExit(h.s)
	h2 := storeHarness(t, store, priv)
	if cur, ok := h2.s.CurrentConsole(); !ok || cur.Verified {
		t.Errorf("restored from disk: %+v %v", cur, ok)
	}
	h3, _ := consoleHarness(t)
	fakeUnit(t, testVerifier(), sysd.Problems{"not the unit's main process"})
	d3 := newDock(t, h3)
	c, id, ask := d3.claimAsk(h3.s.StartConsoleCode())
	h3.s.ConsoleAnswered(ask.ID, true)
	if l := c.replyTo(id); !l.OK {
		t.Fatalf("claim %+v", l.Error)
	}
	if cur, _ := h3.s.CurrentConsole(); cur.Verified {
		t.Error("a claim that did not read clean is verified")
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
	h := alwaysHarness(t)
	fakeUnit(t, testVerifier(), nil)
	d := newDock(t, h)
	p := requestPaired(t, h, d, "")
	n := noteWith(t, h.s, "console paired automatically: "+ConsoleUnitName+" ("+ConsoleUnit+") pid ")
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
