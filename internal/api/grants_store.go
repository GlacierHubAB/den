package api

import (
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	perm "github.com/GlacierHubAB/den/internal/grants"
)

// Den keeps the permission levels across its restarts in permissions.json,
// next to consoles.json, and watches it the same way:
//
//   - it is read strictly into the closed sets (perm.ParseRecord); anything
//     else, a missing file Den had written, or one Den cannot read, puts
//     every class at its default, never higher, and Den says so;
//   - every start, clean or not, caps each class at its default until the
//     human's Ctrl+Y on Den's powers card (the start confirm); a crash, a
//     file changed while Den was stopped or a damaged one is said on that
//     card too;
//   - a clean exit writes a mark of the file as Den wrote it;
//   - while Den runs, the file's hash is checked on a timer and before each
//     write: a change Den did not make holds every level at 0 and takes the
//     console's powers until that Ctrl+Y, and the file is rewritten to what
//     Den knows.
//
// The lowerings it keeps are a notice only: Den never reads a level back
// from them.

// permsFile is the file's writer. mu orders the writes and the checks: a
// write and the hash it leaves change together under it, so Den's own write
// is never taken for another program's. It is never held together with
// perms.mu.
type permsFile struct {
	mu sync.Mutex
	// hash is of the file as Den last wrote or read it; written is the gen
	// of the state last written.
	hash    string
	written uint64
	// kick asks the writer for the state now; done closes when it ended.
	kick chan struct{}
	done chan struct{}
}

// permsMark is what a clean exit leaves.
type permsMark struct {
	Record string `json:"record_sha256"`
}

func (s *Server) permsPath() string     { return s.opts.PermStore }
func (s *Server) permsMarkPath() string { return s.opts.PermStore + ".clean" }

// The sentences of what Den found of its stored levels at start, in its
// chat and on the powers card until the start confirm.
const (
	permsRemoved = "Den's stored permission levels were removed while Den was stopped, so they are at the defaults."
	permsUnread  = "Den could not read its stored permission levels, so they are at the defaults."
	permsDamaged = "Den's stored permission levels were damaged, so they are at the defaults."
	permsCrash   = "Den stopped unexpectedly last time and could not check its stored permission levels."
	permsChanged = "Den's stored permission levels were changed while Den was stopped."
	// permsTamper is a change while Den runs.
	permsTamper = "Den's stored permission levels were changed by another program: every level is at 0 until you press Ctrl+Y on the powers card"
)

// loadPerms reads the stored levels at start and starts the file's writer.
// It returns the last run's lowerings, for the start note.
func (s *Server) loadPerms() []perm.Lowered {
	path := s.permsPath()
	if path == "" {
		return nil
	}
	raw, err := readRecord(path)
	markRaw, markErr := readRecord(s.permsMarkPath())
	os.Remove(s.permsMarkPath()) // a crash from here on leaves no mark
	var rec perm.Record
	why := ""
	switch {
	case errors.Is(err, os.ErrNotExist):
		if markErr == nil {
			why = permsRemoved
		}
	case err != nil:
		why = permsUnread
	default:
		r, perr := perm.ParseRecord(raw)
		if perr != nil {
			why = permsDamaged
			break
		}
		rec = r
		var mark permsMark
		switch {
		case markErr != nil || json.Unmarshal(markRaw, &mark) != nil:
			why = permsCrash
		case mark.Record != sha256Hex(raw):
			why = permsChanged
		}
	}
	s.perms.mu.Lock()
	s.perms.denLevels, s.perms.lineage, s.perms.startWhy, s.perms.kept = rec.DenLevels, rec.Lineage, why, rec.Kept
	// The file is written again at once: this session has no lowerings
	// yet, and a file Den could not read becomes Den's own.
	s.perms.gen++
	s.perms.queued = s.perms.gen
	s.perms.mu.Unlock()
	s.permFile.kick, s.permFile.done = make(chan struct{}, 1), make(chan struct{})
	s.flushPerms()
	go s.permsWriter(s.stopWatch)
	if why != "" {
		s.note(why)
	}
	return rec.Lowered
}

// startNote tells Den's chat which of the last run's lowerings ended with
// the restart. The console is named only when it is the console restored
// now, by Den's own label for it; a label the file alone names is "a
// console". The class and level words are Den's.
func (s *Server) startNote(last []perm.Lowered) {
	if len(last) == 0 {
		return
	}
	who := "a console"
	if cur, ok := s.CurrentConsole(); ok {
		label := lowerLabel(cur)
		same := true
		for _, l := range last {
			same = same && l.Console == label
		}
		if same {
			who = label
		}
	}
	var said []string
	for _, l := range last {
		said = append(said, perm.Label(l.Class)+" to "+strconv.Itoa(l.Level)+" of 5")
	}
	can := who
	if who == "a console" {
		can = "A console"
	}
	if len(said) == 1 {
		s.note("Den restarted, so " + who + "'s lowering of " + said[0] + " ended. " + can + " can lower it again.")
		return
	}
	s.note("Den restarted, so " + who + "'s lowerings ended: " + strings.Join(said, ", ") + ". " + can + " can lower them again.")
}

// kickPerms asks the writer for the state now, without waiting for it.
func (s *Server) kickPerms() {
	if s.permFile.kick == nil {
		return
	}
	select {
	case s.permFile.kick <- struct{}{}:
	default: // one is asked already; it writes the state as it is then
	}
}

// permsWriter writes the file when asked and checks it on a timer, until
// stop; then it writes the last state and ends.
func (s *Server) permsWriter(stop <-chan struct{}) {
	defer close(s.permFile.done)
	t := time.NewTicker(consoleCheckEvery)
	defer t.Stop()
	for {
		select {
		case <-stop:
			s.flushPerms()
			return
		case <-s.permFile.kick:
			s.flushPerms()
		case <-t.C:
			s.checkPerms()
		}
	}
}

// permRecordLocked is the record of the state now. s.perms.mu is held.
func (s *Server) permRecordLocked() perm.Record {
	r := perm.Record{Lineage: s.perms.lineage, DenLevels: map[string]int{}, Kept: append([]string(nil), s.perms.kept...)}
	for k, v := range s.perms.denLevels {
		r.DenLevels[k] = v
	}
	for class, c := range s.perms.caps {
		r.Lowered = append(r.Lowered, perm.Lowered{Class: class, Level: c.Level, At: c.At, Console: c.Console})
	}
	return r
}

// flushPerms writes the state now, unless a newer one is written already.
// The copy is taken under perms.mu, the write under permFile.mu, never
// both at once. A file changed since Den's last write is another
// program's: it is handled as one, and the state is written over it.
func (s *Server) flushPerms() {
	path := s.permsPath()
	if path == "" {
		return
	}
	s.perms.mu.Lock()
	rec, gen := s.permRecordLocked(), s.perms.gen
	s.perms.mu.Unlock()
	b := rec.Encode()
	f := &s.permFile
	f.mu.Lock()
	if gen <= f.written {
		f.mu.Unlock()
		return
	}
	changed := false
	if f.hash != "" {
		raw, err := readRecord(path)
		changed = err != nil || sha256Hex(raw) != f.hash
	}
	if writeAtomic(path, b) == nil {
		f.hash, f.written = sha256Hex(b), gen
	}
	f.mu.Unlock()
	if changed {
		s.permsTampered(false)
	}
}

// checkPerms compares the file with Den's last write.
func (s *Server) checkPerms() {
	f := &s.permFile
	f.mu.Lock()
	if f.hash == "" {
		f.mu.Unlock()
		return
	}
	raw, err := readRecord(s.permsPath())
	changed := err != nil || sha256Hex(raw) != f.hash
	if changed {
		// Handled here, once: the rewrite does not take the same change
		// for another, which would hold the levels again after the
		// human's Ctrl+Y.
		f.hash = ""
	}
	f.mu.Unlock()
	if changed {
		s.permsTampered(true)
	}
}

// permsTampered handles a change of the file Den did not make while it
// runs: every level is held at 0 and the start confirm is taken back, under
// perms.mu; once that is let go, the file is rewritten to what Den knows
// (when rewrite), the console's powers are taken, which raises the powers
// card, and Den says so. The human's Ctrl+Y on that card ends it.
func (s *Server) permsTampered(rewrite bool) {
	s.permsDo(func() {
		s.perms.held, s.perms.confirmed = true, false
		if rewrite {
			s.perms.gen++
		}
		s.grantsLocked(nil)
	})
	s.con.mu.Lock()
	cur := s.con.cur
	had := cur != nil && cur.powers
	s.con.mu.Unlock()
	s.LockConsolePowers(permsTamper) // asks on the card when it took them
	if cur != nil && !had {
		s.askPowers(cur, permsTamper)
	}
	s.note(permsTamper + ".")
}

// permsHeld reports whether the levels are held at 0 after another
// program's change to the file, until the powers card's Ctrl+Y.
func (s *Server) permsHeld() bool {
	s.perms.mu.Lock()
	defer s.perms.mu.Unlock()
	return s.perms.held
}

// permsCardLines are the powers card's lines about the levels, until the
// start confirm: what Den found of its stored levels at start, and the
// classes the human set above their defaults, which that Ctrl+Y confirms.
func (s *Server) permsCardLines() []string {
	var out []string
	s.permsDo(func() {
		s.grantsLocked(nil) // a new pairing is folded in first (F3)
		if s.perms.confirmed {
			return
		}
		if s.perms.startWhy != "" {
			out = append(out, s.perms.startWhy)
		}
		st, _ := s.grantsInput()
		var raised []string
		for _, id := range perm.Classes() {
			if l, ok := st.DenLevels[id]; ok && l > perm.Default(id) {
				raised = append(raised, perm.Label(id)+" "+strconv.Itoa(l)+" of 5 (default "+strconv.Itoa(perm.Default(id))+")")
			}
		}
		if len(raised) > 0 {
			out = append(out, "Ctrl+Y also confirms the levels you set above Den's defaults: "+strings.Join(raised, ", ")+". Until then they are at the defaults.")
		}
		if len(st.Kept) > 0 {
			var kept []string
			for _, id := range st.Kept {
				kept = append(kept, perm.KeptLabel(id))
			}
			out = append(out, "Ctrl+Y also lets your console answer, as before: "+strings.Join(kept, ", ")+". Until then it does not.")
		}
	})
	return out
}

// closePerms ends the writer, which writes the last state, and leaves a
// clean exit's mark of the file, only while it still is what Den wrote.
func (s *Server) closePerms() {
	if s.permFile.done == nil {
		return
	}
	<-s.permFile.done
	f := &s.permFile
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, err := readRecord(s.permsPath())
	if err != nil || f.hash == "" || sha256Hex(raw) != f.hash {
		return // not what Den wrote: no mark, so the next start says so
	}
	b, _ := json.Marshal(permsMark{Record: f.hash})
	writeAtomic(s.permsMarkPath(), b)
}

// grantKept is the upgrade: a console restored from a record without
// kept_era, which loadConsole restored only on a valid clean mark, keeps
// what it answered before the levels, as kept entries, once. The record
// is written with kept_era in this same start, so no later start grants
// again; permissions.json plays no part, so deleting it never brings back
// an entry a console removed. Kept is inactive until the start confirm.
func (s *Server) grantKept() {
	s.con.mu.Lock()
	grant := s.con.keptGrant
	s.con.keptGrant = false
	cur := s.con.cur
	if grant && cur != nil {
		s.saveConsoleLocked() // with kept_era
	}
	s.con.mu.Unlock()
	if !grant || cur == nil {
		return
	}
	s.permsDo(func() {
		s.grantsLocked(nil) // levels of another pairing are folded in first (F3)
		s.perms.kept = perm.KeptAll()
		s.perms.gen++
		s.grantsLocked(nil)
	})
	s.note("Your console keeps what it could answer before Den's permission levels, as kept items, until it or a new pairing removes them. They take effect after you press Ctrl+Y on its powers card.")
}
