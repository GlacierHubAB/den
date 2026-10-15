package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/colors"
	"github.com/GlacierHubAB/den/internal/gitenvtest"
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
	// No test may reach the compositor the run happens to sit in: close
	// plans list windows and a close may close one.
	os.Setenv("HYPRLAND_INSTANCE_SIGNATURE", "")
	// The settle guard is tested on its own; elsewhere ^Y acts at once.
	answerSettle = 0
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestStateFolderIsTheTestsOwn(t *testing.T) {
	if p := colors.Path(); !strings.HasPrefix(p, os.TempDir()) || !strings.Contains(filepath.Dir(filepath.Dir(p)), "den-ui-state-") {
		t.Errorf("colour record at %s, want one under a test state folder", p)
	}
}
