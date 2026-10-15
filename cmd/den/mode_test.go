package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
)

// TestAgentModeAsksTheRunningDen: a running Den's global mode is the
// agent's, and only a mode on the ranked line counts.
func TestAgentModeAsksTheRunningDen(t *testing.T) {
	t.Setenv("DEN_CONFIG", filepath.Join(t.TempDir(), "config.toml"))
	old := askMode
	t.Cleanup(func() { askMode = old })
	for answer, want := range map[string]string{
		"auto":              "auto",
		"plan":              "plan",
		"bypassPermissions": "default",
		"Auto":              "default",
		"":                  "default",
	} {
		askMode = func(_ string, env bridge.Envelope, _ time.Duration) (bridge.Decision, error) {
			if env.Event != bridge.ModeEvent {
				t.Errorf("asked %q", env.Event)
			}
			return bridge.Decision{Behavior: "allow", Message: answer}, nil
		}
		if got := agentMode(); got != want {
			t.Errorf("Den says %q: got %q, want %q", answer, got, want)
		}
	}
}

// TestAgentModeWithoutDen: with no Den to ask, config's mode, but never
// above default.
func TestAgentModeWithoutDen(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	t.Setenv("DEN_CONFIG", p)
	old := askMode
	t.Cleanup(func() { askMode = old })
	askMode = func(string, bridge.Envelope, time.Duration) (bridge.Decision, error) {
		return bridge.Decision{}, bridge.ErrNoDen
	}
	for set, want := range map[string]string{
		"":                    "default",
		`"plan"`:              "plan",
		`"dontAsk"`:           "dontAsk",
		`"auto"`:              "default",
		`"acceptEdits"`:       "default",
		`"bypassPermissions"`: "default",
	} {
		body := ""
		if set != "" {
			body = "[agents]\npermission_mode = " + set + "\n"
		}
		os.WriteFile(p, []byte(body), 0o644)
		if got := agentMode(); got != want {
			t.Errorf("config %s: got %q, want %q", set, got, want)
		}
	}
}
