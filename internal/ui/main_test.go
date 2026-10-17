package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/colors"
	"github.com/GlacierHubAB/den/internal/gitenvtest"
	"github.com/GlacierHubAB/den/internal/notify"
)

// TestMain gives the package's tests a state folder of their own. The UI
// writes the shared colour record and the task history there, and a test run
// against the real one replaced the colours every agent badge reads.
func TestMain(m *testing.M) {
	// Started as "den-test-sleep", the test binary stands in for Den's own
	// processes under a fake orchestrator (its hook, its den mcp).
	if len(os.Args) > 1 && os.Args[1] == "den-test-sleep" {
		time.Sleep(300 * time.Second)
		os.Exit(0)
	}
	dir, err := os.MkdirTemp("", "den-ui-state-")
	if err != nil {
		panic(err)
	}
	gitenvtest.Scrub() // git must not work on a repository the test run inherited
	os.Setenv("XDG_STATE_HOME", dir)
	// Nor may one write the human's config.toml (/mode writes it).
	os.Setenv("DEN_CONFIG", filepath.Join(dir, "config.toml"))
	// No test may reach the compositor the run happens to sit in: close
	// plans list windows and a close may close one.
	os.Setenv("HYPRLAND_INSTANCE_SIGNATURE", "")
	// The settle guard is tested on its own; elsewhere ^Y acts at once.
	answerSettle = 0
	// Nor may a test pop a notification on the desktop the run sits on: a
	// desktop mission test once showed "Desktop control on" there.
	notify.Command = "true"
	// Nor reach the run's own session through its runtime folder: Den's
	// socket defaults there, the agent pointer file a stop path removes,
	// the Wayland socket wl-paste would read the clipboard through.
	run := filepath.Join(dir, "run")
	if err := os.Mkdir(run, 0o700); err != nil {
		panic(err)
	}
	os.Setenv("XDG_RUNTIME_DIR", run)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestStateFolderIsTheTestsOwn(t *testing.T) {
	if p := colors.Path(); !strings.HasPrefix(p, os.TempDir()) || !strings.Contains(filepath.Dir(filepath.Dir(p)), "den-ui-state-") {
		t.Errorf("colour record at %s, want one under a test state folder", p)
	}
}

// No ui test reaches the desktop the run sits on: no notification server,
// no compositor, no runtime folder of the real session (Den's socket, the
// agent pointer file, the Wayland socket). A test that set any of these
// back would fail here, in any order.
func TestTestsReachNoRealDesktop(t *testing.T) {
	if notify.Command != "true" {
		t.Errorf("notify.Command is %q: a test could show a real notification", notify.Command)
	}
	if v := os.Getenv("HYPRLAND_INSTANCE_SIGNATURE"); v != "" {
		t.Errorf("HYPRLAND_INSTANCE_SIGNATURE is set: a test could reach the compositor")
	}
	run := os.Getenv("XDG_RUNTIME_DIR")
	if !strings.HasPrefix(run, os.TempDir()) || !strings.Contains(run, "den-ui-state-") {
		t.Errorf("XDG_RUNTIME_DIR is %q, not the tests' own", run)
	}
}
