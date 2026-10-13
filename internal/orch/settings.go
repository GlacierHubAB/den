package orch

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/hooks"
)

// The orchestrator loads no settings source of the user's
// (--setting-sources ""): their hooks would answer its prompts alongside
// Den's inbound hook, indistinguishably, and their permissions, plugins and
// MCP servers would widen what it may do. What it needs from them to reach
// the API is copied instead, from this list only. These values can hold
// secrets (keys, proxy credentials), so the settings go to a private file,
// never onto the command line, and the file is gone once the process has
// read it.

// copiedKeys are the user settings the orchestrator keeps; model too, when
// Den passes none.
var copiedKeys = []string{"env", "apiKeyHelper", "awsAuthRefresh", "awsCredentialExport", "gcpAuthRefresh"}

// userSettingsPath is the user's Claude Code settings file; tests replace it.
var userSettingsPath = hooks.SettingsPath

// settingsDir is Den's private runtime folder; tests replace it.
var settingsDir = func() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "den")
	}
	return bridge.FallbackDir()
}

// denSettings is everything the orchestrator's settings file holds: Den's
// own (the inbound hook, Den's PermissionRequest hook, which refuses the
// orchestrator anything it asks for, peer messages accepted so that each
// reaches the inbound hook, hooks forced on) and the allowed user keys.
func denSettings(o Options) map[string]any {
	s := map[string]any{}
	if raw, err := os.ReadFile(userSettingsPath()); err == nil {
		var user map[string]json.RawMessage
		if json.Unmarshal(raw, &user) == nil {
			keys := copiedKeys
			if resolveModel(o) == "off" {
				keys = append(append([]string(nil), keys...), "model")
			}
			for _, k := range keys {
				if v, ok := user[k]; ok {
					s[k] = v
				}
			}
		}
	}
	hook := hookCommand(o)
	s["crossSessionInbound"] = "accept"
	s["disableAllHooks"] = false
	s["hooks"] = map[string]any{
		"UserPromptSubmit": []any{map[string]any{"hooks": []any{map[string]any{
			"type": "command", "command": shellJoin(hook), "timeout": HookTimeout,
		}}}},
		"PermissionRequest": []any{map[string]any{"hooks": []any{map[string]any{
			"type": "command", "command": shellJoin([]string{denExecutable(o), "hook"}), "timeout": 600,
		}}}},
	}
	return s
}

// writeSettings writes denSettings to a new file, mode 0600, in Den's
// private runtime folder, and returns its path.
func writeSettings(o Options) (string, error) {
	dir := settingsDir()
	if err := bridge.EnsurePrivateDir(dir); err != nil {
		return "", err
	}
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "orchestrator-settings-"+hex.EncodeToString(b)+".json")
	raw, err := json.Marshal(denSettings(o))
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

// denExecutable is the den binary the orchestrator's hooks and MCP server
// run.
func denExecutable(o Options) string {
	if o.Executable != "" {
		return o.Executable
	}
	exe, _ := os.Executable()
	return exe
}

// resolveModel is the model Argv passes: o.Model, else
// $DEN_ORCHESTRATOR_MODEL, else sonnet; "off" passes none.
func resolveModel(o Options) string {
	if o.Model != "" {
		return o.Model
	}
	if m := os.Getenv("DEN_ORCHESTRATOR_MODEL"); m != "" {
		return m
	}
	return "sonnet"
}

// shellJoin quotes argv for the shell Claude Code runs a hook command in.
func shellJoin(argv []string) string {
	var out []string
	for _, a := range argv {
		if a != "" && strings.Trim(a, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/._-") == "" {
			out = append(out, a)
			continue
		}
		out = append(out, "'"+strings.ReplaceAll(a, "'", `'\''`)+"'")
	}
	return strings.Join(out, " ")
}
