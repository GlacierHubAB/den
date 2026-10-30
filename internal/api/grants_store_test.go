package api

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	perm "github.com/GlacierHubAB/den/internal/grants"
)

// permsHarness is a harness with a console key that keeps its levels in
// dir/permissions.json.
func permsHarness(t *testing.T, dir string) *harness {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	return newHarnessWith(t, 2, func(o *Options) {
		o.ConsoleKey, o.PermStore = priv, filepath.Join(dir, "permissions.json")
	})
}

// waitWritten waits until the file holds the state now.
func waitWritten(t *testing.T, h *harness) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		h.s.perms.mu.Lock()
		gen := h.s.perms.gen
		h.s.perms.mu.Unlock()
		h.s.permFile.mu.Lock()
		written := h.s.permFile.written
		h.s.permFile.mu.Unlock()
		if written >= gen {
			return
		}
	}
	t.Fatal("the levels' file was not written")
}

// storedRecord is the file as it is now, read as Den reads it.
func storedRecord(t *testing.T, h *harness) perm.Record {
	t.Helper()
	waitWritten(t, h)
	raw, err := os.ReadFile(h.s.permsPath())
	if err != nil {
		t.Fatal(err)
	}
	r, err := perm.ParseRecord(raw)
	if err != nil {
		t.Fatalf("Den wrote a record it does not read: %s", raw)
	}
	return r
}

// noteSaid waits for a chat note holding want.
func noteSaid(t *testing.T, h *harness, want string) string {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case n := <-h.s.Notes():
			if strings.Contains(n, want) {
				return n
			}
		case <-deadline:
			t.Fatalf("no note with %q", want)
		}
	}
}

// powersAsked waits for a powers card act and returns it.
func powersAsked(t *testing.T, h *harness) *ConsolePowersAsk {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case a := <-h.s.Acts():
			if a.ConsolePowers != nil {
				return a.ConsolePowers
			}
		case <-deadline:
			t.Fatal("no powers card")
		}
	}
}

// drainActs drops the acts waiting now.
func drainActs(h *harness) {
	for {
		select {
		case <-h.s.Acts():
		default:
			return
		}
	}
}

const validRecord = `{"version":1,"lineage":"","den_levels":{"routine":4,"destructive":2,"code":1,"desktop":0,"outward":0},"kept":[],"lowered":[]}`

// writeMark leaves a clean exit's mark of raw beside path.
func writeMark(t *testing.T, path string, raw []byte) {
	t.Helper()
	b, _ := json.Marshal(permsMark{Record: sha256Hex(raw)})
	if err := os.WriteFile(path+".clean", b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Each way Den finds its stored levels at start: what it says, the levels
// it keeps, and never a class above its default before the start confirm.
// Whatever it found, the file is Den's own record after the start.
func TestPermsLoad(t *testing.T) {
	rows := []struct {
		name  string
		setup func(t *testing.T, path string)
		why   string
		kept  bool // the file's levels are kept (capped until the confirm)
	}{
		{name: "first run", setup: func(*testing.T, string) {}},
		{name: "removed while stopped", setup: func(t *testing.T, path string) { writeMark(t, path, []byte(validRecord)) }, why: permsRemoved},
		{name: "a clean exit", setup: func(t *testing.T, path string) {
			os.WriteFile(path, []byte(validRecord), 0o600)
			writeMark(t, path, []byte(validRecord))
		}, kept: true},
		{name: "a crash", setup: func(t *testing.T, path string) { os.WriteFile(path, []byte(validRecord), 0o600) }, why: permsCrash, kept: true},
		{name: "changed while stopped", setup: func(t *testing.T, path string) {
			os.WriteFile(path, []byte(validRecord), 0o600)
			writeMark(t, path, []byte(validRecord+" "))
		}, why: permsChanged, kept: true},
		{name: "damaged", setup: func(t *testing.T, path string) {
			bad := strings.Replace(validRecord, `"destructive":2`, `"destructive":9`, 1)
			os.WriteFile(path, []byte(bad), 0o600)
			writeMark(t, path, []byte(bad))
		}, why: permsDamaged},
		{name: "a symlink", setup: func(t *testing.T, path string) {
			other := path + ".other"
			os.WriteFile(other, []byte(validRecord), 0o600)
			os.Symlink(other, path)
		}, why: permsUnread},
		{name: "a FIFO", setup: func(t *testing.T, path string) {
			if err := syscall.Mkfifo(path, 0o600); err != nil {
				t.Skip(err)
			}
		}, why: permsUnread},
		{name: "a 2 MiB file", setup: func(t *testing.T, path string) {
			os.WriteFile(path, []byte(strings.Repeat(" ", 2<<20)+validRecord), 0o600)
		}, why: permsUnread},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			dir := t.TempDir()
			row.setup(t, filepath.Join(dir, "permissions.json"))
			h := permsHarness(t, dir)
			h.s.perms.mu.Lock()
			why, levels := h.s.perms.startWhy, h.s.perms.denLevels
			h.s.perms.mu.Unlock()
			if why != row.why {
				t.Errorf("Den found %q, want %q", why, row.why)
			}
			if row.kept != (levels[perm.Destructive] == 2) {
				t.Errorf("levels %v", levels)
			}
			g := perm.Read(perm.State{Powers: true, DenLevels: levels})
			for _, c := range g.Classes {
				if c.Level > c.Default {
					t.Errorf("%s reads %d above its default before the confirm", c.Class, c.Level)
				}
			}
			if r := storedRecord(t, h); len(r.Lowered) != 0 || (row.kept != (r.DenLevels[perm.Destructive] == 2)) {
				t.Errorf("the record after start: %+v", r)
			}
		})
	}
}

// A class above its default reads at the default until the human's ^Y on
// the powers card, which the card says; neither a pairing's ^Y nor
// "always" confirms.
func TestPermsStartConfirm(t *testing.T) {
	h, c := provedConsoleOn(t, permsHarness(t, t.TempDir()))
	readNow(h) // the pairing is folded in first
	h.s.perms.mu.Lock()
	h.s.perms.denLevels[perm.Destructive] = 2
	h.s.perms.mu.Unlock()
	g := getGrants(t, c)
	if cl := classOf(g, perm.Destructive); g.StartConfirmed || cl.DenLevel != 2 || cl.Level != 1 {
		t.Fatalf("before the confirm: confirmed %v, %+v", g.StartConfirmed, cl)
	}
	lines := h.s.permsCardLines()
	if len(lines) != 1 || lines[0] != "Ctrl+Y also confirms the levels you set above Den's defaults: Stop and close 2 of 5 (default 1). Until then they are at the defaults." {
		t.Errorf("card lines %q", lines)
	}
	h.s.AllowConsolePowersAlways(h.s.ConsolePowersLockGen())
	if g := readNow(h); g.StartConfirmed || classOf(g, perm.Destructive).Level != 1 {
		t.Error(`"always" confirmed the levels`)
	}
	cur, _ := h.s.CurrentConsole()
	h.s.ConsolePowersAnswered(cur.ID, true)
	g = nextGrants(t, c)
	if cl := classOf(g, perm.Destructive); !g.StartConfirmed || cl.Level != 2 {
		t.Errorf("after the confirm: confirmed %v, %+v", g.StartConfirmed, cl)
	}
	if lines := h.s.permsCardLines(); len(lines) != 0 {
		t.Errorf("card lines after the confirm %q", lines)
	}
}

// F3: a new pairing puts each class at the lowest of its default, its Den
// level and its session, clears the sessions, writes the file once with
// the new lineage, and names the classes that came down; the same lineage
// (a reconnect, a restore) and an unpair change nothing.
func TestPermsFold(t *testing.T) {
	h, c := provedConsoleOn(t, permsHarness(t, t.TempDir()))
	readNow(h)
	h.s.perms.mu.Lock()
	h.s.perms.denLevels[perm.Destructive] = 2
	h.s.perms.denLevels[perm.Outward] = 1
	h.s.perms.confirmed = true
	h.s.perms.caps = map[string]perm.Session{perm.Routine: {Level: 2, At: "2026-10-30T12:00:00Z", Console: "Drey"}}
	h.s.perms.mu.Unlock()
	before := getGrants(t, c)
	if again := readNow(h); again.Seq != before.Seq {
		t.Error("the same lineage folded")
	}
	c2 := repair(t, h)
	g := getGrants(t, c2)
	for class, want := range map[string]int{perm.Routine: 2, perm.Destructive: 1, perm.Outward: 0, perm.Code: 1} {
		if cl := classOf(g, class); cl.DenLevel != want || cl.Session != nil {
			t.Errorf("after F3, %s: %+v, want den_level %d and no session", class, cl, want)
		}
	}
	noteSaid(t, h, "A new console was paired, so these went back to Den's defaults: Stop and close 1 of 5, Send out 0 of 5.")
	cur, _ := h.s.CurrentConsole()
	if r := storedRecord(t, h); r.Lineage != cur.Lineage || len(r.Lowered) != 0 || r.DenLevels[perm.Destructive] != 1 || r.DenLevels[perm.Routine] != 2 {
		t.Errorf("the record after F3: %+v", r)
	}
	h.s.UnpairConsole(CauseRecord, "")
	h.s.perms.mu.Lock()
	levels := h.s.perms.denLevels
	h.s.perms.mu.Unlock()
	if levels[perm.Destructive] != 1 || levels[perm.Routine] != 2 {
		t.Errorf("an unpair changed the levels: %v", levels)
	}
}

// One class that came down is named on its own.
func TestPermsFoldNamesOne(t *testing.T) {
	h, _ := provedConsoleOn(t, permsHarness(t, t.TempDir()))
	readNow(h)
	h.s.perms.mu.Lock()
	h.s.perms.denLevels[perm.Destructive] = 2
	h.s.perms.confirmed = true
	h.s.perms.mu.Unlock()
	repair(t, h)
	readNow(h)
	noteSaid(t, h, "A new console was paired, so Stop and close went back to Den's default, 1 of 5.")
}

// A change of the file Den did not make while it runs holds every level at
// 0, takes the console's powers and asks on the card, and the file becomes
// Den's again; the human's ^Y on the card ends the hold.
func TestPermsTamper(t *testing.T) {
	h, c := provedConsoleOn(t, permsHarness(t, t.TempDir()))
	getGrants(t, c)
	waitWritten(t, h)
	drainActs(h)
	os.WriteFile(h.s.permsPath(), []byte(validRecord), 0o600)
	h.s.checkPerms()
	if pw := powersAsked(t, h); pw.Why != permsTamper {
		t.Errorf("the card says %q", pw.Why)
	}
	g := readNow(h)
	for k, v := range levelsOf(g) {
		if v != 0 {
			t.Errorf("held: %s at %d", k, v)
		}
	}
	cur, _ := h.s.CurrentConsole()
	if h.s.ConsolePowered(cur.ID) {
		t.Error("the console kept its powers")
	}
	if r := storedRecord(t, h); r.DenLevels[perm.Destructive] != 1 {
		t.Errorf("the file was not rewritten to Den's: %+v", r)
	}
	h.s.ConsolePowersAnswered(cur.ID, true)
	if g := readNow(h); levelsOf(g)[perm.Routine] != 4 || !g.StartConfirmed {
		t.Errorf("after the ^Y: %+v", levelsOf(g))
	}
	// The rewrite never takes the change it answered for another one.
	time.Sleep(50 * time.Millisecond)
	waitWritten(t, h)
	h.s.checkPerms()
	if h.s.permsHeld() {
		t.Error("the levels were held again after the ^Y")
	}
}

// Den's own writes are never taken for another program's: the file's
// checks race its writes, a lowering and a new pairing, and nothing is
// held (run under -race).
func TestPermsWritesNeverLookTampered(t *testing.T) {
	h, c := provedConsoleOn(t, permsHarness(t, t.TempDir()))
	getGrants(t, c)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < 300; i++ {
			h.s.checkPerms()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 300; i++ {
			h.s.permsDo(func() { h.s.perms.gen++ })
		}
	}()
	go func() {
		defer wg.Done()
		g := getGrants(t, c)
		lowerOn(c, `{"class":"routine","level":2,"seq":$seq}`, g.Seq)
		repair(t, h)
	}()
	wg.Wait()
	waitWritten(t, h)
	h.s.checkPerms()
	if h.s.permsHeld() {
		t.Error("Den took its own write for another program's")
	}
}

// A clean exit marks the file only while it is what Den wrote.
func TestPermsCleanMark(t *testing.T) {
	dir := t.TempDir()
	h := permsHarness(t, dir)
	waitWritten(t, h)
	h.s.Close()
	raw, _ := os.ReadFile(filepath.Join(dir, "permissions.json"))
	var mark permsMark
	if b, err := os.ReadFile(filepath.Join(dir, "permissions.json.clean")); err != nil || json.Unmarshal(b, &mark) != nil || mark.Record != sha256Hex(raw) {
		t.Fatalf("the mark %v %+v", err, mark)
	}
	h = permsHarness(t, dir)
	if _, err := os.Stat(filepath.Join(dir, "permissions.json.clean")); err == nil {
		t.Error("the mark outlived the start")
	}
	waitWritten(t, h)
	os.WriteFile(filepath.Join(dir, "permissions.json"), []byte(validRecord), 0o600)
	h.s.Close()
	if _, err := os.Stat(filepath.Join(dir, "permissions.json.clean")); err == nil {
		t.Error("a file another program wrote was marked")
	}
}

// Each taken lowering changes the file once, and a refused one not at
// all: one Den process writes it a bounded number of times.
func TestPermsWritesAreBounded(t *testing.T) {
	h, c := provedConsoleOn(t, permsHarness(t, t.TempDir()))
	g := getGrants(t, c)
	gen := func() uint64 {
		h.s.perms.mu.Lock()
		defer h.s.perms.mu.Unlock()
		return h.s.perms.gen
	}
	start, taken := gen(), uint64(0)
	for _, cl := range g.Classes {
		for level := capOf(g, cl.Class) - 1; level >= 0; level-- {
			g = getGrants(t, c)
			if code, _ := lowerOn(c, `{"class":"`+cl.Class+`","level":`+strconv.Itoa(level)+`,"seq":$seq}`, g.Seq); code == "" {
				taken++
			}
		}
	}
	if gen()-start != taken || taken > 5*perm.Steps {
		t.Errorf("%d changes for %d lowerings", gen()-start, taken)
	}
	g = getGrants(t, c)
	before := gen()
	lowerOn(c, `{"class":"routine","level":4,"seq":$seq}`, g.Seq)
	if gen() != before {
		t.Error("a refused lowering changed the file")
	}
}

// The start note: the last run's lowerings ended, named by Den's words;
// the console only when it is the one restored now, by Den's own label.
func TestPermsStartNote(t *testing.T) {
	h := newHarness(t, 2)
	last := []perm.Lowered{{Class: perm.Code, Level: 0, At: "2026-10-30T12:00:00Z", Console: "drey"}}
	h.s.startNote(last)
	noteSaid(t, h, "Den restarted, so a console's lowering of Run commands to 0 of 5 ended. A console can lower it again.")
	h.s.con.mu.Lock()
	h.s.con.cur = &consoleRec{Console: Console{ID: "c1", Exe: "/usr/bin/drey"}}
	h.s.con.mu.Unlock()
	cur, _ := h.s.CurrentConsole()
	label := lowerLabel(cur)
	last = []perm.Lowered{
		{Class: perm.Code, Level: 0, At: "2026-10-30T12:00:00Z", Console: label},
		{Class: perm.Destructive, Level: 0, At: "2026-10-30T12:00:00Z", Console: label},
	}
	h.s.startNote(last)
	noteSaid(t, h, "Den restarted, so "+label+"'s lowerings ended: Run commands to 0 of 5, Stop and close to 0 of 5. "+label+" can lower them again.")
	last[1].Console = "someone else"
	h.s.startNote(last)
	noteSaid(t, h, "Den restarted, so a console's lowerings ended")
}

// The stored lowerings are this session's, and a start empties them: the
// last run's go to the start note only.
func TestPermsStartEmptiesLowered(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "permissions.json")
	rec := strings.Replace(validRecord, `"lowered":[]`, `"lowered":[{"class":"code","level":0,"at":"2026-10-30T12:00:00Z","console":"drey"}]`, 1)
	os.WriteFile(path, []byte(rec), 0o600)
	writeMark(t, path, []byte(rec))
	h := permsHarness(t, dir)
	noteSaid(t, h, "Den restarted, so a console's lowering of Run commands to 0 of 5 ended.")
	if r := storedRecord(t, h); len(r.Lowered) != 0 {
		t.Errorf("lowered after the start: %+v", r.Lowered)
	}
}
