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
//
// An agent is guarded by its session, never by a name: a name is only how
// the human found it. The session survives a restore and /reopen; a new
// agent that happens to get the same name is not guarded. Names and the
// name-only Flagged entries are a file from before, read once and turned
// into sessions (TakeLegacy).
type Guard struct {
	// Sessions are agents that are never proposed.
	Sessions []Entry `json:"sessions,omitempty"`
	// Paths are folders: an agent whose working directory, edited
	// repositories or task mention one is never proposed.
	Paths []string `json:"paths"`
	// Flags are agents holding context the human may still need: a bulk
	// close lists them as not selected; naming them includes them.
	Flags []Entry `json:"flags,omitempty"`

	// Names and Flagged are the guard as an older Den wrote it, by name.
	Names   []string `json:"names,omitempty"`
	Flagged []Flag   `json:"flagged,omitempty"`
}

// Entry is one guarded agent: its session key (the session id, with
// "@host" for an agent on another machine), the name it had when the
// human guarded it (shown, never matched), and a flag's note.
type Entry struct {
	Session string `json:"session"`
	Name    string `json:"name,omitempty"`
	Note    string `json:"note,omitempty"`
}

// Flag is a flagged agent in an older file, by name.
type Flag struct {
	Name string `json:"name"`
	Note string `json:"note,omitempty"`
}

// Key is the guard's key for a session: its id, with "@host" for an agent
// on another machine.
func Key(sessionID, host string) string {
	if host == "" {
		return sessionID
	}
	return sessionID + "@" + host
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

func find(list []Entry, key string) int {
	for i, e := range list {
		if key != "" && e.Session == key {
			return i
		}
	}
	return -1
}

// Protect guards a session; it reports whether it was new.
func (g *Guard) Protect(key, name string) bool {
	if key == "" || find(g.Sessions, key) >= 0 {
		return false
	}
	g.Sessions = append(g.Sessions, Entry{Session: key, Name: name})
	return true
}

// Unprotect removes a session; it reports whether it was there.
func (g *Guard) Unprotect(key string) bool {
	i := find(g.Sessions, key)
	if i < 0 {
		return false
	}
	g.Sessions = append(g.Sessions[:i], g.Sessions[i+1:]...)
	return true
}

// SetFlag flags a session with a note, replacing an earlier note.
func (g *Guard) SetFlag(key, name, note string) {
	if key == "" {
		return
	}
	if i := find(g.Flags, key); i >= 0 {
		g.Flags[i].Note, g.Flags[i].Name = note, name
		return
	}
	g.Flags = append(g.Flags, Entry{Session: key, Name: name, Note: note})
}

// Unflag removes a flag; it reports whether it was there.
func (g *Guard) Unflag(key string) bool {
	i := find(g.Flags, key)
	if i < 0 {
		return false
	}
	g.Flags = append(g.Flags[:i], g.Flags[i+1:]...)
	return true
}

// IsProtected reports whether the session is guarded by the human's
// /protect, with the entry.
func (g *Guard) IsProtected(key string) (Entry, bool) {
	if i := find(g.Sessions, key); i >= 0 {
		return g.Sessions[i], true
	}
	return Entry{}, false
}

// Held says why a session is worth keeping, "" when it is not flagged.
func (g *Guard) Held(key string) (note string, ok bool) {
	if i := find(g.Flags, key); i >= 0 {
		return g.Flags[i].Note, true
	}
	return "", false
}

// Legacy is the guard's name entries from an older file.
func (g *Guard) Legacy() (protect []string, flags []Flag) {
	return g.Names, g.Flagged
}

// TakeLegacy drops the name entries from an older file once they are
// turned into sessions; keep are the ones to read again later (an agent on
// a host not yet seen).
func (g *Guard) TakeLegacy(keepProtect []string, keepFlags []Flag) {
	g.Names, g.Flagged = keepProtect, keepFlags
}

// Protected says why an agent must be left alone, "" when it is not
// protected. key is its session (Key); dirs are its working directory and
// the repositories it edited; task is what it is working on.
func (g *Guard) Protected(key string, dirs []string, task string) string {
	if e, ok := g.IsProtected(key); ok {
		if e.Name != "" {
			return "protected (" + e.Name + ")"
		}
		return "protected"
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
