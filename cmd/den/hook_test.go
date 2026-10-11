package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/badge"
	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/config"
	"github.com/GlacierHubAB/den/internal/notify"
)

func TestNotifyWithoutDen(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	bin := filepath.Join(dir, "notify-send")
	os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >> "+argsFile+"\n"), 0o755)
	old := notify.Command
	notify.Command = bin
	t.Cleanup(func() { notify.Command = old })
	t.Setenv("HYPRLAND_INSTANCE_SIGNATURE", "")
	t.Setenv("DEN_NOTIFY", "")
	cfg := filepath.Join(dir, "config.toml")
	t.Setenv("DEN_CONFIG", cfg)

	in := bridge.HookInput{SessionID: "11111111-2222-4333-8444-555555555555", HookEventName: "Notification", NotificationType: "permission_prompt", Message: "Claude needs your permission to use Bash"}
	notifyWithoutDen(in) // off by default
	if _, err := os.Stat(argsFile); err == nil {
		t.Fatal("notified with without_den off")
	}
	os.WriteFile(cfg, []byte("[notify]\nwithout_den = true\n"), 0o644)
	notifyWithoutDen(bridge.HookInput{SessionID: in.SessionID, HookEventName: "Notification", NotificationType: "idle_prompt"})
	notifyWithoutDen(bridge.HookInput{SessionID: in.SessionID, HookEventName: "Stop"})
	notifyWithoutDen(in)
	raw, _ := os.ReadFile(argsFile)
	got := string(raw)
	if strings.Count(got, "--app-name=Den") != 1 || !strings.Contains(got, badge.Name(in.SessionID)+" needs your permission") || strings.Contains(got, "Bash") || strings.Contains(got, "--action") {
		t.Errorf("args = %q", got)
	}
}

func TestPermissionOutputForQuestions(t *testing.T) {
	input := []byte(`{"questions":[{"question":"Which one?","header":"Pick","options":[{"label":"Warm","description":""},{"label":"Cool","description":""}],"multiSelect":false}]}`)
	in := bridge.HookInput{HookEventName: "PermissionRequest", ToolName: "AskUserQuestion", ToolInput: input}
	answered := bridge.Decision{Behavior: "allow", Answers: map[string]string{"Which one?": "Cool"}}
	var on, off config.Config
	on.Hooks.AnswerQuestions = true

	if out := permissionOutput(in, answered, off); out != nil {
		t.Errorf("option off: printed %s", out)
	}
	out := permissionOutput(in, answered, on)
	if !strings.Contains(string(out), `"updatedInput":{"answers":{"Which one?":"Cool"},"questions":[`) || !strings.Contains(string(out), `"behavior":"allow"`) {
		t.Errorf("answer = %s", out)
	}
	for name, d := range map[string]bridge.Decision{
		"released":   {},
		"incomplete": {Behavior: "allow", Answers: map[string]string{"Another?": "Cool"}},
		"deny":       {Behavior: "deny", Answers: map[string]string{"Which one?": "Cool"}},
	} {
		if out := permissionOutput(in, d, on); out != nil {
			t.Errorf("%s: printed %s", name, out)
		}
	}
	// Ordinary permissions are unchanged.
	bash := bridge.HookInput{HookEventName: "PermissionRequest", ToolName: "Bash"}
	if got := string(permissionOutput(bash, bridge.Decision{Behavior: "deny", Message: "no"}, off)); got != `{"hookSpecificOutput":{"decision":{"behavior":"deny","message":"no"},"hookEventName":"PermissionRequest"}}`+"\n" {
		t.Errorf("bash deny = %s", got)
	}
}
