package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallKeepsOtherHooksAndUninstallRestores(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	orig := `{"model":"fable","hooks":{"Stop":[{"hooks":[{"type":"command","command":"bash other.sh"}]}]}}`
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte(orig), 0o600)

	backup, err := Install("/usr/local/bin/den")
	if err != nil {
		t.Fatal(err)
	}
	if backup == "" {
		t.Error("expected a backup")
	}
	if !Installed("/usr/local/bin/den") || !Installed("") {
		t.Error("Installed should be true")
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	var s map[string]any
	json.Unmarshal(raw, &s)
	if s["model"] != "fable" {
		t.Error("lost other settings")
	}
	hooks := s["hooks"].(map[string]any)
	if len(hooks["Stop"].([]any)) != 2 {
		t.Errorf("Stop should keep the other hook and add den: %v", hooks["Stop"])
	}
	for _, ev := range []string{"PermissionRequest", "StopFailure", "PostToolUseFailure"} {
		if _, ok := hooks[ev]; !ok {
			t.Errorf("%s hook missing", ev)
		}
	}
	// Installing again must not duplicate.
	Install("/usr/local/bin/den")
	raw, _ = os.ReadFile(filepath.Join(dir, "settings.json"))
	json.Unmarshal(raw, &s)
	if n := len(s["hooks"].(map[string]any)["Stop"].([]any)); n != 2 {
		t.Errorf("Stop after reinstall = %d entries", n)
	}

	if _, err := Uninstall(); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(filepath.Join(dir, "settings.json"))
	json.Unmarshal(raw, &s)
	hooks = s["hooks"].(map[string]any)
	if len(hooks) != 1 || len(hooks["Stop"].([]any)) != 1 {
		t.Errorf("after uninstall hooks = %v", hooks)
	}
	if Installed("") {
		t.Error("Installed should be false")
	}
}
