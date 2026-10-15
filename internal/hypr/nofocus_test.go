package hypr

import (
	"strings"
	"testing"
)

func TestNoInitialFocus(t *testing.T) {
	var got []string
	old := noFocusRun
	t.Cleanup(func() { noFocusRun = old })
	noFocusRun = func(args ...string) ([]byte, error) {
		got = append(got, strings.Join(args, " "))
		return []byte("ok\n"), nil
	}
	if err := NoInitialFocus("io.glacierhub.den.desktop"); err != nil {
		t.Fatal(err)
	}
	want := `eval hl.window_rule({ name = "den-nofocus", match = { class = "^io\\.glacierhub\\.den\\.desktop$" }, no_initial_focus = true })`
	if got[0] != want {
		t.Fatalf("got  %s\nwant %s", got[0], want)
	}
	if err := NoInitialFocus(""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got[1], `name = "den-nofocus", enabled = false`) {
		t.Fatalf("disable: %s", got[1])
	}
	// Nothing but a marker-shaped class goes into the Lua.
	for _, bad := range []string{`x" }) os.execute("rm") --`, "a b", "A", "", "x\nfoo"} {
		if bad == "" {
			continue
		}
		if err := NoInitialFocus(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if len(got) != 2 {
		t.Fatalf("a refused class reached hyprctl: %q", got)
	}
	noFocusRun = func(args ...string) ([]byte, error) { return []byte("error: nope"), nil }
	if err := NoInitialFocus("den-x"); err == nil {
		t.Fatal("a hyprctl error was not reported")
	}
}
