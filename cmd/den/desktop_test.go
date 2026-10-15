package main

import "testing"

// The desktop commands take den.sock only from their argv, as an absolute
// path, never from the environment.
func TestDesktopArgs(t *testing.T) {
	t.Setenv("DEN_SOCKET", "/tmp/elsewhere.sock")
	m, s, err := desktopArgs([]string{"--mission", "m1", "--socket", "/run/user/1/den.sock"})
	if err != nil || m != "m1" || s != "/run/user/1/den.sock" {
		t.Fatalf("got %q %q %v", m, s, err)
	}
	if _, _, err := desktopArgs([]string{"--mission", "m1"}); err == nil {
		t.Fatal("no --socket must fail, not fall back to DEN_SOCKET")
	}
	if _, _, err := desktopArgs([]string{"--socket", "den.sock"}); err == nil {
		t.Fatal("a relative socket must fail")
	}
}
