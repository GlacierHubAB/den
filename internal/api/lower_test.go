package api

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/api/convcases"
	perm "github.com/GlacierHubAB/den/internal/grants"
)

// readNow is the read as Den would give it now, without a connection.
func readNow(h *harness) perm.Grants {
	h.s.perms.mu.Lock()
	defer h.s.perms.mu.Unlock()
	return h.s.grantsLocked(nil)
}

// capOf is class's cap in g: min(den_level, session.level).
func capOf(g perm.Grants, class string) int {
	for _, c := range g.Classes {
		if c.Class == class {
			if c.Session != nil && c.Session.Level < c.DenLevel {
				return c.Session.Level
			}
			return c.DenLevel
		}
	}
	return -1
}

// lowerOn sends grants.lower with params on c, "$seq" and "$old" filled
// from seq, and returns its code ("" when taken) and its read.
func lowerOn(c *client, params string, seq uint64) (string, perm.Grants) {
	params = strings.ReplaceAll(params, "$seq", strconv.FormatUint(seq, 10))
	params = strings.ReplaceAll(params, "$old", strconv.FormatUint(seq-1, 10))
	l := c.call("grants.lower", json.RawMessage(params))
	var g perm.Grants
	if l.OK {
		json.Unmarshal(l.Result, &g)
	}
	return code(l), g
}

// asOtherConsole makes c's connection a proved one of a console that is
// not the one paired now.
func asOtherConsole(h *harness) {
	cur, _ := h.s.CurrentConsole()
	h.s.mu.Lock()
	defer h.s.mu.Unlock()
	for sc := range h.s.conns {
		sc.mu.Lock()
		if sc.console == cur.ID {
			sc.console = "another-console"
		}
		sc.mu.Unlock()
	}
}

// Every row of the lowering table, against grants.lower: the code, and
// the cap and seq after it; a refusal changes nothing and pushes nothing.
func TestLowerTable(t *testing.T) {
	for _, row := range convcases.LowerCases {
		h, c := provedConsole(t)
		h.s.perms.mu.Lock()
		h.s.perms.caps = map[string]perm.Session{}
		for class, level := range row.Before {
			h.s.perms.caps[class] = perm.Session{Level: level, At: "2026-10-29T12:00:00Z", Console: "Drey"}
		}
		h.s.perms.mu.Unlock()
		switch row.Holder {
		case "locked":
			h.s.LockConsole("a test")
		case "nopowers":
			h.s.LockConsolePowers("a test")
		}
		before := getGrants(t, c) // subscribes c
		sender := c
		switch row.Holder {
		case "unproved":
			sender = h.dial()
			sender.hello()
		case "other":
			asOtherConsole(h)
		}
		got, g := lowerOn(sender, row.Request, before.Seq)
		if got != row.Code {
			t.Errorf("%s: code %q, want %q", row.Name, got, row.Code)
			continue
		}
		after := readNow(h)
		if capOf(after, row.Class) != row.Cap {
			t.Errorf("%s: %s's cap is %d, want %d", row.Name, row.Class, capOf(after, row.Class), row.Cap)
		}
		if row.Code != "" {
			if after.Seq != before.Seq {
				t.Errorf("%s: a refusal moved seq %d to %d", row.Name, before.Seq, after.Seq)
			}
			if row.Holder != "other" {
				noGrants(t, c)
			}
			continue
		}
		if after.Seq <= before.Seq || g.Seq != after.Seq || capOf(g, row.Class) != row.Cap {
			t.Errorf("%s: seq %d to %d, reply %d, reply cap %d", row.Name, before.Seq, after.Seq, g.Seq, capOf(g, row.Class))
		}
		for _, cl := range g.Classes {
			if cl.Class == row.Class && (cl.Session == nil || cl.Session.Console == "" || cl.Session.At == "") {
				t.Errorf("%s: session %+v", row.Name, cl.Session)
			}
		}
	}
}

// P9's acceptance: no way up is taken. For every class, every level from
// its cap up is not_lower, before and after a lowering and after a
// re-pair, and each leaves the read and seq as they were, with no grants
// event.
func TestLowerRefusesEveryRaise(t *testing.T) {
	h, c := provedConsole(t)
	raises := func(c *client, who string) {
		t.Helper()
		g := getGrants(t, c)
		for _, cl := range g.Classes {
			for level := capOf(g, cl.Class); level <= perm.Steps; level++ {
				code, _ := lowerOn(c, `{"class":"`+cl.Class+`","level":`+strconv.Itoa(level)+`,"seq":$seq}`, g.Seq)
				if code != CodeNotLower {
					t.Errorf("%s: %s to %d: %q", who, cl.Class, level, code)
				}
			}
		}
		raw, _ := json.Marshal(g)
		now := readNow(h)
		now2, _ := json.Marshal(now)
		if string(raw) != string(now2) {
			t.Errorf("%s: the read changed:\n%s\n%s", who, raw, now2)
		}
		noGrants(t, c)
	}
	raises(c, "at the defaults")
	g := getGrants(t, c)
	if code, _ := lowerOn(c, `{"class":"routine","level":2,"seq":$seq}`, g.Seq); code != "" {
		t.Fatalf("the lowering: %q", code)
	}
	raises(c, "after a lowering to 2")
	d := newDock(t, h)
	p := d.pair(h.s.StartConsoleCode())
	c2 := h.dial()
	c2.hello()
	if l, _, _, _ := d.auth(c2, p); !l.OK {
		t.Fatalf("auth %+v", l.Error)
	}
	if capOf(getGrants(t, c2), perm.Routine) != 2 {
		t.Error("a re-pair dropped the lowering")
	}
	raises(c2, "after a re-pair")
}

// Two lowerings with the same seq: the first is taken, the second is
// stale, since the first moved seq.
func TestLowerTwiceOnOneSeq(t *testing.T) {
	_, c := provedConsole(t)
	g := getGrants(t, c)
	if code, _ := lowerOn(c, `{"class":"routine","level":3,"seq":$seq}`, g.Seq); code != "" {
		t.Fatalf("first: %q", code)
	}
	if code, _ := lowerOn(c, `{"class":"routine","level":2,"seq":$seq}`, g.Seq); code != CodeStale {
		t.Errorf("second on the same seq: %q", code)
	}
}

// A lowering tells Den, for its chat: the class, its label, the level and
// the console's label, all Den's own; and with Den's act queue full, a
// lowering and God mode still return at once.
func TestLowerTellsDenAndNeverWaitsOnIt(t *testing.T) {
	h, c := provedConsole(t)
	g := getGrants(t, c)
	if code, _ := lowerOn(c, `{"class":"code","level":0,"seq":$seq}`, g.Seq); code != "" {
		t.Fatalf("lowering: %q", code)
	}
	a := ctlAct(t, h.s, func(a Act) bool { return a.GrantsLowered != nil }).GrantsLowered
	if a.Class != perm.Code || a.Label != "Run commands" || a.Level != 0 || a.Console == "" {
		t.Errorf("act %+v", a)
	}
	for h.s.act(Act{}) {
	} // Den's queue is full: no one reads it
	done := make(chan struct{})
	go func() {
		g := getGrants(t, c)
		lowerOn(c, `{"class":"routine","level":1,"seq":$seq}`, g.Seq)
		h.s.SetGod(true, time.Now())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a lowering waited on Den's loop")
	}
}

// Lowerings, God mode and publishes at once: no deadlock, and seq only
// rises on the subscribed connection (run under -race too).
func TestLowerUnderConcurrentChanges(t *testing.T) {
	h, c := provedConsole(t)
	g := getGrants(t, c)
	x := h.dial()
	x.hello()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			h.s.SetGod(i%2 == 0, time.Now())
			h.s.Publish(Project(Snapshot{Taken: time.Now()}))
			x.send("grants.lower", json.RawMessage(`{"class":"routine","level":1,"seq":1}`)) // refused: not proved
		}
	}()
	level := 3
	for level >= 0 {
		now := readNow(h)
		code, _ := lowerOn(c, `{"class":"routine","level":`+strconv.Itoa(level)+`,"seq":$seq}`, now.Seq)
		switch code {
		case "":
			level--
		case CodeStale:
		default:
			t.Fatalf("lowering to %d: %q", level, code)
		}
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("deadlocked")
	}
	last := g.Seq
	c.nc.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	for {
		raw, err := c.r.ReadBytes('\n')
		if err != nil {
			break
		}
		var l line
		var got perm.Grants
		if json.Unmarshal(raw, &l) != nil || l.Event != EventGrants || json.Unmarshal(l.Data, &got) != nil {
			continue
		}
		if got.Seq <= last {
			t.Errorf("seq %d after %d", got.Seq, last)
		}
		last = got.Seq
	}
}
