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

func TestGuidanceSetting(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	t.Setenv("DEN_CONFIG", p)
	if g, err := GuidanceSetting(); g != "" || err != nil {
		t.Errorf("no file: %q %v, want off", g, err)
	}
	os.WriteFile(p, []byte("[orchestrator]\nmodel = \"opus\"\nguidance = \"i-have-adhd\"  # on\n"), 0o644)
	if g, err := GuidanceSetting(); g != "i-have-adhd" || err != nil {
		t.Errorf("set: %q %v", g, err)
	}
	if c := Load(); c.Orchestrator.Guidance != "i-have-adhd" {
		t.Errorf("Load: %q", c.Orchestrator.Guidance)
	}
	os.WriteFile(p, []byte("[orchestrator]\nmodel = \"opus\"\n[inbox]\nguidance = \"i-have-adhd\"\n"), 0o644)
	if g, err := GuidanceSetting(); g != "" || err != nil {
		t.Errorf("key removed (another section's does not count): %q %v", g, err)
	}
	t.Setenv("DEN_CONFIG", dir) // a directory: there, but does not read
	if _, err := GuidanceSetting(); err == nil {
		t.Error("a config that does not read must be an error, so the setting in use stays")
	}
}

func TestRestoreAsk(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	t.Setenv("DEN_CONFIG", p)
	t.Setenv("DEN_RESTORE", "")
	if !Load().Restore.Ask {
		t.Error("the restore question is on by default")
	}
	os.WriteFile(p, []byte("[restore]\nask = false\n"), 0o644)
	if Load().Restore.Ask {
		t.Error("restore.ask = false turns it off")
	}
	os.WriteFile(p, []byte(""), 0o644)
	t.Setenv("DEN_RESTORE", "off")
	if Load().Restore.Ask {
		t.Error("DEN_RESTORE=off turns it off")
	}
}

func TestNotifyAndStuck(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(p, []byte(`
[notify]
needs_you = false
remind = 0
done = true

[stuck]
stalled = 0
tool_failures = 5
`), 0o644)
	t.Setenv("DEN_CONFIG", p)
	t.Setenv("DEN_NOTIFY", "")
	c := Load()
	if c.Notify.NeedsYou || c.Notify.Remind != 0 || !c.Notify.Done || c.Notify.Failed || c.Stuck.Stalled != 0 || c.Stuck.ToolFailures != 5 {
		t.Errorf("got %+v %+v", c.Notify, c.Stuck)
	}
	t.Setenv("DEN_NOTIFY", "off")
	if c := Load(); c.Notify.Done || c.Notify.Remind != 0 {
		t.Errorf("DEN_NOTIFY=off should silence everything: %+v", c.Notify)
	}
	t.Setenv("DEN_CONFIG", filepath.Join(t.TempDir(), "missing.toml"))
	t.Setenv("DEN_NOTIFY", "")
	if d := Load(); !d.Notify.NeedsYou || d.Notify.Done || d.Notify.Failed || d.Notify.Stalled || d.Notify.WithoutDen || d.Notify.Remind != 10 {
		t.Errorf("defaults: only needs-you notifications are on: %+v", d.Notify)
	}
}

func TestOnCommands(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(p, []byte(`
[on]
needs_you = "ntfy publish den-topic \"$DEN_AGENT needs you\" # not a comment"   # a comment
done = "echo done >> /tmp/x"
nonsense = "ignored"
`), 0o644)
	t.Setenv("DEN_CONFIG", p)
	c := Load()
	if got := c.OnCommand("needs_you"); got != `ntfy publish den-topic "$DEN_AGENT needs you" # not a comment` {
		t.Errorf("needs_you = %q", got)
	}
	if c.OnCommand("done") != "echo done >> /tmp/x" || c.OnCommand("stalled") != "" || c.OnCommand("nonsense") != "" {
		t.Errorf("on = %+v", c.On)
	}
	if d := Defaults(); d.OnCommand("needs_you") != "" || !d.Events.Log {
		t.Error("no [on] command by default; the event log is on")
	}
}

func TestAnswerQuestionsIsOptIn(t *testing.T) {
	if Defaults().Hooks.AnswerQuestions {
		t.Fatal("answering questions from Den must be off by default")
	}
	p := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(p, []byte("[hooks]\nanswer_questions = true\n"), 0o644)
	t.Setenv("DEN_CONFIG", p)
	if !Load().Hooks.AnswerQuestions {
		t.Error("hooks.answer_questions = true not read")
	}
}

// The API is off by default; /api on writes the key and touches nothing
// else in the file.
func TestAPIKeysAndSetKey(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	t.Setenv("DEN_CONFIG", p)
	if c := Load(); c.API.Enabled || c.API.AskBudgetUSD != 2 {
		t.Errorf("defaults: %+v", c.API)
	}
	if err := SetKey("api", "enabled", "true"); err != nil {
		t.Fatal(err)
	}
	if c := Load(); !c.API.Enabled {
		t.Error("a new file: enabled not written")
	}
	body := "# mine\n[orchestrator]\nmodel = \"opus\" # keep me\n\n[api]\nask_budget_usd = 0.5\nenabled = true   # was on\n\n[notify]\ndone = true\n"
	os.WriteFile(p, []byte(body), 0o600)
	os.Chmod(p, 0o600)
	if err := SetKey("api", "enabled", "false"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	want := "# mine\n[orchestrator]\nmodel = \"opus\" # keep me\n\n[api]\nask_budget_usd = 0.5\nenabled = false\n\n[notify]\ndone = true\n"
	if string(got) != want {
		t.Errorf("file:\n%s\nwant:\n%s", got, want)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want the file's own 0600", fi.Mode().Perm())
	}
	if c := Load(); c.API.Enabled || c.API.AskBudgetUSD != 0.5 || c.Orchestrator.Model != "opus" || !c.Notify.Done {
		t.Errorf("after SetKey: %+v", c)
	}
	os.WriteFile(p, []byte("[notify]\ndone = true\n"), 0o644)
	SetKey("api", "enabled", "true")
	if got, _ := os.ReadFile(p); string(got) != "[notify]\ndone = true\n\n[api]\nenabled = true\n" {
		t.Errorf("a missing section: %q", got)
	}
}

func TestAgentsPermissionMode(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	t.Setenv("DEN_CONFIG", p)
	for in, want := range map[string][2]string{
		`"auto"`:              {"auto", ""},
		`"accept-edits"`:      {"acceptEdits", ""},
		`"dontAsk"`:           {"dontAsk", ""},
		`"bypassPermissions"`: {"default", "bypassPermissions"},
		`"yolo"`:              {"default", "yolo"},
	} {
		os.WriteFile(p, []byte("[agents]\npermission_mode = "+in+"\n"), 0o644)
		c := Load()
		if c.Agents.PermissionMode != want[0] || c.Agents.ModeRefused != want[1] {
			t.Errorf("%s: got %q refused %q, want %q refused %q", in, c.Agents.PermissionMode, c.Agents.ModeRefused, want[0], want[1])
		}
	}
	if err := SetKey("agents", "permission_mode", `"plan"`); err != nil {
		t.Fatal(err)
	}
	if c := Load(); c.Agents.PermissionMode != "plan" || c.Agents.ModeRefused != "" {
		t.Errorf("after SetKey: %q refused %q", c.Agents.PermissionMode, c.Agents.ModeRefused)
	}
}
