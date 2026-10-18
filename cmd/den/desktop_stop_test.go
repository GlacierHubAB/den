package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/desktop"
)

// With no Den, den desktop stop stops what Den recorded, and never a pid
// whose executable differs from the record.
func TestStopWithoutDen(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	ours := exec.Command("sleep", "30")
	other := exec.Command("sleep", "30")
	ours.Start()
	other.Start()
	t.Cleanup(func() { ours.Process.Kill(); other.Process.Kill(); ours.Wait(); other.Wait() })
	dir := t.TempDir()
	write := func(name string, tg desktop.Targets) {
		os.MkdirAll(filepath.Join(dir, name), 0o700)
		data, _ := json.Marshal(map[string]any{"mission": name, "targets": tg})
		os.WriteFile(filepath.Join(dir, name, "mission.json"), data, 0o600)
	}
	write("m1", desktop.Targets{Agent: desktop.RecordProc(ours.Process.Pid)})
	forged := desktop.RecordProc(other.Process.Pid)
	forged.Exe = "/usr/bin/claude"
	write("m2", desktop.Targets{Agent: forged})
	out := stopWithoutDen(dir)
	if !strings.Contains(out, "Den is not running") || !strings.Contains(out, "the agent") {
		t.Fatalf("%s", out)
	}
	done := make(chan struct{})
	go func() { ours.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("our recorded agent survived")
	}
	if err := other.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatal("a process whose executable differs from the record was signalled")
	}
}
