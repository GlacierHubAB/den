package names

import (
	"regexp"
	"strings"
)

// Making a name. A role and a title come from spawn_agent, so from the
// orchestrator, which agents can sway: they are untrusted text, and the
// name Den makes from them is a label, never an identity. Only Den adds a
// number ("backend-engineer2"); only Den writes a name.

// MaxLen and MinLen bound a name.
const (
	MinLen = 2
	MaxLen = 32
)

// Normalise is the name a role or a title gives: lower-case ASCII letters,
// digits and single hyphens, starting with a letter, MinLen to MaxLen
// characters. Every other ASCII run becomes one hyphen; every non-ASCII
// rune is dropped, never transliterated, so no lookalike letter or
// invisible character gets through. A trailing number is dropped too (only
// Den numbers), and a name past MaxLen is cut at its last hyphen within
// it. "" when nothing usable is left.
func Normalise(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
			r += 'a' - 'A'
			fallthrough
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		case r < 0x80:
			dash = true
		}
	}
	out := strings.TrimRight(b.String(), "0123456789-")
	if len(out) > MaxLen {
		out = out[:MaxLen]
		if i := strings.LastIndexByte(out, '-'); i >= MinLen {
			out = out[:i]
		}
		out = strings.TrimRight(out, "0123456789-")
	}
	if len(out) < MinLen || out[0] < 'a' || out[0] > 'z' {
		return ""
	}
	return out
}

// reservedTokens may not be any word of a name: names that read as Den,
// the human, the orchestrator, the console, the system, or everyone.
var reservedTokens = set("den", "orchestrator", "human", "you", "me", "system", "console", "dock", "drey", "api",
	"claude", "anthropic", "root", "admin", "user", "all", "everyone", "here")

// reservedNames may not be a whole name: these, and every word Den reads
// after an agent's name in a command, so that "/close all" never means an
// agent (CommandWords).
var reservedNames = set("the-human", "the-orchestrator")

// CommandWords are the words Den's commands take where an agent's name
// could stand: its verbs and their sub-commands. A test ranges over Den's
// command table to keep this list whole.
var CommandWords = set(
	"all", "none", "stop", "close", "keep", "here", "me", "mine", "every",
	"allow", "deny", "always", "answer", "api", "remote", "remotes", "hosts", "desktop", "open", "focus", "copy",
	"reopen", "protect", "unprotect", "flag", "unflag", "dismiss", "unread", "rescan", "map", "fleet", "inbox",
	"delete", "send", "merge", "cleanup", "review", "overview", "project", "mission", "template", "view", "pause",
	"resume", "god", "usage", "mode", "budget", "memory", "orchestrator", "help", "quit", "exit", "yes", "no",
	"off", "disconnect", "unpair", "lock", "disallow", "control", "retry", "idle", "force", "fork", "add", "drop",
	"move", "save", "rescue", "flat", "set", "clear", "rm", "rules", "new", "restart", "dontask", "plan", "default",
	"acceptedits", "auto", "on", "local",
)

// idLike is a name or word that reads as an id: eight or more hex digits
// (a session id's or a mission id's groups).
var idLike = regexp.MustCompile(`^[0-9a-f]{8,}$`)

// Reserved says why a normalised name may not be used, "" when it may.
func Reserved(name string) string {
	switch {
	case name == "":
		return "no usable letters"
	case reservedNames[name], CommandWords[name]:
		return "it is a word Den uses"
	case idLike.MatchString(strings.ReplaceAll(name, "-", "")):
		return "it reads as an id"
	}
	for _, w := range strings.Split(name, "-") {
		if reservedTokens[w] {
			return "it names " + w
		}
		if idLike.MatchString(w) {
			return "it reads as an id"
		}
	}
	return ""
}

// genericWords make a role too vague to name an agent by.
var genericWords = set("agent", "assistant", "ai", "bot", "claude", "coding", "coder", "dev", "developer",
	"engineer", "general", "helper", "worker", "task")

// Generic reports whether a role says nothing about the work: every word
// of it, normalised, is a generic word ("coding agent", "dev agent",
// "general engineer"). Such a role needs a title.
func Generic(role string) bool {
	n := Normalise(role)
	if n == "" {
		return true
	}
	for _, w := range strings.Split(n, "-") {
		if !genericWords[w] {
			return false
		}
	}
	return true
}

// Base is the name a spawn's role and title give, before Den numbers it:
// the role's, or the title's when the role is generic. "" with why when
// neither gives one Den may use; the agent then keeps its critter name.
func Base(role, title string) (base, why string) {
	from := role
	if Generic(role) {
		from = title
	}
	base = Normalise(from)
	if why = Reserved(base); why != "" {
		return "", why
	}
	return base, ""
}

func set(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}
