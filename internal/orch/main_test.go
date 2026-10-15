package orch

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/GlacierHubAB/den/internal/gitenvtest"
)

// TestMain starts the tests without git's inherited variables, which would
// send their git commands to another repository. Started as
// `<test binary> hook orchestrator-inbound`, it is the inbound hook Start's
// self-test runs (DEN_TEST_HOOK=pass makes it a hook that lets everything
// through). Den's runtime folder and the user's settings are the test's own.
func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == "hook" && os.Args[2] == "orchestrator-inbound" {
		io.Copy(io.Discard, os.Stdin)
		if os.Getenv("DEN_TEST_HOOK") != "pass" {
			fmt.Println(`{"decision":"block","reason":"test"}`)
		}
		os.Exit(0)
	}
	dir, err := os.MkdirTemp("", "den-orch-test-")
	if err != nil {
		panic(err)
	}
	run := filepath.Join(dir, "run")
	settingsDir = func() string { return run }
	userSettingsPath = func() string { return filepath.Join(dir, "user-settings.json") }
	code := gitenvtest.Main(m)
	os.RemoveAll(dir)
	os.Exit(code)
}

// testHook is the inbound hook for tests: this binary (TestMain).
func testHook() []string { return []string{os.Args[0], "hook", "orchestrator-inbound"} }
