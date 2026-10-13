package main

import (
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/usage"
)

// TestSpawnRefusedDuringAUsageStop: den mcp starts nothing while Den has
// the fleet stopped, whoever asks (the orchestrator or a lead).
func TestSpawnRefusedDuringAUsageStop(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := usageStop(); err != nil {
		t.Fatalf("not stopped: %v", err)
	}
	if err := usage.Save(usage.Path(), usage.State{Stopped: true, StopWhy: "stopped by you"}); err != nil {
		t.Fatal(err)
	}
	if err := usageStop(); err == nil || !strings.Contains(err.Error(), "stopped by you") {
		t.Errorf("during a stop: %v", err)
	}
}
