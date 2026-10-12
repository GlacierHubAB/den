package closer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Guard is who a close must leave alone, and who it should only propose
// when named. It lives in Den's state (protected.json) so /protect,
// /unprotect, /flag and /unflag survive a restart, and the human can edit
// the file by hand.
type Guard struct {
	// Names are critter names or peer names that are never proposed.
	Names []string `json:"names"`
	// Paths are folders: an agent whose working directory, edited
	// repositories or task mention one is never proposed.
	Paths []string `json:"paths"`
	// Flagged agents hold context the human may still need: a bulk close
	// lists them as not selected; naming them includes them.
	Flagged []Flag `json:"flagged"`
}

// Flag is one flagged agent with the reason it is worth keeping.
type Flag struct {
	Name string `json:"name"` // critter name or peer name
	Note string `json:"note,omitempty"`
}

// GuardPath is the state file.
func GuardPath() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "den", "protected.json")
}

// LoadGuard reads the file; a missing or broken one is an empty guard.
func LoadGuard(path string) *Guard {
	g := &Guard{}
	raw, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(raw, g)
	}
	return g
}

// Save writes the guard to path. Best effort.
func (g *Guard) Save(path string) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

func (g *Guard) has(list []string, name string) int {
	for i, n := range list {
		if strings.EqualFold(strings.TrimSpace(n), strings.TrimSpace(name)) {
			return i
		}
	}
	return -1
}

// Protect adds a name; it reports whether it was new.
func (g *Guard) Protect(name string) bool {
	if name = strings.TrimSpace(name); name == "" || g.has(g.Names, name) >= 0 {
		return false
	}
	g.Names = append(g.Names, name)
	return true
}

// Unprotect removes a name; it reports whether it was there.
func (g *Guard) Unprotect(name string) bool {
	i := g.has(g.Names, name)
	if i < 0 {
		return false
	}
	g.Names = append(g.Names[:i], g.Names[i+1:]...)
	return true
}

// SetFlag flags a name with a note, replacing an earlier note.
func (g *Guard) SetFlag(name, note string) {
	name = strings.TrimSpace(name)
	for i := range g.Flagged {
		if strings.EqualFold(g.Flagged[i].Name, name) {
			g.Flagged[i].Note = note
			return
		}
	}
	g.Flagged = append(g.Flagged, Flag{Name: name, Note: note})
}

// Unflag removes a flag; it reports whether it was there.
func (g *Guard) Unflag(name string) bool {
	for i := range g.Flagged {
		if strings.EqualFold(g.Flagged[i].Name, strings.TrimSpace(name)) {
			g.Flagged = append(g.Flagged[:i], g.Flagged[i+1:]...)
			return true
		}
	}
	return false
}

// ProtectedName is the protected name among names, as it was written to
// the guard (so /unprotect takes it back), "" when none is protected.
func (g *Guard) ProtectedName(names ...string) string {
	for _, n := range names {
		if n == "" {
			continue
		}
		if i := g.has(g.Names, n); i >= 0 {
			return g.Names[i]
		}
	}
	return ""
}

// Protected says why an agent must be left alone, "" when it is not
// protected. name and peer are its names; dirs are its working directory
// and the repositories it edited; task is what it is working on.
func (g *Guard) Protected(name, peer string, dirs []string, task string) string {
	for _, n := range []string{name, peer} {
		if n != "" && g.has(g.Names, n) >= 0 {
			return "protected (" + n + ")"
		}
	}
	for _, p := range g.Paths {
		p = expandHome(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		base := filepath.Base(p)
		for _, d := range dirs {
			d = expandHome(d)
			if d == p || strings.HasPrefix(d, p+string(filepath.Separator)) {
				return "protected: works in " + shorten(p)
			}
		}
		if task != "" && base != "" && strings.Contains(strings.ToLower(task), strings.ToLower(base)) {
			return "protected: its task mentions " + base
		}
	}
	return ""
}

// Held says why an agent is worth keeping, "" when it is not flagged.
func (g *Guard) Held(name, peer string) (note string, ok bool) {
	for _, f := range g.Flagged {
		if strings.EqualFold(f.Name, name) || (peer != "" && strings.EqualFold(f.Name, peer)) {
			return f.Note, true
		}
	}
	return "", false
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	return p
}

func shorten(p string) string {
	home, _ := os.UserHomeDir()
	if home != "" && strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}
