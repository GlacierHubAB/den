package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/colors"
)

// TestMain gives the package's tests a state folder of their own. The UI
// writes the shared colour record and the task history there, and a test run
// against the real one replaced the colours every agent badge reads.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "den-ui-state-")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_STATE_HOME", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestStateFolderIsTheTestsOwn(t *testing.T) {
	if p := colors.Path(); !strings.HasPrefix(p, os.TempDir()) || !strings.Contains(filepath.Dir(filepath.Dir(p)), "den-ui-state-") {
		t.Errorf("colour record at %s, want one under a test state folder", p)
	}
}
