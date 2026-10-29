// Package hooks installs Den's bridge into Claude Code's user settings.
//
// Every entry it writes runs `<den> hook`, so entries are recognised and
// removed by that command string. The settings file is backed up first.
package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Marker identifies Den's entries inside settings.json.
const Marker = " hook"

// SettingsPath is Claude Code's user settings file.
func SettingsPath() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "settings.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "settings.json")
}

// spec is one hook event Den subscribes to.
type spec struct {
	event   string
	matcher string
	timeout int
}

var specs = []spec{
	{"SessionStart", "", 10},
	{"SessionEnd", "", 5},
	{"UserPromptSubmit", "", 10},
	{"Notification", "", 10},
	{"Stop", "", 10},
	{"PostToolUse", "", 10},
	{"PreToolUse", "AskUserQuestion", 10},
	// Held open while Den waits for your answer; the hook releases itself
	// earlier when you are at that terminal. See docs/PLAN.md.
	{"PermissionRequest", "", 600},
	// Stuck and failure items: a turn ended on an API error, a tool call failed.
	{"StopFailure", "", 5},
	{"PostToolUseFailure", "", 5},
}

// Install adds Den's hooks for the given den executable. It returns the
// backup path. Installing twice is safe: old Den entries are replaced.
func Install(denPath string) (backup string, err error) {
	path := SettingsPath()
	settings, raw, err := load(path)
	if err != nil {
		return "", err
	}
	hooksAny, _ := settings["hooks"].(map[string]any)
	if hooksAny == nil {
		hooksAny = map[string]any{}
	}
	stripDen(hooksAny)
	cmd := quote(denPath) + " hook"
	for _, sp := range specs {
		entry := map[string]any{
			"hooks": []any{map[string]any{"type": "command", "command": cmd, "timeout": sp.timeout}},
		}
		if sp.matcher != "" {
			entry["matcher"] = sp.matcher
		}
		list, _ := hooksAny[sp.event].([]any)
		hooksAny[sp.event] = append(list, entry)
	}
	settings["hooks"] = hooksAny
	backup, err = save(path, raw, settings)
	return backup, err
}

// Uninstall removes every Den entry. It returns the backup path.
func Uninstall() (backup string, err error) {
	path := SettingsPath()
	settings, raw, err := load(path)
	if err != nil {
		return "", err
	}
	hooksAny, _ := settings["hooks"].(map[string]any)
	if hooksAny == nil {
		return "", nil
	}
	if !stripDen(hooksAny) {
		return "", nil
	}
	if len(hooksAny) == 0 {
		delete(settings, "hooks")
	} else {
		settings["hooks"] = hooksAny
	}
	return save(path, raw, settings)
}

// Installed reports whether settings.json currently points at denPath (or
// any den when denPath is "").
func Installed(denPath string) bool {
	settings, _, err := load(SettingsPath())
	if err != nil {
		return false
	}
	hooksAny, _ := settings["hooks"].(map[string]any)
	for _, v := range hooksAny {
		for _, e := range asList(v) {
			if isDen(e) {
				if denPath == "" || strings.Contains(fmt.Sprint(e), quote(denPath)) {
					return true
				}
			}
		}
	}
	return false
}

// Status is what settings.json holds of Den's hooks, for `den doctor`.
type Status struct {
	Missing []string // events Den subscribes to that have no Den entry, in specs order
	Other   bool     // a Den entry runs a den other than the one asked about
}

// Events are the hook events Den installs, in order.
func Events() []string {
	out := make([]string, len(specs))
	for i, sp := range specs {
		out[i] = sp.event
	}
	return out
}

// CheckStatus reads SettingsPath and reports Den's hooks in it for denPath.
// A missing file has every event missing. The error is the read's or the
// parse's; it carries the path.
func CheckStatus(denPath string) (Status, error) {
	raw, err := os.ReadFile(SettingsPath())
	if os.IsNotExist(err) {
		return StatusOf(nil, denPath)
	}
	if err != nil {
		return Status{}, err
	}
	return StatusOf(raw, denPath)
}

// StatusOf is CheckStatus on the file's bytes; nil or empty is no file.
// An event counts as present when it has a Den entry with the matcher Den
// writes, whichever den it runs; Other says one runs another den than
// denPath, so a build moved since install-hooks shows.
func StatusOf(raw []byte, denPath string) (Status, error) {
	settings := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &settings); err != nil {
			return Status{}, err
		}
	}
	hooksAny, _ := settings["hooks"].(map[string]any)
	want := quote(denPath) + Marker
	var st Status
	for _, sp := range specs {
		found := false
		for _, e := range asList(hooksAny[sp.event]) {
			if !isDen(e) {
				continue
			}
			m, _ := e.(map[string]any)
			if matcher, _ := m["matcher"].(string); matcher != sp.matcher {
				continue
			}
			found = true
			for _, h := range asList(m["hooks"]) {
				hm, _ := h.(map[string]any)
				if c, _ := hm["command"].(string); strings.HasSuffix(c, Marker) && c != want {
					st.Other = true
				}
			}
		}
		if !found {
			st.Missing = append(st.Missing, sp.event)
		}
	}
	return st, nil
}

func load(path string) (map[string]any, []byte, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	if settings == nil {
		settings = map[string]any{}
	}
	return settings, raw, nil
}

func save(path string, raw []byte, settings map[string]any) (string, error) {
	backup := ""
	if raw != nil {
		backup = fmt.Sprintf("%s.bak.den.%d", path, time.Now().Unix())
		if err := os.WriteFile(backup, raw, 0o600); err != nil {
			return "", err
		}
	}
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	tmp := path + ".den.tmp"
	if err := os.WriteFile(tmp, append(out, '\n'), 0o600); err != nil {
		return "", err
	}
	return backup, os.Rename(tmp, path)
}

// stripDen removes Den's entries from every event list; true if any were found.
func stripDen(hooksAny map[string]any) bool {
	found := false
	for event, v := range hooksAny {
		var kept []any
		for _, e := range asList(v) {
			if isDen(e) {
				found = true
				continue
			}
			kept = append(kept, e)
		}
		if len(kept) == 0 {
			delete(hooksAny, event)
		} else {
			hooksAny[event] = kept
		}
	}
	return found
}

func isDen(entry any) bool {
	m, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	for _, h := range asList(m["hooks"]) {
		hm, ok := h.(map[string]any)
		if !ok {
			continue
		}
		c, _ := hm["command"].(string)
		if strings.HasSuffix(c, Marker) && (strings.Contains(c, "/den"+Marker) || strings.HasPrefix(c, "den"+Marker) || strings.Contains(c, `den"`+Marker)) {
			return true
		}
	}
	return false
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

func quote(p string) string {
	if strings.ContainsAny(p, " \t\"'") {
		return `"` + strings.ReplaceAll(p, `"`, `\"`) + `"`
	}
	return p
}
