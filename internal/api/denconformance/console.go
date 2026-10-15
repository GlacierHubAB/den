// Copyright (c) 2026 Martin Granström
// SPDX-License-Identifier: MIT
// This file is part of the Den API conformance suite; the MIT licence text
// is in README.md beside it.

package denconformance

import (
	"context"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// ConsoleOptions turns on the console subtests (the console section of
// Den's docs/DREY-API.md: pairing, proof and cards). The suite plays the
// dock: it makes an Ed25519 key pair, claims a pairing code, proves itself
// on connections and answers a card. What only the server's own test can
// do is passed in.
type ConsoleOptions struct {
	// Offer starts a console pairing in the server, as /api console or
	// `den pair` does, and returns its code of eight words. Required.
	Offer func(ctx context.Context) (code string, err error)
	// Confirm stands in for the human's ^Y on the console card the pending
	// claim raised. Left nil, every subtest that needs a confirmed console
	// is skipped.
	Confirm func(ctx context.Context) error
	// Decline stands in for ^N. Left nil, the cooldown subtest is skipped.
	Decline func(ctx context.Context) error
	// Card raises a pending card the dock may answer (dock_answers true,
	// "deny" among its verbs) and returns its id. Left nil, the card
	// subtests are skipped.
	Card func(ctx context.Context) (id string, err error)
	// LongCard raises a pending card like Card's whose body has more than
	// ten lines, for the read timer. Left nil, "card read timer" is skipped.
	LongCard func(ctx context.Context) (id string, err error)
	// View says the server switches views (view.show, view.hide) and can
	// identify its own window, so "view.hide keeps a waiting card" runs:
	// with a card from Card waiting, a hide leaves Den's own window up,
	// counted in shown, with a reason. The subtest really hides the
	// server's windows (and shows them again after): set it only for a
	// test server on a test desktop, never for a Den someone works in.
	// The reply does not name the window kept, so a window that would not
	// move also satisfies shown; the server's own test pins which. Left
	// false, it is skipped.
	View bool
	// ThiefTest and SocketPath turn on "console thief": the suite runs
	// this test binary again (os.Args[0] -test.run=^ThiefTest$), and the
	// copy proves with the console's own key from another process. The
	// server must refuse it and send a refused console event naming its
	// pid. ThiefTest names a test in the server's package whose body is
	// denconformance.ConsoleThiefHelper(t); SocketPath is the server's
	// unix socket. Left empty, the subtest is skipped.
	ThiefTest  string
	SocketPath string
	// Identity is the string the server knows this test process by.
	// Default: "exe:<hex of /proc/self/exe's target>:<boot_id>:<pid>:<start>".
	Identity string
	// DenIdentity returns the server's identity as its signatures name it,
	// from a connection. Default: "den:<boot_id>:<pid>:<start>" of the
	// connection's peer (SO_PEERCRED).
	DenIdentity func(nc net.Conn) (string, error)
}

// ConsoleSubtests are the console subtests, in the order Run runs them
// (after Subtests). They are skipped when Options.Console is nil.
var ConsoleSubtests = []string{
	"console features", "console.challenge", "console.events unproved",
	"console.claim wrong proof", "console.claim", "console.auth",
	"console.events", "card.get", "card.answer", "card read timer", "card replay", "view.hide keeps a waiting card", "console orch.say", "console connections",
	"console.auth refusals", "console thief", "console.unpair unproved", "console.unpair all", "console cooldown",
}

// Wire constants of the console (DREY-API.md, the console section).
const (
	codeForbidden    = "forbidden"
	codeUnavailable  = "unavailable"
	codeConsoleInUse = "console_in_use"
	maxConsoleConns  = 6
)

var (
	hex32Re  = regexp.MustCompile(`^[0-9a-f]{32}$`)
	hex64Re  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	hex128Re = regexp.MustCompile(`^[0-9a-f]{128}$`)
	turnRe   = regexp.MustCompile(`^t-[0-9a-f]{32}$`)
)

// consolePairing is what a confirmed claim leaves for later subtests.
type consolePairing struct {
	pub     ed25519.PublicKey
	priv    ed25519.PrivateKey
	pairing string
	denPub  ed25519.PublicKey
}

func (p *consolePairing) pubHex() string    { return hex.EncodeToString(p.pub) }
func (p *consolePairing) denPubHex() string { return hex.EncodeToString(p.denPub) }

// codeMAC is HMAC-SHA256(HKDF-SHA256(code, salt, info), text), hex.
func codeMAC(code, salt, info, text string) (string, error) {
	k, err := hkdf.Key(sha256.New, []byte(code), []byte(salt), info, sha256.Size)
	if err != nil {
		return "", err
	}
	m := hmac.New(sha256.New, k)
	m.Write([]byte(text))
	return hex.EncodeToString(m.Sum(nil)), nil
}

// signedText is what each side signs to prove a console connection
// (DREY-API.md, the console section).
func signedText(direction, conn, pairing, dockPK, denPK, nd, na, signer string) []byte {
	return []byte(strings.Join([]string{"den-console-v1", direction, conn, pairing, dockPK, denPK, nd, na, signer}, "\n"))
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// procIdentity is "<prefix>:<boot_id>:<pid>:<start>" for pid.
func procIdentity(prefix string, pid int) (string, error) {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", err
	}
	i := strings.LastIndexByte(string(stat), ')')
	if i < 0 {
		return "", errors.New("stat could not be read")
	}
	f := strings.Fields(string(stat[i+1:]))
	if len(f) < 20 {
		return "", errors.New("stat could not be read")
	}
	return prefix + ":" + strings.TrimSpace(string(b)) + ":" + strconv.Itoa(pid) + ":" + f[19], nil
}

// selfIdentity is the server's view of this test process: a readable
// executable, "exe:<hex of the path's bytes>:…".
func selfIdentity() (string, error) {
	exe, err := os.Readlink("/proc/self/exe")
	if err != nil {
		return "", err
	}
	return procIdentity("exe:"+hex.EncodeToString([]byte(exe)), os.Getpid())
}

// peerIdentity is "den:…" of the connection's peer.
func peerIdentity(nc net.Conn) (string, error) {
	sc, ok := nc.(syscall.Conn)
	if !ok {
		return "", errors.New("not a socket: set ConsoleOptions.DenIdentity")
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return "", err
	}
	var cred *syscall.Ucred
	var cerr error
	if err := raw.Control(func(fd uintptr) {
		cred, cerr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return "", err
	}
	if cerr != nil {
		return "", cerr
	}
	return procIdentity("den", int(cred.Pid))
}

// challenge is console.challenge's reply.
type challenge struct {
	Nonce     string `json:"nonce"`
	Conn      string `json:"conn"`
	ClaimSalt string `json:"claim_salt"`
}

func getChallenge(ctx context.Context, c *conn) (challenge, error) {
	// The server takes at most one challenge a second; wait out the rest.
	if wait := time.Second + 50*time.Millisecond - time.Since(c.lastChallenge); !c.lastChallenge.IsZero() && wait > 0 {
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return challenge{}, ctx.Err()
		}
	}
	defer func() { c.lastChallenge = time.Now() }()
	var ch challenge
	if err := c.call(ctx, "console.challenge", struct{}{}, &ch); err != nil {
		return ch, err
	}
	if !hex64Re.MatchString(ch.Nonce) || !hex32Re.MatchString(ch.Conn) || !hex64Re.MatchString(ch.ClaimSalt) {
		return ch, fmt.Errorf("console.challenge: nonce, conn or claim_salt not as pinned: %+v", ch)
	}
	return ch, nil
}

// authReply is console.auth's reply.
type authReply struct {
	Paired bool   `json:"paired"`
	Sig    string `json:"sig"`
}

// prove proves p on c and checks the server's signature; it returns the
// error of console.auth as is.
func (o *ConsoleOptions) prove(ctx context.Context, c *conn, p *consolePairing) error {
	ch, err := getChallenge(ctx, c)
	if err != nil {
		return err
	}
	self, denID, err := o.identities(c)
	if err != nil {
		return err
	}
	na := randHex(32)
	sig := ed25519.Sign(p.priv, signedText("app-to-den", ch.Conn, p.pairing, p.pubHex(), p.denPubHex(), ch.Nonce, na, self))
	var rep authReply
	if err := c.call(ctx, "console.auth", map[string]string{"pairing": p.pairing, "nonce": na, "sig": hex.EncodeToString(sig)}, &rep); err != nil {
		return err
	}
	den, derr := hex.DecodeString(rep.Sig)
	if !rep.Paired || !hex128Re.MatchString(rep.Sig) || derr != nil ||
		!ed25519.Verify(p.denPub, signedText("den-to-app", ch.Conn, p.pairing, p.pubHex(), p.denPubHex(), ch.Nonce, na, denID), den) {
		return fmt.Errorf("console.auth: the server's signature does not verify over the pinned string (signer %q)", denID)
	}
	return nil
}

func (o *ConsoleOptions) identities(c *conn) (self, den string, err error) {
	self = o.Identity
	if self == "" {
		if self, err = selfIdentity(); err != nil {
			return "", "", err
		}
	}
	di := o.DenIdentity
	if di == nil {
		di = peerIdentity
	}
	den, err = di(c.nc)
	return self, den, err
}

func wantCode(t *testing.T, what string, err error, code string) {
	t.Helper()
	if codeOf(err) != code {
		t.Errorf("%s: want %s, got %v", what, code, err)
	}
}

// runConsole adds the console subtests to Run.
func runConsole(t *testing.T, o Options, run func(string, func(*testing.T, context.Context, *client))) {
	co := o.Console
	need := func(t *testing.T, conditions ...bool) {
		t.Helper()
		if co == nil {
			t.Skip("Options.Console is nil")
		}
		for _, ok := range conditions {
			if !ok {
				t.Skip("needs a ConsoleOptions hook that is not set")
			}
		}
	}
	var p *consolePairing // the confirmed console, for later subtests
	var events *conn      // the subscribed connection, kept open
	var cardID string
	t.Cleanup(func() {
		if events != nil {
			events.close()
		}
	})

	run("console features", func(t *testing.T, ctx context.Context, cl *client) {
		need(t)
		c, in, err := open(ctx, o.Dial)
		if err != nil {
			t.Fatal(err)
		}
		c.close()
		if !slices.Contains(in.Features, "console") {
			t.Fatalf(`hello's features %q must include "console"`, in.Features)
		}
	})

	run("console.challenge", func(t *testing.T, ctx context.Context, cl *client) {
		need(t)
		c, _, err := open(ctx, o.Dial)
		if err != nil {
			t.Fatal(err)
		}
		defer c.close()
		a, err := getChallenge(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		b, err := getChallenge(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		if a.Nonce == b.Nonce || a.ClaimSalt == b.ClaimSalt {
			t.Error("console.challenge repeats its nonce or claim_salt")
		}
	})

	run("console.events unproved", func(t *testing.T, ctx context.Context, cl *client) {
		need(t)
		c, _, err := open(ctx, o.Dial)
		if err != nil {
			t.Fatal(err)
		}
		defer c.close()
		wantCode(t, "console.events before a proof", c.call(ctx, "console.events", struct{}{}, nil), codeForbidden)
		wantCode(t, "card.get before a proof", c.call(ctx, "card.get", map[string]string{"id": "i1"}, nil), codeForbidden)
	})

	run("console.claim wrong proof", func(t *testing.T, ctx context.Context, cl *client) {
		need(t, co != nil && co.Offer != nil)
		c, _, err := open(ctx, o.Dial)
		if err != nil {
			t.Fatal(err)
		}
		defer c.close()
		pubA, _, _ := ed25519.GenerateKey(rand.Reader)
		pubB, _, _ := ed25519.GenerateKey(rand.Reader)
		pkA, pkB := hex.EncodeToString(pubA), hex.EncodeToString(pubB)
		for _, tc := range []struct {
			name     string
			code     func(real string) string
			proofKey string
			claimKey string
		}{
			// The proof binds the key: the right code, proved for key A,
			// sent with key B.
			{"a proof for another key", func(real string) string { return real }, pkA, pkB},
			// The proof binds the code.
			{"a proof with the wrong code", func(string) string { return "wrong words here and there too many" }, pkA, pkA},
		} {
			code, err := co.Offer(ctx)
			if err != nil {
				t.Fatal(err)
			}
			ch, err := getChallenge(ctx, c)
			if err != nil {
				t.Fatal(err)
			}
			proof, _ := codeMAC(tc.code(code), ch.ClaimSalt, "den-console-bind-app-v1", "den-console-bind-app-v1\n"+tc.proofKey)
			err = c.call(ctx, "console.claim", map[string]string{"public_key": tc.claimKey, "proof": proof}, nil)
			if codeOf(err) != codeForbidden || !strings.Contains(err.Error(), "wrong code") {
				t.Fatalf(`%s: want forbidden "wrong code", got %v`, tc.name, err)
			}
			// One attempt per code: the right proof now finds the code spent.
			ch, err = getChallenge(ctx, c)
			if err != nil {
				t.Fatal(err)
			}
			right, _ := codeMAC(code, ch.ClaimSalt, "den-console-bind-app-v1", "den-console-bind-app-v1\n"+pkA)
			wantCode(t, tc.name+", then the right proof", c.call(ctx, "console.claim", map[string]string{"public_key": pkA, "proof": right}, nil), codeForbidden)
		}
	})

	run("console.claim", func(t *testing.T, ctx context.Context, cl *client) {
		need(t, co != nil && co.Offer != nil, co != nil && co.Confirm != nil)
		np, err := claimConsole(ctx, o.Dial, co)
		if err != nil {
			t.Fatal(err)
		}
		p = np
	})

	run("console.auth", func(t *testing.T, ctx context.Context, cl *client) {
		need(t, p != nil)
		c, _, err := open(ctx, o.Dial)
		if err != nil {
			t.Fatal(err)
		}
		defer c.close()
		if err := co.prove(ctx, c, p); err != nil {
			t.Fatal(err)
		}
		// A successful proof's nonce and signature, replayed on the same
		// connection: the server's challenge nonce is single use.
		ch, err := getChallenge(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		self, _, err := co.identities(c)
		if err != nil {
			t.Fatal(err)
		}
		na := randHex(32)
		sig := hex.EncodeToString(ed25519.Sign(p.priv, signedText("app-to-den", ch.Conn, p.pairing, p.pubHex(), p.denPubHex(), ch.Nonce, na, self)))
		if err := c.call(ctx, "console.auth", map[string]string{"pairing": p.pairing, "nonce": na, "sig": sig}, nil); err != nil {
			t.Fatal(err)
		}
		if err := c.call(ctx, "console.auth", map[string]string{"pairing": p.pairing, "nonce": na, "sig": sig}, nil); err == nil {
			t.Error("console.auth accepted a replay of the last proof on the same connection")
		}
	})

	run("console.events", func(t *testing.T, ctx context.Context, cl *client) {
		need(t, p != nil)
		c, _, err := open(ctx, o.Dial)
		if err != nil {
			t.Fatal(err)
		}
		if err := co.prove(ctx, c, p); err != nil {
			c.close()
			t.Fatal(err)
		}
		if err := c.call(ctx, "console.events", struct{}{}, nil); err != nil {
			c.close()
			t.Fatal(err)
		}
		events = c
		ev, err := nextEvent(ctx, c, "console")
		if err != nil {
			t.Fatal(err)
		}
		var d struct {
			Kind       string `json:"kind"`
			PID        int    `json:"pid"`
			StartTicks uint64 `json:"start_ticks"`
			Started    string `json:"started"`
		}
		if err := json.Unmarshal(ev, &d); err != nil {
			t.Fatal(err)
		}
		self, _, _ := co.identities(c)
		parts := strings.Split(self, ":")
		if (d.Kind != "connected" && d.Kind != "restored") || strconv.Itoa(d.PID) != parts[len(parts)-2] ||
			strconv.FormatUint(d.StartTicks, 10) != parts[len(parts)-1] || (d.Started != "" && len(d.Started) != 8) {
			t.Fatalf("the console event about this console: %+v (identity %s)", d, self)
		}
	})

	run("card.get", func(t *testing.T, ctx context.Context, cl *client) {
		need(t, events != nil, co != nil && co.Card != nil)
		id, err := co.Card(ctx)
		if err != nil {
			t.Fatal(err)
		}
		cardID = id
		rec, err := cardGet(ctx, events, id)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(rec.Kind + "\n" + rec.Agent + "\n" + rec.Tool + "\n" + rec.Title + "\n" + rec.Note + "\n" + rec.Body))
		if rec.TextSHA256 != hex.EncodeToString(sum[:]) {
			t.Error("card.get's text_sha256 is not SHA-256(kind\\nagent\\ntool\\ntitle\\nnote\\nbody)")
		}
		for _, f := range []string{rec.Kind, rec.Agent, rec.Tool, rec.Title, rec.Note} {
			if strings.ContainsAny(f, "\n\r") {
				t.Errorf("kind, agent, tool, title and note must be single lines: %q", f)
			}
		}
		if !hex64Re.MatchString(rec.Nonce) || !rec.DockAnswers || !slices.Contains(rec.Verbs, "deny") {
			t.Errorf("card.get: nonce, dock_answers or verbs not as pinned: %+v", rec)
		}
		for _, v := range rec.Verbs {
			if v != "allow" && v != "deny" && v != "always" {
				t.Errorf("card.get: verb %q is not allow, deny or always", v)
			}
		}
		if rec.Truncated && (rec.CutAt < 0 || rec.CutAt > strings.Count(rec.Body, "\n")+1) {
			t.Errorf("card.get: cut_at %d outside the body", rec.CutAt)
		}
		wantCode(t, "card.get of no such card", events.call(ctx, "card.get", map[string]string{"id": "i999999999"}, nil), CodeNotFound)
	})

	run("card.answer", func(t *testing.T, ctx context.Context, cl *client) {
		need(t, events != nil, cardID != "")
		rec, err := cardGet(ctx, events, cardID)
		if err != nil {
			t.Fatal(err)
		}
		wait := time.Duration(0)
		if strings.Count(rec.Body, "\n")+1 > 10 {
			wait = 350 * time.Millisecond // past the read timer, counted from the first card.get
		}
		time.Sleep(wait)
		answer := func(c *conn, hash, nonce string) error {
			return c.call(ctx, "card.answer", map[string]string{"id": cardID, "text_sha256": hash, "nonce": nonce, "verb": "deny"}, nil)
		}
		wantCode(t, "card.answer with another hash", answer(events, randHex(32), rec.Nonce), "card_changed")
		// A nonce is bound to the connection it was fetched on.
		other, _, err := open(ctx, o.Dial)
		if err != nil {
			t.Fatal(err)
		}
		defer other.close()
		if err := co.prove(ctx, other, p); err != nil {
			t.Fatal(err)
		}
		wantCode(t, "card.answer on another connection", answer(other, rec.TextSHA256, rec.Nonce), "nonce_expired")
		rec, err = cardGet(ctx, events, cardID)
		if err != nil {
			t.Fatal(err)
		}
		var res struct {
			Result string `json:"result"`
			Note   string `json:"note"`
		}
		if err := events.call(ctx, "card.answer", map[string]string{"id": cardID, "text_sha256": rec.TextSHA256, "nonce": rec.Nonce, "verb": "deny"}, &res); err != nil {
			t.Fatal(err)
		}
		if res.Result != "denied" || res.Note == "" {
			t.Errorf("card.answer's reply: %+v", res)
		}
		// Answered by this connection: result denied, by console.
		if d, err := nextCardEvent(ctx, events, cardID, "settled"); err != nil {
			t.Errorf("after the answer: %v", err)
		} else {
			checkSettled(t, d, "denied", "console")
		}
		if err := answer(events, rec.TextSHA256, rec.Nonce); err == nil {
			t.Error("a nonce answered twice")
		}
	})

	run("card read timer", func(t *testing.T, ctx context.Context, cl *client) {
		need(t, events != nil, co != nil && co.LongCard != nil)
		id, err := co.LongCard(ctx)
		if err != nil {
			t.Fatal(err)
		}
		rec, err := cardGet(ctx, events, id)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(rec.Body, "\n")+1 <= 10 {
			t.Fatalf("LongCard's body has %d lines; the read timer needs more than ten", strings.Count(rec.Body, "\n")+1)
		}
		ans := func(r cardRecord) error {
			return events.call(ctx, "card.answer", map[string]string{"id": id, "text_sha256": r.TextSHA256, "nonce": r.Nonce, "verb": "deny"}, nil)
		}
		wantCode(t, "an answer at once", ans(rec), "read_it_first")
		time.Sleep(350 * time.Millisecond)
		// The timer counts from the first card.get, so a fresh fetch now
		// may be answered at once.
		if rec, err = cardGet(ctx, events, id); err != nil {
			t.Fatal(err)
		}
		if err := ans(rec); err != nil {
			t.Fatalf("an answer 350 ms after the first card.get: %v", err)
		}
		if d, err := nextCardEvent(ctx, events, id, "settled"); err != nil {
			t.Fatal(err)
		} else {
			checkSettled(t, d, "denied", "console")
		}
	})

	run("card replay", func(t *testing.T, ctx context.Context, cl *client) {
		need(t, p != nil, co != nil && co.Card != nil)
		a, err := co.Card(ctx)
		if err != nil {
			t.Fatal(err)
		}
		b, err := co.Card(ctx)
		if err != nil {
			t.Fatal(err)
		}
		c, _, err := open(ctx, o.Dial)
		if err != nil {
			t.Fatal(err)
		}
		defer c.close()
		if err := co.prove(ctx, c, p); err != nil {
			t.Fatal(err)
		}
		var sub struct {
			Cards *[]string `json:"cards"`
		}
		if err := c.call(ctx, "console.events", struct{}{}, &sub); err != nil {
			t.Fatal(err)
		}
		if sub.Cards == nil {
			t.Fatal("console.events' reply has no cards list")
		}
		listed := map[string]bool{}
		for _, id := range *sub.Cards {
			if listed[id] {
				t.Errorf("console.events lists %s twice", id)
			}
			listed[id] = true
		}
		if !listed[a] || !listed[b] {
			t.Fatalf("console.events' cards %v miss the waiting cards %s and %s", *sub.Cards, a, b)
		}
		if dl, ok := ctx.Deadline(); ok {
			_ = c.nc.SetDeadline(dl)
		}
		// Then the connected event, then one card event per waiting card
		// (change "new"), exactly the listed set, before anything else.
		l, err := c.read()
		if err != nil {
			t.Fatal(err)
		}
		if l.Event != "console" {
			t.Fatalf("the first event after console.events: want console, got %q", l.Event)
		}
		want := map[string]bool{}
		for id := range listed {
			want[id] = true
		}
		for len(want) > 0 {
			l, err := c.read()
			if err != nil {
				t.Fatalf("waiting for the replay of %v: %v", want, err)
			}
			if l.Event == "" {
				continue
			}
			var d struct {
				ID         string `json:"id"`
				Change     string `json:"change"`
				TextSHA256 string `json:"text_sha256"`
			}
			_ = json.Unmarshal(l.Data, &d)
			if l.Event != "card" || d.Change != "new" || !want[d.ID] {
				t.Fatalf("before the replay of the listed cards %v ended: %s %s", want, l.Event, l.Data)
			}
			if !hex64Re.MatchString(d.TextSHA256) {
				t.Errorf("replayed card %s: text_sha256 %q", d.ID, d.TextSHA256)
			}
			delete(want, d.ID)
		}
		// Settle them, so later subtests see no waiting cards of ours.
		for _, id := range []string{a, b} {
			if rec, err := cardGet(ctx, c, id); err == nil {
				_ = c.call(ctx, "card.answer", map[string]string{"id": id, "text_sha256": rec.TextSHA256, "nonce": rec.Nonce, "verb": "deny"}, nil)
			}
		}
	})

	// view.hide never takes a waiting card out of sight: Den's own window
	// stays up, counted in shown, and reason says why (Den's DREY-API.md,
	// "What view.hide leaves visible").
	run("view.hide keeps a waiting card", func(t *testing.T, ctx context.Context, cl *client) {
		need(t, events != nil, co != nil && co.Card != nil, co != nil && co.View)
		id, err := co.Card(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if rec, err := cardGet(ctx, events, id); err == nil {
				_ = events.call(ctx, "card.answer", map[string]string{"id": id, "text_sha256": rec.TextSHA256, "nonce": rec.Nonce, "verb": "deny"}, nil)
			}
		}()
		type viewResult struct {
			View   string `json:"view"`
			Shown  int    `json:"shown"`
			Total  int    `json:"total"`
			Reason string `json:"reason"`
		}
		var res viewResult
		if err := events.call(ctx, "view.hide", nil, &res); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = events.call(ctx, "view.show", nil, nil) }()
		if res.View != "user" || res.Total < 1 || res.Shown < 1 || res.Shown > res.Total || res.Reason == "" {
			t.Errorf("view.hide with a card waiting: %+v; want view user, Den's own window counted in shown, and a reason", res)
		}
	})

	// A console speaks to the orchestrator as a paired app does
	// (DREY-API.md, "Speaking to the orchestrator"): orch.say needs
	// pair.auth or console.auth, one app turn runs on the machine at a
	// time (else unavailable, not a queue), every chunk carries its turn,
	// and closing the connection ends the turn. A Den from before console
	// turns (forbidden on a proved console) lists it in Options.Skip.
	run("console orch.say", func(t *testing.T, ctx context.Context, cl *client) {
		need(t, p != nil)
		bare, in, err := open(ctx, o.Dial)
		if err != nil {
			t.Fatal(err)
		}
		defer bare.close()
		if in.Control != "on" {
			t.Skipf("hello's control is %q: orch.say is refused until control is on", in.Control)
		}
		const text = "Say hello to the console, in a few plain words, and name nothing that runs."
		wantCode(t, "orch.say before any proof", bare.call(ctx, "orch.say", map[string]string{"text": text}, nil), codeForbidden)

		proved := func() *conn {
			t.Helper()
			c, _, err := open(ctx, o.Dial)
			if err != nil {
				t.Fatal(err)
			}
			if err := co.prove(ctx, c, p); err != nil {
				c.close()
				t.Fatal(err)
			}
			return c
		}
		var turn struct {
			Turn string `json:"turn"`
		}
		c := proved()
		defer c.close()
		if err := c.call(ctx, "orch.say", map[string]string{"text": text}, &turn); err != nil {
			t.Fatalf("orch.say on a proved console: %v", err)
		}
		if !turnRe.MatchString(turn.Turn) {
			t.Fatalf("orch.say's turn %q is not t- and 32 hex digits", turn.Turn)
		}
		// One app turn on the machine: a second, from another proved
		// connection of the same console, is refused while it runs.
		c2 := proved()
		wantCode(t, "a second orch.say while a turn runs", c2.call(ctx, "orch.say", map[string]string{"text": text}, nil), codeUnavailable)
		c2.close()
		for {
			raw, err := nextEvent(ctx, c, "orch")
			if err != nil {
				t.Fatalf("the turn's orch events: %v", err)
			}
			var ch struct {
				Type string `json:"type"`
				Turn string `json:"turn"`
			}
			if err := json.Unmarshal(raw, &ch); err != nil {
				t.Fatalf("an orch event: %s", raw)
			}
			if ch.Turn != turn.Turn {
				t.Fatalf("an orch event for turn %q during turn %q: %s", ch.Turn, turn.Turn, raw)
			}
			if !slices.Contains([]string{"text", "status", "needs_approval", "done", "error"}, ch.Type) {
				t.Fatalf("an orch event of type %q: %s", ch.Type, raw)
			}
			if ch.Type == "done" || ch.Type == "error" {
				break
			}
		}
		// Closing the connection ends the turn: soon after, another
		// console connection may start one. Two tries at most, so the
		// console's six a minute hold.
		// Den up to v0.5.2 clears its running turn just after it sends
		// that turn's done, so a say sent on done may meet unavailable
		// once; it is retried once (den-lead's guidance).
		c3 := proved()
		err = c3.call(ctx, "orch.say", map[string]string{"text": text}, &turn)
		if codeOf(err) == codeUnavailable {
			time.Sleep(200 * time.Millisecond)
			err = c3.call(ctx, "orch.say", map[string]string{"text": text}, &turn)
		}
		if err != nil {
			t.Fatalf("orch.say after the first turn ended: %v", err)
		}
		c3.close()
		c4 := proved()
		defer c4.close()
		var after error
		for try := 0; try < 2; try++ {
			time.Sleep(time.Duration(500+1500*try) * time.Millisecond)
			if after = c4.call(ctx, "orch.say", map[string]string{"text": text}, &turn); codeOf(after) != codeUnavailable {
				break
			}
		}
		if after != nil {
			t.Fatalf("orch.say after the asking connection closed: %v; closing it must end its turn", after)
		}
	})

	run("console connections", func(t *testing.T, ctx context.Context, cl *client) {
		need(t, p != nil, events != nil)
		// Connections earlier subtests closed may still count for a moment,
		// until the server reads their end; let them drain.
		time.Sleep(300 * time.Millisecond)
		// events is one proved connection; open proved ones up to the cap.
		var held []*conn
		defer func() {
			for _, c := range held {
				c.close()
			}
		}()
		for len(held) < maxConsoleConns-1 {
			c, _, err := open(ctx, o.Dial)
			if err != nil {
				t.Fatal(err)
			}
			held = append(held, c)
			if err := co.prove(ctx, c, p); err != nil {
				t.Fatalf("proof %d of %d: %v", len(held)+1, maxConsoleConns, err)
			}
		}
		c, _, err := open(ctx, o.Dial)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, c)
		wantCode(t, fmt.Sprintf("proof %d", maxConsoleConns+1), co.prove(ctx, c, p), codeConsoleInUse)
	})

	// Refused proofs count against a console: three wrong signatures, or
	// proofs from another process, in ten minutes unpair it. So these run
	// late, each group on a fresh pairing, with at most two failures each.
	run("console.auth refusals", func(t *testing.T, ctx context.Context, cl *client) {
		need(t, co != nil && co.Offer != nil, co != nil && co.Confirm != nil)
		_, otherKey, _ := ed25519.GenerateKey(rand.Reader)
		type refusal struct {
			name string
			mk   func(p *consolePairing, ch challenge, self string) (na, sig string)
		}
		sign := func(key ed25519.PrivateKey, dir, conn string, p *consolePairing, ch challenge, na, self string) string {
			return hex.EncodeToString(ed25519.Sign(key, signedText(dir, conn, p.pairing, p.pubHex(), p.denPubHex(), ch.Nonce, na, self)))
		}
		groups := [][]refusal{
			{
				{"the den-to-app label", func(p *consolePairing, ch challenge, self string) (string, string) {
					na := randHex(32)
					return na, sign(p.priv, "den-to-app", ch.Conn, p, ch, na, self)
				}},
				{"another connection", func(p *consolePairing, ch challenge, self string) (string, string) {
					na := randHex(32)
					return na, sign(p.priv, "app-to-den", randHex(16), p, ch, na, self)
				}},
			},
			{
				{"another key", func(p *consolePairing, ch challenge, self string) (string, string) {
					na := randHex(32)
					return na, sign(otherKey, "app-to-den", ch.Conn, p, ch, na, self)
				}},
				{"a short nonce", func(p *consolePairing, ch challenge, self string) (string, string) {
					na := randHex(16)
					return na, sign(p.priv, "app-to-den", ch.Conn, p, ch, na, self)
				}},
			},
		}
		for _, group := range groups {
			gp, err := claimConsole(ctx, o.Dial, co)
			if err != nil {
				t.Fatal(err)
			}
			p = gp
			for _, r := range group {
				c, _, err := open(ctx, o.Dial)
				if err != nil {
					t.Fatal(err)
				}
				ch, err := getChallenge(ctx, c)
				if err != nil {
					t.Fatal(err)
				}
				self, _, err := co.identities(c)
				if err != nil {
					t.Fatal(err)
				}
				na, sig := r.mk(gp, ch, self)
				if err := c.call(ctx, "console.auth", map[string]string{"pairing": gp.pairing, "nonce": na, "sig": sig}, nil); err == nil {
					t.Errorf("console.auth accepted %s", r.name)
				}
				// The refusal had effect: the connection is unproved, or the
				// server closed it.
				if err := c.call(ctx, "console.events", struct{}{}, nil); !refusedOrClosed(err, codeForbidden) {
					t.Errorf("%s: console.events after the refused proof: want forbidden or a closed connection, got %v", r.name, err)
				}
				c.close()
			}
		}
	})

	run("console thief", func(t *testing.T, ctx context.Context, cl *client) {
		need(t, co != nil && co.Offer != nil, co != nil && co.Confirm != nil, co != nil && co.ThiefTest != "" && co.SocketPath != "")
		// A fresh console, and a connection subscribed to its events.
		tp, err := claimConsole(ctx, o.Dial, co)
		if err != nil {
			t.Fatal(err)
		}
		p = tp
		ec, _, err := open(ctx, o.Dial)
		if err != nil {
			t.Fatal(err)
		}
		if err := co.prove(ctx, ec, tp); err != nil {
			ec.close()
			t.Fatal(err)
		}
		if err := ec.call(ctx, "console.events", struct{}{}, nil); err != nil {
			ec.close()
			t.Fatal(err)
		}
		if events != nil {
			events.close()
		}
		events = ec
		if _, err := nextEvent(ctx, ec, "console"); err != nil { // connected, about us
			t.Fatal(err)
		}
		self, _, err := co.identities(ec)
		if err != nil {
			t.Fatal(err)
		}
		//nolint:gosec // re-running this test binary is the point; ThiefTest is the server's own test name, from its own Options
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+co.ThiefTest+"$", "-test.v")
		cmd.Env = append(os.Environ(),
			"DENCONFORMANCE_THIEF_AS="+self, // the real console's identity, public in Den's header
			"DENCONFORMANCE_THIEF_SOCK="+co.SocketPath,
			"DENCONFORMANCE_THIEF_PAIRING="+tp.pairing,
			"DENCONFORMANCE_THIEF_KEY="+hex.EncodeToString(tp.priv), // a test key only
			"DENCONFORMANCE_THIEF_DEN="+tp.denPubHex())
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("the thief process failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "thief refused") {
			t.Fatalf("the thief process did not report its refusal:\n%s", out)
		}
		ev, err := nextEvent(ctx, ec, "console")
		if err != nil {
			t.Fatal(err)
		}
		var d struct {
			Kind string `json:"kind"`
			PID  int    `json:"pid"`
		}
		_ = json.Unmarshal(ev, &d)
		if d.Kind != "refused" || d.PID != cmd.Process.Pid {
			t.Fatalf("after the thief: want a refused console event with pid %d, got %s", cmd.Process.Pid, ev)
		}
	})

	run("console.unpair unproved", func(t *testing.T, ctx context.Context, cl *client) {
		need(t)
		c, _, err := open(ctx, o.Dial)
		if err != nil {
			t.Fatal(err)
		}
		defer c.close()
		wantCode(t, "console.unpair {} without a proof", c.call(ctx, "console.unpair", struct{}{}, nil), codeForbidden)
	})

	run("console.unpair all", func(t *testing.T, ctx context.Context, cl *client) {
		need(t, p != nil)
		c, _, err := open(ctx, o.Dial) // hello'd, never proved
		if err != nil {
			t.Fatal(err)
		}
		defer c.close()
		var rep struct {
			Unpaired int `json:"unpaired"`
		}
		if err := c.call(ctx, "console.unpair", map[string]bool{"all": true}, &rep); err != nil {
			t.Fatalf("console.unpair {all} without a proof: %v", err)
		}
		if rep.Unpaired < 1 {
			t.Errorf("console.unpair {all}: unpaired %d, want at least the console", rep.Unpaired)
		}
		err = c.call(ctx, "console.unpair", map[string]bool{"all": true}, nil)
		if codeOf(err) != codeUnavailable || !strings.Contains(err.Error(), "too many unpairs") {
			t.Errorf(`a second unpair {all} within 10 s: want unavailable "too many unpairs", got %v`, err)
		}
		c2, _, err := open(ctx, o.Dial)
		if err != nil {
			t.Fatal(err)
		}
		defer c2.close()
		if err := co.prove(ctx, c2, p); err == nil {
			t.Error("the console still proves after console.unpair {all}")
		}
		p = nil
	})

	run("console cooldown", func(t *testing.T, ctx context.Context, cl *client) {
		need(t, co != nil && co.Offer != nil, co != nil && co.Decline != nil)
		claim := func(code string) (chan error, *conn) {
			c, _, err := open(ctx, o.Dial)
			if err != nil {
				t.Fatal(err)
			}
			ch, err := getChallenge(ctx, c)
			if err != nil {
				t.Fatal(err)
			}
			pub, _, _ := ed25519.GenerateKey(rand.Reader)
			pk := hex.EncodeToString(pub)
			proof, _ := codeMAC(code, ch.ClaimSalt, "den-console-bind-app-v1", "den-console-bind-app-v1\n"+pk)
			done := make(chan error, 1)
			go func() { done <- c.call(ctx, "console.claim", map[string]string{"public_key": pk, "proof": proof}, nil) }()
			return done, c
		}
		code, err := co.Offer(ctx)
		if err != nil {
			t.Fatal(err)
		}
		done, c := claim(code)
		defer c.close()
		if err := co.Decline(ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-done; codeOf(err) != codeForbidden || !strings.Contains(err.Error(), "denied in Den") {
			t.Fatalf(`a declined claim: want forbidden "denied in Den", got %v`, err)
		}
		code, err = co.Offer(ctx)
		if err != nil {
			t.Skipf("the server refused a new offer during the cooldown: %v", err)
		}
		done, c2 := claim(code)
		defer c2.close()
		err = <-done
		if codeOf(err) != codeUnavailable || !strings.Contains(err.Error(), "wait: a pairing was just declined") {
			t.Errorf(`a claim within 30 s of a decline: want unavailable "wait: a pairing was just declined", got %v`, err)
		}
	})
}

// claimConsole pairs a new console: Offer, a claim with the code proof,
// Confirm, and the bind checked. A new console replaces the old one.
func claimConsole(ctx context.Context, dial Dialer, co *ConsoleOptions) (*consolePairing, error) {
	code, err := co.Offer(ctx)
	if err != nil {
		return nil, err
	}
	c, _, err := open(ctx, dial)
	if err != nil {
		return nil, err
	}
	defer c.close()
	ch, err := getChallenge(ctx, c)
	if err != nil {
		return nil, err
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	pk := hex.EncodeToString(pub)
	proof, _ := codeMAC(code, ch.ClaimSalt, "den-console-bind-app-v1", "den-console-bind-app-v1\n"+pk)
	var rep struct {
		Pairing      string `json:"pairing"`
		DenPublicKey string `json:"den_public_key"`
		Bind         string `json:"bind"`
		Console      bool   `json:"console"`
	}
	done := make(chan error, 1)
	go func() {
		done <- c.call(ctx, "console.claim", map[string]string{"public_key": pk, "proof": proof}, &rep)
	}()
	if err := co.Confirm(ctx); err != nil {
		return nil, err
	}
	if err := <-done; err != nil {
		return nil, err
	}
	if !hex32Re.MatchString(rep.Pairing) || !hex64Re.MatchString(rep.DenPublicKey) || !hex64Re.MatchString(rep.Bind) || !rep.Console {
		return nil, fmt.Errorf("console.claim's reply not as pinned: %+v", rep)
	}
	want, _ := codeMAC(code, rep.Pairing, "den-console-bind-v1", "den-console-bind-v1\n"+pk+"\n"+rep.DenPublicKey+"\n"+rep.Pairing)
	if !hmac.Equal([]byte(rep.Bind), []byte(want)) {
		return nil, errors.New("console.claim's bind does not match HMAC(HKDF(code, pairing, den-console-bind-v1), …)")
	}
	den, _ := hex.DecodeString(rep.DenPublicKey)
	return &consolePairing{pub: pub, priv: priv, pairing: rep.Pairing, denPub: den}, nil
}

// refusedOrClosed reports whether err is a refusal with code, or the
// server having closed the connection (it closes after a wrong signature).
func refusedOrClosed(err error, code string) bool {
	return codeOf(err) == code || (err != nil && codeOf(err) == "")
}

// cardRecord is card.get's reply (DREY-API.md, card.get).
type cardRecord struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Agent       string   `json:"agent"`
	Tool        string   `json:"tool"`
	Title       string   `json:"title"`
	Note        string   `json:"note"`
	Body        string   `json:"body"`
	TextSHA256  string   `json:"text_sha256"`
	Truncated   bool     `json:"truncated"`
	CutAt       int      `json:"cut_at"`
	Nonce       string   `json:"nonce"`
	Verbs       []string `json:"verbs"`
	DockAnswers bool     `json:"dock_answers"`
}

func cardGet(ctx context.Context, c *conn, id string) (cardRecord, error) {
	var rec cardRecord
	err := c.call(ctx, "card.get", map[string]string{"id": id}, &rec)
	if err == nil && rec.ID != id {
		err = fmt.Errorf("card.get %s answered for %q", id, rec.ID)
	}
	return rec, err
}

// cardEvent is a card event's data (DREY-API.md, console.events; result
// and by come with settled, Den console-1 7ffcafe).
type cardEvent struct {
	ID         string  `json:"id"`
	Change     string  `json:"change"`
	TextSHA256 *string `json:"text_sha256"`
	Result     string  `json:"result"`
	By         *string `json:"by"`
}

// nextCardEvent reads c until the card event for id with change,
// skipping other events (live "new" events for other cards among them).
func nextCardEvent(ctx context.Context, c *conn, id, change string) (cardEvent, error) {
	for {
		ev, err := nextEvent(ctx, c, "card")
		if err != nil {
			return cardEvent{}, err
		}
		var d cardEvent
		_ = json.Unmarshal(ev, &d)
		if d.ID == id && d.Change == change {
			return d, nil
		}
	}
}

// checkSettled checks a settled event: no text_sha256, result from its
// closed set, by den or console unless the card is gone, and the result
// and by expected (when given).
func checkSettled(t *testing.T, d cardEvent, wantResult, wantBy string) {
	t.Helper()
	if d.TextSHA256 != nil {
		t.Errorf("settled event for %s carries text_sha256", d.ID)
	}
	switch {
	case d.Result == "gone" && d.By == nil:
	case (d.Result == "allowed" || d.Result == "denied" || d.Result == "always") && d.By != nil && (*d.By == "den" || *d.By == "console"):
	default:
		t.Errorf("settled event for %s: result %q, by %v, outside the closed sets", d.ID, d.Result, d.By)
		return
	}
	by := ""
	if d.By != nil {
		by = *d.By
	}
	if (wantResult != "" && d.Result != wantResult) || (wantBy != "" && by != wantBy) {
		t.Errorf("settled event for %s: result %q by %q, want %q by %q", d.ID, d.Result, by, wantResult, wantBy)
	}
}

// nextEvent reads c until an event named name, skipping others.
func nextEvent(ctx context.Context, c *conn, name string) (json.RawMessage, error) {
	if dl, ok := ctx.Deadline(); ok {
		_ = c.nc.SetDeadline(dl)
	}
	for {
		l, err := c.read()
		if err != nil {
			return nil, fmt.Errorf("waiting for a %s event: %w", name, err)
		}
		if l.Event == name {
			return l.Data, nil
		}
	}
}

// ConsoleThiefHelper is the body of the test ConsoleOptions.ThiefTest
// names. Run normally it skips; run by the "console thief" subtest, in
// another process, it proves with the console's key and its own identity,
// and passes only when the server refuses.
func ConsoleThiefHelper(t *testing.T) {
	sock := os.Getenv("DENCONFORMANCE_THIEF_SOCK")
	if sock == "" {
		t.Skip("run by the console thief subtest only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	priv, _ := hex.DecodeString(os.Getenv("DENCONFORMANCE_THIEF_KEY"))
	den, _ := hex.DecodeString(os.Getenv("DENCONFORMANCE_THIEF_DEN"))
	if len(priv) != ed25519.PrivateKeySize || len(den) != ed25519.PublicKeySize {
		t.Fatal("the thief's key or Den's key could not be read")
	}
	if os.Getenv("DENCONFORMANCE_THIEF_AS") == "" {
		t.Fatal("DENCONFORMANCE_THIEF_AS is missing: the thief must first sign as the real console")
	}
	p := &consolePairing{priv: priv, pub: ed25519.PrivateKey(priv).Public().(ed25519.PublicKey), pairing: os.Getenv("DENCONFORMANCE_THIEF_PAIRING"), denPub: den}
	dial := func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}
	// First exactly what the real console would sign, its identity
	// included (it is public): only a server that checks the peer's own
	// identity refuses this. Then with the thief's own identity.
	for _, as := range []string{os.Getenv("DENCONFORMANCE_THIEF_AS"), ""} {
		c, _, err := open(ctx, dial)
		if err != nil {
			t.Fatal(err)
		}
		co := &ConsoleOptions{Identity: as}
		err = co.prove(ctx, c, p)
		c.close()
		if codeOf(err) != codeForbidden {
			t.Fatalf("a proof with the console's key from another process, signed as %q: want forbidden, got %v", as, err)
		}
	}
	fmt.Println("thief refused")
}
