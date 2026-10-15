package main

import (
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/usage"
)

// TestSpawnRefusedDuringAUsageStop: den mcp starts nothing while Den has
// the fleet stopped, whoever asks (the orchestrator or a lead). With no
// Den answering, Den's usage.json says.
func TestSpawnRefusedDuringAUsageStop(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	old := askUsage
	askUsage = func(string, bridge.Envelope, time.Duration) (bridge.Decision, error) {
		return bridge.Decision{}, bridge.ErrNoDen
	}
	t.Cleanup(func() { askUsage = old })
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

// TestSpawnAsksTheRunningDen: a running Den's answer is what counts, not
// the file.
func TestSpawnAsksTheRunningDen(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	usage.Save(usage.Path(), usage.State{Stopped: true, StopWhy: "an old file"})
	old := askUsage
	t.Cleanup(func() { askUsage = old })
	askUsage = func(string, bridge.Envelope, time.Duration) (bridge.Decision, error) {
		return bridge.Decision{Behavior: "allow"}, nil
	}
	if err := usageStop(); err != nil {
		t.Errorf("Den says go on: %v", err)
	}
	askUsage = func(_ string, env bridge.Envelope, _ time.Duration) (bridge.Decision, error) {
		if env.Event != bridge.UsageEvent {
			t.Errorf("asked %q", env.Event)
		}
		return bridge.Decision{Behavior: "deny", Message: "the weekly limit reached 96%"}, nil
	}
	if err := usageStop(); err == nil || !strings.Contains(err.Error(), "96%") {
		t.Errorf("Den says stopped: %v", err)
	}
}
