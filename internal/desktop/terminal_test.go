package desktop

import (
	"slices"
	"testing"
)

// The terminal runs den desktop-view, which starts the agent from an argv:
// no shell, so a prompt with quotes or "; rm -rf ~" is one argument.
func TestTerminalArgv(t *testing.T) {
	agent := []string{"claude", "-p", "--", "it's a plan; rm -rf ~ $(id) `x`"}
	argv := TerminalArgv(agent, "/opt/den bin/den", "m1")
	want := append([]string{"/opt/den bin/den", "desktop-view", "--mission", "m1", "--exec", "--"}, agent...)
	if !slices.Equal(argv, want) {
		t.Fatalf("got %q", argv)
	}
	for _, a := range argv {
		if a == "sh" || a == "/bin/sh" || a == "-c" {
			t.Fatal("a shell in the agent's launch")
		}
	}
}
