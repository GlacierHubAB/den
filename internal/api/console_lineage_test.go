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

// The human's answer about the mirror belongs to the pairing's lineage: a
// reconnect Den accepts (the same dock service, restarted on this boot,
// read clean) keeps it, whatever key the restarted dock brings; a refused
// reconnect never takes it; a fresh console.request starts a lineage of
// its own, not asked yet.
func TestMirrorConsentFollowsTheLineage(t *testing.T) {
	h := alwaysHarness(t)
	fakeUnit(t, testVerifier(), nil)
	d := newDock(t, h)
	requestPaired(t, h, d, "")
	first, ok := h.s.CurrentConsole()
	if !ok || !isHex(first.Lineage, 32) || first.Mirror != "" {
		t.Fatalf("a new pairing: %+v", first)
	}
	if h.s.SetMirrorConsent("not-the-lineage", true) {
		t.Error("an answer for another lineage was taken")
	}
	if !h.s.SetMirrorConsent(first.Lineage, true) {
		t.Fatal("the answer was not taken")
	}
	yes, _ := h.s.CurrentConsole()
	if yes.Mirror != MirrorYes || yes.MirrorSince.IsZero() {
		t.Fatalf("after yes: %+v", yes)
	}
	// The dock restarts, with a fresh key as S1 has it: same lineage, the
	// yes kept from when it was given.
	d2 := d.restarted()
	c, id := reconnect(t, h, d2, "")
	if l := c.replyTo(id); !l.OK {
		t.Fatalf("reconnect: %+v", l.Error)
	}
	again, _ := h.s.CurrentConsole()
	if again.ID == first.ID || again.Lineage != first.Lineage || again.Mirror != MirrorYes || !again.MirrorSince.Equal(yes.MirrorSince) {
		t.Fatalf("after a reconnect: %+v (was %+v)", again, yes)
	}
	// A reconnect that fails the verified-unit check pairs nothing and
	// inherits nothing: the console and its lineage stay as they were.
	fakeUnit(t, testVerifier(), sysd.Problems{"the connection is not the unit's main process"})
	time.Sleep(requestEvery)
	c, id = reconnect(t, h, d2.restarted(), "")
	if l := c.replyTo(id); code(l) != CodeForbidden {
		t.Fatalf("an intruder's reconnect: %+v", l.Error)
	}
	if cur, _ := h.s.CurrentConsole(); cur.ID != again.ID || cur.Lineage != first.Lineage {
		t.Errorf("after a refused reconnect: %+v", cur)
	}
	// A no is kept the same way.
	fakeUnit(t, testVerifier(), nil)
	h.s.SetMirrorConsent(first.Lineage, false)
	time.Sleep(requestEvery)
	d3 := d2.restarted()
	c, id = reconnect(t, h, d3, "")
	if l := c.replyTo(id); !l.OK {
		t.Fatalf("reconnect: %+v", l.Error)
	}
	if cur, _ := h.s.CurrentConsole(); cur.Lineage != first.Lineage || cur.Mirror != MirrorNo || !cur.MirrorSince.IsZero() {
		t.Errorf("a no after a reconnect: %+v", cur)
	}
	// Forgotten, Den asks again.
	if !h.s.ForgetMirrorConsent(first.Lineage) {
		t.Fatal("forget")
	}
	if cur, _ := h.s.CurrentConsole(); cur.Mirror != "" {
		t.Errorf("after forgetting: %+v", cur)
	}
	// A fresh console.request is a new pairing: a lineage of its own.
	h.s.SetMirrorConsent(first.Lineage, true)
	time.Sleep(requestEvery)
	requestPaired(t, h, d3.restarted(), "")
	if cur, _ := h.s.CurrentConsole(); cur.Lineage == first.Lineage || !isHex(cur.Lineage, 32) || cur.Mirror != "" {
		t.Errorf("a fresh request: %+v", cur)
	}
}

// The answer is kept in the console's record, under its hash and the clean
// mark: it comes back after Den's restart with the console, and a record
// changed while Den was stopped drops the console and the answer with it.
func TestMirrorConsentKeptInTheRecord(t *testing.T) {
	store := filepath.Join(t.TempDir(), "consoles.json")
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	h := alwaysStore(t, store, priv)
	fakeUnit(t, testVerifier(), nil)
	d := newDock(t, h)
	requestPaired(t, h, d, "")
	cur, _ := h.s.CurrentConsole()
	h.s.SetMirrorConsent(cur.Lineage, true)
	yes, _ := h.s.CurrentConsole()
	cleanExit(h.s)

	h2 := alwaysStore(t, store, priv)
	back, ok := h2.s.CurrentConsole()
	if !ok || back.Lineage != cur.Lineage || back.Mirror != MirrorYes || !back.MirrorSince.Equal(yes.MirrorSince) {
		t.Fatalf("after Den's restart: %+v %v", back, ok)
	}
	cleanExit(h2.s)

	// Another program changes the answer in the record while Den is
	// stopped: the record no longer matches the mark, so no console is
	// restored, and the next pairing is asked again.
	raw, err := os.ReadFile(store)
	if err != nil {
		t.Fatal(err)
	}
	var rec storedConsoles
	if json.Unmarshal(raw, &rec) != nil || len(rec.Consoles) != 1 || rec.Consoles[0].Mirror != MirrorYes {
		t.Fatalf("the record: %s", raw)
	}
	edited := strings.Replace(string(raw), `"mirror":"yes"`, `"mirror":"no"`, 1)
	if edited == string(raw) {
		t.Fatalf("no answer in the record to edit: %s", raw)
	}
	if err := os.WriteFile(store, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	h3 := alwaysStore(t, store, priv)
	if c, ok := h3.s.CurrentConsole(); ok {
		t.Errorf("a changed record restored a console: %+v", c)
	}
}
