// Package discover finds the Claude Code sessions running on this machine.
//
// Claude Code (2.1.289+) writes one JSON file per live session to
// ~/.claude/sessions/<pid>.json with the session id, cwd, a busy/idle
// status and the path of the session's inbox socket. Den treats that
// directory as the source of truth and verifies each pid is still alive.
package discover

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Session is one running Claude Code process as its sessions file reports it.
type Session struct {
	PID         int    `json:"pid"`
	SessionID   string `json:"sessionId"`
	CWD         string `json:"cwd"`
	StartedAt   int64  `json:"startedAt"`
	Version     string `json:"version"`
	Kind        string `json:"kind"`
	Entrypoint  string `json:"entrypoint"`
	Name        string `json:"name"`
	NameSource  string `json:"nameSource"`
	Status      string `json:"status"` // "busy" | "idle" as Claude Code reports it
	UpdatedAt   int64  `json:"updatedAt"`
	StatusAt    int64  `json:"statusUpdatedAt"`
	Socket      string `json:"messagingSocketPath"`
	PeerFeature []string `json:"peerFeatures"`

	// Alive is false when the pid in the file no longer exists; the file is
	// stale and the agent is gone.
	Alive bool `json:"-"`
}

// Started returns the session start time.
func (s Session) Started() time.Time { return time.UnixMilli(s.StartedAt) }

// StatusChanged returns when Claude Code last changed the busy/idle status.
func (s Session) StatusChanged() time.Time { return time.UnixMilli(s.StatusAt) }

// ShortCWD renders the working directory relative to $HOME.
func (s Session) ShortCWD() string {
	home, _ := os.UserHomeDir()
	if home != "" && strings.HasPrefix(s.CWD, home) {
		rest := strings.TrimPrefix(s.CWD, home)
		if rest == "" {
			return "~"
		}
		return "~" + rest
	}
	return s.CWD
}

// Dir is the directory Claude Code writes session files to.
func Dir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "sessions")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "sessions")
}

// Scan reads every session file and reports the sessions, live ones first,
// then by start time. A file that does not parse is skipped.
func Scan() ([]Session, error) {
	entries, err := os.ReadDir(Dir())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []Session
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(Dir(), e.Name()))
		if err != nil {
			continue
		}
		var s Session
		if err := json.Unmarshal(raw, &s); err != nil || s.SessionID == "" {
			continue
		}
		if s.PID == 0 {
			s.PID, _ = strconv.Atoi(strings.TrimSuffix(e.Name(), ".json"))
		}
		s.Alive = alive(s.PID)
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Alive != out[j].Alive {
			return out[i].Alive
		}
		return out[i].StartedAt < out[j].StartedAt
	})
	return out, nil
}

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
