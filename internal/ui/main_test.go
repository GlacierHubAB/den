package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/colors"
	"github.com/GlacierHubAB/den/internal/gitenvtest"
)

// TestMain gives the package's tests a state folder of their own. The UI
// writes the shared colour record and the task history there, and a test run
// against the real one replaced the colours every agent badge reads.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "den-ui-state-")
	if err != nil {
		panic(err)
	}
	gitenvtest.Scrub() // git must not work on a repository the test run inherited
	os.Setenv("XDG_STATE_HOME", dir)
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
