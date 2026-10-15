package permmode

import "testing"

// Every pair of modes, on the line and off it: a lowering only down the
// line, a raise for every other change.
func TestLowersEveryPair(t *testing.T) {
	all := []string{DontAsk, Plan, Default, AcceptEdits, Auto, Bypass, "", "unknown", "Default", "dontask"}
	lowers := map[[2]string]bool{
		{Plan, DontAsk}: true,
		{Default, DontAsk}: true, {Default, Plan}: true,
		{AcceptEdits, DontAsk}: true, {AcceptEdits, Plan}: true, {AcceptEdits, Default}: true,
		{Auto, DontAsk}: true, {Auto, Plan}: true, {Auto, Default}: true, {Auto, AcceptEdits}: true,
	}
	for _, from := range all {
		for _, to := range all {
			want := lowers[[2]string{from, to}]
			if got := Lowers(from, to); got != want {
				t.Errorf("Lowers(%q, %q) = %v, want %v", from, to, got, want)
			}
			if got, want := Raises(from, to), from != to && !want; got != want {
				t.Errorf("Raises(%q, %q) = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestBypassNeverValid(t *testing.T) {
	if Valid(Bypass) {
		t.Fatal("bypassPermissions is valid as the global mode")
	}
	for _, s := range []string{"bypassPermissions", "bypass", "BYPASS_PERMISSIONS", "", " default", "default\n"} {
		if m, ok := Parse(s); ok {
			t.Errorf("Parse(%q) = %q, want refused", s, m)
		}
	}
	for _, m := range Modes() {
		if m == Bypass {
			t.Fatal("Modes lists bypassPermissions")
		}
	}
}

func TestParse(t *testing.T) {
	for in, want := range map[string]string{
		"default": Default, "Default": Default, "plan": Plan, "PLAN": Plan, "auto": Auto,
		"acceptEdits": AcceptEdits, "accept-edits": AcceptEdits, "accept_edits": AcceptEdits,
		"dontAsk": DontAsk, "dont-ask": DontAsk, "dontask": DontAsk,
	} {
		if got, ok := Parse(in); !ok || got != want {
			t.Errorf("Parse(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

func TestLower(t *testing.T) {
	for _, c := range []struct{ a, b, want string }{
		{Auto, Default, Default},
		{Default, Auto, Default},
		{Plan, DontAsk, DontAsk},
		{Bypass, Auto, Default},
		{"", Plan, Plan},
		{"", "", Default},
		{AcceptEdits, AcceptEdits, AcceptEdits},
	} {
		if got := Lower(c.a, c.b); got != c.want {
			t.Errorf("Lower(%q, %q) = %q, want %q", c.a, c.b, got, c.want)
		}
	}
}

func TestAllowsEveryMode(t *testing.T) {
	for _, m := range append(Modes(), Bypass) {
		if Allows(m) == "unknown" {
			t.Errorf("Allows(%q) has no line", m)
		}
	}
}
