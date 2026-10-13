package closer

import (
	"os"
	"testing"

	"github.com/GlacierHubAB/den/internal/gitenvtest"
)

// TestMain starts the tests without git's inherited variables, which would
// send their git commands to another repository, and away from the
// compositor the run sits in: System lists and closes real windows.
func TestMain(m *testing.M) {
	os.Setenv("HYPRLAND_INSTANCE_SIGNATURE", "")
	os.Exit(gitenvtest.Main(m))
}
