package api

import (
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	perm "github.com/GlacierHubAB/den/internal/grants"
)

// bothStores is a harness that keeps its console in store and its levels
// next to it.
func bothStores(t *testing.T, store string, priv ed25519.PrivateKey) *harness {
	t.Helper()
	return newHarnessWith(t, 2, func(o *Options) {
		o.ConsoleKey, o.ConsoleStore, o.PermStore = priv, store, filepath.Join(filepath.Dir(store), "permissions.json")
	})
}

// asBeforeTheLevels makes store's record one a Den without the levels
// wrote (no kept_era), with its clean mark still valid.
func asBeforeTheLevels(t *testing.T, store string) {
	t.Helper()
	raw, _ := os.ReadFile(store)
	var rec map[string]any
	if json.Unmarshal(raw, &rec) != nil {
		t.Fatalf("record %s", raw)
	}
	for _, c := range rec["consoles"].([]any) {
		delete(c.(map[string]any), "kept_era")
	}
	raw, _ = json.Marshal(rec)
	os.WriteFile(store, raw, 0o600)
	var mark cleanMark
	b, _ := os.ReadFile(store + ".clean")
	json.Unmarshal(b, &mark)
	mark.Record = sha256Hex(raw)
	b, _ = json.Marshal(mark)
	os.WriteFile(store+".clean", b, 0o600)
}

func keptNow(h *harness) []string {
	h.s.perms.mu.Lock()
	defer h.s.perms.mu.Unlock()
	return append([]string(nil), h.s.perms.kept...)
}

// K1: the upgrade grants kept by the console record, once. A console
// restored from a record without kept_era keeps the whole list, inactive
// until the start confirm, and the record then has kept_era; deleting
// permissions.json afterwards gives an empty kept, never the list back.
func TestKeptGrantedOnceByTheConsoleRecord(t *testing.T) {
	store, priv, _, _ := pairedStore(t)
	asBeforeTheLevels(t, store)
	h := bothStores(t, store, priv)
	if got := keptNow(h); !reflect.DeepEqual(got, perm.KeptAll()) {
		t.Fatalf("kept after the upgrade: %v", got)
	}
	if raw, _ := os.ReadFile(store); !strings.Contains(string(raw), `"kept_era":true`) {
		t.Errorf("the record lacks kept_era: %s", raw)
	}
	noteSaid(t, h, "Your console keeps what it could answer before")
	if g := readNow(h); len(g.Kept) != len(perm.KeptAll()) || g.Kept[0].Active {
		t.Errorf("kept in the read before the confirm: %+v", g.Kept)
	}
	lines := h.s.permsCardLines()
	if len(lines) == 0 || !strings.HasPrefix(lines[len(lines)-1], "Ctrl+Y also lets your console answer, as before: Allow a tool once") {
		t.Errorf("the card says %q", lines)
	}
	// A removal, then a clean exit, then permissions.json and its mark
	// deleted: the record has kept_era, so nothing is granted again.
	h.s.permsDo(func() {
		seq := json.RawMessage(itoa(int(h.s.grantsLocked(nil).Seq)))
		h.s.keptRemoveLocked(nil, json.RawMessage(`"merge"`), seq)
	})
	if got := keptNow(h); len(got) != len(perm.KeptAll())-1 {
		t.Fatalf("after the removal: %v", got)
	}
	cleanExit(h.s)
	h2 := bothStores(t, store, priv)
	if got := keptNow(h2); len(got) != len(perm.KeptAll())-1 {
		t.Errorf("kept after a clean restart: %v", got)
	}
	cleanExit(h2.s)
	os.Remove(filepath.Join(filepath.Dir(store), "permissions.json"))
	os.Remove(filepath.Join(filepath.Dir(store), "permissions.json.clean"))
	h3 := bothStores(t, store, priv)
	if got := keptNow(h3); len(got) != 0 {
		t.Errorf("deleting permissions.json gave kept back: %v", got)
	}
}

// K1: a forged version 1 permissions.json with the console's lineage
// grants nothing when the console record has kept_era.
func TestKeptForgedFileGrantsNothing(t *testing.T) {
	store, priv, _, p := pairedStore(t)
	h := bothStores(t, store, priv)
	cur, _ := h.s.CurrentConsole()
	if cur.ID != p.ID || len(keptNow(h)) != 0 {
		t.Fatalf("a kept_era record granted: %v", keptNow(h))
	}
	cleanExit(h.s)
	rec := `{"version":1,"lineage":"` + cur.Lineage + `","den_levels":{"routine":4,"destructive":1,"code":1,"desktop":0,"outward":0},"kept":[],"lowered":[]}`
	os.WriteFile(filepath.Join(filepath.Dir(store), "permissions.json"), []byte(rec), 0o600)
	h2 := bothStores(t, store, priv)
	if got := keptNow(h2); len(got) != 0 {
		t.Errorf("a forged version 1 granted %v", got)
	}
}

// grants.lower {kept}: a held entry is removed for good, "*" removes all,
// one not held is not_lower, an id outside the list bad_request; a new
// pairing (F3) clears kept.
func TestKeptRemoval(t *testing.T) {
	h, c := provedConsoleOn(t, permsHarness(t, t.TempDir()))
	readNow(h)
	h.s.perms.mu.Lock()
	h.s.perms.kept = perm.KeptAll()
	h.s.perms.mu.Unlock()
	g := getGrants(t, c)
	for _, row := range []struct{ req, code string }{
		{`{"kept":"merge","seq":$seq}`, ""},
		{`{"kept":"merge","seq":$seq}`, CodeNotLower},
		{`{"kept":"admin","seq":$seq}`, CodeBadRequest},
		{`{"kept":"close","seq":$old}`, CodeStale},
		{`{"kept":"*","seq":$seq}`, ""},
		{`{"kept":"*","seq":$seq}`, CodeNotLower},
	} {
		g = getGrants(t, c)
		if code, _ := lowerOn(c, row.req, g.Seq); code != row.code {
			t.Errorf("%s: %q, want %q", row.req, code, row.code)
		}
	}
	if got := keptNow(h); len(got) != 0 {
		t.Errorf("kept after * %v", got)
	}
	h.s.perms.mu.Lock()
	h.s.perms.kept = perm.KeptAll()
	h.s.perms.mu.Unlock()
	repair(t, h)
	readNow(h)
	if got := keptNow(h); len(got) != 0 {
		t.Errorf("a new pairing kept %v", got)
	}
}

// The answer check through den-api: below_level names the class, a taken
// allow above routine starts the class limit and a second within 3 s is
// rate_limited (a refusal never counts), deny is never limited, and the
// listing never sees the limit.
func TestAnswerAllowedAndTheClassLimit(t *testing.T) {
	h, _ := provedConsoleOn(t, permsHarness(t, t.TempDir()))
	readNow(h)
	cur, _ := h.s.CurrentConsole()
	h.s.ConsolePowersAnswered(cur.ID, true) // the start confirm
	merge := perm.Answer{Kept: perm.KeptMerge, Class: perm.Outward, Step: 2, Verb: "allow", GodTTY: true}
	if code, class := h.s.AnswerAllowed(merge); code != CodeBelowLevel || class != perm.Outward {
		t.Errorf("a merge: %q %q", code, class)
	}
	bash := perm.Answer{Kept: perm.KeptPermission, Class: perm.Code, Step: 1, Verb: "allow"}
	if code, _ := h.s.AnswerAllowed(bash); code != "" {
		t.Fatalf("a command: %q", code)
	}
	if !h.s.AnswerListed(bash) {
		t.Error("the class limit reached the listing")
	}
	if code, _ := h.s.AnswerAllowed(bash); code != CodeRateLimited {
		t.Errorf("a second command at once: %q", code)
	}
	if code, _ := h.s.AnswerAllowed(perm.Answer{Class: perm.Code, Step: 1, Verb: "deny"}); code != "" {
		t.Errorf("a deny: %q", code)
	}
	read := perm.Answer{Kept: perm.KeptPermission, Class: perm.Routine, Step: 3, Verb: "allow"}
	for i := 0; i < 3; i++ {
		if code, _ := h.s.AnswerAllowed(read); code != "" {
			t.Errorf("an everyday answer %d: %q", i, code)
		}
	}
	// Refusals do not count: after 3 s, one more command is taken.
	h.s.perms.mu.Lock()
	for i := range h.s.perms.allows {
		h.s.perms.allows[i] = h.s.perms.allows[i].Add(-4 * time.Second)
	}
	h.s.perms.mu.Unlock()
	if code, _ := h.s.AnswerAllowed(bash); code != "" {
		t.Errorf("a command after the gap: %q", code)
	}
}

// A grants change that moves a card's listing hands Den GrantsChanged,
// once; one that does not (the powers) does not.
func TestGrantsChangedReachesDen(t *testing.T) {
	h, c := provedConsoleOn(t, permsHarness(t, t.TempDir()))
	g := getGrants(t, c)
	drainActs(h)
	lowerOn(c, `{"class":"routine","level":2,"seq":$seq}`, g.Seq)
	ctlAct(t, h.s, func(a Act) bool { return a.GrantsChanged })
	drainActs(h)
	h.s.LockConsolePowers("a test")
	time.Sleep(50 * time.Millisecond)
	for {
		select {
		case a := <-h.s.Acts():
			if a.GrantsChanged {
				t.Fatal("the powers moved the listing")
			}
			continue
		default:
		}
		break
	}
}

// A card whose listing flips (dock_answers or the verbs, which follow the
// levels outside the hash) gets a changed event with the same hash, so the
// console reads it again; a publish that changes nothing pushes nothing.
func TestListingFlipIsAChangedEvent(t *testing.T) {
	h, c := provedConsole(t)
	both := card("i-1", 1, true)
	both.Verbs = []string{"allow", "deny"}
	h.s.Publish(Project(Snapshot{Taken: time.Now(), Cards: []Card{both}}))
	first := c.next()
	denyOnly := both
	denyOnly.Verbs = []string{"deny"}
	h.s.Publish(Project(Snapshot{Taken: time.Now(), Cards: []Card{denyOnly}}))
	var a, b CardEvent
	json.Unmarshal(first.Data, &a)
	if e := c.next(); e.Event != EventCard || json.Unmarshal(e.Data, &b) != nil || b.Change != "changed" || b.Hash != a.Hash {
		t.Fatalf("the flip: %+v (first %+v)", b, a)
	}
	if g := getCard(t, c, "i-1"); len(g.Verbs) != 1 || g.Verbs[0] != "deny" {
		t.Errorf("card.get after the flip: %v", g.Verbs)
	}
	h.s.Publish(Project(Snapshot{Taken: time.Now(), Cards: []Card{denyOnly}}))
	c.nc.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if raw, err := c.r.ReadBytes('\n'); err == nil {
		t.Errorf("a publish with nothing changed pushed %s", raw)
	}
}

// K5: a lowering raced against answer checks. Every check that starts after
// grants.lower has answered is refused, and nothing races (run under
// -race): the check and the lowering are ordered by perms.mu.
func TestAnswerAllowedRacesALowering(t *testing.T) {
	for round := 0; round < 20; round++ {
		h, c := provedConsoleOn(t, permsHarness(t, t.TempDir()))
		readNow(h)
		cur, _ := h.s.CurrentConsole()
		h.s.ConsolePowersAnswered(cur.ID, true)
		read := perm.Answer{Kept: perm.KeptPermission, Class: perm.Routine, Step: 3, Verb: "allow"}
		g := getGrants(t, c)
		lowered := make(chan struct{})
		go func() {
			lowerOn(c, `{"class":"routine","level":2,"seq":$seq}`, g.Seq)
			close(lowered)
		}()
		after := false
		for !after {
			select {
			case <-lowered:
				after = true
			default:
			}
			code, _ := h.s.AnswerAllowed(read)
			if after && code != CodeBelowLevel {
				t.Fatalf("round %d: a check after the lowering gave %q", round, code)
			}
		}
	}
}
