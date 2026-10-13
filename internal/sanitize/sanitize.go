// Package sanitize makes text from outside Den safe to print to a terminal,
// pass to notify-send, put in a window title or write to a log.
//
// Agents, hook payloads, transcripts, git and (later) remote hosts all feed
// Den strings it does not control. Printed raw, such a string could write the
// clipboard (OSC 52), move the cursor, clear the screen, retitle the window,
// plant a hyperlink (OSC 8) or reorder what the human reads with bidi
// overrides. Text removes all of that and keeps ordinary Unicode (CJK, emoji,
// combining marks, ZWJ sequences) intact.
//
// What is removed:
//   - CSI (ESC [ … final byte), OSC (ESC ] … BEL or ST), DCS, SOS, PM and APC
//     (ESC P, X, ^, _ … ST), each with its payload; the 8-bit C1 forms
//     U+009B, U+009D, U+0090, U+0098, U+009E and U+009F likewise. A string
//     sequence that never ends is dropped to the end of the input. CAN and
//     SUB end any sequence, as they do in a terminal, and BEL ends any
//     string: where a terminal would read on, Den shows the rest as plain
//     text, which is harmless.
//   - Every other escape: two-byte ESC sequences, nF sequences such as
//     ESC ( B, and a lone ESC.
//   - C0 controls other than \n and \t, DEL, and all C1 (U+0080–U+009F).
//   - Bidi controls: U+202A–U+202E, U+2066–U+2069, U+200E, U+200F, U+061C.
//   - Invisible characters that can carry text a reader does not see (an
//     LLM reads them all the same) or make two strings look alike: every
//     Unicode format character (category Cf: tag characters U+E0000–E007F,
//     zero-width space and non-joiner, word joiner and invisible operators
//     U+2060–U+2064, U+FEFF, soft hyphen U+00AD, U+180E and the rest), the
//     combining grapheme joiner U+034F, the Hangul fillers U+115F, U+1160,
//     U+3164 and U+FFA0, and variation selectors U+FE00–U+FE0D and
//     U+E0100–U+E01EF. Some are kept where they do what they are for, one
//     at a time: a zero-width joiner between two emoji (a family); a joiner
//     or non-joiner between two letters of a script that spells with them
//     (Arabic and Persian, Syriac, N'Ko, the Indic scripts, Sinhala,
//     Mongolian, Khmer, Myanmar); U+FE0E or U+FE0F right after the
//     character it styles. Between Latin letters, or in a run, they go:
//     that is how text is hidden in plain sight.
//     Tag sequences (subdivision flags) lose their tags and show as a plain
//     black flag.
//   - Invalid UTF-8, byte by byte. It is dropped rather than replaced with
//     U+FFFD, so a stray byte cannot change the width of what is shown.
//
// \r\n and a lone \r become \n, so a carriage return cannot overwrite a line.
// U+2028 and U+2029 (line and paragraph separator) become \n too: some
// terminals and fonts break a line there and some show nothing, so a name
// or a line of code could look shorter, or other, than it is. Line and Code
// then make that newline a space, like any other.
//
// Every function runs in one pass, linear in the input, and is idempotent:
// Text(Text(s)) == Text(s). Den's own styling (lipgloss) is added after
// sanitising; never sanitise Den's styled output, it would lose its colours.
package sanitize

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	esc = 0x1b
	bel = 0x07
	can = 0x18
	sub = 0x1a
	del = 0x7f
	st  = 0x9c // the C1 string terminator, as a rune
)

// Text returns s with escape sequences, control characters other than \n and
// \t, bidi controls and invalid UTF-8 removed, and \r\n, \r, U+2028 and
// U+2029 turned into \n.
// It is for text that may span lines: a command, a plan, a message body.
func Text(s string) string {
	return text(s, false)
}

// Styled is Text that keeps SGR sequences (colours, bold, underline: ESC [
// digits, ';' and ':' only, then 'm'), so it can run over a frame Den has
// already styled. It is a last line of defence at the renderer; outside text
// should already have been through Text or Line where it came in.
func Styled(s string) string {
	return text(s, true)
}

func text(s string, sgr bool) string {
	if clean(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	// last is the last rune written, for what a joiner or a variation
	// selector may follow; base is the last one that is neither.
	var last, base rune
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == esc:
			if sgr {
				if n := sgrLen(s[i:]); n > 0 {
					b.WriteString(s[i : i+n])
					i += n
					continue
				}
			}
			i = skipEscape(s, i+1)
			continue
		case c == '\r':
			b.WriteByte('\n')
			i++
			if i < len(s) && s[i] == '\n' {
				i++
			}
			continue
		case c == '\n' || c == '\t':
			b.WriteByte(c)
			last, base = rune(c), rune(c)
			i++
			continue
		case c < 0x20 || c == del:
			i++
			continue
		case c < utf8.RuneSelf:
			b.WriteByte(c)
			last, base = rune(c), rune(c)
			i++
			continue
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		i += n
		switch {
		case r == utf8.RuneError && n == 1:
			// Invalid UTF-8: drop the byte.
		case r == 0x9b:
			i = skipCSI(s, i)
		case r == 0x9d || r == 0x90 || r == 0x98 || r == 0x9e || r == 0x9f:
			i = skipString(s, i)
		case r >= 0x80 && r <= 0x9f:
			// Any other C1 control.
		case bidi(r):
		case r == 0x2028 || r == 0x2029:
			b.WriteByte('\n')
			last, base = '\n', '\n'
		case r == zwj || r == zwnj:
			next, _ := utf8.DecodeRuneInString(s[i:])
			if joins(r, base, last, next) {
				b.WriteString(s[i-n : i])
				last = r
			}
		case r == vs15 || r == vs16:
			if last != 0 && last == base && !invisible(last) {
				b.WriteString(s[i-n : i])
				last = r
			}
		case invisible(r):
		default:
			b.WriteString(s[i-n : i])
			last, base = r, r
		}
	}
	return b.String()
}

const (
	zwnj = 0x200c // zero-width non-joiner
	zwj  = 0x200d // zero-width joiner
	vs15 = 0xfe0e // text presentation
	vs16 = 0xfe0f // emoji presentation
)

// invisible reports whether r is an invisible character Text removes
// (see the package comment). Bidi controls, the joiner and U+FE0E/U+FE0F
// are handled where Text meets them.
func invisible(r rune) bool {
	switch {
	case unicode.Is(unicode.Cf, r):
		return true
	case r == 0x034f, r == 0x115f, r == 0x1160, r == 0x3164, r == 0xffa0:
		return true
	case r >= 0xfe00 && r <= 0xfe0f, r >= 0xe0100 && r <= 0xe01ef:
		return true
	}
	return false
}

// joins reports whether the joiner or non-joiner j, met after base (the
// last visible rune written; last is the last rune written at all) and
// before next, is kept.
func joins(j, base, last, next rune) bool {
	if last != base && !(j == zwj && last == vs16) {
		return false // one joiner at a time, right after what it joins
	}
	if j == zwj && emoji(base) && emoji(next) {
		return true
	}
	return joinerScript(base) && joinerScript(next)
}

// joinerScripts spell with the zero-width joiner and non-joiner.
var joinerScripts = []*unicode.RangeTable{
	unicode.Arabic, unicode.Syriac, unicode.Nko, unicode.Devanagari, unicode.Bengali, unicode.Gurmukhi,
	unicode.Gujarati, unicode.Oriya, unicode.Tamil, unicode.Telugu, unicode.Kannada, unicode.Malayalam,
	unicode.Sinhala, unicode.Mongolian, unicode.Khmer, unicode.Myanmar,
}

// joinerScript reports whether r is a letter or mark of a joiner script.
func joinerScript(r rune) bool {
	return (unicode.IsLetter(r) || unicode.IsMark(r)) && unicode.In(r, joinerScripts...)
}

// emoji reports whether r is a character a zero-width joiner may join:
// pictographs, symbols and dingbats, as near as a range check gets.
func emoji(r rune) bool {
	switch {
	case r >= 0x1f000 && r <= 0x1faff:
		return true
	case r >= 0x2300 && r <= 0x23ff, r >= 0x2600 && r <= 0x27bf, r >= 0x2b00 && r <= 0x2bff:
		return true
	}
	return r == 0x00a9 || r == 0x00ae || r == 0x203c || r == 0x2049 || r == 0x2122 || r == 0x2139
}

// Line is Text for a single line: a name, a title, a label, a one-line
// summary. Newlines and tabs become spaces, runs of spaces collapse to one,
// and the ends are trimmed.
func Line(s string) string {
	t := Text(s)
	var b strings.Builder
	b.Grow(len(t))
	space := false
	for i := 0; i < len(t); i++ {
		c := t[i]
		if c == ' ' || c == '\n' || c == '\t' {
			space = b.Len() > 0
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteByte(c)
	}
	return b.String()
}

// Hidden finds the first character in s that Text would remove or change,
// with none of the exceptions Text makes: a control character other than
// \n and \t (\r included), a bidi control, an invisible character (a
// joiner too), U+2028, U+2029 or invalid UTF-8 (reported as
// utf8.RuneError). It is for text Den refuses rather than cleans, such as
// guidance it would put in a prompt as written.
func Hidden(s string) (rune, bool) {
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && n <= 1,
			r < 0x20 && r != '\n' && r != '\t',
			r >= del && r <= 0x9f,
			bidi(r), invisible(r), r == 0x2028, r == 0x2029:
			return r, true
		}
		i += n
	}
	return 0, false
}

// Code is Text for one line of code or a path, where spacing matters: it
// keeps spaces and tabs as they are, and a newline or carriage return
// becomes a space, so the line stays one line.
func Code(s string) string {
	t := Text(s)
	if strings.IndexByte(t, '\n') < 0 {
		return t
	}
	return strings.ReplaceAll(t, "\n", " ")
}

// Label is Line capped at max runes. A cut label ends in "…", which counts
// toward max. A max below 1 means no cap.
func Label(s string, max int) string {
	l := Line(s)
	if max < 1 || utf8.RuneCountInString(l) <= max {
		return l
	}
	n := 0
	for i := range l {
		if n == max-1 {
			return strings.TrimRight(l[:i], " ") + "…"
		}
		n++
	}
	return l
}

// JSON makes encoded JSON safe to print without changing what it decodes
// to. encoding/json already escapes C0 controls; JSON spells DEL, C1
// controls, bidi controls and U+2028 and U+2029 out as \uXXXX escapes too, and invalid UTF-8 as
// \ufffd, which is how encoding/json decodes it anyway. Outside strings
// valid JSON is ASCII, so b must be valid JSON for the result to be.
func JSON(b []byte) []byte {
	ok := true
	for _, c := range b {
		if c >= del {
			ok = false
			break
		}
	}
	if ok {
		return b
	}
	out := make([]byte, 0, len(b)+16)
	for i := 0; i < len(b); {
		c := b[i]
		if c < del {
			out = append(out, c)
			i++
			continue
		}
		r, n := utf8.DecodeRune(b[i:])
		switch {
		case r == utf8.RuneError && n == 1:
			out = append(out, `\ufffd`...)
		case r == del || (r >= 0x80 && r <= 0x9f) || bidi(r) || r == 0x2028 || r == 0x2029:
			out = fmt.Appendf(out, `\u%04x`, r)
		default:
			out = append(out, b[i:i+n]...)
		}
		i += n
	}
	return out
}

// clean reports whether s is printable ASCII, \n and \t only, the common
// case, which Text returns unchanged without copying.
func clean(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 0x20 && c != '\n' && c != '\t') || c >= del {
			return false
		}
	}
	return true
}

// sgrLen returns the length of the SGR sequence s starts with, or 0.
func sgrLen(s string) int {
	if len(s) < 3 || s[0] != esc || s[1] != '[' {
		return 0
	}
	for i := 2; i < len(s); i++ {
		switch c := s[i]; {
		case c == 'm':
			return i + 1
		case c >= '0' && c <= '9', c == ';', c == ':':
		default:
			return 0
		}
	}
	return 0
}

// skipEscape skips the escape sequence whose ESC was just before s[i] and
// returns the index after it.
func skipEscape(s string, i int) int {
	if i >= len(s) {
		return i
	}
	switch c := s[i]; {
	case c == '[':
		return skipCSI(s, i+1)
	case c == ']' || c == 'P' || c == 'X' || c == '^' || c == '_':
		return skipString(s, i+1)
	case c >= 0x20 && c <= 0x2f:
		// nF: intermediates, then one final byte.
		for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
			i++
		}
		if i < len(s) && s[i] >= 0x30 && s[i] <= 0x7e {
			i++
		}
		return i
	case c >= 0x30 && c <= 0x7e:
		return i + 1 // a two-byte sequence: ESC 7, ESC c, ESC =, …
	}
	// A lone ESC: drop it and read on from the next byte.
	return i
}

// skipCSI skips a control sequence's parameters, intermediates and final
// byte, starting just after its introducer. Like a terminal, it stops at the
// first byte that cannot belong to one, which Text then reads normally.
func skipCSI(s string, i int) int {
	for i < len(s) {
		c := s[i]
		switch {
		case c >= 0x40 && c <= 0x7e:
			return i + 1
		case c >= 0x20 && c <= 0x3f:
			i++
		case c == can || c == sub:
			return i + 1
		default:
			return i
		}
	}
	return i
}

// skipString skips the payload of an OSC, DCS, SOS, PM or APC string,
// starting just after its introducer, through its terminator: BEL, ESC \,
// U+009C, CAN or SUB. ESC followed by anything else ends the string too and
// is left for Text to read as a new sequence. A string with no terminator
// runs to the end of s.
func skipString(s string, i int) int {
	for i < len(s) {
		c := s[i]
		switch {
		case c == bel || c == can || c == sub:
			return i + 1
		case c == esc:
			if i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
			return i
		case c == 0xc2 && i+1 < len(s) && s[i+1] == st:
			return i + 2
		}
		i++
	}
	return i
}

// bidi reports whether r is a bidirectional formatting control.
func bidi(r rune) bool {
	switch {
	case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
		return true
	}
	return r == 0x200e || r == 0x200f || r == 0x061c
}
