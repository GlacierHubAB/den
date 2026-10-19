package procs

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestAncestorsIncludesSelf(t *testing.T) {
	a := Ancestors(os.Getpid())
	if len(a) == 0 || a[0].PID != os.Getpid() {
		t.Fatalf("ancestors = %+v", a)
	}
}

// TestMain lets the test binary stand in for Den's: started with
// "procs-test-sleep" it just waits.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "procs-test-sleep" {
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// grandchild is the first child of pid, waited for.
func firstChild(t *testing.T, pid int) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		raw, _ := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/children", pid, pid))
		var c int
		if _, err := fmt.Sscan(string(raw), &c); err == nil && c > 0 {
			return c
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no child of %d", pid)
	return 0
}

func start(t *testing.T, name string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	return cmd
}

// TestChildOf: Den's binary as the direct child, or behind one shell, is
// the orchestrator's; deeper, another binary or another start is not.
func TestChildOf(t *testing.T) {
	me := os.Getpid()
	mine, exe := StartTicks(me), Exe(me)
	bin := os.Args[0]
	if abs, err := filepath.Abs(bin); err == nil {
		bin = abs
	}
	direct := start(t, bin, "procs-test-sleep").Process.Pid
	viaShell := firstChild(t, start(t, "sh", "-c", bin+" procs-test-sleep; true").Process.Pid)
	deep := firstChild(t, firstChild(t, start(t, "sh", "-c", "sh -c '"+bin+" procs-test-sleep; true'; true").Process.Pid))
	other := start(t, "sleep", "30").Process.Pid
	for name, c := range map[string]struct {
		pid  int
		want bool
	}{
		"direct child":       {direct, true},
		"behind a shell":     {viaShell, true},
		"two levels down":    {deep, false},
		"another binary":     {other, false},
		"the parent itself":  {me, false},
		"a pid that is gone": {1 << 30, false},
	} {
		if got := ChildOf(c.pid, me, mine, exe); got != c.want {
			t.Errorf("%s (%d): %v, want %v", name, c.pid, got, c.want)
		}
	}
	if ChildOf(direct, me, "1", exe) {
		t.Error("another start time is another parent")
	}
	if ChildOf(direct, me, mine, "/usr/bin/den") {
		t.Error("another binary than Den's")
	}
}

// A child whose binary was replaced at its path after it started (an
// install or an update in place) still runs that path's binary and is
// still the orchestrator's; one whose binary is at another path, replaced
// too, is not.
func TestChildOfAfterAnUpdateInPlace(t *testing.T) {
	me := os.Getpid()
	mine := StartTicks(me)
	raw, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	install := func(path string) {
		t.Helper()
		os.Remove(path) // a new file at the path, as an install writes one
		if err := os.WriteFile(path, raw, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	den, other := filepath.Join(dir, "den"), filepath.Join(dir, "other")
	install(den)
	install(other)
	updated := start(t, den, "procs-test-sleep").Process.Pid
	stranger := start(t, other, "procs-test-sleep").Process.Pid
	install(den)
	install(other)
	link, _ := os.Readlink(fmt.Sprintf("/proc/%d/exe", updated))
	if link != den+" (deleted)" {
		t.Fatalf("the kernel's link is %q, not the deleted form this tests", link)
	}
	if got := Exe(updated); got != den {
		t.Errorf("Exe after the update: %q", got)
	}
	if !ChildOf(updated, me, mine, den) {
		t.Error("Den's child after Den's binary was updated in place is not counted")
	}
	if ChildOf(stranger, me, mine, den) {
		t.Error("another binary's process with the same parent counts as Den's")
	}
	if ChildOf(updated, me, "1", den) {
		t.Error("another start time is another parent, after an update too")
	}
}

// Undeleted takes off only the kernel's mark for a replaced file.
func TestUndeleted(t *testing.T) {
	for in, want := range map[string]string{
		"/usr/bin/den (deleted)":           "/usr/bin/den",
		"/usr/bin/den":                     "/usr/bin/den",
		"/usr/bin/den (deleted) (deleted)": "/usr/bin/den (deleted)",
		"":                                 "",
	} {
		if got := Undeleted(in); got != want {
			t.Errorf("Undeleted(%q) = %q, want %q", in, got, want)
		}
	}
}
