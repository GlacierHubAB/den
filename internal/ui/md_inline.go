package ui

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Inline markdown, parsed the way CommonMark does it where it matters for
// a chat: code spans first (nothing inside them is markup), backslash
// escapes, links, and emphasis from delimiter runs, so bold can hold code
// and links, and an underscore inside a word stays an underscore. The
// result is a run of spans, each with the markup it sits in; styling and
// wrapping happen later, one line at a time.

// mdAttr is the markup a span of text sits in.
type mdAttr struct {
	bold, italic, strike bool
	code                 bool // a code span
	link                 bool // the text of a link
	url                  bool // a link's target, shown after its text
}

type mdSpan struct {
	text string
	a    mdAttr
}

// mdNode is a piece of a line while emphasis is resolved: text, or a run
// of delimiters (delim set) that may open or close emphasis. bold, italic
// and strike count the emphasis the node is inside.
type mdNode struct {
	text                 string
	a                    mdAttr
	delim                byte
	n, orig              int
	open, close          bool
	bold, italic, strike int
}

// mdInlineMax is the longest line given inline markup; a longer one is
// shown as it is. The parser is linear, this keeps it cheap as well.
const mdInlineMax = 8 << 10

// parseInline turns one line of markdown into styled spans.
func parseInline(s string) []mdSpan {
	if len(s) > mdInlineMax {
		return []mdSpan{{text: s}}
	}
	var out []mdSpan
	var text []string // the pieces of the last span, joined at the end
	for _, n := range inlineNodes(s, mdAttr{}) {
		a := n.a
		a.bold = a.bold || n.bold > 0
		a.italic = a.italic || n.italic > 0
		a.strike = a.strike || n.strike > 0
		t := n.text
		if n.delim != 0 {
			t = strings.Repeat(string(n.delim), n.n)
		}
		if t == "" {
			continue
		}
		if k := len(out) - 1; k >= 0 && out[k].a == a {
			text = append(text, t)
			continue
		}
		if k := len(out) - 1; k >= 0 {
			out[k].text = strings.Join(text, "")
		}
		out = append(out, mdSpan{a: a})
		text = append(text[:0], t)
	}
	if k := len(out) - 1; k >= 0 {
		out[k].text = strings.Join(text, "")
	}
	return out
}

// inlineNodes splits s into nodes with emphasis resolved. Every node takes
// attr as its starting markup (a link's text is parsed with link set).
func inlineNodes(s string, attr mdAttr) []mdNode {
	ix := newLineIndex(s)
	var nodes []mdNode
	var text strings.Builder
	flush := func() {
		if text.Len() > 0 {
			nodes = append(nodes, mdNode{text: text.String(), a: attr})
			text.Reset()
		}
	}
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s) && isASCIIPunct(s[i+1]):
			text.WriteByte(s[i+1])
			i += 2
			continue
		case c == '`':
			n := runLen(s, i, '`')
			if end := ix.closingTicks(i+n, n); end >= 0 {
				flush()
				a := attr
				a.code = true
				nodes = append(nodes, mdNode{text: codeContent(s[i+n : end]), a: a})
				i = end + n
				continue
			}
			text.WriteString(s[i : i+n]) // no closer: the ticks are literal
			i += n
			continue
		case c == '[' || c == '!' && i+1 < len(s) && s[i+1] == '[':
			start := i
			if c == '!' {
				start++
			}
			if label, url, end, ok := ix.parseLink(start); ok && !attr.link {
				flush()
				la := attr
				la.link = true
				// Emphasis does not reach into or out of a link's text.
				for _, n := range inlineNodes(label, la) {
					n.open, n.close = false, false
					nodes = append(nodes, n)
				}
				if url != "" && url != label {
					ua := attr
					ua.url = true
					nodes = append(nodes, mdNode{text: " (" + url + ")", a: ua})
				}
				i = end
				continue
			}
		case c == '<':
			if end := ix.autolinkEnd(i); end > 0 && isAutolink(s[i+1:i+end]) {
				flush()
				a := attr
				a.link = true
				nodes = append(nodes, mdNode{text: s[i+1 : i+end], a: a})
				i += end + 1
				continue
			}
		case c == '*' || c == '_' || c == '~':
			n := runLen(s, i, c)
			if c == '~' && n != 2 {
				text.WriteString(s[i : i+n])
				i += n
				continue
			}
			prev, next := ' ', ' '
			if i > 0 {
				prev, _ = utf8.DecodeLastRuneInString(s[:i])
			}
			if i+n < len(s) {
				next, _ = utf8.DecodeRuneInString(s[i+n:])
			}
			left := !unicode.IsSpace(next) && (!isPunct(next) || unicode.IsSpace(prev) || isPunct(prev))
			right := !unicode.IsSpace(prev) && (!isPunct(prev) || unicode.IsSpace(next) || isPunct(next))
			open, close := left, right
			if c == '_' { // not inside a word: snake_case_names stay as they are
				open = left && (!right || isPunct(prev))
				close = right && (!left || isPunct(next))
			}
			flush()
			nodes = append(nodes, mdNode{a: attr, delim: c, n: n, orig: n, open: open, close: close})
			i += n
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		text.WriteString(s[i : i+size])
		i += size
	}
	flush()
	resolveEmphasis(nodes)
	return nodes
}

// resolveEmphasis pairs closers with the nearest opener of the same kind,
// as CommonMark's delimiter algorithm does: two from each side make bold
// (or strikethrough for ~~), one makes italic, and delimiters skipped over
// inside a pair stay literal. Openers wait on a stack and leave it once
// passed over, so a hostile line costs little more than its length.
func resolveEmphasis(nodes []mdNode) {
	// Each pair adds one level to the nodes between it, marked at both
	// ends here and summed at the end.
	type level struct{ bold, italic, strike int }
	marks := make([]level, len(nodes)+1)
	var stack []int
	// bottom[k] is the lowest node worth searching for an opener for a
	// closer of kind k; below it an earlier search already found none.
	bottom := map[[3]int]int{}
	for c := range nodes {
		cl := &nodes[c]
		if cl.delim == 0 {
			continue
		}
		key := [3]int{int(cl.delim), cl.orig % 3, b2i(cl.open)}
		for cl.close && cl.n > 0 {
			p := -1
			for k := len(stack) - 1; k >= 0 && stack[k] >= bottom[key]; k-- {
				op := &nodes[stack[k]]
				if op.delim != cl.delim {
					continue
				}
				if (op.close || cl.open) && (op.orig+cl.orig)%3 == 0 && (op.orig%3 != 0 || cl.orig%3 != 0) {
					continue
				}
				p = k
				break
			}
			if p < 0 {
				bottom[key] = c
				break
			}
			o := stack[p]
			op := &nodes[o]
			use := 1
			if op.n >= 2 && cl.n >= 2 {
				use = 2
			}
			if cl.delim == '~' && use != 2 {
				break
			}
			switch {
			case cl.delim == '~':
				marks[o+1].strike++
				marks[c].strike--
			case use == 2:
				marks[o+1].bold++
				marks[c].bold--
			default:
				marks[o+1].italic++
				marks[c].italic--
			}
			// Openers between the pair are passed over: literal now.
			stack = stack[:p+1]
			op.n -= use
			cl.n -= use
			if op.n == 0 {
				stack = stack[:p]
			}
		}
		if cl.open && cl.n > 0 {
			stack = append(stack, c)
		}
	}
	var cur level
	for i := range nodes {
		cur.bold += marks[i].bold
		cur.italic += marks[i].italic
		cur.strike += marks[i].strike
		nodes[i].bold += cur.bold // a link's text has its own already
		nodes[i].italic += cur.italic
		nodes[i].strike += cur.strike
	}
}

// lineIndex holds what the inline parser looks ahead for, found in one
// pass over the line, so no construct rescans the rest of it: a line of
// thousands of [ or < or backtick runs costs its length, not its square.
type lineIndex struct {
	s string
	// ticks lists the starts of the backtick runs of each length, in order.
	ticks map[int][]int
	// bracket and paren map an opening [ or ( to its match.
	bracket, paren map[int]int
	// nextLT and nextGT give, for each position, the next < or > at or
	// after it (len(s) for none); built only when the line has a <.
	nextLT, nextGT []int32
}

func newLineIndex(s string) *lineIndex {
	ix := &lineIndex{s: s}
	if strings.IndexByte(s, '`') >= 0 {
		ix.ticks = map[int][]int{}
		for i := 0; i < len(s); {
			if s[i] != '`' {
				i++
				continue
			}
			n := runLen(s, i, '`')
			ix.ticks[n] = append(ix.ticks[n], i)
			i += n
		}
	}
	if strings.IndexByte(s, '[') >= 0 {
		// Brackets pair as the parser reads them: escapes and code spans
		// hide them.
		ix.bracket = map[int]int{}
		var stack []int
		for i := 0; i < len(s); i++ {
			switch s[i] {
			case '\\':
				i++
			case '`':
				n := runLen(s, i, '`')
				if e := ix.closingTicks(i+n, n); e >= 0 {
					i = e + n - 1
				} else {
					i += n - 1
				}
			case '[':
				stack = append(stack, i)
			case ']':
				if k := len(stack) - 1; k >= 0 {
					ix.bracket[stack[k]] = i
					stack = stack[:k]
				}
			}
		}
		ix.paren = map[int]int{}
		stack = stack[:0]
		for i := 0; i < len(s); i++ {
			switch s[i] {
			case '\\':
				i++
			case '(':
				stack = append(stack, i)
			case ')':
				if k := len(stack) - 1; k >= 0 {
					ix.paren[stack[k]] = i
					stack = stack[:k]
				}
			}
		}
	}
	if strings.IndexByte(s, '<') >= 0 {
		ix.nextLT, ix.nextGT = make([]int32, len(s)+1), make([]int32, len(s)+1)
		ix.nextLT[len(s)], ix.nextGT[len(s)] = int32(len(s)), int32(len(s))
		for i := len(s) - 1; i >= 0; i-- {
			ix.nextLT[i], ix.nextGT[i] = ix.nextLT[i+1], ix.nextGT[i+1]
			switch s[i] {
			case '<':
				ix.nextLT[i] = int32(i)
			case '>':
				ix.nextGT[i] = int32(i)
			}
		}
	}
	return ix
}

// closingTicks is the start of the first run of exactly n backticks at or
// after i, -1 for none.
func (ix *lineIndex) closingTicks(i, n int) int {
	runs := ix.ticks[n]
	k := sort.SearchInts(runs, i)
	if k == len(runs) {
		return -1
	}
	return runs[k]
}

// autolinkEnd is the offset from the < at i to the > that closes it, with
// no other < between; 0 for none.
func (ix *lineIndex) autolinkEnd(i int) int {
	if ix.nextGT == nil {
		return 0
	}
	gt := int(ix.nextGT[i+1])
	if gt >= len(ix.s) || int(ix.nextLT[i+1]) < gt {
		return 0
	}
	return gt - i
}

// parseLink reads [label](url) at s[i]. end is just past the closing
// parenthesis. A title after the url is dropped.
func (ix *lineIndex) parseLink(i int) (label, url string, end int, ok bool) {
	s := ix.s
	if i >= len(s) || s[i] != '[' {
		return "", "", 0, false
	}
	j, found := ix.bracket[i]
	if !found || j+1 >= len(s) || s[j+1] != '(' {
		return "", "", 0, false
	}
	k, found := ix.paren[j+1]
	if !found {
		return "", "", 0, false
	}
	label = s[i+1 : j]
	dest := strings.TrimSpace(s[j+2 : k])
	if f := strings.Fields(dest); len(f) > 0 {
		dest = f[0]
	}
	dest = strings.TrimSuffix(strings.TrimPrefix(dest, "<"), ">")
	return label, dest, k + 1, true
}

// isAutolink is the inside of <scheme:…> or <user@host>.
func isAutolink(s string) bool {
	if s == "" || strings.ContainsAny(s, " <>\t") {
		return false
	}
	if c := strings.IndexByte(s, ':'); c >= 2 && c <= 32 {
		for _, r := range s[:c] {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '+' || r == '.' || r == '-') {
				return false
			}
		}
		return true
	}
	at := strings.IndexByte(s, '@')
	return at > 0 && at < len(s)-1 && !strings.Contains(s, ":")
}

// codeContent is a code span's text: one space either side is dropped
// when both are there, so “ `a` “ can be written.
func codeContent(s string) string {
	if len(s) >= 2 && s[0] == ' ' && s[len(s)-1] == ' ' && strings.TrimSpace(s) != "" {
		return s[1 : len(s)-1]
	}
	return s
}

func runLen(s string, i int, c byte) int {
	n := 0
	for i+n < len(s) && s[i+n] == c {
		n++
	}
	return n
}

func isASCIIPunct(c byte) bool {
	return c >= '!' && c <= '/' || c >= ':' && c <= '@' || c >= '[' && c <= '`' || c >= '{' && c <= '~'
}

func isPunct(r rune) bool {
	return r < 0x80 && isASCIIPunct(byte(r)) || unicode.IsPunct(r) || unicode.IsSymbol(r)
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
