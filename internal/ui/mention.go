package ui

import (
	"errors"
	"strings"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/names"
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

// resolveMention finds the agent a message is addressed to, through
// names.Resolve. A whole name at the start wins ("@Mossy Lemur rebase…"),
// the longest one that fits, and a peer handle or an alias counts as a
// name; otherwise a single word is matched as a case-insensitive prefix of
// a name ("@mossy rebase…"). Either may carry a host ("@Mossy
// Lemur@devbox …", "@mossy@devbox …"); without one it names an agent on
// this machine, and an agent elsewhere needs its host. A mention that fits
// more than one agent is ambiguous: none is picked. Text without a leading
// @ has no target.
func resolveMention(text string, agents []fleet.Agent) mention {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "@") {
		return mention{body: text}
	}
	rest := strings.TrimPrefix(text, "@")
	lower := strings.ToLower(rest)
	cands := fleet.Candidates(agents)
	best := 0
	for _, c := range cands {
		for _, n := range []string{c.Name, c.Alias, c.Peer} {
			n = strings.ToLower(n)
			if n == "" || len(n) <= best || !strings.HasPrefix(lower, n) {
				continue
			}
			if len(lower) != len(n) && lower[len(n)] != ' ' && lower[len(n)] != '@' {
				continue
			}
			best = len(n)
		}
	}
	query, body, opts := "", "", names.Opts{}
	if best > 0 {
		query = rest[:best]
		after := rest[best:]
		if host, b, ok := hostQualifier(after); ok {
			query, body = query+"@"+host, b
		} else {
			body = strings.TrimSpace(after)
		}
	} else {
		query = rest
		if i := strings.IndexByte(rest, ' '); i >= 0 {
			query, body = rest[:i], strings.TrimSpace(rest[i+1:])
		}
		query = strings.Trim(query, `"'`)
		opts.Prefix = true
		if strings.TrimSuffix(query, "@") == "" || strings.HasPrefix(query, "@") {
			return mention{missing: ""}
		}
	}
	c, err := names.Resolve(query, cands, opts)
	var amb *names.Ambiguous
	var elsewhere *names.NeedsHost
	switch {
	case err == nil:
		for i := range agents {
			if agents[i].Session.SessionID == c.ID && agents[i].Host == c.Host {
				return mention{target: &agents[i], body: body}
			}
		}
		return mention{missing: query}
	case errors.As(err, &amb):
		return mention{ambiguous: names.Labels(amb.Candidates), body: body}
	case errors.As(err, &elsewhere):
		return mention{ambiguous: names.Labels(elsewhere.Candidates), body: body}
	}
	return mention{missing: query}
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
