package hypr

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/procs"
)

func TestStripStatusGlyph(t *testing.T) {
	for in, want := range map[string]string{
		"✳ Install herdr":      "Install herdr",
		"◑ TUI agent platform": "TUI agent platform",
		"~/projects/x":         "~/projects/x",
		"  ✳  Fix login  ":     "Fix login",
		"devbox: Main Session": "devbox: Main Session",
	} {
		if got := StripStatusGlyph(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestFindPrefersHerdrThenTitleThenCwd(t *testing.T) {
	cs := []Client{
		{Address: "a", Class: "com.mitchellh.ghostty", Title: "✳ Fix login", FocusHist: 3},
		{Address: "b", Class: "com.mitchellh.ghostty", Title: "~/code/den", FocusHist: 1},
		{Address: "c", Class: "com.mitchellh.ghostty", Title: "devbox: Main Session", FocusHist: 2},
		{Address: "d", Class: "zen", Title: "Fix login", FocusHist: 0},
	}
	if m, ok := Find(cs, "Fix login", "~/code/den", ""); !ok || m.Client.Address != "a" || m.How != "title" {
		t.Errorf("title match = %+v %v", m, ok)
	}
	if m, ok := Find(cs, "", "~/code/den", ""); !ok || m.Client.Address != "b" {
		t.Errorf("cwd match = %+v %v", m, ok)
	}
	if m, ok := Find(cs, "Fix login", "", "devbox: "); !ok || m.Client.Address != "c" || m.How != "herdr" {
		t.Errorf("herdr match = %+v %v", m, ok)
	}
	if _, ok := Find(cs, "Nope", "", ""); ok {
		t.Error("unexpected match")
	}
}

func TestMarker(t *testing.T) {
	for in, want := range map[string]string{
		"helper":                           "io.glacierhub.den.helper",
		"Quiet Panda":                      "io.glacierhub.den.quiet-panda",
		"drey/../x;rm -rf":                 "io.glacierhub.den.drey-x-rm-rf",
		"--":                               "",
		strings.Repeat("ab", 30):           "io.glacierhub.den." + strings.Repeat("ab", 20),
		"a" + strings.Repeat("-", 3) + "b": "io.glacierhub.den.a-b",
	} {
		if got := Marker(in); got != want {
			t.Errorf("Marker(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestMarkerHolds: only a process under the one window with the marker,
// whose terminal has no other window, holds it.
func TestMarkerHolds(t *testing.T) {
	mark := "io.glacierhub.den.helper"
	win := Client{Address: "0x1", PID: 100, InitialClass: mark}
	other := Client{Address: "0x2", PID: 200, InitialClass: "kitty"}
	under := []procs.Proc{{PID: 300, Comm: "claude"}, {PID: 250, Comm: "sh"}, {PID: 100, Comm: "ghostty"}}
	elsewhere := []procs.Proc{{PID: 400, Comm: "claude"}, {PID: 200, Comm: "kitty"}}
	for _, c := range []struct {
		name  string
		all   []Client
		chain []procs.Proc
		want  bool
	}{
		{"the agent in its window", []Client{win, other}, under, true},
		{"an impostor in another window", []Client{win, other}, elsewhere, false},
		{"no window has the marker", []Client{other}, under, false},
		{"two windows have it", []Client{win, {Address: "0x3", PID: 500, InitialClass: mark}}, under, false},
		{"its terminal has another window", []Client{win, {Address: "0x4", PID: 100, InitialClass: "x"}}, under, false},
		{"a window with no pid", []Client{{Address: "0x1", InitialClass: mark}}, under, false},
	} {
		if got := markerHolds(c.all, mark, c.chain); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// TestHeldWindow: HeldWindow gives the window only when it is provably the
// agent's: the single window with the marker, its terminal with no other
// window, the agent's process under that terminal.
func TestHeldWindow(t *testing.T) {
	mark := "io.glacierhub.den.helper"
	win := Client{Address: "0x1", PID: 100, InitialClass: mark}
	under := []procs.Proc{{PID: 300, Comm: "claude"}, {PID: 100, Comm: "ghostty"}}
	if c, ok := HeldWindow([]Client{win}, mark, under); !ok || c.Address != "0x1" {
		t.Errorf("held = %+v %v", c, ok)
	}
	if _, ok := HeldWindow([]Client{win}, "", under); ok {
		t.Error("an empty marker holds a window")
	}
	if _, ok := HeldWindow([]Client{win, {Address: "0x2", PID: 101, InitialClass: mark}}, mark, under); ok {
		t.Error("two windows carry the marker")
	}
}

// TestOwnWindow: Den's own window is the one window of a process above
// Den's. Two such windows, a terminal process that holds several windows,
// or no process above that has a window: Den cannot say.
func TestOwnWindow(t *testing.T) {
	chain := []procs.Proc{{PID: 300, Comm: "den"}, {PID: 250, Comm: "zsh"}, {PID: 100, Comm: "ghostty"}}
	mine := Client{Address: "0x1", PID: 100}
	other := Client{Address: "0x2", PID: 200}
	for _, c := range []struct {
		name string
		all  []Client
		want string
	}{
		{"its one window", []Client{mine, other}, "0x1"},
		{"another process's windows only", []Client{other}, ""},
		{"its terminal holds two windows", []Client{mine, {Address: "0x3", PID: 100}, other}, ""},
		{"two terminals above it each with a window", []Client{mine, {Address: "0x4", PID: 250}}, ""},
		{"a window with no pid", []Client{{Address: "0x5"}}, ""},
	} {
		got, ok := OwnWindow(c.all, chain)
		if c.want == "" && ok || c.want != "" && (!ok || got.Address != c.want) {
			t.Errorf("%s: %+v %v, want %q", c.name, got, ok, c.want)
		}
	}
}

// fakeHyprctl puts a hyprctl script first on PATH that logs its arguments
// and answers per call; no real compositor is ever asked.
func fakeHyprctl(t *testing.T, body string) (log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "calls")
	script := "#!/bin/sh\necho \"$@\" >> " + log + "\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "hyprctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return log
}

func calls(t *testing.T, log string) []string {
	t.Helper()
	raw, _ := os.ReadFile(log)
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

// TestMoveToWorkspaceDispatch: the exact text sent. The Lua move leaves the
// focus and any special workspace alone only with follow = false (checked
// on Hyprland 0.56.2, which ignores silent = true, so the move followed the
// window); the classic dispatcher is the fallback.
func TestMoveToWorkspaceDispatch(t *testing.T) {
	log := fakeHyprctl(t, `echo ok`)
	if err := MoveToWorkspace("0x1", "special:den"); err != nil {
		t.Fatal(err)
	}
	want := `dispatch hl.dsp.window.move({ workspace = "special:den", window = "address:0x1", follow = false })`
	if got := calls(t, log); len(got) != 1 || got[0] != want {
		t.Errorf("calls %q, want [%s]", got, want)
	}

	log = fakeHyprctl(t, `case "$2" in hl.*) echo "unknown dispatcher";; *) echo ok;; esac`)
	if err := MoveToWorkspace("0x1", "3"); err != nil {
		t.Fatal(err)
	}
	got := calls(t, log)
	if len(got) != 2 || got[1] != "dispatch movetoworkspacesilent 3,address:0x1" {
		t.Errorf("calls %q", got)
	}

	fakeHyprctl(t, `echo "Window not found"`)
	if err := MoveToWorkspace("0x1", "3"); err == nil {
		t.Error("a refused move reported success")
	}
	if err := MoveToWorkspace("", "3"); err == nil {
		t.Error("a move with no address ran")
	}
}

// TestHyprctlCallsEndWithTheirContext: a compositor that does not answer
// cannot hold a caller past its deadline.
func TestHyprctlCallsEndWithTheirContext(t *testing.T) {
	fakeHyprctl(t, `exec sleep 5`)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := ClientsContext(ctx); err == nil {
		t.Error("a list that never answered reported success")
	}
	if err := MoveToWorkspaceContext(ctx, "0x1", "special:den"); err == nil {
		t.Error("a move that never answered reported success")
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("took %v", took)
	}
}

// TestWindowsLock: one holder at a time, and the try never waits.
func TestWindowsLock(t *testing.T) {
	unlock := LockWindows()
	if _, ok := TryLockWindows(); ok {
		t.Error("the lock was taken twice")
	}
	unlock()
	u, ok := TryLockWindows()
	if !ok {
		t.Fatal("the lock was not free")
	}
	u()
}
