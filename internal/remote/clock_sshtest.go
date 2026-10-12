//go:build sshtest

package remote

import (
	"os"
	"time"
)

// A test build reads DEN_TEST_REMOTE_NOW_OFFSET (a Go duration, e.g. 10m)
// and runs serve's clock that far ahead. Never in a release build.
func init() {
	if d, err := time.ParseDuration(os.Getenv("DEN_TEST_REMOTE_NOW_OFFSET")); err == nil {
		clockOffset = d
	}
}
