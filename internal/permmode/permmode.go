// Package permmode ranks the permission modes Den starts agents in, by
// what an agent may do without asking the human. Claude Code's selector
// offers default, acceptEdits, plan, auto and dontAsk. Den ranks them as
// Claude Code 2.1.289 and 2.1.296 behave:
//
//		dontAsk < default < plan, acceptEdits < auto
//
//	  - dontAsk denies whatever was not allowed in advance, so it sits at the
//	    bottom, not the top.
//	  - default asks before edits, commands and MCP tools.
//	  - plan asks before edits and MCP tools, but runs any Bash command
//	    Claude Code's safety classifier allows (it is not read-only), where
//	    default would ask: so plan is above default, not below it.
//	  - acceptEdits makes edits in the working folder without asking.
//	  - plan and acceptEdits are not ranked against each other: each runs
//	    without asking something the other asks for.
//	  - auto asks for nothing; the classifier decides the rest.
//
// bypassPermissions is not ranked: only God mode starts an agent in it,
// and no setting of the global mode may name it.
//
// A change to a mode below is a lowering, which Den applies at once. Every
// other change, up, between plan and acceptEdits, or to or from anything
// unranked, is a raise, which waits for the human's ^Y.
package permmode

import "slices"

// The modes, as claude --permission-mode names them.
const (
	DontAsk     = "dontAsk"
	Plan        = "plan"
	Default     = "default"
	AcceptEdits = "acceptEdits"
	Auto        = "auto"
	// Bypass is God mode's, never the global mode's.
	Bypass = "bypassPermissions"
)

// Shipped is the global mode Den starts with when config.toml names none:
// auto, which is what Claude Code runs with no flag, so agents started
// before the global mode existed ran in it too. It is also the most
// config.toml may grant at Den's start: the file only ever asks.
const Shipped = Auto

// ranked are the modes the global mode may take, most restrictive first.
var ranked = []string{DontAsk, Default, Plan, AcceptEdits, Auto}

// below lists, for each ranked mode, every mode strictly below it.
var below = map[string][]string{
	DontAsk:     nil,
	Default:     {DontAsk},
	Plan:        {DontAsk, Default},
	AcceptEdits: {DontAsk, Default},
	Auto:        {DontAsk, Default, Plan, AcceptEdits},
}

// Modes are the modes the global mode may take, most restrictive first.
func Modes() []string {
	return append([]string(nil), ranked...)
}

// Valid reports whether mode may be the global mode: a ranked one,
// spelled exactly as Claude Code spells it.
func Valid(mode string) bool {
	_, ok := below[mode]
	return ok
}

// Lowers reports whether going from one mode to another is a lowering:
// both ranked, and to strictly below from. No change is not a lowering,
// nor is a change between plan and acceptEdits, nor any change that
// involves an unranked mode.
func Lowers(from, to string) bool {
	return slices.Contains(below[from], to)
}

// AsksFirst reports whether an agent in the mode asks the human before it
// acts on anything beyond reading: only default. dontAsk does not ask (it
// runs what was allowed in advance and denies the rest), acceptEdits edits
// without asking, plan and auto run what the classifier allows, and Den
// knows nothing of any other mode. This is the mode in which Den sees
// what a message makes an agent do (standing allowances).
func AsksFirst(mode string) bool {
	return mode == Default
}

// Raises reports whether the change needs the human's ^Y: any change that
// is not a lowering.
func Raises(from, to string) bool {
	return from != to && !Lowers(from, to)
}

// Lower is the most permissive mode at or below both: the lower of the two
// when they are ranked against each other, else default (for plan and
// acceptEdits). An unranked mode counts as default, so the result is
// always ranked.
func Lower(a, b string) string {
	if !Valid(a) {
		a = Default
	}
	if !Valid(b) {
		b = Default
	}
	switch {
	case a == b, Lowers(b, a):
		return a
	case Lowers(a, b):
		return b
	}
	return Default
}

// Parse reads a mode as the human may type it: case aside, and with the
// usual spellings of each (accept-edits, dont-ask). It returns the mode
// as Claude Code spells it, and false for anything else, bypass included.
func Parse(s string) (string, bool) {
	k := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '-' || c == '_':
			continue
		case 'A' <= c && c <= 'Z':
			c += 'a' - 'A'
		}
		k = append(k, c)
	}
	switch string(k) {
	case "dontask":
		return DontAsk, true
	case "plan":
		return Plan, true
	case "default":
		return Default, true
	case "acceptedits":
		return AcceptEdits, true
	case "auto":
		return Auto, true
	}
	return "", false
}

// Allows says in a line what an agent started in the mode does without
// asking, for the card that raises it and for /mode.
func Allows(mode string) string {
	switch mode {
	case DontAsk:
		return "runs only the tools allowed in advance and denies the rest without asking"
	case Plan:
		return "reads and plans; asks before edits and MCP tools, but runs any command Claude Code's classifier allows"
	case Default:
		return "reads without asking; asks before it edits a file or runs a command"
	case AcceptEdits:
		return "edits files in its working folder without asking; asks before commands, MCP tools and edits elsewhere"
	case Auto:
		return "never asks: Claude Code's classifier allows or denies everything it does not run outright"
	case Bypass:
		return "acts without asking, for everything"
	}
	return "unknown"
}
