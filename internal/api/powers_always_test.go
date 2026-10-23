package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/sysd"
)

// "always" is off at every start: a harness without the human's ^Y pairs
// the dock by itself, without its powers, and asks.
func TestAlwaysOffAtStart(t *testing.T) {
	h, _ := consoleHarness(t)
	if h.s.ConsolePowersAlways() {
		t.Fatal("always at start")
	}
	fakeUnit(t, testVerifier(), nil)
	if newDock(t, h).requestPowers() {
		t.Error("powers without always or a ^Y")
	}
}

// C1: every lock of the powers ends "always" for the run and forgets what
// the human last gave them to: no automatic re-pair, the same program's
// included, brings them back without a ^Y.
func TestAlwaysEndsOnEveryLock(t *testing.T) {
	locks := map[string]func(h *harness){
		"another program": func(h *harness) { h.s.LockConsolePowers("another program tried your console") },
		"a wrong proof":   func(h *harness) { h.s.LockConsolePowers("a wrong proof for your console") },
		"God mode": func(h *harness) {
			h.s.SetGodMode(func() bool { return true })
			h.s.LockConsolePowers("God mode is on")
			h.s.SetGodMode(func() bool { return false })
		},
	}
	for name, lock := range locks {
		t.Run(name, func(t *testing.T) {
			h := alwaysHarness(t)
			fakeUnit(t, testVerifier(), nil)
			d := newDock(t, h)
			if !d.requestPowers() {
				t.Fatal("no powers with always")
			}
			lock(h)
			if h.s.ConsolePowersAlways() {
				t.Error("always outlived the lock")
			}
			time.Sleep(requestEvery)
			c, id := reconnect(t, h, d.restarted(), "")
			var r reconnectReply
			if l := c.replyTo(id); !l.OK || json.Unmarshal(l.Result, &r) != nil {
				t.Fatalf("reconnect %+v %s", l.Error, l.Result)
			}
			if r.Powers {
				t.Error("an automatic re-pair after the lock has its powers")
			}
		})
	}
}

// C1, /api lock: it ends "always" too, besides the lock's own hold.
func TestAlwaysEndsOnAPILock(t *testing.T) {
	h := alwaysHarness(t)
	fakeUnit(t, testVerifier(), nil)
	if !newDock(t, h).requestPowers() {
		t.Fatal("no powers with always")
	}
	h.s.LockConsole("you locked the console's powers")
	if h.s.ConsolePowersAlways() {
		t.Error("always outlived /api lock")
	}
}

// C2: "always" changes only when the powers come, never who may pair: a
// service that fails a check is refused as without it.
func TestAlwaysNeverPairsAnotherProgram(t *testing.T) {
	h := alwaysHarness(t)
	fakeUnit(t, testVerifier(), sysd.Problems{"its environment sets LD_PRELOAD"})
	d := newDock(t, h)
	c := h.dial()
	c.hello()
	l := c.call("console.request", map[string]string{"public_key": d.pub})
	if code(l) != CodeForbidden || !strings.Contains(l.Error.Message, "LD_PRELOAD") {
		t.Errorf("reply %+v", l.Error)
	}
	if cur, ok := h.s.CurrentConsole(); ok {
		t.Errorf("paired: %+v", cur)
	}
}

// C5: the setter takes the console's lock for one assignment and returns;
// held by another goroutine, it waits, and nothing else blocks on it.
func TestSetConsolePowersAlwaysUnderTheLock(t *testing.T) {
	h, _ := consoleHarness(t)
	h.s.con.mu.Lock()
	done := make(chan struct{})
	go func() {
		h.s.SetConsolePowersAlways(true)
		close(done)
	}()
	select {
	case <-done:
		h.s.con.mu.Unlock()
		t.Fatal("set without the lock")
	case <-time.After(50 * time.Millisecond):
	}
	h.s.con.mu.Unlock()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the setter did not return after the lock was free")
	}
	if !h.s.ConsolePowersAlways() {
		t.Error("not set")
	}
	h.s.SetConsolePowersAlways(false)
	if h.s.ConsolePowersAlways() {
		t.Error("not lowered")
	}
}
