package ui

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

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
	lower := names.Fold(rest)
	cands := fleet.Candidates(agents)
	// The longest whole name that starts the text, on this machine first:
	// a longer name on a host never captures a local agent's mention.
	longest := func(local bool) int {
		best := 0
		for _, c := range cands {
			if (c.Host == "") != local {
				continue
			}
			for _, n := range []string{c.Name, c.Alias, c.Peer} {
				n = names.Fold(n)
				if n == "" || len(n) <= best || !strings.HasPrefix(lower, n) {
					continue
				}
				if !boundaryAfter(lower, len(n)) {
					continue
				}
				best = len(n)
			}
		}
		return best
	}
	best := longest(true)
	if best == 0 {
		best = longest(false)
	}
	query, body, opts := "", "", names.Opts{}
	if best > 0 {
		query = rest[:best]
		after := rest[best:]
		if host, b, ok := hostQualifier(after); ok {
			query, body = query+"@"+host, b
		} else {
			body = mentionBody(after)
		}
	} else {
		query, body = mentionWord(rest)
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
// A host ends at white space or sentence punctuation, but a dot inside it
// is its own ("@devbox.lan, rebase"); dots at its end are not.
func hostQualifier(after string) (host, body string, ok bool) {
	rest, found := strings.CutPrefix(after, "@")
	if !found {
		return "", "", false
	}
	end := strings.IndexFunc(rest, func(r rune) bool { return r != '.' && mentionEndRune(r) })
	if end < 0 {
		end = len(rest)
	}
	host = strings.TrimRight(rest[:end], ".")
	return names.Fold(host), mentionBody(rest[len(host):]), true
}

// mentionWord reads a one-word mention, "mossy, rebase…" or
// "mossy@devbox: rebase…": the word (with its host) and the body. The word
// ends where a mention ends (mentionEndRune).
func mentionWord(rest string) (query, body string) {
	i := strings.IndexFunc(rest, mentionEndRune)
	if i < 0 {
		return rest, ""
	}
	if host, b, ok := hostQualifier(rest[i:]); ok {
		return rest[:i] + "@" + host, b
	}
	return rest[:i], mentionBody(rest[i:])
}

// mentionBody is the message after a mention: punctuation that ended the
// name ("@Name, rebase", "@Name: rebase") is not part of it when white
// space or the end follows; otherwise it is the message's own ("@Name's
// branch").
func mentionBody(after string) string {
	rest := strings.TrimLeftFunc(after, func(r rune) bool { return strings.ContainsRune(mentionPunct, r) })
	if r, _ := utf8.DecodeRuneInString(rest); rest == "" || unicode.IsSpace(r) {
		after = rest
	}
	return strings.TrimSpace(after)
}

// mentionText is what completing a mention inserts: the name, with the
// host after it for a remote agent.
func mentionText(a fleet.Agent) string {
	if a.Host != "" {
		return "@" + a.Name + "@" + a.Host
	}
	return "@" + a.Name
}

// shownName is how a row names an agent: "@name" for the name Den gave
// it, the critter name as it is otherwise.
func shownName(a fleet.Agent) string {
	if a.Alias != "" {
		return "@" + a.Name
	}
	return a.Name
}

// cutName cuts a name to w columns, keeping a trailing number, so that
// two agents of one role stay apart: "@technical-wri…2".
func cutName(name string, w int) string {
	if w <= 0 || ansi.StringWidth(name) <= w {
		return name
	}
	num := name[strings.LastIndexFunc(name, func(r rune) bool { return r < '0' || r > '9' })+1:]
	if num == "" || len(num)+2 > w {
		return ansi.Truncate(name, w, "…")
	}
	return ansi.Truncate(strings.TrimSuffix(name, num), w-len(num), "…") + num
}

// roleShown reports whether a row shows the agent's role beside its name:
// not when the name already says it.
func roleShown(a fleet.Agent) bool {
	r := names.Normalise(a.Labels.Role)
	return a.Labels.Role != "" && (a.Alias == "" || r == "" || !strings.HasPrefix(a.Name, r))
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
	partial := names.Fold(strings.TrimPrefix(text, "@"))
	var out []fleet.Agent
	for _, a := range agents {
		if a.Status == fleet.Gone {
			continue
		}
		if strings.HasPrefix(names.Fold(a.Name), partial) || strings.HasPrefix(names.Fold(mentionText(a)[1:]), partial) ||
			a.Alias != "" && strings.HasPrefix(names.Fold(a.Alias), partial) {
			out = append(out, a)
		}
	}
	return out
}
