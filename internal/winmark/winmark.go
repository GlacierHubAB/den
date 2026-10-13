// Package winmark keeps the window markers Den gave the terminals of the
// agents it spawned: the window class (hypr.Marker) set at launch, bound to
// the agent's session once it registers. Closing reads it to know which
// window, if any, is the agent's; a marker computed again from a name could
// match a window Den never made. The record is a file in Den's state,
// mode 0600, written under a lock since every Den process may spawn.
package winmark

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Entry is one spawn: the peer name it was given, its session once known,
// and the marker its terminal carries.
type Entry struct {
	Peer    string    `json:"peer"`
	Session string    `json:"session,omitempty"`
	Marker  string    `json:"marker"`
	At      time.Time `json:"at"`
}

// keep is how many entries the file holds; older ones go first.
const keep = 500

// Path is window-markers.json in Den's state folder.
func Path() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "den", "window-markers.json")
}

// Record notes the marker given to the terminal of the agent named peer.
func Record(peer, marker string) error {
	if peer == "" || marker == "" {
		return nil
	}
	return update(func(es []Entry) []Entry {
		return append(es, Entry{Peer: peer, Marker: marker, At: time.Now()})
	})
}

// Bind ties the newest unbound entry for peer to the session that
// registered under that name.
func Bind(peer, session string) error {
	if peer == "" || session == "" {
		return nil
	}
	return update(func(es []Entry) []Entry {
		for i := len(es) - 1; i >= 0; i-- {
			if es[i].Peer == peer && es[i].Session == "" {
				es[i].Session = session
				break
			}
		}
		return es
	})
}

// For is the marker Den gave the session's terminal, "" when Den did not
// spawn it (or its session never registered).
func For(session string) string {
	if session == "" {
		return ""
	}
	es, _ := load()
	for i := len(es) - 1; i >= 0; i-- {
		if es[i].Session == session {
			return es[i].Marker
		}
	}
	return ""
}

// Held reports whether a session alive says is live holds marker.
func Held(marker string, alive func(session string) bool) bool {
	es, _ := load()
	for _, e := range es {
		if e.Marker == marker && e.Session != "" && alive(e.Session) {
			return true
		}
	}
	return false
}

func load() ([]Entry, error) {
	p := Path()
	if p == "" {
		return nil, os.ErrNotExist
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var es []Entry
	if err := json.Unmarshal(raw, &es); err != nil {
		return nil, err
	}
	return es, nil
}

// update changes the record under an exclusive lock and writes it back
// whole, through a temporary file.
func update(change func([]Entry) []Entry) error {
	p := Path()
	if p == "" {
		return os.ErrNotExist
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(p+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	es, _ := load()
	es = change(es)
	if len(es) > keep {
		es = es[len(es)-keep:]
	}
	raw, err := json.Marshal(es)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".window-markers-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}
