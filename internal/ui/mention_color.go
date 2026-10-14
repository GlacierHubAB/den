package ui

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// Mentions in rendered text. Wherever Den draws markdown (the
// orchestrator's replies, mission threads, agent replies in the details
// pane) an agent named by its critter name shows as @Name in that agent's
// colour. Only agents of this machine's fleet count, under the names Den
// gave them; a name two of them share counts for neither. "@Name" always
// matches; a bare name only when it is several words ("Quiet Panda"),
// written exactly. Code, links and urls are never scanned, and an inbox
// body a human approves by its exact text is never restyled.

// mentionName is one agent the set matches.
type mentionName struct {
	name string
	sid  string
}

// mentionSet is the names text is matched against, longest first, and a
// key that changes with them (part of every rendering's cache key).
type mentionSet struct {
	names []mentionName
	key   string
}

// buildMentions makes the set from this machine's live agents, never the
// orchestrator, never a name two agents share. Remote agents are left out
// altogether: their names come from their host.
func buildMentions(agents []fleet.Agent, isOrchestrator func(fleet.Agent) bool) *mentionSet {
	count := map[string]int{}
	sid := map[string]string{}
	for _, a := range agents {
		if a.Host != "" || a.Status == fleet.Gone || isOrchestrator(a) {
			continue
		}
		name := sanitize.Line(a.Name)
		if strings.TrimSpace(name) == "" || name != strings.TrimSpace(name) {
			continue
		}
		count[name]++
		sid[name] = a.Session.SessionID
	}
	s := &mentionSet{}
	for name, n := range count {
		if n == 1 {
			s.names = append(s.names, mentionName{name, sid[name]})
		}
	}
	sort.Slice(s.names, func(i, j int) bool {
		if len(s.names[i].name) != len(s.names[j].name) {
			return len(s.names[i].name) > len(s.names[j].name)
		}
		return s.names[i].name < s.names[j].name
	})
	h := sha256.New()
	for _, n := range s.names {
		h.Write([]byte(n.name + "\x00" + n.sid + "\x00"))
	}
	s.key = hex.EncodeToString(h.Sum(nil))[:16]
	return s
}

// wordRune is a rune that continues a word: a name glued to one is not a
// mention.
func wordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-'
}

// boundaryBefore reports whether a mention may start at i.
func boundaryBefore(s string, i int) bool {
	if i == 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return !wordRune(r) && r != '@'
}

// boundaryAfter reports whether a mention may end at j.
func boundaryAfter(s string, j int) bool {
	if j >= len(s) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(s[j:])
	return !wordRune(r)
}

// match is the mention at s[i:], if any: its length and the agent.
func (ms *mentionSet) match(s string, i int) (int, mentionName, bool) {
	if !boundaryBefore(s, i) {
		return 0, mentionName{}, false
	}
	at := s[i] == '@'
	for _, n := range ms.names {
		switch {
		case at && strings.HasPrefix(s[i+1:], n.name) && boundaryAfter(s, i+1+len(n.name)):
			return 1 + len(n.name), n, true
		case !at && strings.Contains(n.name, " ") && strings.HasPrefix(s[i:], n.name) && boundaryAfter(s, i+len(n.name)):
			return len(n.name), n, true
		}
	}
	return 0, mentionName{}, false
}

// apply splits the plain spans of a line at its mentions; a mention
// becomes its own span, "@Name", carrying the agent.
func (ms *mentionSet) apply(spans []mdSpan) []mdSpan {
	if ms == nil || len(ms.names) == 0 {
		return spans
	}
	var out []mdSpan
	for _, sp := range spans {
		if sp.a.code || sp.a.link || sp.a.url || sp.a.mention != "" {
			out = append(out, sp)
			continue
		}
		text, last := sp.text, 0
		for i := 0; i < len(text); {
			n, who, ok := ms.match(text, i)
			if !ok {
				_, size := utf8.DecodeRuneInString(text[i:])
				i += size
				continue
			}
			if i > last {
				out = append(out, mdSpan{text[last:i], sp.a})
			}
			a := sp.a
			a.mention = who.sid
			out = append(out, mdSpan{"@" + who.name, a})
			i += n
			last = i
		}
		if last < len(text) {
			out = append(out, mdSpan{text[last:], sp.a})
		}
	}
	return out
}

// mentionKey is the set's part of a rendering's cache key.
func (m Model) mentionKey() string {
	if m.ments == nil {
		return ""
	}
	return m.ments.key
}
