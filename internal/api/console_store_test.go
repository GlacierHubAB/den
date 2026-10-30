package api

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// storeHarness is a console harness that keeps its console in store.
func storeHarness(t *testing.T, store string, priv ed25519.PrivateKey) *harness {
	t.Helper()
	return newHarnessWith(t, 2, func(o *Options) { o.ConsoleKey, o.ConsoleStore = priv, store })
}

// A FIFO planted at consoles.json's clean mark never stalls Den's start: it
// reads as no mark.
func TestConsoleMarkFIFODoesNotStall(t *testing.T) {
	store := filepath.Join(t.TempDir(), "consoles.json")
	if err := syscall.Mkfifo(store+".clean", 0o600); err != nil {
		t.Skip(err)
	}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	h := storeHarness(t, store, priv)
	if _, ok := h.s.CurrentConsole(); ok {
		t.Error("a console was restored")
	}
}

// noteWith waits for a note of Den's that contains want, and returns it.
func noteWith(t *testing.T, s *Server, want string) string {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case n := <-s.Notes():
			if strings.Contains(n, want) {
				return n
			}
		case <-deadline:
			t.Fatalf("no note with %q", want)
			return ""
		}
	}
}

// pairedStore pairs this test process as the console of a server keeping
// it in a fresh store, and leaves a clean-exit mark as Den's exit would.
func pairedStore(t *testing.T) (store string, priv ed25519.PrivateKey, d *dock, p consolePaired) {
	t.Helper()
	store = filepath.Join(t.TempDir(), "consoles.json")
	_, priv, _ = ed25519.GenerateKey(rand.Reader)
	h := storeHarness(t, store, priv)
	d = newDock(t, h)
	p = d.pair(h.s.StartConsoleCode())
	// What a clean exit does first: the watch stops and the mark is
	// written, so this server never touches the record again.
	cleanExit(h.s)
	return store, priv, d, p
}

// A console survives Den's restart: it is announced, and the console's
// first subscribed connection hears it was restored.
func TestConsoleRestoredAcrossARestart(t *testing.T) {
	store, priv, d, p := pairedStore(t)
	h := storeHarness(t, store, priv)
	if cur, ok := h.s.CurrentConsole(); !ok || cur.ID != p.ID {
		t.Fatalf("not restored: %+v", cur)
	}
	noteWith(t, h.s, "console restored from before Den's restart")
	d.h = h
	c := h.dial()
	c.hello()
	if l, _, _, _ := d.auth(c, p); !l.OK {
		t.Fatalf("auth after the restart: %+v", l.Error)
	}
	c.call("console.events", nil)
	var ev ConsoleEvent
	if e := c.next(); json.Unmarshal(e.Data, &ev) != nil || ev.Kind != "restored" {
		t.Errorf("first event %+v", e)
	}
	c2 := h.dial()
	c2.hello()
	time.Sleep(challengeEvery)
	d.auth(c2, p)
	c2.call("console.events", nil)
	if e := c2.next(); json.Unmarshal(e.Data, &ev) != nil || ev.Kind != "connected" {
		t.Errorf("a later connection hears %+v", e)
	}
}

// Every way the record can be wrong at start drops the console, loudly.
func TestConsoleBadRecord(t *testing.T) {
	for _, c := range []struct {
		name  string
		spoil func(store string, rec *storedConsoles, priv *ed25519.PrivateKey)
		note  string
	}{
		{"nomark", func(store string, _ *storedConsoles, _ *ed25519.PrivateKey) { os.Remove(store + ".clean") }, "did not exit cleanly"},
		{"two", func(_ string, rec *storedConsoles, _ *ed25519.PrivateKey) {
			rec.Consoles = append(rec.Consoles, rec.Consoles[0])
		}, "more than one console"},
		{"edited", func(_ string, rec *storedConsoles, _ *ed25519.PrivateKey) {
			rec.Consoles[0].Key = strings.Repeat("ab", 32)
		}, "changed while Den was stopped"},
		{"key", func(_ string, _ *storedConsoles, priv *ed25519.PrivateKey) {
			_, *priv, _ = ed25519.GenerateKey(rand.Reader)
		}, "key changed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			store, priv, _, _ := pairedStore(t)
			raw, _ := os.ReadFile(store)
			var rec storedConsoles
			json.Unmarshal(raw, &rec)
			before := len(raw)
			c.spoil(store, &rec, &priv)
			if b, _ := json.Marshal(rec); len(b) != before || c.name == "edited" {
				os.WriteFile(store, b, 0o600)
			}
			h := storeHarness(t, store, priv)
			if _, ok := h.s.CurrentConsole(); ok {
				t.Error("restored")
			}
			noteWith(t, h.s, c.note)
		})
	}
}

// A console from another boot, or whose process started again, is not
// restored, and a record that does not parse is not either.
func TestConsoleOtherProc(t *testing.T) {
	for name, spoil := range map[string]func(*storedConsole){
		"boot": func(c *storedConsole) {
			c.Identity = strings.Replace(c.Identity, ":"+bootID()+":", ":00000000-0000-0000-0000-000000000000:", 1)
		},
		"restart": func(c *storedConsole) {
			c.Start++
			c.Identity = c.Identity[:strings.LastIndex(c.Identity, ":")+1] + itoa(int(c.Start))
		},
	} {
		t.Run(name, func(t *testing.T) {
			store, priv, _, _ := pairedStore(t)
			raw, _ := os.ReadFile(store)
			var rec storedConsoles
			json.Unmarshal(raw, &rec)
			spoil(&rec.Consoles[0])
			b, _ := json.Marshal(rec)
			os.WriteFile(store, b, 0o600)
			// The mark as a clean exit would have written it for this record:
			// the check that catches this is the process's, not the mark's.
			mark, _ := json.Marshal(cleanMark{Record: sha256Hex(b), Key: sha256Hex([]byte(pubHex(priv))), Consoles: 1})
			os.WriteFile(store+".clean", mark, 0o600)
			h := storeHarness(t, store, priv)
			if _, ok := h.s.CurrentConsole(); ok {
				t.Error("restored")
			}
		})
	}
}

// While Den runs, a change to the record it did not make unpairs the
// console, and the file is rewritten to what Den knows.
func TestConsoleRecordTamperUnpairs(t *testing.T) {
	store := filepath.Join(t.TempDir(), "consoles.json")
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	h := storeHarness(t, store, priv)
	d := newDock(t, h)
	d.pair(h.s.StartConsoleCode())
	os.WriteFile(store, []byte(`{"consoles":[]}`), 0o600)
	h.s.checkConsoleRecord()
	if _, ok := h.s.CurrentConsole(); ok {
		t.Error("still paired after the record changed")
	}
	noteWith(t, h.s, "changed by another program")
	raw, _ := os.ReadFile(store)
	if sha256Hex(raw) != h.s.con.recordHash {
		t.Error("the tampered file was left as it was")
	}
}

func pubHex(priv ed25519.PrivateKey) string {
	return hex.EncodeToString(priv.Public().(ed25519.PublicKey))
}

// cleanExit does what Close does first: stop the watch, write the mark.
func cleanExit(s *Server) {
	s.closeOnce.Do(func() {
		close(s.stopWatch)
		s.writeCleanMark()
	})
}

// An entry written into the record while Den runs, with or without a
// console, is never blessed by the clean-exit mark: nothing of it is
// restored at the next start, and Den says the record was changed.
func TestConsoleInjectedWhileRunningIsNotRestored(t *testing.T) {
	for _, withConsole := range []bool{false, true} {
		store := filepath.Join(t.TempDir(), "consoles.json")
		_, priv, _ := ed25519.GenerateKey(rand.Reader)
		h := storeHarness(t, store, priv)
		d := newDock(t, h)
		if withConsole {
			d.pair(h.s.StartConsoleCode())
		}
		// An agent's own entry: this process, its own key.
		evil := storedConsoles{Consoles: []storedConsole{{ID: randHex(), Identity: d.identity, Key: d.pub, Exe: "/usr/bin/evil", PID: os.Getpid(), Start: func() uint64 { st, _, _ := procStat(os.Getpid()); return st }(), Since: time.Now()}}}
		b, _ := json.Marshal(evil)
		os.WriteFile(store, b, 0o600)
		cleanExit(h.s) // the exit comes before the next 10 s check
		noteWith(t, h.s, "changed by another program")
		if _, err := os.Stat(store + ".clean"); err == nil {
			t.Errorf("console %v: a mark was written over a tampered record", withConsole)
		}
		h2 := storeHarness(t, store, priv)
		if cur, ok := h2.s.CurrentConsole(); ok {
			t.Errorf("console %v: the injected entry was restored: %+v", withConsole, cur)
		}
	}
}

func TestIdentityParts(t *testing.T) {
	boot, pid, start, ok := identityParts(unreadablePrefix + "b-1:42:7")
	if !ok || boot != "b-1" || pid != 42 || start != 7 {
		t.Errorf("unreadable: %q %d %d %v", boot, pid, start, ok)
	}
	boot, pid, start, ok = identityParts("exe:2f78:b-2:43:8")
	if !ok || boot != "b-2" || pid != 43 || start != 8 {
		t.Errorf("exe: %q %d %d %v", boot, pid, start, ok)
	}
	for _, bad := range []string{"", "exe:2f78:b:x:8", "unreadable:b:1", "other:a:b:c:d", "exe::b:1:2:3"} {
		if _, _, _, ok := identityParts(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}
