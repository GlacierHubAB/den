package remote

import (
	"math"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/transcript"
)

// Bounds on the figures a host sends per agent. They keep a host from
// putting absurd numbers in Den's cost views and totals, and from growing
// the file Den keeps of it.
const (
	// maxSessionTokens caps one session's tokens, all models, its own and
	// its subagents', cached or not. A Claude Code call reads at most its
	// context window (1M tokens at most today) and takes seconds, so ten
	// billion is some ten thousand calls each with a full 1M context: days
	// of non-stop work at the largest window, and tens of thousands of
	// dollars at any listed price. Real sessions stay far below it; a count
	// above it, or below zero, is a host's error or a lie, not a cost.
	maxSessionTokens = 10_000_000_000
	// maxReportedUSD caps Claude Code's own cost figure for one session, by
	// the same reasoning: well above what the token ceiling could cost at
	// cache-read prices, far below a figure that would swamp every total.
	maxReportedUSD = 100_000
	// maxModels caps the models one agent's tokens are kept for, per map;
	// a session uses a handful.
	maxModels = 32
	// maxModelKey caps a model id used as a key. Model ids are short
	// ("claude-opus-4-5-20251101", "us.anthropic.claude-…-v1:0").
	maxModelKey = 128
)

// checkAgents is what Den keeps of a snapshot's agents: each one checked
// (checkMeta), rechecked against the host's policy and the one it applies
// (overreach when it sent what they do not allow) and sanitised. flags says
// what was dropped, once per kind.
func checkAgents(in []fleet.Meta, h Host, applied Policy) (agents []fleet.Meta, flags []string, overreach bool) {
	agents = make([]fleet.Meta, 0, len(in))
	seen := map[string]bool{}
	for _, a := range in {
		a, why, ok := checkMeta(a)
		for _, f := range why {
			if !seen[f] {
				seen[f] = true
				flags = append(flags, f)
			}
		}
		if !ok {
			continue
		}
		a, dropped := recheck(a, h, applied)
		overreach = overreach || dropped
		agents = append(agents, cleanMeta(a))
	}
	return agents, flags, overreach
}

// checkMeta checks one agent from a host. ok is false when it must be
// dropped whole: its session id is not a UUID. Otherwise figures that are
// not plausible are cleared, never shown or summed: tokens out of bounds
// (the agent then shows no cost), a reported cost out of bounds, a model
// id with anything but letters, digits and . _ : -, or more models than a
// session uses. why says what was dropped.
func checkMeta(a fleet.Meta) (_ fleet.Meta, why []string, ok bool) {
	if !isSessionID(a.Session) {
		return a, []string{"an agent whose session id is not a UUID"}, false
	}
	var badKey, tooMany bool
	a.Tokens, badKey, tooMany = checkModels(a.Tokens)
	var bk, tm bool
	a.Sub, bk, tm = checkModels(a.Sub)
	if badKey || bk {
		why = append(why, "a model name with characters Den does not accept")
	}
	if tooMany || tm {
		why = append(why, "tokens for more models than a session uses")
	}
	if !plausible(a.Tokens, a.Sub) {
		a.Tokens, a.Sub = nil, nil
		why = append(why, "implausible token counts")
	}
	if r := a.Reported; r != nil && (math.IsNaN(*r) || *r < 0 || *r > maxReportedUSD) {
		a.Reported, a.Unpriced = nil, false
		why = append(why, "an implausible reported cost")
	}
	return a, why, true
}

// checkModels keeps the entries of a tokens-by-model map whose key is a
// model id Den accepts; more than maxModels entries drop the whole map.
func checkModels(in map[string]transcript.Tokens) (out map[string]transcript.Tokens, badKey, tooMany bool) {
	if len(in) > maxModels {
		return nil, false, true
	}
	for k, v := range in {
		if !modelKey(k) {
			badKey = true
			continue
		}
		if out == nil {
			out = map[string]transcript.Tokens{}
		}
		out[k] = v
	}
	return out, badKey, false
}

// modelKey reports whether s may be kept as a model id: 1 to maxModelKey
// characters, each a letter, a digit or one of . _ : -.
func modelKey(s string) bool {
	if s == "" || len(s) > maxModelKey {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == ':' || c == '-':
		default:
			return false
		}
	}
	return true
}

// plausible reports whether a session's tokens are within bounds: no count
// below zero, and all of them together at most maxSessionTokens.
func plausible(maps ...map[string]transcript.Tokens) bool {
	var total int64
	for _, m := range maps {
		for _, t := range m {
			for _, n := range []int64{t.Input, t.Output, t.CacheCreation, t.CacheRead} {
				if n < 0 || n > maxSessionTokens {
					return false
				}
				total += n // each n and the total so far are within bounds: no overflow
				if total > maxSessionTokens {
					return false
				}
			}
		}
	}
	return true
}

// Flag marks a host for something Den noticed beyond a single message,
// such as two hosts sending the same session id. It shows in /remote with
// the host's other flags.
func (m *Manager) Flag(label, what string) {
	for _, h := range m.hosts {
		if h.Label == label {
			m.flag(h, what)
		}
	}
}
