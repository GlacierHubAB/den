package api

import (
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/permmode"
)

// Den's global mode reaches a console in the levels push: no key before Den
// has read it, then the mode and Den's own list of what it lowers to, in
// grants.get and as a grants event on each change (a new seq), and nothing
// when it is set again to what it was.
func TestModeInTheLevelsPush(t *testing.T) {
	h, c := provedConsole(t)
	l := c.call("grants.get", nil)
	if !l.OK || strings.Contains(string(l.Result), `"mode"`) {
		t.Fatalf("before Den has read its mode: %+v %s", l.Error, l.Result)
	}
	first := getGrants(t, c)

	h.s.SetMode(permmode.Auto)
	g := nextGrants(t, c)
	if g.Mode == nil || g.Mode.Current != permmode.Auto || g.Seq <= first.Seq {
		t.Fatalf("after SetMode(auto): %+v, seq %d (was %d)", g.Mode, g.Seq, first.Seq)
	}
	if want := permmode.LowerTo(permmode.Auto); strings.Join(g.Mode.LowerTo, ",") != strings.Join(want, ",") {
		t.Errorf("lower_to %v, want %v", g.Mode.LowerTo, want)
	}

	h.s.SetMode(permmode.Auto)
	noGrants(t, c)

	h.s.SetMode(permmode.Plan)
	g = nextGrants(t, c)
	if g.Mode == nil || g.Mode.Current != permmode.Plan || strings.Join(g.Mode.LowerTo, ",") != "dontAsk,default" {
		t.Fatalf("after SetMode(plan): %+v", g.Mode)
	}
	if got := getGrants(t, c); got.Mode == nil || got.Mode.Current != permmode.Plan || got.Seq != g.Seq {
		t.Errorf("grants.get after the push: %+v seq %d, the push had %d", got.Mode, got.Seq, g.Seq)
	}
	if h.s.ModeNow() != permmode.Plan {
		t.Errorf("ModeNow %q", h.s.ModeNow())
	}
}

// A mode that is not ranked is not offered: bypassPermissions is God mode's
// and no setting of the global mode names it, so the key goes away rather
// than carry it.
func TestModePushNeverOffersBypass(t *testing.T) {
	h, c := provedConsole(t)
	getGrants(t, c)
	h.s.SetMode(permmode.Default)
	if g := nextGrants(t, c); g.Mode == nil {
		t.Fatal("no mode after SetMode(default)")
	}
	h.s.SetMode(permmode.Bypass)
	if g := nextGrants(t, c); g.Mode != nil {
		t.Fatalf("a console is shown %+v for bypassPermissions", g.Mode)
	}
	if l := c.call("grants.get", nil); strings.Contains(string(l.Result), `"mode"`) {
		t.Fatalf("the read still carries a mode: %s", l.Result)
	}
}

// With no console to tell, SetMode only records; the console that pairs
// next reads the mode in grants.get.
func TestModeRecordedWithoutAConsole(t *testing.T) {
	h, _ := consoleHarness(t)
	h.s.SetMode(permmode.AcceptEdits)
	if h.s.ModeNow() != permmode.AcceptEdits {
		t.Fatalf("ModeNow %q", h.s.ModeNow())
	}
	_, c := provedConsoleOn(t, h)
	if g := getGrants(t, c); g.Mode == nil || g.Mode.Current != permmode.AcceptEdits {
		t.Fatalf("a console paired afterwards reads %+v", g.Mode)
	}
}
