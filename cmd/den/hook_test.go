package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/badge"
	"github.com/GlacierHubAB/den/internal/bridge"
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

	in := bridge.HookInput{SessionID: "6070ed32-568e-46a3-9690-ac2048b12385", HookEventName: "Notification", NotificationType: "permission_prompt", Message: "Claude needs your permission to use Bash"}
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
