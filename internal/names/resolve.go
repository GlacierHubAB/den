// Package names turns a name the human, the orchestrator or an app typed
// into one agent's session id. Every place Den resolves a name goes
// through Resolve, so that a name is only ever a way to find a session:
// what Den stores, guards, assigns and routes is the session id, never the
// name.
package names

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Candidate is one agent a name may point to.
type Candidate struct {
	ID    string // its session id: what the caller keeps
	Peer  string // its peer handle (Claude Code's --name)
	Name  string // the name Den shows for it
	Alias string // an older name that still resolves, "" for none
	Host  string // "" for this machine
	PID   int
}

// Opts widens what a query may be.
type Opts struct {
	// IDs: a session id prefix (at least 4 characters) or a pid matches
	// too, before any name. For commands typed in a terminal.
	IDs bool
	// Prefix: when nothing matches whole, a case-insensitive prefix of a
	// name or alias matches ("mossy" for "Mossy Lemur").
	Prefix bool
}

// ErrNoMatch is a query that matches no agent.
var ErrNoMatch = errors.New("no agent matches")

// Ambiguous is a query that matches more than one agent: Resolve never
// picks one. Candidates says which, for the caller to show.
type Ambiguous struct {
	Query      string
	Candidates []Candidate
}

func (e *Ambiguous) Error() string {
	return fmt.Sprintf("%q matches more than one agent: %s; name one exactly", e.Query, strings.Join(Labels(e.Candidates), ", "))
}

// NeedsHost is a query with no host that names nothing on this machine
// but agents elsewhere: the caller asks for the host rather than guess.
type NeedsHost struct {
	Query      string
	Candidates []Candidate
}

func (e *NeedsHost) Error() string {
	return fmt.Sprintf("%q is on another host: %s; add the host", e.Query, strings.Join(Labels(e.Candidates), ", "))
}

// Labels names candidates for a message: the name, its peer handle when
// two share the name, and the host for one elsewhere.
func Labels(cs []Candidate) []string {
	count := map[string]int{}
	for _, c := range cs {
		count[strings.ToLower(c.Name)]++
	}
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		l := c.Name
		if l == "" {
			l = c.Peer
		}
		if count[strings.ToLower(c.Name)] > 1 && c.Peer != "" && c.Peer != c.Name {
			l += " (" + c.Peer + ")"
		}
		if c.Host != "" {
			l += "@" + c.Host
		}
		out = append(out, l)
	}
	return out
}

// Resolve is the one agent query names. The query may start with "@" and
// may end with "@host" ("@local" for this machine). Without a host it
// names an agent on this machine; an agent elsewhere needs its host. The
// tiers are tried in order, and the first that matches anything decides:
// a session id or pid (with Opts.IDs), the exact peer handle, the exact
// name, the exact alias, then (with Opts.Prefix) a prefix of a name or an
// alias. Names match case-insensitively. More than one match in the
// deciding tier is *Ambiguous; none here is *NeedsHost when agents
// elsewhere match, else ErrNoMatch.
func Resolve(query string, cands []Candidate, o Opts) (Candidate, error) {
	q := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(query), "@"))
	if q == "" {
		return Candidate{}, ErrNoMatch
	}
	host, qualified := "", false
	if i := strings.LastIndexByte(q, '@'); i >= 0 {
		host, q, qualified = strings.ToLower(strings.TrimSpace(q[i+1:])), strings.TrimSpace(q[:i]), true
		if host == "local" {
			host = ""
		}
		if q == "" {
			return Candidate{}, ErrNoMatch
		}
	}
	var pool, elsewhere []Candidate
	for _, c := range cands {
		switch {
		case qualified && strings.EqualFold(c.Host, host), !qualified && c.Host == "":
			pool = append(pool, c)
		case !qualified:
			elsewhere = append(elsewhere, c)
		}
	}
	if c, err, ok := decide(query, q, pool, o); ok {
		return c, err
	}
	// Nothing here: say which agents elsewhere it would name, so the
	// caller can ask for the host rather than guess.
	if !qualified {
		if c, err, ok := decide(query, q, elsewhere, o); ok {
			var cs []Candidate
			var amb *Ambiguous
			if errors.As(err, &amb) {
				cs = amb.Candidates
			} else {
				cs = []Candidate{c}
			}
			return Candidate{}, &NeedsHost{Query: query, Candidates: cs}
		}
	}
	return Candidate{}, ErrNoMatch
}

// decide runs the tiers over pool: ok when a tier matched anything.
func decide(query, q string, pool []Candidate, o Opts) (Candidate, error, bool) {
	lower := strings.ToLower(q)
	tiers := []func(Candidate) bool{}
	if o.IDs {
		pid, _ := strconv.Atoi(q)
		tiers = append(tiers,
			func(c Candidate) bool { return c.ID != "" && c.ID == q },
			func(c Candidate) bool { return pid > 0 && c.PID == pid },
			func(c Candidate) bool { return len(q) >= 4 && strings.HasPrefix(c.ID, q) },
		)
	}
	tiers = append(tiers,
		func(c Candidate) bool { return c.Peer != "" && strings.EqualFold(c.Peer, q) },
		func(c Candidate) bool { return c.Name != "" && strings.EqualFold(c.Name, q) },
		func(c Candidate) bool { return c.Alias != "" && strings.EqualFold(c.Alias, q) },
	)
	if o.Prefix {
		tiers = append(tiers, func(c Candidate) bool {
			return strings.HasPrefix(strings.ToLower(c.Name), lower) || c.Alias != "" && strings.HasPrefix(strings.ToLower(c.Alias), lower)
		})
	}
	for _, match := range tiers {
		var hits []Candidate
		for _, c := range pool {
			if match(c) {
				hits = append(hits, c)
			}
		}
		switch len(hits) {
		case 0:
			continue
		case 1:
			return hits[0], nil, true
		default:
			return Candidate{}, &Ambiguous{Query: query, Candidates: hits}, true
		}
	}
	return Candidate{}, nil, false
}
