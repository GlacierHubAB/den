package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// allEvents is a settings.json with Den's entry for every event, running den.
func allEvents(den string) string {
	h := map[string]any{}
	for _, sp := range specs {
		e := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": den + " hook"}}}
		if sp.matcher != "" {
			e["matcher"] = sp.matcher
		}
		h[sp.event] = []any{e}
	}
	raw, _ := json.Marshal(map[string]any{"hooks": h})
	return string(raw)
}

func TestStatusOfNamesMissingEventsAndAnotherDen(t *testing.T) {
	for _, tc := range []struct {
		name    string
		raw     string
		missing int
		other   bool
		err     bool
	}{
		{"absent", "", len(specs), false, false},
		{"no hooks", `{"model":"fable"}`, len(specs), false, false},
		{"null", `null`, len(specs), false, false},
		{"every event", allEvents("/opt/den"), 0, false, false},
		{"another den", allEvents("/old/build/den"), 0, true, false},
		{"one missing", strings.Replace(allEvents("/opt/den"), `"Stop":`, `"Other":`, 1), 1, false, false},
		{"matcher changed", strings.Replace(allEvents("/opt/den"), `"matcher":"AskUserQuestion"`, `"matcher":"Bash"`, 1), 1, false, false},
		{"only other hooks", `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"bash other.sh"}]}]}}`, len(specs), false, false},
		{"does not parse", `{"hooks":`, 0, false, true},
	} {
		st, err := StatusOf([]byte(tc.raw), "/opt/den")
		if (err != nil) != tc.err {
			t.Errorf("%s: err = %v", tc.name, err)
			continue
		}
		if len(st.Missing) != tc.missing || st.Other != tc.other {
			t.Errorf("%s: missing %v other %v, want %d missing, other %v", tc.name, st.Missing, st.Other, tc.missing, tc.other)
		}
	}
}

func TestInstallThenStatusHasNothingMissing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	st, err := CheckStatus("/opt/my den/den")
	if err != nil || len(st.Missing) != len(specs) {
		t.Fatalf("before install: %+v, %v", st, err)
	}
	if _, err := Install("/opt/my den/den"); err != nil {
		t.Fatal(err)
	}
	st, err = CheckStatus("/opt/my den/den")
	if err != nil || len(st.Missing) != 0 || st.Other {
		t.Errorf("after install: %+v, %v", st, err)
	}
	if st, _ := CheckStatus("/usr/bin/den"); !st.Other || len(st.Missing) != 0 {
		t.Errorf("another den: %+v", st)
	}
	if got := Events(); len(got) != len(specs) || got[0] != "SessionStart" {
		t.Errorf("Events = %v", got)
	}
}

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

// Answering a question from Den (hooks.answer_questions) rides on the held
// PermissionRequest hook: its timeout must outlast the longest wait Den
// allows (hooks.permission_wait, 580 seconds by default), and installing
// twice must not duplicate it.
func TestPermissionHookOutlastsTheWait(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	for i := 0; i < 2; i++ {
		if _, err := Install("/usr/local/bin/den"); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	var s struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Timeout int `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	json.Unmarshal(raw, &s)
	perm := s.Hooks["PermissionRequest"]
	if len(perm) != 1 || perm[0].Matcher != "" || perm[0].Hooks[0].Timeout < 590 {
		t.Errorf("PermissionRequest = %+v", perm)
	}
	pre := s.Hooks["PreToolUse"]
	if len(pre) != 1 || pre[0].Matcher != "AskUserQuestion" {
		t.Errorf("PreToolUse = %+v", pre)
	}
}
