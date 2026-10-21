package api

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Den's own saves never pass for a change by another program: a check of
// the record that runs while Den is writing it (a claim, a reconnect, the
// powers, every save) never unpairs the console. #179: the check read the
// file before taking the lock and the hash after, so a save between the
// two looked like tampering, and the next console.auth found no pairing.
func TestConsoleRecordCheckDuringSaves(t *testing.T) {
	store := filepath.Join(t.TempDir(), "consoles.json")
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	h := alwaysStore(t, store, priv)
	fakeUnit(t, testVerifier(), nil)
	d := newDock(t, h)
	paired := requestPaired(t, h, d, "")
	stop := time.Now().Add(time.Second)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		// Each save changes the record, as a real one does (here the
		// /api lock flag, back and forth).
		for time.Now().Before(stop) {
			h.s.con.mu.Lock()
			h.s.con.locked = !h.s.con.locked
			h.s.saveConsoleLocked()
			h.s.con.mu.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		for time.Now().Before(stop) {
			h.s.checkConsoleRecord()
		}
	}()
	wg.Wait()
	if cur, ok := h.s.CurrentConsole(); !ok || cur.ID != paired.ID {
		t.Fatalf("Den's own saves unpaired the console: %+v %v", cur, ok)
	}
}

// The check reads the record under con.mu, so nothing planted at its path
// may block or stall it: a FIFO (or an outsized file) is a change by
// another program, which unpairs, and the check returns at once.
func TestConsoleRecordCheckRefusesAFIFO(t *testing.T) {
	store := filepath.Join(t.TempDir(), "consoles.json")
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	h := alwaysStore(t, store, priv)
	fakeUnit(t, testVerifier(), nil)
	d := newDock(t, h)
	requestPaired(t, h, d, "")
	os.Remove(store)
	if err := syscall.Mkfifo(store, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		h.s.checkConsoleRecord()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a FIFO at the record blocked the check")
	}
	if cur, ok := h.s.CurrentConsole(); ok {
		t.Errorf("a FIFO at the record left the console paired: %+v", cur)
	}
	// Outsized: refused the same way, without reading it all.
	os.Remove(store)
	if err := os.WriteFile(store, make([]byte, recordMax+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readRecord(store); err == nil {
		t.Error("an outsized record was read")
	}
}
