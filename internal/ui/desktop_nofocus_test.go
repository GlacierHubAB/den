package ui

import (
	"os/exec"
	"testing"
)

// Den's start-up disable of the no-focus rule runs only inside a Hyprland
// session: with no instance signature it never reaches a compositor.
func TestClearNoFocusOnlyUnderHyprland(t *testing.T) {
	var calls []string
	old := deskNoFocus
	t.Cleanup(func() { deskNoFocus = old })
	deskNoFocus = func(class string) error {
		calls = append(calls, class)
		return nil
	}
	t.Setenv("HYPRLAND_INSTANCE_SIGNATURE", "")
	clearNoFocus()
	if len(calls) != 0 {
		t.Fatalf("disabled the rule with no Hyprland session: %q", calls)
	}
	if _, err := exec.LookPath("hyprctl"); err != nil {
		t.Skip("no hyprctl here")
	}
	t.Setenv("HYPRLAND_INSTANCE_SIGNATURE", "test-instance")
	clearNoFocus()
	if len(calls) != 1 || calls[0] != "" {
		t.Fatalf("calls %q", calls)
	}
}
