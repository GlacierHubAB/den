package sysd

import (
	"os"
	"strconv"
	"testing"
	"time"
)

// Escape is reversible, so two different ids never make one unit name,
// and its output is a valid unit-name part.
func TestEscapeRoundTrip(t *testing.T) {
	for _, s := range []string{"", "m1", "d-abc", "a/b", ".lead", "x.y", "a b", "\\x2d", "ünï", "--", "a\x00b"} {
		e := Escape(s)
		back, ok := Unescape(e)
		if !ok || back != s {
			t.Errorf("Escape(%q) = %q, back %q %v", s, e, back, ok)
		}
		for i := 0; i < len(e); i++ {
			c := e[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == ':' || c == '_' || c == '.' || c == '\\' || c == '-') {
				t.Errorf("Escape(%q) = %q has %q", s, e, c)
			}
		}
	}
	if Escape("d-1") == Escape("d/1") {
		t.Error("'-' and '/' escape alike")
	}
	if Escape("d-1") != `d\x2d1` {
		t.Errorf("Escape(d-1) = %q", Escape("d-1"))
	}
	for _, bad := range []string{`\`, `\x`, `\x4`, `\y41`, `\xzz`} {
		if _, ok := Unescape(bad); ok {
			t.Errorf("Unescape(%q) took it", bad)
		}
	}
}

// A service needs a command, a .service name and a .slice: anything else
// is refused before the manager is asked.
func TestStartServiceRefusesShapes(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir()) // no manager here: a refusal must come first
	for _, s := range []Service{
		{Name: "a.service", Slice: "b.slice"},
		{Name: "a", Slice: "b.slice", Argv: []string{"true"}},
		{Name: "a.service", Slice: "b", Argv: []string{"true"}},
	} {
		if err := StartService(s); err == nil || err.Error() != "a service needs a command, a .service name and a .slice" {
			t.Errorf("%+v: %v", s, err)
		}
	}
}

// With DEN_REAL_SYSTEMD=1: a real transient service in a slice of its
// own is started, frozen, thawed and stopped on this machine's user
// manager, and is gone after. Never in CI.
func TestRealUnits(t *testing.T) {
	if os.Getenv("DEN_REAL_SYSTEMD") != "1" {
		t.Skip("set DEN_REAL_SYSTEMD=1 to start a unit on this machine's systemd user manager")
	}
	id := "dentest-" + strconv.Itoa(os.Getpid())
	slice := "den-desk-" + Escape(id) + ".slice"
	svc := "den-desk-" + Escape(id) + "-1.service"
	t.Cleanup(func() { Stop(slice) })
	if err := StartService(Service{Name: svc, Slice: slice, Description: "Den test", Argv: []string{"/bin/sleep", "60"}, Env: []string{"PATH=/usr/bin:/bin"}}); err != nil {
		t.Fatal(err)
	}
	wait := func(want func(UnitState) bool, unit string) UnitState {
		var u UnitState
		for i := 0; i < 50; i++ {
			var err error
			if u, err = State(unit); err != nil {
				t.Fatal(err)
			}
			if want(u) {
				return u
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("%s stayed %+v", unit, u)
		return u
	}
	wait(func(u UnitState) bool { return u.Active == "active" }, slice)
	if err := Freeze(slice); err != nil {
		t.Fatal(err)
	}
	wait(func(u UnitState) bool { return u.Freeze == "frozen" }, slice)
	if err := Thaw(slice); err != nil {
		t.Fatal(err)
	}
	wait(func(u UnitState) bool { return u.Freeze == "running" }, slice)
	names, err := List("den-desk-" + Escape(id) + "*.slice")
	if err != nil || len(names) != 1 || names[0] != slice {
		t.Errorf("List: %v %v", names, err)
	}
	if err := Stop(slice); err != nil {
		t.Fatal(err)
	}
	wait(UnitState.Gone, slice)
	wait(UnitState.Gone, svc)
	if err := Stop(slice); err != nil {
		t.Errorf("a second stop of a gone slice: %v", err)
	}
}
