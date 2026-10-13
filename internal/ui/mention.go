package ui

import (
	"strings"

	"github.com/GlacierHubAB/den/internal/fleet"
)

// A remote agent is mentioned with its host after the name, "@Mossy
// Lemur@devbox"; the same name on two hosts needs it. A local agent is
// "@Mossy Lemur", or "@Mossy Lemur@local" when a remote one has that name.

// mention is the result of reading "@Name text".
type mention struct {
	target    *fleet.Agent
	body      string
	ambiguous []string // names that all match a partial mention
	missing   string   // the partial that matched nobody
}

// resolveMention finds the agent a message is addressed to. A full name at
// the start wins ("@Mossy Lemur rebase…"); otherwise a single word is matched
// as a case-insensitive prefix of a name ("@mossy rebase…"). Either may carry
// a host ("@Mossy Lemur@devbox …", "@mossy@devbox …"); a name that matches
// agents on more than one host is ambiguous until it does. Text without a
// leading @ has no target.
func resolveMention(text string, agents []fleet.Agent) mention {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "@") {
		return mention{body: text}
	}
	// An agent on this machine always wins an unqualified mention: one on a
	// host is reached by writing the host, or when nothing local fits.
	var local []fleet.Agent
	for _, a := range agents {
		if a.Host == "" {
			local = append(local, a)
		}
	}
	if len(local) < len(agents) {
		if mt := mentionAmong(text, local); mt.target != nil || len(mt.ambiguous) > 0 {
			return mt
		}
	}
	return mentionAmong(text, agents)
}

func mentionAmong(text string, agents []fleet.Agent) mention {
	rest := strings.TrimPrefix(text, "@")
	lower := strings.ToLower(rest)
	var best []*fleet.Agent
	bestLen := 0
	for i := range agents {
		name := strings.ToLower(agents[i].Name)
		if name == "" || !strings.HasPrefix(lower, name) || len(name) < bestLen {
			continue
		}
		if len(lower) != len(name) && lower[len(name)] != ' ' && lower[len(name)] != '@' {
			continue
		}
		if len(name) > bestLen {
			best, bestLen = nil, len(name)
		}
		best = append(best, &agents[i])
	}
	if len(best) > 0 {
		after := rest[bestLen:]
		if host, body, ok := hostQualifier(after); ok {
			best = onHost(best, host)
			if len(best) == 0 {
				return mention{missing: rest[:bestLen] + "@" + host}
			}
			return pick(best, body)
		}
		return pick(best, strings.TrimSpace(after))
	}
	word := rest
	body := ""
	if i := strings.IndexByte(rest, ' '); i >= 0 {
		word, body = rest[:i], strings.TrimSpace(rest[i+1:])
	}
	word = strings.Trim(word, `"'`)
	host, qualified := "", false
	if i := strings.LastIndexByte(word, '@'); i >= 0 {
		word, host, qualified = word[:i], word[i+1:], true
	}
	if word == "" {
		return mention{missing: ""}
	}
	var hits []*fleet.Agent
	for i := range agents {
		if strings.HasPrefix(strings.ToLower(agents[i].Name), strings.ToLower(word)) {
			hits = append(hits, &agents[i])
		}
	}
	if qualified {
		if hits = onHost(hits, host); len(hits) == 0 {
			return mention{missing: word + "@" + host}
		}
	}
	if len(hits) == 0 {
		return mention{missing: word}
	}
	return pick(hits, body)
}

// pick is the one agent of hits, or the ambiguity: hits on several hosts
// are listed with theirs.
func pick(hits []*fleet.Agent, body string) mention {
	if len(hits) == 1 {
		return mention{target: hits[0], body: body}
	}
	multi := false
	for _, h := range hits {
		multi = multi || h.Host != "" || h.Name != hits[0].Name
	}
	var names []string
	for _, h := range hits {
		n := h.Name
		if h.Host != "" || (multi && h.Name == hits[0].Name) {
			n += "@" + fleet.HostName(h.Host)
		}
		names = append(names, n)
	}
	return mention{ambiguous: names, body: body}
}

// hostQualifier reads "@devbox rest…" after a name: the host and the body.
func hostQualifier(after string) (host, body string, ok bool) {
	rest, found := strings.CutPrefix(after, "@")
	if !found {
		return "", "", false
	}
	host, body, _ = strings.Cut(rest, " ")
	return strings.ToLower(host), strings.TrimSpace(body), true
}

// onHost keeps the agents on host ("local" or "" is this machine).
func onHost(as []*fleet.Agent, host string) []*fleet.Agent {
	if host == "local" {
		host = ""
	}
	var out []*fleet.Agent
	for _, a := range as {
		if strings.EqualFold(a.Host, host) {
			out = append(out, a)
		}
	}
	return out
}

// mentionText is what completing a mention inserts: the name, with the
// host after it for a remote agent.
func mentionText(a fleet.Agent) string {
	if a.Host != "" {
		return "@" + a.Name + "@" + a.Host
	}
	return "@" + a.Name
}

// mentionLabel is how a completion row names an agent: "Mossy Lemur
// @devbox" for a remote one.
func mentionLabel(a fleet.Agent) string { return a.Name + hostChip(a) }

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
		if strings.HasPrefix(strings.ToLower(a.Name), partial) || strings.HasPrefix(strings.ToLower(mentionText(a)[1:]), partial) {
			out = append(out, a)
		}
	}
	return out
}
