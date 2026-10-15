// Package scripts holds tests for the shell scripts next to it.
package scripts

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestDevUpdateCountsOnlyItsOwnBinary: dev-update.sh refuses while a den
// runs from the binary it would replace, also once that file was replaced
// under it, and not for a den of the same name built elsewhere (a test's
// den remote serve, say). Every path is a scratch one; the update itself
// stops at a source repository that does not exist.
func TestDevUpdateCountsOnlyItsOwnBinary(t *testing.T) {
	if _, err := os.Stat("/proc/self/exe"); err != nil {
		t.Skip("no /proc")
	}
	for _, tool := range []string{"bash", "pgrep", "readlink", "sleep"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not found")
		}
	}
	sleep, _ := exec.LookPath("sleep")
	dir, err := filepath.EvalSymlinks(t.TempDir()) // as /proc shows it
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("dut%d", os.Getpid()%1000000) // a comm of its own, under 16 bytes
	bin := filepath.Join(dir, "bin", name)
	other := filepath.Join(dir, "elsewhere", name)
	for _, p := range []string{bin, other} {
		copyFile(t, sleep, p)
	}

	run := func() string {
		cmd := exec.Command("bash", "dev-update.sh")
		cmd.Env = append(os.Environ(),
			"HOME="+filepath.Join(dir, "home"),
			"XDG_DATA_HOME="+filepath.Join(dir, "data"),
			"DEN_DEV_REPO="+filepath.Join(dir, "no-such-repo"),
			"DEN_DEV_SRC="+filepath.Join(dir, "src"),
			"DEN_DEV_BIN="+bin,
			"DEN_DEV_PGREP_NAME="+name,
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		)
		out, _ := cmd.CombinedOutput()
		return string(out)
	}
	refused := func(out string) bool { return strings.Contains(out, "refusing: Den is running") }

	elsewhere := startSleep(t, other)
	if out := run(); refused(out) {
		t.Errorf("refused for a den that is not %s:\n%s", bin, out)
	}

	mine := startSleep(t, bin)
	out := run()
	if !refused(out) || !strings.Contains(out, strconv.Itoa(mine)+" ") {
		t.Errorf("did not refuse for pid %d running %s:\n%s", mine, bin, out)
	}
	if strings.Contains(out, strconv.Itoa(elsewhere)+" ") {
		t.Errorf("listed pid %d, which is not %s:\n%s", elsewhere, bin, out)
	}

	// An earlier update renamed a new binary over the one it runs.
	copyFile(t, sleep, bin+".new")
	if err := os.Rename(bin+".new", bin); err != nil {
		t.Fatal(err)
	}
	if out := run(); !refused(out) || !strings.Contains(out, strconv.Itoa(mine)+" ") {
		t.Errorf("did not refuse for pid %d running a replaced %s:\n%s", mine, bin, out)
	}
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, b, 0o755); err != nil {
		t.Fatal(err)
	}
}

// startSleep runs path (a copy of sleep) until the test ends, and waits
// until /proc shows it running path.
func startSleep(t *testing.T, path string) int {
	t.Helper()
	cmd := exec.Command(path, "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	pid := cmd.Process.Pid
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if exe, _ := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid)); exe == path {
			return pid
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not start", path)
		}
	}
}
