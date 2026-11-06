package desktop

import (
	"os"
	"testing"

	"github.com/GlacierHubAB/den/internal/teststandin"
)

// TestMain: started as a stand-in for Claude Code, the test binary only
// sleeps.
func TestMain(m *testing.M) {
	teststandin.Main()
	os.Exit(m.Run())
}
