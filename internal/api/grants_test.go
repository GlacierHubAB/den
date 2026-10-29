package api

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/api/convcases"
	perm "github.com/GlacierHubAB/den/internal/grants"
)

// getGrants is grants.get on c.
func getGrants(t *testing.T, c *client) perm.Grants {
	t.Helper()
	l := c.call("grants.get", nil)
	var g perm.Grants
	if !l.OK || json.Unmarshal(l.Result, &g) != nil {
		t.Fatalf("grants.get: %+v %s", l.Error, l.Result)
	}
	return g
}

// nextGrants is the next event on c, which must be a grants event.
func nextGrants(t *testing.T, c *client) perm.Grants {
	t.Helper()
	l := c.next()
	var g perm.Grants
	if l.Event != EventGrants || json.Unmarshal(l.Data, &g) != nil {
		t.Fatalf("want a grants event, got %q %s", l.Event, l.Data)
	}
	return g
}

// noGrants fails if a grants event comes on c within a short wait.
func noGrants(t *testing.T, c *client) {
	t.Helper()
	c.nc.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	raw, err := c.r.ReadBytes('\n')
	if err != nil {
		return
	}
	var l line
	if json.Unmarshal(raw, &l) == nil && l.Event == EventGrants {
		t.Errorf("a grants event: %s", l.Data)
	}
}

// levelsOf is each class's effective level.
func levelsOf(g perm.Grants) map[string]int {
	out := map[string]int{}
	for _, c := range g.Classes {
		out[c.Class] = c.Level
	}
	return out
}

// grants.get needs a proved console; it answers the read, and from then
// on the connection hears each change once, in seq order: God mode, the
// lock, the powers. A publish that changes nothing pushes nothing.
func TestGrantsGetAndChanges(t *testing.T) {
	h, c := provedConsole(t)
	other := h.dial()
	other.hello()
	if l := other.call("grants.get", nil); code(l) != CodeForbidden {
		t.Errorf("grants.get without a proof: %+v", l)
	}
	g := getGrants(t, c)
	if !g.Powers || g.Locked || g.God || !g.StartConfirmed || g.Enforced || g.Seq == 0 {
		t.Fatalf("the read %+v", g)
	}
	if lv := levelsOf(g); lv[perm.Routine] != 4 || lv[perm.Destructive] != 1 || lv[perm.Code] != 1 {
		t.Errorf("levels %v", lv)
	}
	seq := g.Seq
	h.s.Publish(Project(Snapshot{Taken: time.Now()}))
	noGrants(t, c)

	h.s.SetGod(true, time.Now())
	if e := c.next(); e.Event != EventGod {
		t.Fatalf("god event first: %+v", e)
	}
	g = nextGrants(t, c)
	if !g.God || g.Seq <= seq || levelsOf(g)[perm.Routine] != 4 {
		t.Errorf("after God mode: %+v", g)
	}
	seq = g.Seq
	h.s.SetGod(false, time.Time{})
	c.next() // god off
	if g = nextGrants(t, c); g.God || g.Seq <= seq {
		t.Errorf("after God mode off: %+v", g)
	}
	seq = g.Seq

	h.s.LockConsole("a test")
	for {
		l := c.next()
		if l.Event != EventGrants {
			continue // the powers event
		}
		json.Unmarshal(l.Data, &g)
		break
	}
	if !g.Locked || g.Powers || g.Seq <= seq {
		t.Errorf("after the lock: %+v", g)
	}
	for k, v := range levelsOf(g) {
		if v != 0 {
			t.Errorf("locked: %s at %d", k, v)
		}
	}
	noGrants(t, c) // once per change
}

// A connection that never asked hears no grants event, and the
// console.events snapshot carries none.
func TestGrantsArePushedOnlyToWhoAsked(t *testing.T) {
	h, c := provedConsole(t)
	h.s.SetGod(true, time.Now())
	if e := c.next(); e.Event != EventGod {
		t.Fatalf("god event: %+v", e)
	}
	noGrants(t, c)
}

// seq is Den's and only rises: across a re-pair, while Den runs.
func TestGrantsSeqOnlyRises(t *testing.T) {
	h, c := provedConsole(t)
	first := getGrants(t, c).Seq
	h.s.LockConsolePowers("a test")
	h.s.Publish(Project(Snapshot{Taken: time.Now()}))
	d := newDock(t, h)
	p := d.pair(h.s.StartConsoleCode())
	c2 := h.dial()
	c2.hello()
	if l, _, _, _ := d.auth(c2, p); !l.OK {
		t.Fatalf("auth %+v", l.Error)
	}
	if g := getGrants(t, c2); g.Seq <= first {
		t.Errorf("seq %d after a re-pair, was %d", g.Seq, first)
	}
}

// The read is Den's fixed words exactly, as the table both sides pin says,
// in every state.
func TestGrantsTable(t *testing.T) {
	states := map[string]perm.State{
		"a console with its powers": {Powers: true},
		"no console, or no powers":  {},
		"locked":                    {Locked: true},
		"God mode":                  {Powers: true, God: true},
	}
	for _, c := range convcases.GrantsCases {
		st, ok := states[c.Name]
		if !ok {
			t.Errorf("%s: no state for it", c.Name)
			continue
		}
		g := perm.Read(st)
		g.Seq = 7
		raw, _ := json.Marshal(g)
		var got, want any
		json.Unmarshal(raw, &got)
		if err := json.Unmarshal([]byte(c.JSON), &want); err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: Den sends\n%s\nthe table says\n%s", c.Name, raw, c.JSON)
		}
	}
}

// Levels gate nothing yet: under each state (powers, God mode on and off,
// locked and unlocked), card.answer gives the same code whether or not the
// connection has read the grants, and whatever the read says.
func TestGrantsChangeNoAnswer(t *testing.T) {
	answer := func(god, lock, read bool) string {
		h, c := provedConsole(t)
		h.s.Publish(Project(Snapshot{Taken: time.Now(), Cards: []Card{card("i-1", 1, true)}}))
		c.next() // the card
		if god {
			h.s.SetGod(true, time.Now())
			c.next()
		}
		if lock {
			h.s.LockConsole("a test")
		}
		if read {
			getGrants(t, c)
		}
		g := getCard(t, c, "i-1")
		id := answerCard(c, g, "allow")
	acts:
		for {
			select {
			case a := <-h.s.Acts():
				if a.CardAnswer != nil {
					a.CardAnswer.Reply <- CardResult{Result: "allowed"}
					break acts
				}
			case <-time.After(300 * time.Millisecond):
				break acts
			}
		}
		for {
			l := c.next()
			if l.ID != nil && *l.ID == id {
				return code(l)
			}
		}
	}
	for _, god := range []bool{false, true} {
		for _, lock := range []bool{false, true} {
			if a, b := answer(god, lock, false), answer(god, lock, true); a != b {
				t.Errorf("god %v lock %v: %q without the read, %q with it", god, lock, a, b)
			}
		}
	}
}

// The seq table follows the one rule: take an event only when its seq is
// higher than the one kept.
func TestGrantsSeqTable(t *testing.T) {
	for _, c := range convcases.GrantsSeqCases {
		if c.Take != (c.Event > c.Kept) {
			t.Errorf("%s: %+v", c.Name, c)
		}
	}
}

// A subscribed connection that never reads stalls nothing: God mode and
// the lock return at once however full its queue is, and the connection is
// dropped rather than waited for.
func TestGrantsNeverWaitOnAStalledConsole(t *testing.T) {
	h, c := provedConsole(t)
	getGrants(t, c)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 2000; i++ {
			h.s.SetGod(i%2 == 0, time.Now())
		}
		h.s.LockConsole("a test")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Den waited on a console that does not read")
	}
}

// A pairing that replaces the console ends the old one's grants events:
// the old connection hears no change; the new console asks again.
func TestGrantsGoOnlyToTheConsolePairedNow(t *testing.T) {
	h, old := provedConsole(t)
	getGrants(t, old)
	d := newDock(t, h)
	p := d.pair(h.s.StartConsoleCode())
	c := h.dial()
	c.hello()
	if l, _, _, _ := d.auth(c, p); !l.OK {
		t.Fatalf("auth %+v", l.Error)
	}
	g := getGrants(t, c)
	h.s.SetGod(true, time.Now())
	if g2 := nextGrants(t, c); !g2.God || g2.Seq <= g.Seq {
		t.Errorf("the new console: %+v", g2)
	}
	old.nc.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	for {
		raw, err := old.r.ReadBytes('\n')
		if err != nil {
			break
		}
		var l line
		if json.Unmarshal(raw, &l) == nil && l.Event == EventGrants {
			t.Errorf("the old console heard: %s", l.Data)
		}
	}
	// Nor does it read the new console's levels, if its connection is still
	// open: refused, or closed.
	old.nc.SetWriteDeadline(time.Now().Add(time.Second))
	raw, _ := json.Marshal(map[string]any{"v": 1, "id": 99, "op": "grants.get"})
	if _, err := old.nc.Write(append(raw, '\n')); err == nil {
		old.nc.SetReadDeadline(time.Now().Add(time.Second))
		for {
			raw, err := old.r.ReadBytes('\n')
			if err != nil {
				break // closed: nothing read
			}
			var l line
			if json.Unmarshal(raw, &l) != nil || l.ID == nil || *l.ID != 99 {
				continue
			}
			if code(l) != CodeForbidden {
				t.Errorf("the old console read the grants: %+v %s", l.Error, l.Result)
			}
			break
		}
	}
}

// A proved connection of a console that is no longer the one paired is
// refused the read, whatever happened to its connection.
func TestGrantsGetRefusesAConsoleNotPairedNow(t *testing.T) {
	h, c := provedConsole(t)
	getGrants(t, c)
	cur, _ := h.s.CurrentConsole()
	h.s.mu.Lock()
	for sc := range h.s.conns {
		sc.mu.Lock()
		if sc.console == cur.ID {
			sc.console = "another-console"
		}
		sc.mu.Unlock()
	}
	h.s.mu.Unlock()
	if l := c.call("grants.get", nil); code(l) != CodeForbidden {
		t.Errorf("a console not paired now: %+v %s", l.Error, l.Result)
	}
}

// Lock, God mode, publishes and reads at once: no deadlock, and seq only
// rises on the connection that asked (run under -race too).
func TestGrantsUnderConcurrentChanges(t *testing.T) {
	h, c := provedConsole(t)
	g := getGrants(t, c)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			h.s.SetGod(i%2 == 0, time.Now())
			h.s.Publish(Project(Snapshot{Taken: time.Now()}))
		}
		h.s.LockConsolePowers("a test")
		h.s.LockConsole("a test")
	}()
	reads := make(chan struct{})
	x := h.dial()
	x.hello()
	go func() {
		defer close(reads)
		for i := 0; i < 50; i++ {
			x.send("grants.get", nil) // refused: not proved; it must still not block
		}
	}()
	for _, ch := range []chan struct{}{done, reads} {
		select {
		case <-ch:
		case <-time.After(10 * time.Second):
			t.Fatal("deadlocked")
		}
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
