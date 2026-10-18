package api

import (
	"errors"
	"os"
	"testing"

	"github.com/GlacierHubAB/den/internal/sysd"
)

// TestMain keeps every test off the machine's own systemd manager: a
// console claim reads the claimant's service too, and a test that wants
// a read fakes one (fakeUnit).
func TestMain(m *testing.M) {
	readUnit = func(sysd.ProcID) (sysd.Verifier, error) {
		return sysd.Verifier{}, errors.New("no systemd manager in tests")
	}
	os.Exit(m.Run())
}
