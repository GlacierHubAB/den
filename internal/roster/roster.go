// Package roster remembers the agents Den has seen live, so they can be
// reopened with `claude --resume` after a reboot, a Den restart or a close.
// It is a snapshot, not a log: one entry per session, rewritten atomically
// to $XDG_STATE_HOME/den/roster.json whenever the fleet changes.
//
// Claude Code 2.1.289 keeps the session id on `--resume <id>`, so a
// resumed agent takes its old place in the tree and its mission as it is.
// Only `--fork-session` gives a new id; Forks maps the old one to it.
package roster

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/GlacierHubAB/den/internal/cost"
	"github.com/GlacierHubAB/den/internal/names"
)

// Agent is what Den knew of one session the last time it saw it live.
type Agent struct {
	SessionID string `json:"session_id"`
	Peer      string `json:"peer,omitempty"` // the session's --name
	Critter   string `json:"critter"`
	Given     string `json:"given,omitempty"` // the name Den gave it at spawn, "" for none
	Role      string `json:"role,omitempty"`
	Model     string `json:"model,omitempty"` // raw model id from the transcript
	// Mode is the permission mode its hooks last reported, "" for none.
	// A reopen never runs it in more than this (permmode.Lower).
	Mode string `json:"mode,omitempty"`
	CWD  string `json:"cwd"`
	// Parent directs it: a session id or tree.Orchestrator, "" for none.
	Parent  string `json:"parent,omitempty"`
	Brief   string `json:"brief,omitempty"`   // the brief on the edge to its parent
	Mission string `json:"mission,omitempty"` // the mission its card or an ancestor's card serves
	// Spawned means Den started it (spawn_agent), so it had Den's MCP
	// server and gets it back when reopened.
	Spawned   bool      `json:"spawned,omitempty"`
	SkillsDir string    `json:"skills_dir,omitempty"` // its private skills folder, given with --add-dir
	LastSeen  time.Time `json:"last_seen"`
	// Ended is when it went away while Den was watching; Closed when Den
	// closed it (/close, ^X, close_agent); Dismissed when the human said
	// no to reopening it. Each keeps it out of the restore question.
	Ended     time.Time `json:"ended,omitzero"`
	Closed    time.Time `json:"closed,omitzero"`
	Dismissed time.Time `json:"dismissed,omitzero"`
}

// Name is the name Den gave it, else the critter name, else the peer
// name.
func (a Agent) Name() string {
	if a.Given != "" {
		return a.Given
	}
	if a.Critter != "" {
		return a.Critter
	}
	return a.Peer
}

// Roster is the snapshot on disk.
type Roster struct {
	Saved  time.Time         `json:"saved"`
	Agents []Agent           `json:"agents"`
	Forks  map[string]string `json:"forks,omitempty"` // old session id → the id of its fork
}

// Keep is how long an entry outlives its last sighting.
const Keep = 14 * 24 * time.Hour

// Burst is how close to Den's last save an agent may have ended and still
// count as part of the shutdown that took Den down too.
const Burst = 2 * time.Minute

// Path is the snapshot on disk.
func Path() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "den", "roster.json")
}

// Load reads the snapshot; a missing or unreadable file is an empty roster.
func Load(path string) *Roster {
	r := &Roster{}
	raw, err := os.ReadFile(path)
	if err != nil {
		return r
	}
	_ = json.Unmarshal(raw, r)
	return r
}

// Save writes the snapshot atomically: a temporary file renamed over the
// old one, so a crash never leaves half a roster.
func (r *Roster) Save(path string, now time.Time) error {
	if path == "" {
		return nil
	}
	r.Saved = now
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".roster-*.json")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// Get finds an entry by session id.
func (r *Roster) Get(id string) (Agent, bool) {
	for _, a := range r.Agents {
		if a.SessionID == id {
			return a, true
		}
	}
	return Agent{}, false
}

func (r *Roster) index(id string) int {
	for i, a := range r.Agents {
		if a.SessionID == id {
			return i
		}
	}
	return -1
}

// Observe folds in the agents live right now. A live agent's entry is
// replaced by what Den sees now, and it is no longer ended, closed or
// dismissed. An entry seen live since watchingSince that is not live any
// more ended while Den watched. Entries past Keep are dropped. It reports
// whether anything other than the sighting times changed, which is when
// the roster is worth saving at once.
func (r *Roster) Observe(live []Agent, now, watchingSince time.Time) bool {
	changed := false
	seen := map[string]bool{}
	for _, a := range live {
		seen[a.SessionID] = true
		a.LastSeen = now
		a.Ended, a.Closed, a.Dismissed = time.Time{}, time.Time{}, time.Time{}
		i := r.index(a.SessionID)
		if i < 0 {
			r.Agents = append(r.Agents, a)
			changed = true
			continue
		}
		// A sighting that lacks a fact keeps the one Den knew: badge labels
		// and the transcript's model can be missing for a moment, and a
		// restored agent's edge no longer says it was spawned.
		old := r.Agents[i]
		if a.SkillsDir == "" {
			a.SkillsDir = old.SkillsDir
		}
		if a.Mission == "" {
			a.Mission = old.Mission
		}
		if a.Role == "" {
			a.Role = old.Role
		}
		if a.Model == "" {
			a.Model = old.Model
		}
		if a.Mode == "" {
			a.Mode = old.Mode
		}
		a.Spawned = a.Spawned || old.Spawned
		probe := old
		probe.LastSeen = a.LastSeen
		if probe != a {
			changed = true
		}
		r.Agents[i] = a
	}
	var kept []Agent
	for _, a := range r.Agents {
		if !seen[a.SessionID] && a.Ended.IsZero() && !a.LastSeen.Before(watchingSince) {
			a.Ended = now
			changed = true
		}
		if now.Sub(a.LastSeen) > Keep {
			changed = true
			continue
		}
		kept = append(kept, a)
	}
	r.Agents = kept
	for old, fork := range r.Forks {
		if r.index(old) < 0 && r.index(fork) < 0 {
			delete(r.Forks, old)
			changed = true
		}
	}
	return changed
}

// Gone lists the agents to offer after a restart: not live now, and neither
// closed, dismissed nor ended on their own while Den watched. An agent that
// ended within Burst of Den's last save went down with Den and is offered.
// Newest sighting first.
func (r *Roster) Gone(alive func(id string) bool) []Agent {
	var out []Agent
	for _, a := range r.Agents {
		if alive(a.SessionID) || !a.Closed.IsZero() || !a.Dismissed.IsZero() {
			continue
		}
		if !a.Ended.IsZero() && a.Ended.Before(r.Saved.Add(-Burst)) {
			continue
		}
		out = append(out, a)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out
}

// MarkClosed records that Den closed the session.
func (r *Roster) MarkClosed(id string, now time.Time) bool {
	if i := r.index(id); i >= 0 {
		r.Agents[i].Closed = now
		return true
	}
	return false
}

// Dismiss records that the human declined to reopen the session.
func (r *Roster) Dismiss(id string, now time.Time) bool {
	if i := r.index(id); i >= 0 {
		r.Agents[i].Dismissed = now
		return true
	}
	return false
}

// Fork records that newID was forked from oldID. The fork takes the old
// entry's place in the tree and its mission until Den sees it live.
func (r *Roster) Fork(oldID, newID string) {
	if oldID == "" || newID == "" || oldID == newID {
		return
	}
	if r.Forks == nil {
		r.Forks = map[string]string{}
	}
	r.Forks[oldID] = newID
}

// Find is the entry name means (names.Resolve: a session id or its
// prefix, a peer handle, then a name), among the entries not superseded by
// a fork. A name more than one entry has is refused, listing them: Den
// never picks the most recent for the human.
func (r *Roster) Find(name string) (Agent, error) {
	var cands []names.Candidate
	for _, a := range r.Agents {
		if _, forked := r.Forks[a.SessionID]; forked {
			continue
		}
		c := names.Candidate{ID: a.SessionID, Peer: a.Peer, Name: a.Name()}
		if a.Given != "" {
			c.Alias = a.Critter
		}
		cands = append(cands, c)
	}
	c, err := names.Resolve(name, cands, names.Opts{IDs: true})
	if err != nil {
		return Agent{}, err
	}
	a, _ := r.Get(c.ID)
	return a, nil
}

// ResumeEstimate is an estimate, in US dollars, of what reopening a
// conversation of the given size costs before its first new turn: the
// whole context is written to the prompt cache again, at the model's
// cache-write rate from Den's price table (the cost package, with the
// user's [prices] overrides). Claude Code's SessionStart hook reports the
// same figure (estimated_cache_write_usd) only after the resume. ok is
// false for a model the table does not price.
func ResumeEstimate(prices cost.Table, model string, tokens int) (usd float64, ok bool) {
	p, ok := prices.For(model)
	if !ok || tokens <= 0 {
		return 0, false
	}
	return float64(tokens) / 1e6 * p.CacheWrite, true
}
