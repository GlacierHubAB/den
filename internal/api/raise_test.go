package api

import (
	"os"
	"path/filepath"
	"testing"

	perm "github.com/GlacierHubAB/den/internal/grants"
)

// epochOK is a raise epoch as Den makes one: 32 lowercase hex digits.
func epochOK(e string) bool {
	if len(e) != 32 {
		return false
	}
	for _, r := range e {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// Only the human's raise in Den's window counts a class's raise_n up, by
// one, with its time (P1); a lowering in Den, the confirm, a kept removal
// and a console's lowering leave the counter alone. The record keeps it.
func TestRaiseCounterCountsOnlyRaises(t *testing.T) {
	h, c := confirmed(t)
	g := readNow(h)
	if !epochOK(g.RaiseEpoch) {
		t.Fatalf("no epoch at start: %q", g.RaiseEpoch)
	}
	for _, cl := range g.Classes {
		if cl.RaiseN != 0 || cl.RaisedAt != nil {
			t.Fatalf("%s starts at %d %v", cl.Class, cl.RaiseN, cl.RaisedAt)
		}
	}
	epoch := g.RaiseEpoch
	if code, _ := lowerOn(c, `{"class":"routine","level":1,"seq":$seq}`, getGrants(t, c).Seq); code != "" {
		t.Fatalf("lower: %s", code)
	}
	if err := h.s.RaiseDenLevel(perm.Routine, 4, readNow(h).Seq); err != nil {
		t.Fatal(err)
	}
	cl := classOf(readNow(h), perm.Routine)
	if cl.RaiseN != 1 || cl.RaisedAt == nil || *cl.RaisedAt == "" {
		t.Fatalf("after a raise: %+v", cl)
	}
	// Every other writer leaves the counter as it is.
	h.s.SetDenLevel(perm.Routine, 3)
	h.s.ConfirmLevels(readNow(h).Seq)
	h.s.RemoveKept("*")
	lowerOn(c, `{"class":"code","level":0,"seq":$seq}`, getGrants(t, c).Seq)
	g = readNow(h)
	if cl := classOf(g, perm.Routine); cl.RaiseN != 1 || g.RaiseEpoch != epoch {
		t.Errorf("after the other writers: %+v, epoch %q (was %q)", cl, g.RaiseEpoch, epoch)
	}
	for _, other := range []string{perm.Code, perm.Destructive, perm.Desktop, perm.Outward} {
		if n := classOf(g, other).RaiseN; n != 0 {
			t.Errorf("%s counted %d with no raise", other, n)
		}
	}
	if r := storedRecord(t, h); r.RaiseEpoch != epoch || r.RaiseN[perm.Routine] != 1 || r.RaisedAt[perm.Routine] == "" {
		t.Errorf("the record: %+v", r)
	}
}

// A raise always moves seq (P5), even one that changes nothing else (the
// level as it was, no session, a stamp in the same second): so a console's
// lowering sent with the seq from before is stale, never taken over it.
func TestRaiseAlwaysMovesSeq(t *testing.T) {
	h, c := confirmed(t)
	if err := h.s.RaiseDenLevel(perm.Routine, 4, readNow(h).Seq); err != nil {
		t.Fatal(err)
	}
	before := getGrants(t, c).Seq
	if err := h.s.RaiseDenLevel(perm.Routine, 4, readNow(h).Seq); err != nil {
		t.Fatal(err)
	}
	after := readNow(h)
	if after.Seq == before {
		t.Fatal("a raise that changed nothing else did not move seq")
	}
	if code, _ := lowerOn(c, `{"class":"routine","level":1,"seq":$seq}`, before); code != CodeStale {
		t.Errorf("a lowering on the seq from before the raise: %q", code)
	}
	if capNow(h, perm.Routine) != 4 {
		t.Errorf("the raise was undone: %d", capNow(h, perm.Routine))
	}
}

// What Den takes of the counter from its file (P2, P3): a file it vouches
// for keeps its epoch, counts and times, and its set_in_den; any other
// (no clean mark, changed, damaged, none) starts the counter again under
// a new epoch, with no set_in_den; one from before version 4 starts a
// new one too.
func TestRaiseCounterFromTheFile(t *testing.T) {
	const epoch = "0123456789abcdef0123456789abcdef"
	rec := perm.Record{DenLevels: map[string]int{perm.Outward: 2}, SetInDen: map[string]string{perm.Outward: "2026-11-06T09:00:00Z"},
		RaiseEpoch: epoch, RaiseN: map[string]int{perm.Outward: 3}, RaisedAt: map[string]string{perm.Outward: "2026-11-06T09:00:00Z"}}
	raw := rec.Encode()
	load := func(t *testing.T, setup func(path string)) *harness {
		dir := t.TempDir()
		setup(filepath.Join(dir, "permissions.json"))
		return permsHarness(t, dir)
	}
	t.Run("vouched", func(t *testing.T) {
		h := load(t, func(p string) { os.WriteFile(p, raw, 0o600); writeMark(t, p, raw) })
		g := readNow(h)
		cl := classOf(g, perm.Outward)
		if g.RaiseEpoch != epoch || cl.RaiseN != 3 || cl.RaisedAt == nil || cl.SetInDen == nil {
			t.Errorf("a vouched file: epoch %q, %+v", g.RaiseEpoch, cl)
		}
	})
	for name, setup := range map[string]func(p string){
		"no clean mark": func(p string) { os.WriteFile(p, raw, 0o600) },
		"changed":       func(p string) { os.WriteFile(p, raw, 0o600); writeMark(t, p, append(append([]byte{}, raw...), ' ')) },
		"no file":       func(string) {},
	} {
		t.Run(name, func(t *testing.T) {
			h := load(t, setup)
			g := readNow(h)
			cl := classOf(g, perm.Outward)
			if g.RaiseEpoch == epoch || !epochOK(g.RaiseEpoch) || cl.RaiseN != 0 || cl.RaisedAt != nil || cl.SetInDen != nil {
				t.Errorf("not vouched for: epoch %q, %+v", g.RaiseEpoch, cl)
			}
			if r := storedRecord(t, h); r.RaiseEpoch != g.RaiseEpoch || r.RaiseN[perm.Outward] != 0 {
				t.Errorf("the record written after: %+v", r)
			}
		})
	}
	t.Run("from before version 4", func(t *testing.T) {
		h := load(t, func(p string) { os.WriteFile(p, []byte(validRecord), 0o600); writeMark(t, p, []byte(validRecord)) })
		if g := readNow(h); !epochOK(g.RaiseEpoch) || classOf(g, perm.Outward).RaiseN != 0 {
			t.Errorf("a version 1 file: epoch %q", g.RaiseEpoch)
		}
	})
}

// With no file at all (no store, so the start's load never runs), the
// first read already carries a real epoch, and a raise counts under it:
// a read never says "" while Den counts (drey-lead's edge).
func TestRaiseEpochFromTheFirstRead(t *testing.T) {
	h := newHarness(t, 2)
	g := readNow(h)
	if !epochOK(g.RaiseEpoch) {
		t.Fatalf("the first read's epoch: %q", g.RaiseEpoch)
	}
	if err := h.s.RaiseDenLevel(perm.Routine, 4, g.Seq); err != nil {
		t.Fatal(err)
	}
	if again := readNow(h); again.RaiseEpoch != g.RaiseEpoch || classOf(again, perm.Routine).RaiseN != 1 {
		t.Errorf("after a raise: epoch %q (was %q), %+v", again.RaiseEpoch, g.RaiseEpoch, classOf(again, perm.Routine))
	}
}

// A new pairing's fold starts the counter again: a new epoch, every count
// 0, every time gone, all in the read the fold makes (P2).
func TestRaiseCounterResetsOnAFold(t *testing.T) {
	h, _ := confirmed(t)
	if err := h.s.RaiseDenLevel(perm.Routine, 4, readNow(h).Seq); err != nil {
		t.Fatal(err)
	}
	before := readNow(h)
	c2 := repair(t, h)
	g := getGrants(t, c2)
	if g.RaiseEpoch == before.RaiseEpoch || !epochOK(g.RaiseEpoch) {
		t.Errorf("the fold kept the epoch %q", g.RaiseEpoch)
	}
	for _, cl := range g.Classes {
		if cl.RaiseN != 0 || cl.RaisedAt != nil {
			t.Errorf("after the fold, %s: %d %v", cl.Class, cl.RaiseN, cl.RaisedAt)
		}
	}
}
