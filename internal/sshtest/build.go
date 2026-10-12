package sshtest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// realEnv is the environment as the test process started, before Local.Apply
// swaps HOME and friends for scratch values: go build must keep using the real
// module and build caches, not fill a scratch home.
var realEnv = os.Environ()

// realHome is the human's home as the test process started, captured before
// Local.Apply can change HOME. Isolation checks compare against it.
var realHome, _ = os.UserHomeDir()

// RealHome is the real home directory that no test may touch.
func RealHome() string { return realHome }

var (
	buildOnce sync.Once
	buildPath string
	buildErr  error
)

// BuildDen builds cmd/den once per test run, so the "remote" runs the code
// under test, and returns the binary. It is built with -tags sshtest, which
// enables test-only hooks (such as DEN_TEST_REMOTE_NOW_OFFSET) that release
// builds never contain. It is stamped with a version string
// ("sshtest-<pid>") that `den version` reports.
func BuildDen(t testing.TB) string {
	t.Helper()
	buildOnce.Do(func() { buildPath, buildErr = buildDen() })
	if buildErr != nil {
		t.Fatalf("sshtest: %v", buildErr)
	}
	return buildPath
}

// Version is what `den version` prints for the test-built binary.
func Version() string { return "den sshtest-" + strconv.Itoa(os.Getpid()) }

func buildDen() (string, error) {
	root, err := moduleRoot()
	if err != nil {
		return "", err
	}
	base := filepath.Join("/tmp", fmt.Sprintf("den-test-%d", os.Getuid()))
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", err
	}
	sweepStale(base)
	dir := filepath.Join(base, "stb-"+strconv.Itoa(os.Getpid()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	out := filepath.Join(dir, "den")
	cmd := exec.Command("go", "build", "-tags", "sshtest", "-o", out, "-ldflags", "-X main.version=sshtest-"+strconv.Itoa(os.Getpid()), "./cmd/den")
	cmd.Dir, cmd.Env = root, realEnv
	if b, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("go build ./cmd/den: %v\n%s", err, b)
	}
	return out, nil
}

// sweepStale removes build dirs left by test runs that are gone.
func sweepStale(base string) {
	entries, _ := os.ReadDir(base)
	for _, e := range entries {
		pid, ok := strings.CutPrefix(e.Name(), "stb-")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(pid)
		if err != nil || n == os.Getpid() {
			continue
		}
		if err := syscall.Kill(n, 0); err == syscall.ESRCH {
			os.RemoveAll(filepath.Join(base, e.Name()))
		}
	}
}

// Main is an optional TestMain body that removes the shared build when the
// package's tests finish: func TestMain(m *testing.M) { os.Exit(sshtest.Main(m)) }.
// Without it the build is swept by the next run once its pid is gone.
func Main(m *testing.M) int {
	code := m.Run()
	if buildPath != "" {
		os.RemoveAll(filepath.Dir(buildPath))
	}
	return code
}

func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found above the test's directory")
		}
		dir = parent
	}
}

// BuildDenAt builds cmd/den as it was at a git revision (from the module's
// own repository), for a before/after comparison. The build is removed with
// the test. It skips the test when git or the revision is unavailable.
func BuildDenAt(t testing.TB, rev string) string {
	t.Helper()
	root, err := moduleRoot()
	if err != nil {
		t.Fatalf("sshtest: %v", err)
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("sshtest: git not found")
	}
	if err := exec.Command("git", "-C", root, "cat-file", "-e", rev+"^{commit}").Run(); err != nil {
		t.Skipf("sshtest: revision %s is not in this clone (shallow checkout?)", rev)
	}
	base := filepath.Join("/tmp", fmt.Sprintf("den-test-%d", os.Getuid()))
	dir, err := os.MkdirTemp(base, "sta")
	if err != nil {
		t.Fatalf("sshtest: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	src := filepath.Join(dir, "src")
	os.MkdirAll(src, 0o755)
	arch := exec.Command("sh", "-c", "git -C "+shq(root)+" archive "+shq(rev)+" | tar -x -C "+shq(src))
	if out, err := arch.CombinedOutput(); err != nil {
		t.Fatalf("sshtest: git archive %s: %v: %s", rev, err, out)
	}
	// Files go:embed needs but git does not track (the mods' build output).
	if out, err := exec.Command("cp", "-rn", filepath.Join(root, "mods")+"/.", filepath.Join(src, "mods")).CombinedOutput(); err != nil {
		t.Fatalf("sshtest: copying mods: %v: %s", err, out)
	}
	out := filepath.Join(dir, "den")
	cmd := exec.Command("go", "build", "-o", out, "./cmd/den")
	cmd.Dir, cmd.Env = src, realEnv
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sshtest: building %s: %v\n%s", rev, err, b)
	}
	return out
}
