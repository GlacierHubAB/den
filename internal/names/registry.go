package names

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/GlacierHubAB/den/internal/sanitize"
)

// The registry is the names Den gave: one entry per session, written only
// by Den, at spawn (and once for the agents that ran before, at start), in
// names.json in Den's state, mode 0600, under a lock, since every Den
// process may spawn. A name is a label: Den keys everything on the session
// id and looks the name up for display only.

// Entry is the name Den gave one session.
type Entry struct {
	Session string `json:"session"`
	// Base and N make the name: Base alone when N is 1, else Base and N
	// ("backend-engineer2"). Base is "" when the role and title gave no
	// name Den may use: the agent keeps its critter name.
	Base string `json:"base,omitempty"`
	N    int    `json:"n,omitempty"`
	// Alias is the critter name it had, which still resolves.
	Alias string `json:"alias,omitempty"`
	// Role and Title are what the spawn asked for, as labels.
	Role  string    `json:"role,omitempty"`
	Title string    `json:"title,omitempty"`
	Since time.Time `json:"since"`
}

// Name is the entry's name, "" for none.
func (e Entry) Name() string {
	switch {
	case e.Base == "":
		return ""
	case e.N <= 1:
		return e.Base
	}
	return e.Base + strconv.Itoa(e.N)
}

// Namespace is what a new name must not equal, besides the names of the
// registry's sessions that still hold theirs.
type Namespace struct {
	// Taken are the names in use, lower-case: the peer handles of every
	// session Den can see on this machine, and the critter names (aliases)
	// of every session live or in the roster.
	Taken map[string]bool
	// Held reports whether a session still holds its name: it is live, or
	// in the roster or the restore list. A name is free again only once
	// its session holds it no longer.
	Held func(session string) bool
}

// RegistryPath is names.json in Den's state folder.
func RegistryPath() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "den", "names.json")
}

// registry is the file's content.
type registry struct {
	Entries []Entry `json:"entries"`
	// Migrated marks the one-time naming of the agents that ran before.
	Migrated bool `json:"migrated,omitempty"`
}

// Load is the registry by session id; a missing or broken file is empty.
func Load(path string) map[string]Entry {
	r, _ := load(path)
	out := make(map[string]Entry, len(r.Entries))
	for _, e := range r.Entries {
		out[e.Session] = e
	}
	return out
}

// Assign gives session its name from role and title and records it, or
// returns the one it has: a session keeps its name. The number is the
// lowest that makes the name free in ns and among the sessions that still
// hold theirs; entries whose sessions hold theirs no longer are dropped.
func Assign(path, session, role, title, alias string, ns Namespace, now time.Time) (Entry, error) {
	var out Entry
	err := update(path, func(r *registry) {
		var kept []Entry
		for _, e := range r.Entries {
			if e.Session == session {
				out = e
			}
			if e.Session == session || ns.Held == nil || ns.Held(e.Session) {
				kept = append(kept, e)
			}
		}
		r.Entries = kept
		if out.Session != "" {
			return
		}
		out = Entry{Session: session, Alias: sanitize.Label(alias, 60), Role: sanitize.Label(role, 80), Title: sanitize.Label(title, 80), Since: now}
		out.Base, _ = Base(role, title)
		if out.Base != "" {
			out.N = freeNumber(out.Base, r.Entries, ns)
		}
		r.Entries = append(r.Entries, out)
	})
	return out, err
}

// freeNumber is the lowest n that makes base's name free.
func freeNumber(base string, held []Entry, ns Namespace) int {
	used := map[string]bool{}
	for _, e := range held {
		if n := e.Name(); n != "" {
			used[strings.ToLower(n)] = true
		}
	}
	for n := 1; ; n++ {
		name := Entry{Base: base, N: n}.Name()
		if !used[name] && !ns.Taken[name] && Reserved(name) == "" {
			return n
		}
	}
}

func load(path string) (registry, error) {
	var r registry
	raw, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	err = json.Unmarshal(raw, &r)
	return r, err
}

// update changes the registry under an exclusive lock and writes it back
// whole, through a temporary file.
func update(path string, change func(*registry)) error {
	if path == "" {
		return os.ErrNotExist
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	r, _ := load(path)
	change(&r)
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".names-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
