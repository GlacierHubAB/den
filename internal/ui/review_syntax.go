package ui

import (
	"fmt"
	"hash/fnv"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/review"
	"github.com/GlacierHubAB/den/internal/theme"
)

// Syntax highlighting for the Review diff. Each hunk is lexed twice, once
// as the new file (context and added lines) and once as the old (context
// and removed lines), so a comment or string that spans lines is coloured
// right on both sides. The lexer comes from the file name; files chroma
// does not know are drawn plain. Tokens are coloured from Den's palette,
// and changed lines sit on a tint of green or red so they stay apart from
// context once the code itself is coloured.

// synClass is the handful of token kinds the palette tells apart.
type synClass uint8

const (
	synPlain synClass = iota
	synKeyword
	synFunc // function, class and type names being declared or called
	synType // built-in types and names, tags
	synLiteral
	synComment
	synPunct
	synHeading
)

type synSpan struct {
	text string
	cls  synClass
}

// synEntry is one file's tokens, per diff line, and the bodies already
// drawn from them at one width.
type synEntry struct {
	spans  [][]synSpan
	width  int
	bodies []string
	drawn  []bool
}

// synCache keeps lexed files across renders: diffRows runs on every key
// press and mouse move, and lexing is the costly part.
var synCache = struct {
	sync.Mutex
	m map[string]*synEntry
}{m: map[string]*synEntry{}}

const (
	synCacheMax = 64
	// synMaxBytes is the largest diff lexed; bigger ones are drawn plain
	// so a generated file never stalls the view.
	synMaxBytes = 256 << 10
	// Some lexers are quadratic on hostile input (a Rust raw string opener
	// repeated takes seconds at 64 KB), and lexing runs on the UI
	// goroutine, the first time a file is drawn. So a hunk is lexed in
	// pieces of whole lines up to synChunkMax, and a file gets synFileMax
	// of lexing in all: about 180 ms at worst, once, as for markdown.
	// Once the budget is spent, the rest of the file is drawn plain. A
	// line longer than a piece is drawn plain on its own, and lexing goes
	// on with the lines after it.
	synChunkMax = 4 << 10
	synFileMax  = 16 << 10
)

// synLexer picks the lexer for a path, nil when chroma does not know it.
func synLexer(path string) chroma.Lexer {
	l := lexers.Match(filepath.Base(path))
	if l == nil {
		return nil
	}
	return chroma.Coalesce(l)
}

// synLexed counts lexDiff runs that reached the lexer, synChunks the
// pieces handed to it and synChunkBytes their size. Tests read them to show a file is lexed once and
// then served from synCache, and never past its budget, whatever the
// clock says.
var synLexed, synChunks, synChunkBytes atomic.Int64

// lexDiff splits a file's diff lines into coloured spans. A line the lexer
// gave nothing for (no lexer, an error, or an empty line) gets no spans.
func lexDiff(path string, lines []diffLine) [][]synSpan {
	lexer := synLexer(path)
	if lexer == nil {
		return nil
	}
	size := 0
	for _, d := range lines {
		size += len(d.text) + 1
	}
	if size > synMaxBytes {
		return nil
	}
	synLexed.Add(1)
	budget := synFileMax
	out := make([][]synSpan, len(lines))
	for start := 0; start < len(lines); {
		end := start
		for end < len(lines) && lines[end].hunk == lines[start].hunk {
			end++
		}
		for _, side := range []byte{'+', '-'} {
			// Context lines are part of both documents, so the lexer has
			// the same state either side, but take their colours from the
			// new side only.
			var idx []int
			var keep []bool
			for i := start; i < end; i++ {
				if k := lines[i].kind; k == side || k == ' ' {
					idx = append(idx, i)
					keep = append(keep, k == side || side == '+')
				}
			}
			// Lex the side in pieces of whole lines, while the file's
			// budget lasts; a piece starts the lexer afresh, which can
			// misread a string or comment that spans the cut.
			for p := 0; p < len(idx) && budget > 0; {
				q, size := p, 0
				for q < len(idx) && size+len(lines[idx[q]].text)+1 <= synChunkMax {
					size += len(lines[idx[q]].text) + 1
					q++
				}
				if q == p { // a line longer than a piece: that line plain, go on
					p++
					continue
				}
				if size > budget {
					budget = 0
					break
				}
				budget -= size
				lexPiece(lexer, lines, idx[p:q], keep[p:q], out)
				p = q
			}
		}
		start = end
	}
	return out
}

// lexPiece lexes the lines at idx as one document and adds the spans of
// the ones to keep to out.
func lexPiece(lexer chroma.Lexer, lines []diffLine, idx []int, keep []bool, out [][]synSpan) {
	var b strings.Builder
	for _, i := range idx {
		b.WriteString(lines[i].text)
		b.WriteByte('\n')
	}
	synChunks.Add(1)
	synChunkBytes.Add(int64(b.Len()))
	it, err := lexer.Tokenise(nil, b.String())
	if err != nil {
		return
	}
	row := 0
	for _, tok := range it.Tokens() {
		cls := synClassOf(tok.Type)
		for pi, part := range strings.Split(tok.Value, "\n") {
			if pi > 0 {
				row++
			}
			if part != "" && row < len(idx) && keep[row] {
				out[idx[row]] = append(out[idx[row]], synSpan{part, cls})
			}
		}
	}
}

func synClassOf(t chroma.TokenType) synClass {
	switch {
	case t == chroma.CommentPreproc || t == chroma.CommentPreprocFile:
		return synKeyword
	case t.InCategory(chroma.Comment):
		return synComment
	case t == chroma.KeywordConstant:
		return synLiteral
	case t == chroma.KeywordType:
		return synType
	case t.InCategory(chroma.Keyword):
		return synKeyword
	case t.InCategory(chroma.Literal):
		return synLiteral
	case t == chroma.NameFunction || t == chroma.NameFunctionMagic || t == chroma.NameClass || t == chroma.NameDecorator:
		return synFunc
	case t.InSubCategory(chroma.NameBuiltin) || t == chroma.NameTag:
		return synType
	case t.InCategory(chroma.Operator) || t.InCategory(chroma.Punctuation):
		return synPunct
	case t == chroma.GenericHeading || t == chroma.GenericSubheading || t == chroma.GenericStrong:
		return synHeading
	}
	return synPlain
}

// synFor returns the cached entry for a file, lexing it on first sight.
// The key covers the content and the palette, so a reload or a theme
// change draws afresh. Callers hold synCache.
func (m Model) synFor(f review.File, lines []diffLine) *synEntry {
	h := fnv.New64a()
	for _, d := range lines {
		h.Write([]byte{d.kind})
		h.Write([]byte(d.text))
		h.Write([]byte{0})
	}
	p := m.st.pal
	key := fmt.Sprintf("%s\x00%x\x00%s%s%s%s%s%s%s%s", f.Path, h.Sum64(), p.Bg, p.Fg, p.FgDim, p.Bright, p.Ok, p.Err, p.Attn, p.Code)
	if e, ok := synCache.m[key]; ok {
		return e
	}
	if len(synCache.m) >= synCacheMax {
		synCache.m = map[string]*synEntry{}
	}
	e := &synEntry{spans: lexDiff(f.Path, lines)}
	synCache.m[key] = e
	return e
}

// synFade is how far context and removed lines lean toward the background,
// matching the dim they were drawn in before highlighting.
const synFade = 0.45

// synColor is the foreground for a token class on a line of the given kind.
func synColor(p theme.Palette, cls synClass, kind byte) string {
	c := p.Fg
	switch cls {
	case synKeyword, synHeading:
		c = p.Bright
	case synFunc, synType:
		c = p.Code
	case synLiteral:
		c = p.Attn
	case synComment:
		c = p.FgDim
	}
	if kind != '+' {
		c = mixHex(c, p.Bg, synFade)
	}
	return c
}

// lineBg is the background of a changed line: a faint tint of the colour
// its sign is drawn in. Context lines have none.
func lineBg(p theme.Palette, kind byte) string {
	switch kind {
	case '+':
		return mixHex(p.Bg, p.Ok, 0.16)
	case '-':
		return mixHex(p.Bg, p.Err, 0.16)
	}
	return ""
}

// codeCell draws the sign and code of diff line i, truncated to textW, on
// the selection background when sel is set. Unselected lines come from the
// cache once drawn.
func (m Model) codeCell(e *synEntry, lines []diffLine, i, textW int, sel bool) string {
	if !sel {
		if e.width != textW || len(e.bodies) != len(lines) {
			e.width, e.bodies, e.drawn = textW, make([]string, len(lines)), make([]bool, len(lines))
		}
		if e.drawn[i] {
			return e.bodies[i]
		}
	}
	p := m.st.pal
	d := lines[i]
	bg := lineBg(p, d.kind)
	if sel {
		bg = p.Sel
	}
	style := func(fg string) lipgloss.Style {
		st := lipgloss.NewStyle().Foreground(lipgloss.Color(fg))
		if bg != "" {
			st = st.Background(lipgloss.Color(bg))
		}
		return st
	}
	var sign string
	switch d.kind {
	case '+':
		sign = style(p.Ok).Render("+ ")
	case '-':
		sign = style(p.Err).Render("- ")
	default:
		sign = style(p.FgDim).Render("  ")
	}
	var spans []synSpan
	if i < len(e.spans) {
		spans = e.spans[i]
	}
	if spans == nil && d.text != "" {
		spans = []synSpan{{d.text, synPlain}}
	}
	var b strings.Builder
	w := 0
	for _, sp := range spans {
		if w > textW {
			break // the rest is cut off anyway
		}
		text := strings.ReplaceAll(sp.text, "\t", "    ")
		w += ansi.StringWidth(text)
		st := style(synColor(p, sp.cls, d.kind))
		switch sp.cls {
		case synKeyword, synHeading:
			st = st.Bold(true)
		case synComment:
			st = st.Italic(true)
		}
		b.WriteString(st.Render(text))
	}
	out := sign + ansi.Truncate(b.String(), textW, "…")
	if !sel {
		e.bodies[i], e.drawn[i] = out, true
	}
	return out
}

// mixHex moves colour a toward b by t (0 keeps a, 1 gives b). Colours that
// are not #rrggbb come back unchanged.
func mixHex(a, b string, t float64) string {
	pa, okA := hexRGB(a)
	pb, okB := hexRGB(b)
	if !okA || !okB {
		return a
	}
	var out [3]int
	for i := range out {
		out[i] = int(float64(pa[i])*(1-t) + float64(pb[i])*t + 0.5)
	}
	return fmt.Sprintf("#%02x%02x%02x", out[0], out[1], out[2])
}

func hexRGB(h string) ([3]int, bool) {
	var out [3]int
	h = strings.TrimPrefix(h, "#")
	if len(h) != 6 {
		return out, false
	}
	_, err := fmt.Sscanf(h, "%02x%02x%02x", &out[0], &out[1], &out[2])
	return out, err == nil
}
