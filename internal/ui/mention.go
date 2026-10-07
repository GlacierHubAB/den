package ui

import (
	"strings"

	"github.com/GlacierHubAB/den/internal/fleet"
)

// mention is the result of reading "@Name text".
type mention struct {
	target    *fleet.Agent
	body      string
	ambiguous []string // names that all match a partial mention
	missing   string   // the partial that matched nobody
}

// resolveMention finds the agent a message is addressed to. A full name at
// the start wins ("@Mossy Lemur rebase…"); otherwise a single word is matched
// as a case-insensitive prefix of a name ("@mossy rebase…"). Text without a
// leading @ has no target.
func resolveMention(text string, agents []fleet.Agent) mention {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "@") {
		return mention{body: text}
	}
	rest := strings.TrimPrefix(text, "@")
	lower := strings.ToLower(rest)
	var best *fleet.Agent
	bestLen := 0
	for i := range agents {
		name := strings.ToLower(agents[i].Name)
		if strings.HasPrefix(lower, name) && (len(lower) == len(name) || lower[len(name)] == ' ') && len(name) > bestLen {
			best, bestLen = &agents[i], len(name)
		}
	}
	if best != nil {
		return mention{target: best, body: strings.TrimSpace(rest[bestLen:])}
	}
	word := rest
	body := ""
	if i := strings.IndexByte(rest, ' '); i >= 0 {
		word, body = rest[:i], strings.TrimSpace(rest[i+1:])
	}
	word = strings.Trim(word, `"'`)
	if word == "" {
		return mention{missing: ""}
	}
	var hits []*fleet.Agent
	for i := range agents {
		if strings.HasPrefix(strings.ToLower(agents[i].Name), strings.ToLower(word)) {
			hits = append(hits, &agents[i])
		}
	}
	switch len(hits) {
	case 1:
		return mention{target: hits[0], body: body}
	case 0:
		return mention{missing: word}
	}
	var names []string
	for _, h := range hits {
		names = append(names, h.Name)
	}
	return mention{ambiguous: names, body: body}
}

// completions lists agents whose name starts with what follows the @ while
// the mention is still being typed. It returns nil once a full name is in.
func completions(text string, agents []fleet.Agent) []fleet.Agent {
	if !strings.HasPrefix(text, "@") {
		return nil
	}
	if resolveMention(text, agents).target != nil && strings.Contains(text, " ") {
		return nil
	}
	partial := strings.ToLower(strings.TrimPrefix(text, "@"))
	var out []fleet.Agent
	for _, a := range agents {
		if a.Status == fleet.Gone {
			continue
		}
		if strings.HasPrefix(strings.ToLower(a.Name), partial) {
			out = append(out, a)
		}
	}
	return out
}
