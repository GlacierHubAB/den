package main

import (
	"io"
	"os"
	"os/exec"
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

// captureStdout runs f and returns what it wrote to os.Stdout.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	f()
	os.Stdout = old
	w.Close()
	b, _ := io.ReadAll(r)
	return string(b)
}

// A panic anywhere in den hook ends in exit 0 with nothing on stdout, not
// Go's exit 2, which Claude Code would read as a denial.
func TestHookPanicExitsZeroQuietly(t *testing.T) {
	code := -1
	oldExit, oldPanic := exitHook, hookPanic
	exitHook = func(c int) { code = c }
	hookPanic = func() { panic("a bug on the hook path") }
	t.Cleanup(func() { exitHook, hookPanic = oldExit, oldPanic })
	out := captureStdout(t, runHook)
	if code != 0 || out != "" {
		t.Errorf("exit %d, stdout %q", code, out)
	}
}

// The built binary agrees: malformed or odd input never exits non-zero.
func TestHookBinaryAlwaysExitsZero(t *testing.T) {
	if testing.Short() {
		t.Skip("builds den")
	}
	bin := filepath.Join(t.TempDir(), "den")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	dir := t.TempDir()
	env := []string{"HOME=" + dir, "XDG_STATE_HOME=" + dir, "XDG_CONFIG_HOME=" + dir, "XDG_RUNTIME_DIR=" + dir,
		"DEN_SOCKET=" + filepath.Join(dir, "den.sock"), "DEN_REMOTE_SOCKET=" + filepath.Join(dir, "den-remote.sock"),
		"DEN_CONFIG=" + filepath.Join(dir, "config.toml"), "CLAUDE_CONFIG_DIR=" + dir, "PATH=/usr/bin:/bin"}
	for _, in := range []string{
		"",
		"not json",
		`{"hook_event_name":"PermissionRequest","session_id":"s1","tool_name":"Bash","tool_input":{"command":"ls"}}`,
		`{"hook_event_name":"PermissionRequest","session_id":"s1","tool_input":"\u0000\u001b]52;c;x\u0007"}`,
		`{"hook_event_name":"StopFailure","session_id":"s1","error":{"nested":[1,2]}}`,
		`{"hook_event_name":"Notification","session_id":"s1","notification_type":"permission_prompt"}`,
	} {
		cmd := exec.Command(bin, "hook")
		cmd.Env = env
		cmd.Stdin = strings.NewReader(in)
		out, err := cmd.Output()
		if err != nil || len(out) != 0 {
			t.Errorf("input %q: err %v, stdout %q", in, err, out)
		}
	}
}
