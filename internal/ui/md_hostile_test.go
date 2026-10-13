package ui

import (
	"math/rand"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/theme"
	"github.com/GlacierHubAB/den/internal/transcript"
)

// Regression tests for the security review of the markdown renderer:
// hostile lines and code blocks must not stall the view.

// bestOf is the fastest of three runs of f, which takes out most noise.
func bestOf(f func()) time.Duration {
	best := time.Duration(1 << 62)
	for range 3 {
		start := time.Now()
		f()
		best = min(best, time.Since(start))
	}
	return best
}

// TestInlineParserLinear: each hostile line costs about its length. The
// time at 128 KB must stay within 4 times linear growth from 16 KB (8x
// size, so 32x time); a quadratic parser grows 64x. The parser is driven
// directly, past the 8 KB backstop, and the 16 KB time has a floor so a
// fast machine's noise cannot fail it.
func TestInlineParserLinear(t *testing.T) {
	ladder := func(n int) string {
		var b strings.Builder
		for k := 1; b.Len() < n; k++ {
			b.WriteString(strings.Repeat("`", k) + "a")
		}
		return b.String()[:n]
	}
	rep := func(unit string) func(int) string {
		return func(n int) string { return strings.Repeat(unit, n/len(unit)+1)[:n] }
	}
	for name, gen := range map[string]func(int) string{
		"open-brackets":  rep("["),
		"bang-bracket":   rep("!["),
		"bracket-parens": rep("[]("),
		"label-open":     rep("[a]("),
		"lt":             rep("<a"),
		"tick-ladder":    ladder,
		"stars":          rep("*a"),
		"mixed":          rep("*a _b [c](` <d"),
	} {
		small, large := gen(16<<10), gen(128<<10)
		ts := bestOf(func() { inlineNodes(small, mdAttr{}) })
		tl := bestOf(func() { inlineNodes(large, mdAttr{}) })
		if limit := 32 * max(ts, 5*time.Millisecond); tl > limit {
			t.Errorf("%s: 16 KB %v, 128 KB %v: grows faster than linear", name, ts, tl)
		}
	}
}

// TestLongLineBackstop: a line over 8 KB is shown as it is, unparsed.
func TestLongLineBackstop(t *testing.T) {
	line := strings.Repeat("**b** ", (mdInlineMax/6)+10)
	spans := parseInline(line)
	if len(spans) != 1 || spans[0].text != line || spans[0].a != (mdAttr{}) {
		t.Errorf("a long line should be one plain span, got %d spans", len(spans))
	}
	if got := spanString(parseInline("**b**")); got != "[b:b]" {
		t.Errorf("short lines still parse: %s", got)
	}
}

// hostileCode is a block each lexer the review timed finds slow.
var hostileCode = map[string]string{
	"rust": `r#"`, "sql": `'''--`, "go": `"\`, "markdown": "*_[`",
	"js": "`${", "python": `'''\`, "bash": `$("${`, "c": "/*", "perl": `s/\/`,
}

// TestHighlightLimits: chroma sees a block only when it is 8 KB or less,
// and one frame colours at most 32 KB; the rest is drawn plain.
func TestHighlightLimits(t *testing.T) {
	m := Model{st: newStyles(theme.Deepharness)}
	block := func(lang, unit string, n int) string {
		return "```" + lang + "\n" + strings.Repeat(unit, n/len(unit)) + "\n```"
	}
	for lang, unit := range hostileCode {
		before := mdLexed.Load()
		out := m.markdownLines(block(lang, unit, 250<<10), 100)
		if mdLexed.Load() != before {
			t.Errorf("%s: a 250 KB block reached the lexer", lang)
		}
		if len(out) == 0 || !strings.HasPrefix(ansi.Strip(out[0]), "┃ ") {
			t.Errorf("%s: the block should still be drawn as code", lang)
		}
	}
	// Five 7 KB blocks in one reply: four fit the frame's 32 KB.
	var reply []string
	for i := range 5 {
		reply = append(reply, block("go", "x := 1 // "+string(rune('a'+i))+"\n", 7<<10))
	}
	before := mdLexed.Load()
	m.markdownLines(strings.Join(reply, "\n\n"), 100)
	if n := mdLexed.Load() - before; n != 4 {
		t.Errorf("lexed %d blocks in one frame, want 4", n)
	}
	// The same reply again comes from the cache: no lexing at all.
	before = mdLexed.Load()
	m.markdownLines(strings.Join(reply, "\n\n"), 100)
	if n := mdLexed.Load() - before; n != 0 {
		t.Errorf("a cached reply was lexed again (%d)", n)
	}
	// A reply that found the frame's budget spent is not kept, so a later
	// frame colours it.
	frame := Model{st: m.st, hl: &hlBudget{}}
	small := block("go", "y := 2\n", 1<<10)
	before = mdLexed.Load()
	frame.markdownLines(small, 100)
	if mdLexed.Load() != before {
		t.Error("a spent frame still lexed")
	}
	m.markdownLines(small, 100)
	if mdLexed.Load() != before+1 {
		t.Error("the next frame should colour what the spent one left plain")
	}
}

// TestRenderCacheCopies: what the cache hands out can be changed (the
// streaming cursor is appended in place) without touching the cache.
func TestRenderCacheCopies(t *testing.T) {
	m := Model{st: newStyles(theme.Deepharness)}
	a := m.markdownLines("cache **me**", 40)
	a[0] += "▍"
	if b := m.markdownLines("cache **me**", 40); strings.Contains(b[0], "▍") {
		t.Error("a caller's change reached the cache")
	}
	// The width and the code colour are part of the key.
	if w20, w40 := m.markdownLines(strings.Repeat("word ", 10), 20), m.markdownLines(strings.Repeat("word ", 10), 40); len(w20) == len(w40) {
		t.Error("two widths share one rendering")
	}
}

// TestRemoteCodeIsNotLexed: a remote agent's replies are drawn with plain
// code blocks, however many there are; a local agent's tail is coloured
// within the frame's budget.
func TestRemoteCodeIsNotLexed(t *testing.T) {
	m := Model{st: newStyles(theme.Deepharness)}
	// The review's case: a host under full sends 20 entries of 4 KB.
	body := "```rust\n" + strings.Repeat(`r#"`, (fleet.FullTextMax-16)/3) + "\n```"
	var tail []transcript.Entry
	for range fleet.FullTailMax {
		tail = append(tail, transcript.Entry{Kind: "claude", Text: body})
	}
	remote := fleet.Agent{Host: "build1", Tail: tail}
	before := mdLexed.Load()
	start := time.Now()
	out := m.activityLines(remote.Tail, remote, 100, true)
	if mdLexed.Load() != before {
		t.Errorf("a remote agent's code reached the lexer %d times", mdLexed.Load()-before)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("a remote tail took %v", d)
	}
	if len(out) == 0 {
		t.Fatal("nothing drawn")
	}
	local := fleet.Agent{Tail: tail[:1]}
	local.Tail = []transcript.Entry{{Kind: "claude", Text: "```go\nfunc f() {}\n```"}}
	before = mdLexed.Load()
	m.activityLines(local.Tail, local, 100, true)
	if mdLexed.Load() != before+1 {
		t.Error("a local agent's code should be coloured")
	}
	// Even a local tail of hostile blocks lexes no more than the frame's
	// budget: eight of the twenty 4 KB blocks.
	m.hl = newHLBudget()
	local.Tail = tail
	for i := range local.Tail {
		local.Tail[i].Text += strings.Repeat(" ", i) // not cached from above
	}
	before = mdLexed.Load()
	m.activityLines(local.Tail, local, 100, true)
	if n := mdLexed.Load() - before; n > int64(hlFrameMax/(fleet.FullTextMax-16)) {
		t.Errorf("one frame lexed %d blocks", n)
	}
}

// TestCodeBlockFitsWidth: tabs and wide spaces never push a code line
// past the pane.
func TestCodeBlockFitsWidth(t *testing.T) {
	m := Model{st: newStyles(theme.Deepharness)}
	for _, code := range []string{
		strings.Repeat("\t", 30) + "x",
		strings.Repeat("\u3000", 40),
		"a\tb\tc\td\te\tf\tg\th\ti\tj\tk",
		strings.Repeat("漢", 50),
	} {
		for _, w := range []int{12, 30, 61} {
			for _, l := range append(m.codeBlock(code, w), m.codeBlockN(code, w, 2)...) {
				if lw := ansi.StringWidth(l); lw > max(10, w) {
					t.Errorf("%q at %d: line %d wide", code, w, lw)
				}
			}
		}
	}
}

// TestMarkdownProperty is the review's fuzz: random markup, escapes and
// odd characters never leave a control character, an invisible format
// character or an open style on a line, and no line is wider than asked.
func TestMarkdownProperty(t *testing.T) {
	m := Model{st: newStyles(theme.Deepharness)}
	sgr := regexp.MustCompile("\x1b\\[[0-9;:]*m")
	atoms := []string{"*", "**", "_", "__", "~~", "`", "```", "```go\n", "```js\n", "\n```\n", "[", "]", "(", ")", "<", ">", "!", "\\", "|", "|-|-|\n", "\n", "> ", "- ", "1. ", "  ", "# ", "---\n",
		"a", "word ", "http://x.y/z", "\x1b", "\x1b]8;;http://e\x07", "\x1b]52;c;QQ==\x07", "\x9b31m", "\x1b[31m", "\u202e", "\u2066", "\u200b", "\U000E0041", "\ufeff", "\u00ad", "\r", "\t", "\x00", "\x7f",
		"漢字", "\U0001f600", "\U0001f469\u200d\U0001f4bb", "e\u0301", "\u3000", "ｗ", "→", "◇ mission proposal: ", "\u2028"}
	rng := rand.New(rand.NewSource(1))
	for it := 0; it < 4000; it++ {
		var b strings.Builder
		for k := rng.Intn(60); k >= 0; k-- {
			b.WriteString(atoms[rng.Intn(len(atoms))])
		}
		in, w := b.String(), 20+rng.Intn(80)
		for _, l := range m.markdownLines(in, w) {
			why := ""
			for _, r := range sgr.ReplaceAllString(l, "") {
				switch {
				case r < 0x20 || r == 0x7f || r >= 0x80 && r < 0xa0:
					why = "control character"
				case unicode.Is(unicode.Cf, r) && r != 0x200d:
					why = "format character"
				case r == 0x2028 || r == 0x2029:
					why = "line separator"
				}
			}
			if all := sgr.FindAllString(l, -1); len(all) > 0 && all[len(all)-1] != "\x1b[m" && all[len(all)-1] != "\x1b[0m" {
				why = "style left open"
			}
			if ansi.StringWidth(l) > w {
				why = "wider than the width"
			}
			if why != "" {
				t.Fatalf("%s (w=%d): in=%q line=%q", why, w, in, l)
			}
		}
	}
}

// TestMarkdownFitsWidthCases are the review fuzz's finds: a struck or
// linked emoji sequence was drawn a rune at a time, and table columns
// could not hold a wide character, so lines came out wider than asked.
func TestMarkdownFitsWidthCases(t *testing.T) {
	m := Model{st: newStyles(theme.Deepharness)}
	for _, c := range []struct {
		in string
		w  int
	}{
		{"\n\x9b31m\u2028\x00ahttp://x.y/z---\n\n```\nhttp://x.y/z\ufeff\n~~\u2028>`\u202e!\r> \ufeff\x1b[31m`\n```\n\r- ><\n~~\x9b31m\x1b]52;c;QQ==\ahttp://x.y/z<\U0001f600\x1b\U0001f469\u200d\U0001f4bb[\\a~~\x00\x9b31m|-|-|\n\U0001f469\u200d\U0001f4bb> word 1. >```js\n\u2028http://x.y/z", 28},
		{"|e\u0301```go\n\x00|-|-|\n- http://x.y/z\uff57\t1. \U0001f600**\ufeff)(|e\u0301  \n\u2192**(\u2192<~~\ufeff\r\u2066\U0001f469\u200d\U0001f4bba\u00ad1. # ||-|-|\n`!```js\ne\u0301]\x00|-|-|\n", 23},
		{"# \u3000\x1b> - *\u2192```*```js\n# \x1b[31m~~\u2066\U0001f469\u200d\U0001f4bbe\u0301>\t|~~\u25c7 mission proposal: a```\ufeff*|# \x1b]52;c;QQ==\a\u25c7 mission proposal: ", 47},
		{"~~\U0001f469\u200d\U0001f4bb\U0001f469\u200d\U0001f4bb~~ [\U0001f469\u200d\U0001f4bb](u)", 12},
	} {
		for _, l := range m.markdownLines(c.in, c.w) {
			if lw := ansi.StringWidth(l); lw > c.w {
				t.Errorf("w=%d: line %d wide: %q", c.w, lw, l)
			}
		}
	}
}
