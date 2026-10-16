package api

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/sysd"
)

// requestPaired pairs the dock by console.request, at once, and returns
// the pairing.
func requestPaired(t *testing.T, h *harness, d *dock, pages string) consolePaired {
	t.Helper()
	c := h.dial()
	c.hello()
	p := map[string]string{"public_key": d.pub}
	if pages != "" {
		p["pages_sha256"] = pages
	}
	l := c.call("console.request", p)
	var r struct {
		Pairing string `json:"pairing"`
		DenKey  string `json:"den_public_key"`
	}
	if !l.OK || json.Unmarshal(l.Result, &r) != nil {
		t.Fatalf("request: %+v", l.Error)
	}
	return consolePaired{ID: r.Pairing, DenKey: r.DenKey}
}

// restarted is the same dock with a fresh key, as after a restart.
func (d *dock) restarted() *dock {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	return &dock{t: d.t, h: d.h, pub: hex.EncodeToString(pub), priv: priv, identity: d.identity}
}

type reconnectReply struct {
	Pairing string `json:"pairing"`
	DenSig  string `json:"den_sig"`
	Powers  bool   `json:"powers"`
}

// testNonce is the dock's nonce in these tests.
var testNonce = strings.Repeat("5a", 32)

// verified reports whether Den signed the reply for this dock's key and
// the test nonce, under the key hello publishes.
func (r reconnectReply) verified(h *harness, d *dock) bool {
	sig, err := hex.DecodeString(r.DenSig)
	pub, _ := hex.DecodeString(h.s.ckey.pubHex)
	return err == nil && ed25519.Verify(ed25519.PublicKey(pub), reconnectSigned(testNonce, r.Pairing, d.pub), sig)
}

func reconnect(t *testing.T, h *harness, d *dock, pages string) (*client, int64) {
	t.Helper()
	c := h.dial()
	c.hello()
	p := map[string]string{"public_key": d.pub, "nonce": testNonce}
	if pages != "" {
		p["pages_sha256"] = pages
	}
	return c, c.send("console.reconnect", p)
}

// The service restarts while Den runs: it re-pairs at once, with its
// powers and no card, signed under Den's key; Den names the program in
// its chat, and the console's first subscribed connection hears it
// reconnected, automatically.
func TestReconnectAtOnce(t *testing.T) {
	h := alwaysHarness(t)
	fakeUnit(t, testVerifier(), nil)
	d := newDock(t, h)
	old := requestPaired(t, h, d, "")
	d2 := d.restarted()
	c, id := reconnect(t, h, d2, "")
	l := c.replyTo(id)
	var r reconnectReply
	if !l.OK || json.Unmarshal(l.Result, &r) != nil || r.Pairing == old.ID || !r.verified(h, d2) || !r.Powers {
		t.Fatalf("reply %+v %s", l.Error, l.Result)
	}
	noteWith(t, h.s, "console paired automatically: "+ConsoleUnitName+" ("+ConsoleUnit+") pid ")
	noCard(t, h.s)
	// The old pairing is gone; the new one proves, and hears it reconnected.
	pc := h.dial()
	pc.hello()
	if l, _, _, _ := d.auth(pc, old); l.OK {
		t.Error("the old pairing still proves")
	}
	nc := h.dial()
	nc.hello()
	p := consolePaired{ID: r.Pairing, DenKey: h.s.ckey.pubHex}
	if l, _, _, _ := d2.auth(nc, p); !l.OK || !strings.Contains(string(l.Result), `"powers":true`) {
		t.Fatalf("auth %+v %s", l.Error, l.Result)
	}
	nc.call("console.events", nil)
	var ev ConsoleEvent
	skipSnapshot(t, h.s, nc)
	if e := nc.next(); json.Unmarshal(e.Data, &ev) != nil || ev.Kind != "reconnected" || ev.Reason != "paired automatically" || ev.Unit != ConsoleUnit || ev.ProgramSHA != testVerifier().Exe.SHA256 || len(ev.Changes) != 0 {
		t.Errorf("event %+v", ev)
	}
	nc.send("cmd.run", map[string]string{"text": "/fleet flat"})
	ctlAct(t, h.s, func(a Act) bool { return a.Command != nil })
}

// What changed since Den last read the service is named, in Den's chat
// and in the event, and re-pairs all the same: a new build, the dock's
// pages. God mode re-pairs without the powers.
func TestReconnectNamesWhatChanged(t *testing.T) {
	changed := testVerifier()
	changed.Exe.SHA256 = strings.Repeat("99", 32)
	for _, c := range []struct {
		name   string
		setup  func(t *testing.T, h *harness)
		v      sysd.Verifier
		pages  string
		want   string
		powers bool
	}{
		{"new build", nil, changed, "", "the program changed", true},
		{"pages", nil, testVerifier(), strings.Repeat("12", 32), "the dock's pages changed", true},
		{"god mode", func(t *testing.T, h *harness) { h.s.SetGodMode(func() bool { return true }) }, testVerifier(), "", "God mode is on", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := alwaysHarness(t)
			fakeUnit(t, testVerifier(), nil)
			d := newDock(t, h)
			requestPaired(t, h, d, "")
			drainNotes(h.s)
			if c.setup != nil {
				c.setup(t, h)
			}
			fakeUnit(t, c.v, nil)
			conn, id := reconnect(t, h, d.restarted(), c.pages)
			var r reconnectReply
			if l := conn.replyTo(id); !l.OK || json.Unmarshal(l.Result, &r) != nil || r.Powers != c.powers || r.DenSig == "" {
				t.Fatalf("reply %+v %s", l.Error, l.Result)
			}
			noteWith(t, h.s, c.want)
			if !c.powers {
				if pw := ctlAct(t, h.s, func(a Act) bool { return a.ConsolePowers != nil }).ConsolePowers; pw.ID != r.Pairing || pw.Why != "God mode is on" {
					t.Errorf("powers card %+v", pw)
				}
			}
		})
	}
}

// After a Den start the record is read back from disk: a restart of the
// service re-pairs at once all the same, with its powers.
func TestReconnectAfterDenRestart(t *testing.T) {
	store := filepath.Join(t.TempDir(), "consoles.json")
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	h := alwaysStore(t, store, priv)
	fakeUnit(t, testVerifier(), nil)
	d := newDock(t, h)
	requestPaired(t, h, d, "")
	cleanExit(h.s)
	h2 := alwaysStore(t, store, priv)
	d.h = h2
	d2 := d.restarted()
	conn, id := reconnect(t, h2, d2, "")
	var r reconnectReply
	if l := conn.replyTo(id); !l.OK || json.Unmarshal(l.Result, &r) != nil || !r.Powers || !r.verified(h2, d2) {
		t.Fatalf("reply %+v %s", l.Error, l.Result)
	}
	if cur, ok := h2.s.CurrentConsole(); !ok || cur.ID != r.Pairing || !cur.Powers {
		t.Errorf("after the reconnect: %+v %v", cur, ok)
	}
}

// Nothing to reconnect to, or not the service: refused, the second as a
// strike that takes the console's powers.
func TestReconnectRefused(t *testing.T) {
	h, _ := consoleHarness(t)
	fakeUnit(t, testVerifier(), nil)
	d := newDock(t, h)
	c, id := reconnect(t, h, d, "")
	if l := c.replyTo(id); code(l) != CodeForbidden || !strings.Contains(l.Error.Message, "no pairing") {
		t.Errorf("no pairing: %+v", l.Error)
	}
	requestPaired(t, h, d, "")
	fakeUnit(t, testVerifier(), sysd.Problems{"the connection is not the unit's main process"})
	time.Sleep(requestEvery)
	c, id = reconnect(t, h, d.restarted(), "")
	if l := c.replyTo(id); code(l) != CodeForbidden || !strings.Contains(l.Error.Message, "main process") {
		t.Errorf("not the service: %+v", l.Error)
	}
	ctlAct(t, h.s, func(a Act) bool { return a.ConsolePowers != nil })
	h.s.con.mu.Lock()
	cur := h.s.con.cur
	h.s.con.mu.Unlock()
	if cur == nil || cur.powers || len(cur.strikes) != 1 {
		t.Errorf("console after a refused reconnect: %+v", cur)
	}
	fakeUnit(t, testVerifier(), errors.New("no manager"))
	time.Sleep(requestEvery)
	c, id = reconnect(t, h, d.restarted(), "")
	if l := c.replyTo(id); code(l) != CodeForbidden {
		t.Errorf("no manager: %+v", l.Error)
	}
	h.s.con.mu.Lock()
	strikes := len(cur.strikes)
	h.s.con.mu.Unlock()
	if strikes != 1 {
		t.Errorf("a manager Den could not read counted as a strike: %d", strikes)
	}
}

// The dock may reconnect before hello, so it sends nothing until Den has
// proved itself: the first line back is the reply, signed by the key
// hello publishes.
func TestReconnectBeforeHello(t *testing.T) {
	h, _ := consoleHarness(t)
	fakeUnit(t, testVerifier(), nil)
	d := newDock(t, h)
	requestPaired(t, h, d, "")
	d2 := d.restarted()
	// Before Den has published a view it cannot tell an agent's process:
	// a reconnect before hello is refused, fail closed.
	early := h.dial()
	if l := early.call("console.reconnect", map[string]string{"public_key": d2.pub, "nonce": testNonce}); code(l) != CodeUnavailable || l.Error.Message != "Den is starting" {
		t.Errorf("before a view: %+v", l.Error)
	}
	h.s.Publish(Project(Snapshot{Taken: time.Now()}))
	c := h.dial()
	id := c.send("console.reconnect", map[string]string{"public_key": d2.pub, "nonce": testNonce})
	l := c.next()
	var r reconnectReply
	if l.ID == nil || *l.ID != id || !l.OK || json.Unmarshal(l.Result, &r) != nil || !r.verified(h, d2) {
		t.Fatalf("first line %+v %s", l, l.Result)
	}
	if strings.Contains(string(l.Result), "den_public_key") {
		t.Error("the reply carries a key the dock should already hold")
	}
	// Nothing else but a reconnect goes before hello.
	if l := c.call("fleet.snapshot", nil); code(l) != CodeHelloFirst {
		t.Errorf("fleet before hello: %+v", l.Error)
	}
	// A reconnect needs the dock's nonce.
	time.Sleep(requestEvery)
	if l := c.call("console.reconnect", map[string]string{"public_key": d2.pub}); code(l) != CodeBadRequest {
		t.Errorf("no nonce: %+v", l.Error)
	}
}

// The known-answer vector DREY-API.md publishes: Den's console key from
// the seed of 32 bytes 0x01, signing with Den's own framing, gives these
// exact bytes; the dock proves the same.
func TestReconnectSignatureKnownAnswer(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = 0x01
	}
	priv := ed25519.NewKeyFromSeed(seed)
	const (
		vectorDenPub = "8a88e3dd7409f195fd52db2d3cba5d72ca6709bf1d94121bf3748801b40f6f5c"
		vectorDenSig = "7a3ce0532530d6793e45ebbf3360530055b842dbb42c30278e5fd70e0fe77c285967f693c26f10e4996d2ead6e8990a2729b50a525e3ac3e3de6bfa35927dd05"
	)
	if got := hex.EncodeToString(priv.Public().(ed25519.PublicKey)); got != vectorDenPub {
		t.Errorf("key %s", got)
	}
	sig := ed25519.Sign(priv, reconnectSigned(strings.Repeat("5a", 32), strings.Repeat("0a", 16), strings.Repeat("0b", 32)))
	if got := hex.EncodeToString(sig); got != vectorDenSig {
		t.Errorf("den_sig %s", got)
	}
}

// Den's signature covers its label, the nonce, the pairing and the dock's
// key: change any one and it no longer verifies.
func TestReconnectSignatureCoversAll(t *testing.T) {
	h, _ := consoleHarness(t)
	pub, _ := hex.DecodeString(h.s.ckey.pubHex)
	nonce, pairing, key := testNonce, strings.Repeat("0a", 16), strings.Repeat("0b", 32)
	sig := ed25519.Sign(h.s.ckey.priv, reconnectSigned(nonce, pairing, key))
	if !ed25519.Verify(pub, reconnectSigned(nonce, pairing, key), sig) {
		t.Fatal("does not verify")
	}
	other := strings.Repeat("0c", 32)
	for name, msg := range map[string][]byte{
		"nonce":   reconnectSigned(other, pairing, key),
		"pairing": reconnectSigned(nonce, strings.Repeat("0d", 16), key),
		"key":     reconnectSigned(nonce, pairing, other),
		"label":   []byte(consoleSigLabel + "\n" + nonce + "\n" + pairing + "\n" + key),
	} {
		if ed25519.Verify(pub, msg, sig) {
			t.Errorf("verifies with another %s", name)
		}
	}
}

// ConsolePowered is true only for the console paired now, while it has its
// powers: not for another id, and not once they are locked.
func TestConsolePowered(t *testing.T) {
	h, _ := provedConsole(t)
	cur, ok := h.s.CurrentConsole()
	if !ok || !h.s.ConsolePowered(cur.ID) {
		t.Fatalf("a freshly paired console without its powers: %+v %v", cur, ok)
	}
	if h.s.ConsolePowered("") || h.s.ConsolePowered(cur.ID+"x") {
		t.Error("powers for another id")
	}
	h.s.LockConsolePowers("a test")
	if h.s.ConsolePowered(cur.ID) {
		t.Error("powers after the lock")
	}
}
