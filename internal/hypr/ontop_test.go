package hypr

import (
	"strings"
	"testing"
)

// KeepOnTop toggles float and pin only when the window lacks them (both
// are toggles in Hyprland), always raises it, and takes only an address.
func TestKeepOnTop(t *testing.T) {
	var ran []string
	old := dispatchRun
	dispatchRun = func(args ...string) ([]byte, error) {
		ran = append(ran, strings.Join(args, " "))
		return []byte("ok"), nil
	}
	t.Cleanup(func() { dispatchRun = old })

	if err := KeepOnTop("0xabc", false, false); err != nil {
		t.Fatal(err)
	}
	if len(ran) != 3 || !strings.Contains(ran[0], "window.float") || !strings.Contains(ran[1], "window.pin") || !strings.Contains(ran[2], "alter_zorder") {
		t.Fatalf("ran %q", ran)
	}
	for _, r := range ran {
		if !strings.Contains(r, `"address:0xabc"`) {
			t.Errorf("%q does not name the window", r)
		}
	}
	ran = nil
	if err := KeepOnTop("0xabc", true, true); err != nil || len(ran) != 1 || !strings.Contains(ran[0], "alter_zorder") {
		t.Fatalf("already floating and pinned: ran %q (%v)", ran, err)
	}
	ran = nil
	if err := KeepOnTop(`0xabc" }) hl.exec_cmd("x`, false, false); err == nil || len(ran) != 0 {
		t.Fatalf("a non-address was dispatched: %q", ran)
	}
}

// A compositor that does not take the Lua form gets the legacy one.
func TestKeepOnTopLegacy(t *testing.T) {
	var ran []string
	old := dispatchRun
	dispatchRun = func(args ...string) ([]byte, error) {
		ran = append(ran, strings.Join(args, " "))
		if strings.HasPrefix(args[0], "hl.") {
			return []byte("invalid dispatcher"), nil
		}
		return []byte("ok"), nil
	}
	t.Cleanup(func() { dispatchRun = old })
	if err := KeepOnTop("0xabc", false, true); err != nil {
		t.Fatal(err)
	}
	want := []string{"togglefloating address:0xabc", "alterzorder top,address:0xabc"}
	var legacy []string
	for _, r := range ran {
		if !strings.HasPrefix(r, "hl.") {
			legacy = append(legacy, r)
		}
	}
	if strings.Join(legacy, "|") != strings.Join(want, "|") {
		t.Fatalf("legacy %q", legacy)
	}
}
