package permmode

import "testing"

// Every pair of modes, on the line and off it: a lowering only down the
// line, a raise for every other change.
func TestLowersEveryPair(t *testing.T) {
	all := []string{DontAsk, Plan, Default, AcceptEdits, Auto, Bypass, "", "unknown", "Default", "dontask"}
	// dontAsk < default < plan, acceptEdits < auto; plan and acceptEdits
	// unranked against each other, so either way between them is a raise.
	lowers := map[[2]string]bool{
		{Default, DontAsk}: true,
		{Plan, DontAsk}:    true, {Plan, Default}: true,
		{AcceptEdits, DontAsk}: true, {AcceptEdits, Default}: true,
		{Auto, DontAsk}: true, {Auto, Default}: true, {Auto, Plan}: true, {Auto, AcceptEdits}: true,
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
		{"", Plan, Default},
		{"", DontAsk, DontAsk},
		{"", "", Default},
		{Plan, AcceptEdits, Default},
		{AcceptEdits, Plan, Default},
		{Auto, Plan, Plan},
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

// plan and acceptEdits are not ranked against each other: each runs
// without asking something the other asks for (plan, classifier-allowed
// commands; acceptEdits, edits in its folder). Either way is a raise, and
// so is default to plan.
func TestPlanAndAcceptEditsAreUnranked(t *testing.T) {
	for _, c := range [][2]string{{Plan, AcceptEdits}, {AcceptEdits, Plan}, {Default, Plan}} {
		if Lowers(c[0], c[1]) || !Raises(c[0], c[1]) {
			t.Errorf("%s to %s is not a raise", c[0], c[1])
		}
	}
}

// The shipped mode is auto, the top: config.toml at most that grants
// nothing more than no config at all.
func TestShippedIsTheTop(t *testing.T) {
	if Shipped != Auto {
		t.Fatalf("Shipped = %q", Shipped)
	}
	for _, m := range Modes() {
		if Lower(m, Shipped) != m {
			t.Errorf("Lower(%q, Shipped) = %q", m, Lower(m, Shipped))
		}
	}
}
