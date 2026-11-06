package teststandin

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	Main()
	os.Exit(m.Run())
}

// A stand-in runs, under a link or a copy, with the name it was given.
func TestStandInRunsUnderAnyName(t *testing.T) {
	dir := t.TempDir()
	link, copied := filepath.Join(dir, "claude"), filepath.Join(dir, "dut1")
	Link(t, link)
	Copy(t, copied)
	for _, c := range []struct{ path, comm string }{{link, "claude"}, {copied, "dut1"}} {
		cmd := Command(c.path, "30")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
		Started(t, cmd, c.comm)
	}
}

// Started fails for a process that has ended.
func TestStartedFailsForAnEndedProcess(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	ft := &fakeTB{TB: t}
	func() {
		defer func() { recover() }()
		Started(ft, cmd, "x")
	}()
	if !ft.failed {
		t.Error("Started passed for an ended process")
	}
}

type fakeTB struct {
	testing.TB
	failed bool
}

func (f *fakeTB) Helper() {}
func (f *fakeTB) Fatalf(string, ...any) {
	f.failed = true
	panic("fatal")
}

// The stand-in switch must never reach den: neither this package nor
// sshtest, which uses it, may be in cmd/den's dependencies.
func TestNotInDen(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go")
	}
	out, err := exec.Command("go", "list", "-deps", "github.com/GlacierHubAB/den/cmd/den").Output()
	if err != nil {
		var stderr []byte
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = ee.Stderr
		}
		t.Fatalf("go list: %v\n%s", err, stderr)
	}
	for _, p := range strings.Fields(string(out)) {
		if p == "github.com/GlacierHubAB/den/internal/teststandin" || p == "github.com/GlacierHubAB/den/internal/sshtest" {
			t.Errorf("cmd/den depends on %s", p)
		}
	}
	if !strings.Contains(string(out), "github.com/GlacierHubAB/den/internal/") {
		t.Errorf("go list gave no den packages:\n%s", out)
	}
}
