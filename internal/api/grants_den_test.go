package api

import (
	"os"
	"path/filepath"
	"testing"

	perm "github.com/GlacierHubAB/den/internal/grants"
)

// confirmed is a proved console with its powers, the start confirm given.
func confirmed(t *testing.T) (*harness, *client) {
	t.Helper()
	h, c := provedConsoleOn(t, permsHarness(t, t.TempDir()))
	readNow(h)
	cur, _ := h.s.CurrentConsole()
	h.s.ConsolePowersAnswered(cur.ID, true)
	return h, c
}

func capNow(h *harness, class string) int { return capOf(readNow(h), class) }

// SetDenLevel lowers and never raises: with a console's session below the
// new level it keeps the session (P1), and a level that would raise the
// cap is ErrRaise. A class or level outside the sets is ErrNoClass.
func TestSetDenLevelNeverRaises(t *testing.T) {
	h, c := confirmed(t)
	g := getGrants(t, c)
	lowerOn(c, `{"class":"routine","level":1,"seq":$seq}`, g.Seq)
	if err := h.s.SetDenLevel(perm.Routine, 3); err != nil || capNow(h, perm.Routine) != 1 {
		t.Errorf("den 3 over a session at 1: %v, cap %d", err, capNow(h, perm.Routine))
	}
	if cl := classOf(readNow(h), perm.Routine); cl.Session == nil || cl.DenLevel != 3 || cl.SetInDen == nil {
		t.Errorf("the class after: %+v", cl)
	}
	if err := h.s.SetDenLevel(perm.Routine, 0); err != nil || capNow(h, perm.Routine) != 0 || classOf(readNow(h), perm.Routine).Session != nil {
		t.Errorf("den 0: %v, cap %d", err, capNow(h, perm.Routine))
	}
	if err := h.s.SetDenLevel(perm.Routine, 1); err != ErrRaise || capNow(h, perm.Routine) != 0 {
		t.Errorf("a raise through SetDenLevel: %v, cap %d", err, capNow(h, perm.Routine))
	}
	// Under a lower session, den_level rising is a raise too: the session
	// ends with Den, and the higher level would then hold without a ^Y.
	g = getGrants(t, c)
	lowerOn(c, `{"class":"destructive","level":0,"seq":$seq}`, g.Seq)
	if err := h.s.SetDenLevel(perm.Destructive, 2); err != ErrRaise || classOf(readNow(h), perm.Destructive).DenLevel != 1 {
		t.Errorf("den_level up under a session: %v, %+v", err, classOf(readNow(h), perm.Destructive))
	}
	for _, bad := range []struct {
		class string
		level int
	}{{"admin", 0}, {perm.Routine, 5}, {perm.Code, 2}, {perm.Routine, -1}} {
		if err := h.s.SetDenLevel(bad.class, bad.level); err != ErrNoClass {
			t.Errorf("%s %d: %v", bad.class, bad.level, err)
		}
	}
}

// RaiseDenLevel raises only on the read it was asked on, clears the
// console's session, and marks set_in_den; on any other seq it is
// ErrMoved and nothing changes.
func TestRaiseDenLevelHoldsTheSeq(t *testing.T) {
	h, c := confirmed(t)
	g := getGrants(t, c)
	lowerOn(c, `{"class":"routine","level":1,"seq":$seq}`, g.Seq)
	seq := readNow(h).Seq
	if err := h.s.RaiseDenLevel(perm.Routine, 4, seq-1); err != ErrMoved || capNow(h, perm.Routine) != 1 {
		t.Errorf("an old seq: %v, cap %d", err, capNow(h, perm.Routine))
	}
	if err := h.s.RaiseDenLevel(perm.Routine, 4, seq); err != nil || capNow(h, perm.Routine) != 4 {
		t.Fatalf("the raise: %v, cap %d", err, capNow(h, perm.Routine))
	}
	if cl := classOf(readNow(h), perm.Routine); cl.Session != nil || cl.SetInDen == nil || cl.SetInDen.At == "" {
		t.Errorf("after the raise: %+v", cl)
	}
	if r := storedRecord(t, h); r.SetInDen[perm.Routine] == "" || r.DenLevels[perm.Routine] != 4 {
		t.Errorf("the record: %+v", r)
	}
}

// ConfirmLevels holds the seq too, and confirms the levels, not the
// powers; RemoveKept removes for good, "*" every entry.
func TestConfirmAndRemoveFromDen(t *testing.T) {
	h, _ := provedConsoleOn(t, permsHarness(t, t.TempDir()))
	readNow(h)
	h.s.perms.mu.Lock()
	h.s.perms.kept = perm.KeptAll()
	h.s.perms.mu.Unlock()
	seq := readNow(h).Seq
	if err := h.s.ConfirmLevels(seq + 1); err != ErrMoved || readNow(h).StartConfirmed {
		t.Errorf("an old seq: %v", err)
	}
	if err := h.s.ConfirmLevels(readNow(h).Seq); err != nil || !readNow(h).StartConfirmed {
		t.Errorf("the confirm: %v", err)
	}
	if err := h.s.RemoveKept(perm.KeptMerge); err != nil || len(keptNow(h)) != len(perm.KeptAll())-1 {
		t.Errorf("remove merge: %v %v", err, keptNow(h))
	}
	if err := h.s.RemoveKept(perm.KeptMerge); err != ErrNotKept {
		t.Errorf("again: %v", err)
	}
	if err := h.s.RemoveKept("*"); err != nil || len(keptNow(h)) != 0 {
		t.Errorf("*: %v %v", err, keptNow(h))
	}
}

// G1: after another program changed the stored levels, GrantsStart says
// held and ConfirmLevels is ErrHeld on any seq: only the powers card ends
// that hold.
func TestConfirmLevelsRefusedWhileHeld(t *testing.T) {
	h, _ := confirmed(t)
	h.s.permsTampered(false)
	if held, _ := h.s.GrantsStart(); !held {
		t.Fatal("not held after the tamper")
	}
	if err := h.s.ConfirmLevels(readNow(h).Seq); err != ErrHeld || readNow(h).StartConfirmed {
		t.Errorf("the confirm while held: %v", err)
	}
	if held, _ := h.s.GrantsStart(); !held {
		t.Error("the hold ended without the powers card")
	}
}

// P5: set_in_den is shown only. A version 3 file with set_in_den on every
// class and levels above the defaults still loads capped until the
// confirm.
func TestSetInDenIsShownOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "permissions.json")
	at := `"2026-10-30T12:00:00Z"`
	rec := `{"version":3,"lineage":"","den_levels":{"routine":4,"destructive":2,"code":1,"desktop":2,"outward":3},"kept":[],"lowered":[],"set_in_den":{"routine":` + at + `,"destructive":` + at + `,"code":` + at + `,"desktop":` + at + `,"outward":` + at + `}}`
	os.WriteFile(path, []byte(rec), 0o600)
	writeMark(t, path, []byte(rec))
	h := permsHarness(t, dir)
	g := perm.Read(func() perm.State {
		h.s.perms.mu.Lock()
		defer h.s.perms.mu.Unlock()
		st, _ := h.s.grantsInput()
		st.Powers = true
		return st
	}())
	for _, c := range g.Classes {
		if c.Level > c.Default || c.SetInDen == nil {
			t.Errorf("%s: level %d over default %d, set_in_den %v", c.Class, c.Level, c.Default, c.SetInDen)
		}
	}
}

// The bounds: a console that lowers every class to 0 and removes every
// kept entry makes at most 25 + 9 writes; after one raise in Den, at most
// one more.
func TestConsoleWritesAreBoundedBetweenRaises(t *testing.T) {
	h, c := confirmed(t)
	h.s.perms.mu.Lock()
	h.s.perms.kept = perm.KeptAll()
	h.s.perms.mu.Unlock()
	gen := func() uint64 {
		h.s.perms.mu.Lock()
		defer h.s.perms.mu.Unlock()
		return h.s.perms.gen
	}
	start := gen()
	g := getGrants(t, c)
	for _, cl := range g.Classes {
		for level := capOf(g, cl.Class) - 1; level >= 0; level-- {
			g = getGrants(t, c)
			lowerOn(c, `{"class":"`+cl.Class+`","level":`+itoa(level)+`,"seq":$seq}`, g.Seq)
		}
	}
	for _, id := range perm.KeptAll() {
		g = getGrants(t, c)
		lowerOn(c, `{"kept":"`+id+`","seq":$seq}`, g.Seq)
	}
	if n := gen() - start; n > 25+9 {
		t.Errorf("%d console writes", n)
	}
	h.s.RaiseDenLevel(perm.Code, 1, readNow(h).Seq)
	before := gen()
	for i := 0; i < 5; i++ {
		g = getGrants(t, c)
		lowerOn(c, `{"class":"code","level":0,"seq":$seq}`, g.Seq)
	}
	if n := gen() - before; n > 1 {
		t.Errorf("%d console writes after one raise step", n)
	}
}
