// Package colors hands out one body colour per live session and remembers
// the choice on disk, so Den and the agent-badge mod show the same colour and
// it stays put while the session lives.
package colors

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"

	"github.com/GlacierHubAB/den/internal/critter"
)

// Agent is what Assign needs to know about a session.
type Agent struct {
	SessionID string
	StartedAt int64 // oldest wins collisions
}

// Path is the shared record: session id → hex colour.
func Path() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "den", "colors.json")
}

// Load reads the record; a missing file is an empty map.
func Load(path string) map[string]string {
	out := map[string]string{}
	raw, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	_ = json.Unmarshal(raw, &out)
	return out
}

// Save writes the record atomically.
func Save(path string, m map[string]string) error {
	if path == "" {
		return errors.New("no state directory")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Assign gives every agent a colour from hues. A recorded colour is kept when
// it is still free; otherwise the session's seed picks, moving on to the next
// free hue on a collision. Older sessions choose first so a newcomer never
// recolours an agent you already know. Entries for sessions not in agents are
// dropped. It returns the new record and whether it differs from prev.
func Assign(prev map[string]string, agents []Agent, hues []string) (map[string]string, bool) {
	next := map[string]string{}
	if len(hues) == 0 {
		return next, len(prev) != 0
	}
	order := append([]Agent(nil), agents...)
	sort.SliceStable(order, func(i, j int) bool { return order[i].StartedAt < order[j].StartedAt })
	index := map[string]int{}
	for i, h := range hues {
		index[h] = i
	}
	taken := map[int]bool{}
	claim := func(a Agent, idx int) {
		taken[idx] = true
		next[a.SessionID] = hues[idx]
	}
	var rest []Agent
	for _, a := range order {
		if idx, ok := index[prev[a.SessionID]]; ok && !taken[idx] {
			claim(a, idx)
			continue
		}
		rest = append(rest, a)
	}
	for _, a := range rest {
		idx := int(critter.Hash(a.SessionID) % uint32(len(hues)))
		if len(taken) < len(hues) {
			for taken[idx] {
				idx = (idx + 1) % len(hues)
			}
		}
		claim(a, idx)
	}
	changed := len(next) != len(prev)
	if !changed {
		for k, v := range next {
			if prev[k] != v {
				changed = true
				break
			}
		}
	}
	return next, changed
}
