package api

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// consoleHarness is a harness whose server offers console pairing, and the
// public half of Den's key.
func consoleHarness(t *testing.T) (*harness, ed25519.PublicKey) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	h := newHarnessWith(t, 2, func(o *Options) { o.ConsoleKey = priv })
	return h, pub
}

// dock is the app side of a console, as this test process: its key pair
// and the identity Den knows it by.
type dock struct {
	t        *testing.T
	h        *harness
	pub      string
	priv     ed25519.PrivateKey
	identity string
}

func newDock(t *testing.T, h *harness) *dock {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	exe, _ := os.Readlink("/proc/self/exe")
	start, _, _ := procStat(os.Getpid())
	id, ok := consoleIdentity(Peer{PID: os.Getpid(), Exe: exe, Start: start})
	if !ok {
		t.Fatal("no identity for the test process")
	}
	return &dock{t: t, h: h, pub: hex.EncodeToString(pub), priv: priv, identity: id}
}

type challenge struct{ Nonce, Conn, ClaimSalt string }

func (d *dock) challenge(c *client) challenge {
	d.t.Helper()
	l := c.call("console.challenge", nil)
	var r struct {
		Nonce     string `json:"nonce"`
		Conn      string `json:"conn"`
		ClaimSalt string `json:"claim_salt"`
	}
	if !l.OK || json.Unmarshal(l.Result, &r) != nil || !isHex(r.Nonce, 64) || !isHex(r.Conn, 32) || !isHex(r.ClaimSalt, 64) {
		d.t.Fatalf("console.challenge: %+v %s", l.Error, l.Result)
	}
	return challenge{r.Nonce, r.Conn, r.ClaimSalt}
}

// proof is console.claim's proof for words under salt.
func proof(words []string, salt, pub string) string {
	k := codeKey([]byte(strings.Join(words, " ")), salt, consoleClaimInfo)
	return macHex(k, consoleClaimInfo+"\n"+pub)
}

// claim pairs d as the console with words; answer is the human's on the
// card. It returns the claim's reply line and the connection.
func (d *dock) claim(words []string, answer bool) (line, *client) {
	d.t.Helper()
	c := d.h.dial()
	c.hello()
	ch := d.challenge(c)
	id := c.send("console.claim", map[string]string{"public_key": d.pub, "proof": proof(words, ch.ClaimSalt, d.pub)})
	ask := ctlAct(d.t, d.h.s, func(a Act) bool { return a.Console != nil }).Console
	d.h.s.ConsoleAnswered(ask.ID, answer)
	return c.replyTo(id), c
}

type consolePaired struct {
	ID, DenKey, Bind string
}

func (d *dock) pair(words []string) consolePaired {
	d.t.Helper()
	l, _ := d.claim(words, true)
	var r struct {
		Pairing string `json:"pairing"`
		DenKey  string `json:"den_public_key"`
		Bind    string `json:"bind"`
		Console bool   `json:"console"`
	}
	if !l.OK || json.Unmarshal(l.Result, &r) != nil || !isHex(r.Pairing, 32) || !isHex(r.DenKey, 64) || !isHex(r.Bind, 64) || !r.Console {
		d.t.Fatalf("console.claim: %+v %s", l.Error, l.Result)
	}
	// The dock checks the bind before it pins Den's key.
	k := codeKey([]byte(strings.Join(words, " ")), r.Pairing, consoleBindInfo)
	if want := macHex(k, consoleBindInfo+"\n"+d.pub+"\n"+r.DenKey+"\n"+r.Pairing); want != r.Bind {
		d.t.Fatalf("bind %s, want %s", r.Bind, want)
	}
	return consolePaired{r.Pairing, r.DenKey, r.Bind}
}

// auth proves c as the console; it returns the reply, the nonces and
// Den's signature.
func (d *dock) auth(c *client, p consolePaired) (line, challenge, string, string) {
	d.t.Helper()
	ch := d.challenge(c)
	na := randHex64()
	sig := ed25519.Sign(d.priv, consoleSigned(dirAppToDen, ch.Conn, p.ID, d.pub, p.DenKey, ch.Nonce, na, d.identity))
	l := c.call("console.auth", map[string]string{"pairing": p.ID, "nonce": na, "sig": hex.EncodeToString(sig)})
	var r struct {
		Sig string `json:"sig"`
	}
	json.Unmarshal(l.Result, &r)
	return l, ch, na, r.Sig
}

// The whole way: a code, a claim with its proof, the human's ^Y, Den's
// bind, then a proof on another connection both sides sign; the console
// then gets its events.
func TestConsolePairAndProve(t *testing.T) {
	h, denPub := consoleHarness(t)
	var info Info
	c0 := h.dial()
	json.Unmarshal(c0.call("hello", map[string]any{"v": 1, "client": "test"}).Result, &info)
	if len(info.Features) != 1 || info.Features[0] != FeatureConsole {
		t.Fatalf("features %v", info.Features)
	}
	d := newDock(t, h)
	p := d.pair(h.s.StartConsoleCode())
	if p.DenKey != hex.EncodeToString(denPub) {
		t.Fatal("Den's public key")
	}
	if cur, ok := h.s.CurrentConsole(); !ok || cur.ID != p.ID || cur.PID != os.Getpid() {
		t.Fatalf("console %+v", cur)
	}
	c := h.dial()
	c.hello()
	l, ch, na, denSig := d.auth(c, p)
	if !l.OK {
		t.Fatalf("auth: %+v", l.Error)
	}
	sig, _ := hex.DecodeString(denSig)
	if !ed25519.Verify(denPub, consoleSigned(dirDenToApp, ch.Conn, p.ID, d.pub, p.DenKey, ch.Nonce, na, h.s.ckey.identity), sig) {
		t.Error("Den's signature does not verify")
	}
	if !strings.HasPrefix(h.s.ckey.identity, "den:"+bootID()+":") {
		t.Errorf("Den's identity %q", h.s.ckey.identity)
	}
	if l := c.call("console.events", nil); !l.OK {
		t.Fatalf("console.events: %+v", l.Error)
	}
	var ev ConsoleEvent
	if e := c.next(); e.Event != EventConsole || json.Unmarshal(e.Data, &ev) != nil || ev.Kind != "connected" || ev.PID != os.Getpid() {
		t.Errorf("console event %+v", e)
	}
	other := h.dial()
	other.hello()
	if l := other.call("console.events", nil); code(l) != CodeForbidden {
		t.Errorf("events before a proof: %+v", l)
	}
}

// Without Den's key there is no console: hello says nothing, and the ops
// refuse.
func TestNoConsoleWithoutAKey(t *testing.T) {
	h := newHarness(t, 2)
	c := h.dial()
	var info Info
	json.Unmarshal(c.call("hello", map[string]any{"v": 1, "client": "test"}).Result, &info)
	if len(info.Features) != 0 {
		t.Errorf("features %v", info.Features)
	}
	if l := c.call("console.challenge", nil); code(l) != CodeUnavailable {
		t.Errorf("challenge: %+v", l)
	}
}

// One claim per code: a wrong proof spends it, and Den says so.
func TestConsoleWrongProofSpendsTheCode(t *testing.T) {
	h, _ := consoleHarness(t)
	d := newDock(t, h)
	words := h.s.StartConsoleCode()
	c := h.dial()
	c.hello()
	ch := d.challenge(c)
	wrong := append([]string{"wrong"}, words[1:]...)
	if l := c.call("console.claim", map[string]string{"public_key": d.pub, "proof": proof(wrong, ch.ClaimSalt, d.pub)}); code(l) != CodeForbidden || l.Error.Message != ClaimWrong {
		t.Fatalf("a wrong proof: %+v", l)
	}
	if _, live := h.s.ConsoleCodeLive(); live {
		t.Error("the code outlived a wrong proof")
	}
	time.Sleep(challengeEvery)
	ch = d.challenge(c)
	if l := c.call("console.claim", map[string]string{"public_key": d.pub, "proof": proof(words, ch.ClaimSalt, d.pub)}); code(l) != CodeForbidden || l.Error.Message != ClaimSpent {
		t.Errorf("the right code after a wrong one: %+v", l)
	}
	// A claim without a challenge first has no salt.
	c2 := h.dial()
	c2.hello()
	h.s.StartConsoleCode()
	if l := c2.call("console.claim", map[string]string{"public_key": d.pub, "proof": strings.Repeat("0", 64)}); code(l) != CodeBadRequest {
		t.Errorf("no challenge: %+v", l)
	}
}

// A signature is good for its own connection only: Den's, and the dock's.
func TestConsoleSignaturesDoNotReplay(t *testing.T) {
	h, denPub := consoleHarness(t)
	d := newDock(t, h)
	p := d.pair(h.s.StartConsoleCode())
	c1 := h.dial()
	c1.hello()
	l, ch1, na1, denSig1 := d.auth(c1, p)
	if !l.OK {
		t.Fatal(l.Error)
	}
	// Den's signature from c1 onto a new connection with a new dock nonce.
	c2 := h.dial()
	c2.hello()
	ch2 := d.challenge(c2)
	na2 := randHex64()
	sig, _ := hex.DecodeString(denSig1)
	if ed25519.Verify(denPub, consoleSigned(dirDenToApp, ch2.Conn, p.ID, d.pub, p.DenKey, ch2.Nonce, na2, h.s.ckey.identity), sig) {
		t.Error("Den's signature verifies on another connection")
	}
	// The dock's signature from c1, sent on c2.
	dockSig1 := ed25519.Sign(d.priv, consoleSigned(dirAppToDen, ch1.Conn, p.ID, d.pub, p.DenKey, ch1.Nonce, na1, d.identity))
	if l := c2.call("console.auth", map[string]string{"pairing": p.ID, "nonce": na1, "sig": hex.EncodeToString(dockSig1)}); code(l) != CodeForbidden {
		t.Errorf("a replayed dock signature: %+v", l)
	}
	// Den's own direction from the dock is refused.
	c3 := h.dial()
	c3.hello()
	ch3 := d.challenge(c3)
	na3 := randHex64()
	wrongDir := ed25519.Sign(d.priv, consoleSigned(dirDenToApp, ch3.Conn, p.ID, d.pub, p.DenKey, ch3.Nonce, na3, d.identity))
	if l := c3.call("console.auth", map[string]string{"pairing": p.ID, "nonce": na3, "sig": hex.EncodeToString(wrongDir)}); code(l) != CodeForbidden {
		t.Errorf("the wrong direction: %+v", l)
	}
}

// Another program's proof for the console is refused with a card, the
// console hears of it, and three in ten minutes unpair it.
func TestConsoleRefusesOtherPrograms(t *testing.T) {
	h, _ := consoleHarness(t)
	d := newDock(t, h)
	p := d.pair(h.s.StartConsoleCode())
	mine := h.dial()
	mine.hello()
	if l, _, _, _ := d.auth(mine, p); !l.OK {
		t.Fatal(l.Error)
	}
	mine.call("console.events", nil)
	mine.next() // connected
	h.s.setIdentify(func(p Peer) Peer { p.Exe = "/usr/bin/other"; return p })
	for i := 0; i < consoleStrikesMax; i++ {
		c := h.dial()
		c.hello()
		if l, _, _, _ := d.auth(c, p); code(l) != CodeForbidden || l.Error.Message != "another program holds this console" {
			t.Fatalf("another program %d: %+v", i, l)
		}
		if i == 0 {
			// One card per program in the strike window.
			ctlAct(t, h.s, func(a Act) bool { return a.ConsoleIntruder != nil })
		}
		if i < consoleStrikesMax-1 {
			var ev ConsoleEvent
			if e := mine.next(); json.Unmarshal(e.Data, &ev) != nil || ev.Kind != "refused" || ev.Program != "other" {
				t.Errorf("refused event %+v", e)
			}
		}
	}
	if _, ok := h.s.CurrentConsole(); ok {
		t.Error("still paired after three other programs")
	}
	for {
		select {
		case a := <-h.s.Acts():
			if a.ConsoleIntruder != nil {
				t.Fatal("a second intruder card for the same program")
			}
			continue
		default:
		}
		break
	}
}

// A declined claim holds new ones back for consoleCooldown; and one claim
// waits for the human at a time.
func TestConsoleCooldownAndOneAtATime(t *testing.T) {
	h, _ := consoleHarness(t)
	d := newDock(t, h)
	if l, _ := d.claim(h.s.StartConsoleCode(), false); code(l) != CodeForbidden || l.Error.Message != ClaimDenied {
		t.Fatalf("declined: %+v", l)
	}
	c := h.dial()
	c.hello()
	ch := d.challenge(c)
	words := h.s.StartConsoleCode()
	if l := c.call("console.claim", map[string]string{"public_key": d.pub, "proof": proof(words, ch.ClaimSalt, d.pub)}); code(l) != CodeUnavailable || l.Error.Message != ClaimCooldown {
		t.Errorf("within the cooldown: %+v", l)
	}

	h2, _ := consoleHarness(t)
	d2 := newDock(t, h2)
	a := h2.dial()
	a.hello()
	cha := d2.challenge(a)
	first := a.send("console.claim", map[string]string{"public_key": d2.pub, "proof": proof(h2.s.StartConsoleCode(), cha.ClaimSalt, d2.pub)})
	ask := ctlAct(t, h2.s, func(x Act) bool { return x.Console != nil }).Console
	b := h2.dial()
	b.hello()
	chb := d2.challenge(b)
	if l := b.call("console.claim", map[string]string{"public_key": d2.pub, "proof": proof(h2.s.StartConsoleCode(), chb.ClaimSalt, d2.pub)}); code(l) != CodeUnavailable || l.Error.Message != ClaimBusy {
		t.Errorf("a second claim while one waits: %+v", l)
	}
	h2.s.ConsoleAnswered(ask.ID, true)
	if l := a.replyTo(first); !l.OK {
		t.Errorf("the first claim: %+v", l.Error)
	}
}

// A new console replaces the old one, whose connections are told and
// closed.
func TestConsoleReplacesTheOld(t *testing.T) {
	h, _ := consoleHarness(t)
	d := newDock(t, h)
	p1 := d.pair(h.s.StartConsoleCode())
	c := h.dial()
	c.hello()
	d.auth(c, p1)
	c.call("console.events", nil)
	c.next()
	ask := func() *ConsoleAsk {
		cl := h.dial()
		cl.hello()
		ch := d.challenge(cl)
		cl.send("console.claim", map[string]string{"public_key": d.pub, "proof": proof(h.s.StartConsoleCode(), ch.ClaimSalt, d.pub)})
		return ctlAct(t, h.s, func(a Act) bool { return a.Console != nil }).Console
	}()
	if ask.Replaces == nil || ask.Replaces.ID != p1.ID {
		t.Fatalf("the card does not name the console it replaces: %+v", ask)
	}
	h.s.ConsoleAnswered(ask.ID, true)
	var ev ConsoleEvent
	if e := c.next(); json.Unmarshal(e.Data, &ev) != nil || ev.Kind != "unpaired" {
		t.Errorf("the old console was not told: %+v", e)
	}
	if cur, _ := h.s.CurrentConsole(); cur.ID == p1.ID {
		t.Error("the old console stayed")
	}
}

// console.unpair {"all": true} needs no proof, unpairs apps too, and is
// limited for everyone, not per connection.
func TestConsoleUnpairAll(t *testing.T) {
	h, _ := consoleHarness(t)
	d := newDock(t, h)
	d.pair(h.s.StartConsoleCode())
	stranger := h.dial()
	stranger.hello()
	l := stranger.call("console.unpair", map[string]bool{"all": true})
	var r struct{ Unpaired int }
	if !l.OK || json.Unmarshal(l.Result, &r) != nil || r.Unpaired != 1 {
		t.Fatalf("unpair all: %+v %s", l.Error, l.Result)
	}
	if _, ok := h.s.CurrentConsole(); ok {
		t.Error("still paired")
	}
	again := h.dial()
	again.hello()
	if l := again.call("console.unpair", map[string]bool{"all": true}); code(l) != CodeUnavailable {
		t.Errorf("a second unpair all within 10 s, on a new connection: %+v", l)
	}
	if l := again.call("console.unpair", map[string]any{}); code(l) != CodeForbidden {
		t.Errorf("unpair of this console without a proof: %+v", l)
	}
}

// A console's identity is one process in one boot, whatever its path.
func TestConsoleIdentity(t *testing.T) {
	boot := bootID()
	path := Peer{PID: 4242, Exe: "/usr/bin/app-host", Start: 99}
	cases := []struct {
		name string
		a, b Peer
		same bool
	}{
		{"itself", path, path, true},
		{"the same path in another process", path, Peer{PID: 4243, Exe: path.Exe, Start: 99}, false},
		{"the same pid, started again", path, Peer{PID: 4242, Exe: path.Exe, Start: 100}, false},
		{"unreadable vs path", path, Peer{PID: 4242, Exe: unreadableExe(4242, 99), Start: 99}, false},
		{"unreadable itself", Peer{PID: 7, Exe: unreadableExe(7, 5), Start: 5}, Peer{PID: 7, Exe: unreadableExe(7, 5), Start: 5}, true},
	}
	for _, c := range cases {
		a, okA := consoleIdentity(c.a)
		b, okB := consoleIdentity(c.b)
		if !okA || !okB || (a == b) != c.same {
			t.Errorf("%s: %q vs %q", c.name, a, b)
		}
	}
	id, _ := consoleIdentity(path)
	if id != "exe:"+hex.EncodeToString([]byte(path.Exe))+":"+boot+":4242:99" {
		t.Errorf("path form %q", id)
	}
	if id, _ := consoleIdentity(Peer{PID: 1, Exe: "/a\nb:c", Start: 3}); strings.ContainsAny(strings.TrimPrefix(id, "exe:"), "\n") {
		t.Errorf("a newline in a path reached the identity: %q", id)
	}
	for _, p := range []Peer{{PID: 0, Exe: "/x", Start: 1}, {PID: 5, Exe: "/x"}, {PID: 5, Exe: unreadablePrefix + boot + ":conn:ab", Start: 1}} {
		if _, ok := consoleIdentity(p); ok {
			t.Errorf("%+v has an identity", p)
		}
	}
}
