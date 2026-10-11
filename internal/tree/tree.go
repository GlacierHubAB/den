// Package tree records who directs whom: one parent per agent session,
// learned from the messages that hand work on. The orchestrator is the
// root "orchestrator" whatever its session id, since it restarts and
// resumes under new ids.
package tree

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Orchestrator is the parent id Den's own orchestrator carries.
const Orchestrator = "orchestrator"

// Edge says Parent gave Child its current task.
type Edge struct {
	Parent string    `json:"parent"`
	Child  string    `json:"child"`
	Brief  string    `json:"brief,omitempty"` // first line of what was sent
	At     time.Time `json:"at"`
	Source string    `json:"source,omitempty"` // "dispatch", "peer", "spawn"
	Drop   bool      `json:"drop,omitempty"`   // the child reported back or left
}

// Set holds the current edge of every directed child.
type Set struct {
	byChild map[string]Edge
}

// New returns an empty set.
func New() *Set { return &Set{byChild: map[string]Edge{}} }

// Add records an edge. A child keeps its newest parent; an edge that only
// repeats the current parent refreshes the brief and time. It reports
// whether anything changed.
func (s *Set) Add(e Edge) bool {
	if e.Child == "" || e.Parent == "" || e.Child == e.Parent {
		return false
	}
	if e.Drop {
		if _, ok := s.byChild[e.Child]; !ok {
			return false
		}
		delete(s.byChild, e.Child)
		return true
	}
	// Never let a child become its own ancestor's parent: a reply from a
	// child to its parent is not a new assignment.
	if s.IsAncestor(e.Child, e.Parent) {
		return false
	}
	cur, ok := s.byChild[e.Child]
	if ok && cur.Parent == e.Parent && cur.Brief == e.Brief {
		return false
	}
	s.byChild[e.Child] = e
	return true
}

// Remove forgets the child's edge.
func (s *Set) Remove(child string) bool {
	if _, ok := s.byChild[child]; !ok {
		return false
	}
	delete(s.byChild, child)
	return true
}

// Parent returns who directs the child.
func (s *Set) Parent(child string) (Edge, bool) {
	e, ok := s.byChild[child]
	return e, ok
}

// Children lists the edges from a parent, oldest first.
func (s *Set) Children(parent string) []Edge {
	var out []Edge
	for _, e := range s.byChild {
		if e.Parent == parent {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.Before(out[j].At)
		}
		return out[i].Child < out[j].Child
	})
	return out
}

// IsAncestor reports whether a is above b in the tree.
func (s *Set) IsAncestor(a, b string) bool {
	seen := map[string]bool{}
	for cur := b; cur != "" && !seen[cur]; {
		seen[cur] = true
		e, ok := s.byChild[cur]
		if !ok {
			return false
		}
		if e.Parent == a {
			return true
		}
		cur = e.Parent
	}
	return false
}

// Chain lists the ancestors of the child, root first.
func (s *Set) Chain(child string) []string {
	var out []string
	seen := map[string]bool{child: true}
	for cur := child; ; {
		e, ok := s.byChild[cur]
		if !ok || seen[e.Parent] {
			break
		}
		seen[e.Parent] = true
		out = append([]string{e.Parent}, out...)
		cur = e.Parent
	}
	return out
}

// Prune drops edges whose child is no longer around, and whose parent is
// neither around nor the orchestrator. It reports whether anything changed.
func (s *Set) Prune(alive func(id string) bool) bool {
	changed := false
	for child, e := range s.byChild {
		if !alive(child) || (e.Parent != Orchestrator && !alive(e.Parent)) {
			delete(s.byChild, child)
			changed = true
		}
	}
	return changed
}

// Len is the number of directed children.
func (s *Set) Len() int { return len(s.byChild) }

// Path is the shared record, one JSON edge per line, appended by Den and by
// the spawn tool.
func Path() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "den", "edges.jsonl")
}

// Load replays the record; the last line for a child wins.
func Load(path string) *Set {
	s := New()
	f, err := os.Open(path)
	if err != nil {
		return s
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var e Edge
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		s.Add(e)
	}
	return s
}

// Append adds one edge to the record. Best effort: a missing directory is
// created, a write error is returned for the caller to ignore.
func Append(path string, e Edge) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = f.Write(append(raw, '\n'))
	return err
}

var peerHeader = regexp.MustCompile(`<cross-session-message from="uds:[^"]*/cc-socks/(\d+)\.sock"(?: from-name="([^"]*)")?`)

// Peer reads the sender of a cross-session message out of the prompt
// Claude Code wrapped it in: the sender's process id and peer name, and
// the first line of the message as the brief.
func Peer(prompt string) (pid int, name, brief string, ok bool) {
	loc := peerHeader.FindStringSubmatchIndex(prompt)
	if loc == nil {
		return 0, "", "", false
	}
	pid, err := strconv.Atoi(prompt[loc[2]:loc[3]])
	if err != nil {
		return 0, "", "", false
	}
	if loc[4] >= 0 {
		name = prompt[loc[4]:loc[5]]
	}
	rest := prompt[loc[1]:]
	if i := strings.IndexByte(rest, '>'); i >= 0 {
		rest = rest[i+1:]
	}
	for _, l := range strings.Split(rest, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "</cross-session-message") {
			continue
		}
		brief = strings.TrimSpace(strings.TrimSuffix(l, "</cross-session-message>"))
		break
	}
	return pid, name, brief, true
}
