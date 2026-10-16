package view

import (
	"os"
	"testing"

	"github.com/GlacierHubAB/den/internal/gitenvtest"
)

// TestMain gives the tests a state folder of their own, as every package that
// records window markers has: a test that sets none writes the human's.
func TestMain(m *testing.M) { os.Exit(gitenvtest.Main(m)) }
