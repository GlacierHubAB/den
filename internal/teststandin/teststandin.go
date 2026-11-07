// Package teststandin is test support only, and never part of den: it makes
// the running test binary stand in for another program (Claude Code, a den
// binary) under any name, as a process that only sleeps. A test places the
// binary at a path with Link or Copy, starts it with Command and waits for
// it with Started; the package's TestMain calls Main first.
//
// The tests used to rename sleep for this, but a multicall coreutils (uutils,
// as Ubuntu ships it) refuses to run under a name that is not its own.
package teststandin

import (
	"bytes"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// env, set on a started stand-in only, makes Main sleep.
const env = "DEN_TEST_STANDIN=1"

// Main sleeps for os.Args[1] seconds (30 when absent) and exits when this
// process is a stand-in, and returns at once when it is not. A package's
// TestMain calls it before anything else.
func Main() {
	if os.Getenv("DEN_TEST_STANDIN") != "1" {
		return
	}
	secs := 30
	if len(os.Args) > 1 {
		if n, err := strconv.Atoi(os.Args[1]); err == nil {
			secs = n
		}
	}
	time.Sleep(time.Duration(secs) * time.Second)
	os.Exit(0)
}

// Bytes is the test binary's contents.
func Bytes(t testing.TB) []byte {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Link makes path a symbolic link to the test binary, so a stand-in run
// from it has path's base name as its comm and the test binary as its exe.
func Link(t testing.TB, path string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(exe, path); err != nil && !os.IsExist(err) {
		t.Fatal(err)
	}
}

// Copy writes a copy of the test binary at path, a file of its own.
func Copy(t testing.TB, path string) {
	t.Helper()
	if err := os.WriteFile(path, Bytes(t), 0o755); err != nil {
		t.Fatal(err)
	}
}

// Command runs path as a stand-in with args (args[0], if any, is the
// seconds it sleeps). Callers that set Env add to cmd.Env, which holds the
// stand-in's switch.
func Command(path string, args ...string) *exec.Cmd {
	cmd := exec.Command(path, args...)
	cmd.Env = append(os.Environ(), env)
	return cmd
}

// Started waits until the stand-in cmd runs with comm as /proc shows it
// (cut to the kernel's 15 bytes), and fails the test if it ends first or
// does not show within 5 seconds.
func Started(t testing.TB, cmd *exec.Cmd, comm string) {
	t.Helper()
	if len(comm) > 15 {
		comm = comm[:15]
	}
	dir := "/proc/" + strconv.Itoa(cmd.Process.Pid)
	var seen string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		stat, err := os.ReadFile(dir + "/stat")
		if err != nil {
			t.Fatalf("stand-in %s did not start: it ended (%v)", comm, err)
		}
		// The state follows the parenthesised comm, which may hold anything.
		if i := bytes.LastIndexByte(stat, ')'); i >= 0 && i+2 < len(stat) && stat[i+2] == 'Z' {
			t.Fatalf("stand-in %s did not start: it ended", comm)
		}
		raw, _ := os.ReadFile(dir + "/comm")
		if seen = strings.TrimSuffix(string(raw), "\n"); seen == comm {
			return
		}
	}
	t.Fatalf("stand-in %s did not start: its comm is %q", comm, seen)
}
