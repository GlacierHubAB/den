package names

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
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
	// Former are names Den gave this session before the human renamed it:
	// they still resolve to it, and stay taken while it lives.
	Former []string `json:"former,omitempty"`
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

// Load is the registry by session id, as read and checked (valid): a
// missing or broken file is empty. Anything running as the human can write
// the file, so an entry Den would not have written is dropped, and that
// agent shows its critter name.
func Load(path string) map[string]Entry {
	r, _ := load(path)
	return valid(r.Entries)
}

// maxN bounds a name's number.
const maxN = 999

// sessionID is what a session id looks like.
var sessionID = regexp.MustCompile(`^[0-9A-Fa-f][0-9A-Fa-f-]{15,63}$`)

// valid keeps the entries Den could have written: a session id, a base
// Normalise leaves as it is and Reserved allows, a number in 1..maxN, and a
// name no other entry has (two entries with one name both go).
func valid(es []Entry) map[string]Entry {
	out := map[string]Entry{}
	count := map[string]int{}
	for _, e := range es {
		if e.Base != "" {
			count[strings.ToLower(e.Name())]++
		}
	}
	for _, e := range es {
		switch {
		case !sessionID.MatchString(e.Session):
			continue
		case e.Base != "" && (Normalise(e.Base) != e.Base || Reserved(e.Base) != "" || e.N < 1 || e.N > maxN || count[strings.ToLower(e.Name())] > 1):
			continue
		case e.Base == "" && e.N != 0:
			continue
		}
		e.Alias, e.Role, e.Title = sanitize.Label(e.Alias, 60), sanitize.Label(e.Role, 80), sanitize.Label(e.Title, 80)
		var former []string
		for _, f := range e.Former {
			if Valid(f) && len(former) < 8 {
				former = append(former, f)
			}
		}
		e.Former = former
		out[e.Session] = e
	}
	return out
}

// Live is the registry as one Den process holds it: read and checked once,
// changed only by its own Assign. Nothing written to the file while Den
// runs changes a name it shows.
type Live struct {
	mu       sync.Mutex
	path     string
	by       map[string]Entry
	migrated bool
}

// OpenLive reads the registry once.
func OpenLive(path string) *Live {
	r, _ := load(path)
	return &Live{path: path, by: valid(r.Entries), migrated: r.Migrated}
}

// Migrated reports whether Den has named the agents that ran before it
// named agents by role (once, at start).
func (l *Live) Migrated() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.migrated
}

// MarkMigrated records that it has.
func (l *Live) MarkMigrated() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.migrated = true
	return l.save()
}

// Rename gives session the name the human typed (Base of it as a title:
// normalised, checked, numbered by Den), keeping a name it had as a former
// one, which still resolves to it and stays taken while it lives. alias is
// its critter name. It is the human's /rename in Den, and nothing else.
func (l *Live) Rename(session, text, alias string, ns Namespace, now time.Time) (Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, err := l.renamed(session, text, alias, ns, now)
	if err != nil {
		return Entry{}, err
	}
	l.by[session] = e
	return e, l.save()
}

// Preview is the entry Rename would make now, changing nothing.
func (l *Live) Preview(session, text, alias string, ns Namespace) (Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.renamed(session, text, alias, ns, time.Time{})
}

// renamed is the entry renaming session would make; l.mu is held.
func (l *Live) renamed(session, text, alias string, ns Namespace, now time.Time) (Entry, error) {
	if Generic(text) {
		return Entry{}, errors.New("no name Den may use: it says nothing about the work; give a few words on the agent's job")
	}
	base, why := Base("", text)
	if base == "" {
		return Entry{}, errors.New("no name Den may use: " + why)
	}
	if !sessionID.MatchString(session) {
		return Entry{}, errors.New("not a session id")
	}
	old, had := l.by[session]
	var held []Entry
	for sid, e := range l.by {
		if sid == session {
			// Its own former names stay taken; its current one is free to
			// keep if the base is the same.
			held = append(held, Entry{Former: e.Former})
			continue
		}
		if ns.Held == nil || ns.Held(sid) {
			held = append(held, e)
		}
	}
	e := Entry{Session: session, Alias: sanitize.Label(alias, 60), Title: sanitize.Label(text, 80), Base: base, Since: now}
	if had {
		e.Role, e.Former = old.Role, old.Former
		if n := old.Name(); n != "" && !slices.Contains(e.Former, n) {
			e.Former = append(e.Former, n)
		}
	}
	e.N = freeNumber(base, held, ns)
	if n := e.Name(); slices.Contains(e.Former, n) {
		e.Former = slices.DeleteFunc(e.Former, func(f string) bool { return f == n })
	}
	return e, nil
}

// Drop forgets session's name: it shows its critter name again.
func (l *Live) Drop(session string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.by, session)
	return l.save()
}

// save writes the held registry whole; l.mu is held.
func (l *Live) save() error {
	all := make([]Entry, 0, len(l.by))
	for _, x := range l.by {
		all = append(all, x)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Since.Before(all[j].Since) })
	migrated := l.migrated
	return update(l.path, func(r *registry) { r.Entries, r.Migrated = all, migrated })
}

// All is a copy of the names Den holds.
func (l *Live) All() map[string]Entry {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make(map[string]Entry, len(l.by))
	for k, v := range l.by {
		out[k] = v
	}
	return out
}

// Assign gives session its name, from what this process holds, and writes
// the registry back whole; a session keeps the name it has. ns is checked
// with the other held names, as in Assign.
func (l *Live) Assign(session, role, title, alias string, ns Namespace, now time.Time) (Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, ok := l.by[session]; ok {
		return e, nil
	}
	if !sessionID.MatchString(session) {
		return Entry{}, errors.New("not a session id")
	}
	var held []Entry
	for sid, e := range l.by {
		if ns.Held == nil || ns.Held(sid) {
			held = append(held, e)
		} else {
			delete(l.by, sid)
		}
	}
	e := Entry{Session: session, Alias: sanitize.Label(alias, 60), Role: sanitize.Label(role, 80), Title: sanitize.Label(title, 80), Since: now}
	e.Base, _ = Base(role, title)
	if e.Base != "" {
		e.N = freeNumber(e.Base, held, ns)
	}
	l.by[session] = e
	return e, l.save()
}

// freeNumber is the lowest n that makes base's name free.
func freeNumber(base string, held []Entry, ns Namespace) int {
	used := map[string]bool{}
	for _, e := range held {
		if n := e.Name(); n != "" {
			used[strings.ToLower(n)] = true
		}
		for _, f := range e.Former {
			used[f] = true
		}
	}
	for n := 1; ; n++ {
		name := Entry{Base: base, N: n}.Name()
		if !used[name] && !ns.Taken[name] && Reserved(name) == "" {
			return n
		}
	}
}

// maxFile bounds the registry file.
const maxFile = 1 << 20

func load(path string) (registry, error) {
	var r registry
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return r, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return r, errors.New("not a regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxFile+1))
	if err != nil {
		return r, err
	}
	if len(raw) > maxFile {
		return r, errors.New("too large")
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
