package gitsafe

import (
	"os"
	"testing"

	"github.com/GlacierHubAB/den/internal/gitenvtest"
)

// TestMain starts the tests without git's inherited variables, which would
// send their git commands to another repository.
func TestMain(m *testing.M) { os.Exit(gitenvtest.Main(m)) }
