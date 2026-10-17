package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/orch"
	"github.com/GlacierHubAB/den/internal/spawn"
)

// TestAgentModeAsksTheRunningDen: a running Den's global mode is the
// agent's, and only a mode on the ranked line counts.
func TestAgentModeAsksTheRunningDen(t *testing.T) {
	// An answer that does not count falls back to config's mode.
	p := filepath.Join(t.TempDir(), "config.toml")
	t.Setenv("DEN_CONFIG", p)
	os.WriteFile(p, []byte("[agents]\npermission_mode = \"dontAsk\"\n"), 0o644)
	old := askMode
	t.Cleanup(func() { askMode = old })
	for answer, want := range map[string]string{
		"auto":              "auto",
		"plan":              "plan",
		"bypassPermissions": "dontAsk",
		"Auto":              "dontAsk",
		"":                  "dontAsk",
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

// TestSpawnInputNeverPicksTheMode: a spawn_agent input naming a mode, in
// any field, never reaches argv; Den's answer does.
func TestSpawnInputNeverPicksTheMode(t *testing.T) {
	t.Setenv("DEN_CONFIG", filepath.Join(t.TempDir(), "config.toml"))
	old := askMode
	t.Cleanup(func() { askMode = old })
	askMode = func(string, bridge.Envelope, time.Duration) (bridge.Decision, error) {
		return bridge.Decision{Behavior: "allow", Message: "plan"}, nil
	}
	in, ok := orch.ParseSpawn([]byte(`{"dir":"/tmp","brief":"go","permission_mode":"bypassPermissions","permissionMode":"bypassPermissions","mode":"auto","dangerously_skip_permissions":true}`))
	if !ok {
		t.Fatal("input refused")
	}
	req := spawnRequest(in, "den-orchestrator", "", "/bin/den")
	if req.PermissionMode != "" {
		t.Fatalf("the input set the mode: %q", req.PermissionMode)
	}
	req.PermissionMode = agentMode()
	argv := strings.Join(spawn.Command(req, "ui"), "\x00")
	if !strings.Contains(argv, "--permission-mode\x00plan\x00") || strings.Contains(argv, "bypass") || strings.Contains(argv, "dangerously") {
		t.Errorf("argv %q", strings.Split(argv, "\x00"))
	}
}

// TestAgentModeWithoutDen: with no Den to ask, config's mode as Den would
// start with it: at most the shipped mode, never bypassPermissions.
func TestAgentModeWithoutDen(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	t.Setenv("DEN_CONFIG", p)
	old := askMode
	t.Cleanup(func() { askMode = old })
	askMode = func(string, bridge.Envelope, time.Duration) (bridge.Decision, error) {
		return bridge.Decision{}, bridge.ErrNoDen
	}
	for set, want := range map[string]string{
		"":                    "auto",
		`"plan"`:              "plan",
		`"dontAsk"`:           "dontAsk",
		`"default"`:           "default",
		`"auto"`:              "auto",
		`"acceptEdits"`:       "acceptEdits",
		`"bypassPermissions"`: "auto",
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
