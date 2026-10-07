package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/hooks"
)

// runHook is `den hook`: Claude Code runs it for each subscribed event with
// the hook JSON on stdin. It always exits 0 so a missing or busy Den never
// changes what Claude Code does.
func runHook() {
	raw, err := io.ReadAll(os.Stdin)
	if err != nil || len(raw) == 0 {
		return
	}
	var in bridge.HookInput
	if json.Unmarshal(raw, &in) != nil || in.HookEventName == "" {
		return
	}
	if in.AgentID != "" {
		return // subagents are not fleet members
	}
	env := bridge.Envelope{Seq: time.Now().UnixNano(), Event: in.HookEventName, SessionID: in.SessionID, Payload: raw}
	path := bridge.SocketPath()
	if in.HookEventName != "PermissionRequest" {
		bridge.Send(path, env)
		return
	}
	wait := 580 * time.Second
	if v := os.Getenv("DEN_PERMISSION_WAIT"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil {
			wait = time.Duration(secs) * time.Second
		}
	}
	d, ok := bridge.Ask(path, env, wait)
	if !ok || d.Behavior == "" {
		return // no decision: Claude Code shows its own dialog
	}
	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName": "PermissionRequest",
			"decision":      map[string]any{"behavior": d.Behavior, "message": d.Message},
		},
	}
	json.NewEncoder(os.Stdout).Encode(out)
}

func installHooks() {
	exe, err := os.Executable()
	if err != nil {
		fail("den: %v", err)
	}
	if real, err := os.Readlink(exe); err == nil && real != "" {
		exe = real
	}
	if isTempBuild(exe) {
		fail("den: this binary lives in a temporary build directory (%s).\nBuild it somewhere stable first, e.g.\n  go build -o ~/.local/bin/den ./cmd/den && ~/.local/bin/den install-hooks", exe)
	}
	backup, err := hooks.Install(exe)
	if err != nil {
		fail("den: %v", err)
	}
	fmt.Printf("Installed den hooks into %s\n", hooks.SettingsPath())
	if backup != "" {
		fmt.Printf("Backup: %s\n", backup)
	}
	fmt.Println("Hooks apply to Claude Code sessions started from now on. Undo with: den uninstall-hooks")
}

func uninstallHooks() {
	backup, err := hooks.Uninstall()
	if err != nil {
		fail("den: %v", err)
	}
	if backup == "" {
		fmt.Println("No den hooks were installed.")
		return
	}
	fmt.Printf("Removed den hooks from %s (backup: %s)\n", hooks.SettingsPath(), backup)
}

func isTempBuild(p string) bool {
	for _, d := range []string{os.TempDir(), "/tmp", os.Getenv("GOCACHE"), os.Getenv("GOTMPDIR")} {
		if d != "" && len(p) > len(d) && p[:len(d)] == d {
			return true
		}
	}
	return false
}

// runEvents is `den events`: a headless Den that prints every hook event it
// receives and releases permission requests to the terminal straight away.
// Useful to see what Claude Code sends. For protocol testing only,
// DEN_EVENTS_DECISION=allow|deny answers every permission request instead.
func runEvents() {
	srv, err := bridge.Listen(bridge.SocketPath())
	if err != nil {
		fail("den: %v", err)
	}
	defer srv.Close()
	fmt.Fprintf(os.Stderr, "listening on %s (ctrl+c to stop)\n", bridge.SocketPath())
	for r := range srv.Requests() {
		fmt.Printf("%s  %-18s %s  %s %s %s\n", time.Now().Format("15:04:05"), r.Event, short(r.SessionID), r.Input.ToolName, r.Input.NotificationType, firstN(r.Input.LastAssistant+r.Input.Message+r.Input.Prompt, 80))
		if r.WantsReply() {
			d := bridge.Decision{}
			if v := os.Getenv("DEN_EVENTS_DECISION"); v == "allow" || v == "deny" {
				d = bridge.Decision{Behavior: v, Message: "den events test decision"}
				fmt.Printf("          -> replied %s for %s %s\n", v, r.Input.ToolName, firstN(string(r.Input.ToolInput), 100))
			}
			r.Reply(d)
		}
	}
}

func short(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func firstN(s string, n int) string {
	r := []rune(s)
	for i, c := range r {
		if c == '\n' {
			r = r[:i]
			break
		}
	}
	if len(r) > n {
		r = append(r[:n-1], '…')
	}
	return string(r)
}
