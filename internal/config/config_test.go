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
