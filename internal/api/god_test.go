package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// hello and console.auth carry God mode as Den set it; god.off needs the
// console's proof and only asks Den to turn it off; a card only Den's
// window answers in God mode says tty_only.
func TestGodModeOnTheWire(t *testing.T) {
	h, c := provedConsole(t)
	hello := func() God {
		x := h.dial()
		l := x.call("hello", map[string]any{"v": 1, "client": "test", "version": "0"})
		var in Info
		if !l.OK || json.Unmarshal(l.Result, &in) != nil || in.God == nil {
			t.Fatalf("hello %+v %s", l.Error, l.Result)
		}
		return *in.God
	}
	if g := hello(); g.On {
		t.Errorf("God mode on at start: %+v", g)
	}
	since := time.Date(2026, 10, 15, 18, 0, 0, 0, time.UTC)
	h.s.SetGod(true, since)
	if e := c.next(); e.Event != EventGod || !strings.Contains(string(e.Data), `"on":true`) {
		t.Errorf("god event %+v", e)
	}
	if g := hello(); !g.On || g.Since != "2026-10-15T18:00:00Z" {
		t.Errorf("hello god %+v", g)
	}
	other := h.dial()
	other.hello()
	if l := other.call("god.off", nil); code(l) != CodeForbidden {
		t.Errorf("god.off without a proof: %+v", l)
	}
	if l := c.call("god.off", nil); !l.OK || !strings.Contains(string(l.Result), `"was_on":true`) {
		t.Errorf("god.off %+v %s", l.Error, l.Result)
	}
	if a := ctlAct(t, h.s, func(a Act) bool { return a.GodOff != "" }); a.GodOff == "" {
		t.Error("no GodOff act")
	}
	// A card Den marks TTY only.
	tty := card("t-1", 1, false)
	tty.TTYOnly = true
	h.s.Publish(Project(Snapshot{Taken: time.Now(), Cards: []Card{tty}}))
	c.next() // new
	g := getCard(t, c, "t-1")
	if l := c.replyTo(answerCard(c, g, "deny")); code(l) != CodeTTYOnly {
		t.Errorf("a TTY-only card answered from the dock: %+v", l)
	}
	// Unpairing ends God mode too.
	h.s.UnpairConsole("")
	if a := ctlAct(t, h.s, func(a Act) bool { return a.GodOff != "" }); a.GodOff == "" {
		t.Error("no GodOff act on unpair")
	}
}
