package api

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Den keeps its side of the console across its own restarts: the console's
// identity and public key, never a key that could prove (the app keeps its
// private key in memory only). The record is Den's alone, so Den watches it:
//
//   - at most one console: more than one entry is a tamper;
//   - a clean exit writes a mark (the record's and Den's key's hashes, and
//     how many consoles); at start, a missing or wrong mark, more consoles
//     than it counted, or another key drop every console;
//   - an entry from another boot, or whose process is gone or started
//     again, is dropped;
//   - while Den runs, the record's hash is checked on a timer and on each
//     console proof: a change Den did not make unpairs the console loudly.
//
// Any program of the user can still write both files and the mark: the
// restored console is therefore announced (ConsoleEvent restored, a note
// and Den's header), so the app can tell whether it is the one restored.

// consoleCheckEvery is how often the record's hash is checked.
const consoleCheckEvery = 10 * time.Second

// storedConsole is one console as consoles.json keeps it.
type storedConsole struct {
	ID       string    `json:"id"`
	Identity string    `json:"identity"`
	Key      string    `json:"public_key"`
	Exe      string    `json:"exe"`
	PID      int       `json:"pid"`
	Start    uint64    `json:"start_ticks"`
	Started  time.Time `json:"started,omitzero"`
	Since    time.Time `json:"since"`
	Pages    string    `json:"pages_sha256,omitempty"`
}

type storedConsoles struct {
	Consoles []storedConsole `json:"consoles"`
}

// cleanMark is what a clean exit leaves.
type cleanMark struct {
	Record   string `json:"record_sha256"`
	Key      string `json:"key_sha256"`
	Consoles int    `json:"consoles"`
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (s *Server) consolePath() string { return s.opts.ConsoleStore }
func (s *Server) markPath() string    { return s.opts.ConsoleStore + ".clean" }

// writeAtomic writes b to path by a 0600 file in the same folder, then
// renames it there.
func writeAtomic(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// saveConsole writes the record as it is now and remembers its hash.
// Called with s.con.mu held.
func (s *Server) saveConsoleLocked() {
	if s.consolePath() == "" {
		return
	}
	var rec storedConsoles
	if c := s.con.cur; c != nil {
		rec.Consoles = []storedConsole{{ID: c.ID, Identity: c.identity, Key: c.pubHex, Exe: c.Exe, PID: c.PID, Start: c.start, Started: c.Started, Since: c.Since, Pages: c.PagesSHA}}
	}
	b, _ := json.Marshal(rec)
	if err := writeAtomic(s.consolePath(), b); err != nil {
		return
	}
	s.con.recordHash = sha256Hex(b)
}

// loadConsole restores the console from before a restart, if every check
// passes, and returns why it did not when there was one to restore.
func (s *Server) loadConsole() string {
	path := s.consolePath()
	if path == "" || s.ckey == nil {
		return ""
	}
	raw, err := os.ReadFile(path)
	markRaw, markErr := os.ReadFile(s.markPath())
	os.Remove(s.markPath()) // a crash from here on leaves no mark
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	drop := func(why string) string {
		s.con.mu.Lock()
		s.con.cur = nil
		s.saveConsoleLocked()
		s.con.mu.Unlock()
		return why
	}
	if err != nil {
		return drop("Den could not read its console record, so no console was restored")
	}
	var rec storedConsoles
	if json.Unmarshal(raw, &rec) != nil {
		return drop("Den's console record was damaged, so no console was restored")
	}
	if len(rec.Consoles) == 0 {
		s.con.recordHash = sha256Hex(raw)
		return ""
	}
	var mark cleanMark
	switch {
	case len(rec.Consoles) > 1:
		return drop("Den's console record holds more than one console: it was changed by another program, and every console was dropped")
	case markErr != nil || json.Unmarshal(markRaw, &mark) != nil:
		return drop("Den did not exit cleanly last time, so its console was not restored; pair again with den pair")
	case mark.Record != sha256Hex(raw) || mark.Consoles < len(rec.Consoles):
		return drop("Den's console record was changed while Den was stopped: every console was dropped")
	case mark.Key != sha256Hex(s.ckey.priv):
		return drop("Den's key changed since it last exited, so its console was not restored")
	}
	c := rec.Consoles[0]
	pub, err := hex.DecodeString(c.Key)
	if err != nil || len(pub) != ed25519.PublicKeySize || !isHex(c.ID, 32) {
		return drop("Den's console record was damaged, so no console was restored")
	}
	boot, pid, start, ok := identityParts(c.Identity)
	if !ok || pid != c.PID || start != c.Start {
		return drop("Den's console record was damaged, so no console was restored")
	}
	if boot == "unknown" || boot != bootID() {
		return drop("the console was paired before this boot: pair again with den pair")
	}
	if start, _, err := procStatFn(c.PID); err != nil || start != c.Start {
		return drop("the console's process is gone or was started again: pair again with den pair")
	}
	s.con.mu.Lock()
	s.con.cur = &consoleRec{
		Console:  Console{ID: c.ID, Exe: c.Exe, PID: c.PID, Started: c.Started, Since: c.Since, PagesSHA: c.Pages},
		identity: c.Identity, start: c.Start, pub: ed25519.PublicKey(pub), pubHex: c.Key, restored: true,
	}
	s.con.recordHash = sha256Hex(raw)
	s.con.mu.Unlock()
	s.note("console restored from before Den's restart: " + ExeLabel(c.Exe) + " pid " + itoa(c.PID) + ", started " + hhmmss(c.Started) + ". If that is not your console, unpair it: den unpair all")
	s.act(Act{ConsoleChanged: true})
	return ""
}

// identityParts reads the boot, pid and start out of a console identity:
// unreadable:<boot>:<pid>:<start> or exe:<hex path>:<boot>:<pid>:<start>.
func identityParts(identity string) (boot string, pid int, start uint64, ok bool) {
	f := strings.Split(identity, ":")
	switch {
	case len(f) == 4 && f[0]+":" == unreadablePrefix:
		f = f[1:]
	case len(f) == 5 && f[0] == "exe":
		f = f[2:]
	default:
		return "", 0, 0, false
	}
	p, err1 := strconv.Atoi(f[1])
	st, err2 := strconv.ParseUint(f[2], 10, 64)
	if err1 != nil || err2 != nil || f[0] == "" {
		return "", 0, 0, false
	}
	return f[0], p, st, true
}

// checkConsoleRecord compares the record with what Den last wrote: a
// change Den did not make unpairs the console.
func (s *Server) checkConsoleRecord() {
	path := s.consolePath()
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	s.con.mu.Lock()
	want := s.con.recordHash
	s.con.mu.Unlock()
	if want == "" {
		return
	}
	if err != nil || sha256Hex(raw) != want {
		s.UnpairConsole("Den's console record was changed by another program: the console is unpaired; pair again with den pair")
	}
}

// watchConsoleRecord checks the record every consoleCheckEvery until stop.
func (s *Server) watchConsoleRecord(stop <-chan struct{}) {
	t := time.NewTicker(consoleCheckEvery)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			s.checkConsoleRecord()
		}
	}
}

// writeCleanMark is a clean exit's mark.
func (s *Server) writeCleanMark() {
	if s.consolePath() == "" || s.ckey == nil {
		return
	}
	raw, err := os.ReadFile(s.consolePath())
	if err != nil {
		return
	}
	var rec storedConsoles
	json.Unmarshal(raw, &rec)
	b, _ := json.Marshal(cleanMark{Record: sha256Hex(raw), Key: sha256Hex(s.ckey.priv), Consoles: len(rec.Consoles)})
	writeAtomic(s.markPath(), b)
}
