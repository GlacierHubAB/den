package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/permmode"
	"github.com/GlacierHubAB/den/internal/spawn"
)

// An agent starts in bypassPermissions only when Den says so with the
// guard it wrote (a session id and an absolute --settings file), only for
// one the orchestrator starts, and then without the spawn tool; anything
// else, Den down included, is the fallback, never bypass.
func TestStartModeBypassOnlyWithTheGuard(t *testing.T) {
	t.Setenv("DEN_CONFIG", filepath.Join(t.TempDir(), "config.toml"))
	old := askMode
	t.Cleanup(func() { askMode = old })
	sid, settings := "0f2a3b4c-5d6e-4f70-8192-a3b4c5d6e7f8", "/state/den/mediation/x.json"
	answer := func(d bridge.Decision, err error) {
		askMode = func(_ string, env bridge.Envelope, _ time.Duration) (bridge.Decision, error) { return d, err }
	}
	start := func(lead string) spawn.Request {
		req := spawn.Request{Spawner: "/bin/den"}
		startMode(&req, lead)
		return req
	}
	answer(bridge.Decision{Behavior: "allow", Message: permmode.Bypass, Answers: map[string]string{"session_id": sid, "settings": settings}}, nil)
	if r := start(""); r.PermissionMode != permmode.Bypass || r.SessionID != sid || r.Settings != settings || r.Spawner != "" {
		t.Errorf("God mode's agent: %+v", r)
	}
	if r := start("otter"); r.PermissionMode == permmode.Bypass || r.SessionID != "" || r.Settings != "" || r.Spawner == "" {
		t.Errorf("a lead's agent: %+v", r)
	}
	for _, a := range []map[string]string{nil, {"session_id": sid}, {"settings": settings}, {"session_id": sid, "settings": "x.json"}, {"session_id": "not-a-uuid", "settings": settings}} {
		answer(bridge.Decision{Behavior: "allow", Message: permmode.Bypass, Answers: a}, nil)
		if r := start(""); r.PermissionMode == permmode.Bypass || r.Settings != "" || r.SessionID != "" {
			t.Errorf("bypass without a whole guard %v: %+v", a, r)
		}
	}
	answer(bridge.Decision{}, bridge.ErrNoDen)
	if r := start(""); r.PermissionMode == permmode.Bypass || r.PermissionMode == "" {
		t.Errorf("no Den: %+v", r)
	}
}
