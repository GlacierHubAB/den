package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFileAndEnv(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	os.WriteFile(p, []byte(`
# comment
[orchestrator]
model = "opus"   # trailing comment
name = "fleet-lead"

[inbox]
history = 25

[hooks]
log = false
permission_wait = nonsense
`), 0o644)
	t.Setenv("DEN_CONFIG", p)
	t.Setenv("DEN_ORCHESTRATOR_MODEL", "")
	t.Setenv("DEN_PERMISSION_WAIT", "")
	t.Setenv("DEN_HOOK_LOG", "")
	c := Load()
	if c.Orchestrator.Model != "opus" || c.Orchestrator.Name != "fleet-lead" || c.Inbox.History != 25 || c.Hooks.Log || c.Hooks.PermissionWait != 580 || c.Detail.Recent != 20 {
		t.Errorf("got %+v", c)
	}
	t.Setenv("DEN_ORCHESTRATOR_MODEL", "haiku")
	t.Setenv("DEN_PERMISSION_WAIT", "30")
	if c := Load(); c.Orchestrator.Model != "haiku" || c.Hooks.PermissionWait != 30 {
		t.Errorf("env should win: %+v", c)
	}
	t.Setenv("DEN_CONFIG", filepath.Join(dir, "missing.toml"))
	t.Setenv("DEN_ORCHESTRATOR_MODEL", "")
	t.Setenv("DEN_PERMISSION_WAIT", "")
	if c := Load(); c != Defaults() {
		t.Errorf("missing file should give defaults: %+v", c)
	}
}

func TestExampleParsesToDefaults(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(p, []byte(Example), 0o644)
	t.Setenv("DEN_CONFIG", p)
	t.Setenv("DEN_ORCHESTRATOR_MODEL", "")
	t.Setenv("DEN_PERMISSION_WAIT", "")
	t.Setenv("DEN_HOOK_LOG", "")
	if c := Load(); c != Defaults() {
		t.Errorf("example differs from defaults: %+v", c)
	}
}
