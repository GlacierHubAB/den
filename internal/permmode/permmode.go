// Package permmode ranks the permission modes Den starts agents in, by
// what an agent may do without asking the human. Claude Code's selector
// offers default, acceptEdits, plan, auto and dontAsk; Den ranks them on
// one line:
//
//	dontAsk < plan < default < acceptEdits < auto
//
// dontAsk denies whatever was not allowed in advance, so it sits at the
// bottom, not the top. bypassPermissions is not on the line: only God mode
// starts an agent in it, and no setting of the global mode may name it.
//
// A change down the line is a lowering, which Den applies at once. Every
// other change, up the line or to or from anything off it, is a raise,
// which waits for the human's ^Y.
//
// auto's place at the top is provisional until the offline mode spike on
// Claude Code 2.1.289 and 2.1.296 confirms it.
package permmode

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

// line is the ranked line, most restrictive first.
var line = []string{DontAsk, Plan, Default, AcceptEdits, Auto}

// Modes are the modes the global mode may take, most restrictive first.
func Modes() []string {
	return append([]string(nil), line...)
}

// Rank is the mode's place on the line, 0 for the most restrictive, and
// false for a mode not on it.
func Rank(mode string) (int, bool) {
	for i, m := range line {
		if m == mode {
			return i, true
		}
	}
	return 0, false
}

// Valid reports whether mode may be the global mode: one on the line,
// spelled exactly as Claude Code spells it.
func Valid(mode string) bool {
	_, ok := Rank(mode)
	return ok
}

// Lowers reports whether going from one mode to another is a lowering:
// both on the line, and to strictly below from. No change is not a
// lowering, and neither is any change that involves a mode off the line.
func Lowers(from, to string) bool {
	f, okf := Rank(from)
	t, okt := Rank(to)
	return okf && okt && t < f
}

// Raises reports whether the change needs the human's ^Y: any change that
// is not a lowering.
func Raises(from, to string) bool {
	return from != to && !Lowers(from, to)
}

// Lower is the more restrictive of two modes on the line; a mode off it
// counts as Default, so the result is always on the line.
func Lower(a, b string) string {
	if !Valid(a) {
		a = Default
	}
	if !Valid(b) {
		b = Default
	}
	if Lowers(a, b) {
		return b
	}
	return a
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
		return "reads and plans; it edits and runs nothing until you approve its plan"
	case Default:
		return "reads without asking; asks before it edits a file or runs a command"
	case AcceptEdits:
		return "edits files without asking; asks before it runs other commands"
	case Auto:
		return "acts without asking wherever Claude Code's classifier judges it safe; asks only for what it flags"
	case Bypass:
		return "acts without asking, for everything"
	}
	return "unknown"
}
