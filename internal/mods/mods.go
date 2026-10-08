// Package mods installs the Claude Code mods that ship inside Den.
package mods

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/GlacierHubAB/den"
)

// Manifest is the subset of .claude-plugin/plugin.json Den reads.
type Manifest struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Mod is one bundled plugin.
type Mod struct {
	Name     string
	Version  string
	Root     string // path inside den.Mods
	Loaded   string // where Claude Code already loads a copy from, if anywhere
	Installs string // where Install puts it
}

// SkillsDir is the folder Claude Code loads plugins from without any flag.
func SkillsDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "skills")
}

// List describes the mods bundled into this binary.
func List() ([]Mod, error) {
	entries, err := fs.ReadDir(den.Mods, "mods")
	if err != nil {
		return nil, err
	}
	var out []Mod
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		root := "mods/" + e.Name()
		raw, err := fs.ReadFile(den.Mods, root+"/.claude-plugin/plugin.json")
		if err != nil {
			continue
		}
		var man Manifest
		if json.Unmarshal(raw, &man) != nil || man.Name == "" {
			continue
		}
		m := Mod{Name: man.Name, Version: man.Version, Root: root, Installs: filepath.Join(SkillsDir(), man.Name)}
		m.Loaded = loadedFrom(man.Name)
		out = append(out, m)
	}
	return out, nil
}

// loadedFrom returns a development checkout Claude Code already loads for
// this plugin through CLAUDE_CODE_PLUGIN_DIRS, so Install can avoid a second
// copy of the same mod. It checks the live environment and the env block of
// ~/.claude/settings.json.
func loadedFrom(name string) string {
	dirs := os.Getenv("CLAUDE_CODE_PLUGIN_DIRS")
	home, _ := os.UserHomeDir()
	if raw, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json")); err == nil {
		var s struct {
			Env map[string]string `json:"env"`
		}
		if json.Unmarshal(raw, &s) == nil && s.Env["CLAUDE_CODE_PLUGIN_DIRS"] != "" {
			dirs += ":" + s.Env["CLAUDE_CODE_PLUGIN_DIRS"]
		}
	}
	for _, d := range strings.FieldsFunc(dirs, func(r rune) bool { return r == ':' || r == ',' }) {
		d = strings.TrimSpace(d)
		if strings.HasPrefix(d, "~/") {
			d = filepath.Join(home, d[2:])
		}
		if d == "" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(d, ".claude-plugin", "plugin.json"))
		if err != nil {
			continue
		}
		var man Manifest
		if json.Unmarshal(raw, &man) == nil && man.Name == name {
			return d
		}
	}
	return ""
}

// Install writes the mod into its Installs directory, replacing whatever
// version was there. Sessions started afterwards load it.
func Install(m Mod) error {
	if err := os.RemoveAll(m.Installs); err != nil {
		return err
	}
	return fs.WalkDir(den.Mods, m.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(p, m.Root)
		dst := filepath.Join(m.Installs, filepath.FromSlash(rel))
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		raw, err := fs.ReadFile(den.Mods, p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, raw, 0o644)
	})
}

// Uninstall removes an installed copy. It refuses to touch anything that is
// not a plugin directory.
func Uninstall(m Mod) error {
	if _, err := os.Stat(filepath.Join(m.Installs, ".claude-plugin", "plugin.json")); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%s is not installed at %s", m.Name, m.Installs)
		}
		return err
	}
	return os.RemoveAll(m.Installs)
}

// InstalledVersion reports the version at Installs, or "".
func InstalledVersion(m Mod) string {
	raw, err := os.ReadFile(filepath.Join(m.Installs, ".claude-plugin", "plugin.json"))
	if err != nil {
		return ""
	}
	var man Manifest
	if json.Unmarshal(raw, &man) != nil {
		return ""
	}
	return man.Version
}
