package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/theme"
	"github.com/GlacierHubAB/den/internal/transcript"
)

func TestMarkdownLines(t *testing.T) {
	m := Model{st: newStyles(theme.Deepharness)}
	cases := []struct{ in, want string }{
		{"## Feedback", "Feedback"},
		{"### Closed heading ###", "Closed heading"},
		{"> quoted **bold** text", "│ quoted bold text"},
		{"> > nested", "│ │ nested"},
		{"> - quoted item", "│ • quoted item"},
		{"- item with `code`", "• item with code"},
		{"* star item", "• star item"},
		{"+ plus item", "• plus item"},
		{"  - nested item", "  ◦ nested item"},
		{"    - deeper", "    ▪ deeper"},
		{"1. first", "1. first"},
		{"  2) second", "  2) second"},
		{"---", "────────────────────────"},
		{"* * *", "────────────────────────"},
		{"plain **bold** and `code` here", "plain bold and code here"},
		{"unmatched `tick", "unmatched `tick"},
		{"**bold line**", "bold line"},
		{"   indented text", "   indented text"},
		{"#hashtag", "#hashtag"},
		{"tab\tseparated", "tab    separated"},
	}
	for _, c := range cases {
		got := m.markdownLines(c.in, 80)
		if len(got) != 1 || ansi.Strip(got[0]) != c.want {
			t.Errorf("markdownLines(%q) = %q, want %q", c.in, stripAll(got), c.want)
		}
	}
	got := m.markdownLines("before\n```go\nx := 1\n```\nafter", 80)
	if want := []string{"before", "┃ x := 1", "after"}; !equalLines(stripAll(got), want) {
		t.Errorf("fenced block = %q, want %q", stripAll(got), want)
	}
	block := m.codeBlock("go test ./...\nlonger line that will need to wrap around the block width", 30)
	if len(block) < 3 || !strings.HasPrefix(ansi.Strip(block[0]), "┃ go test") {
		t.Errorf("codeBlock = %q", block)
	}
	for _, l := range block {
		if w := ansi.StringWidth(l); w > 30 {
			t.Errorf("block line width %d: %q", w, ansi.Strip(l))
		}
	}
}

// spanString shows spans with their markup: [b:text] bold, [i:…] italic,
// [s:…] struck, [c:…] code, [l:…] link text, [u:…] a link's target, and
// combinations such as [bc:…].
func spanString(spans []mdSpan) string {
	var b strings.Builder
	for _, sp := range spans {
		var f string
		for _, x := range []struct {
			on bool
			c  string
		}{{sp.a.bold, "b"}, {sp.a.italic, "i"}, {sp.a.strike, "s"}, {sp.a.code, "c"}, {sp.a.link, "l"}, {sp.a.url, "u"}} {
			if x.on {
				f += x.c
			}
		}
		if f == "" {
			b.WriteString(sp.text)
			continue
		}
		b.WriteString("[" + f + ":" + sp.text + "]")
	}
	return b.String()
}

func TestParseInline(t *testing.T) {
	cases := []struct{ in, want string }{
		// The screenshot's lines: bold that holds inline code.
		{"**On `dev` (all green):** the HTML pages", "[b:On ][bc:dev][b: (all green):] the HTML pages"},
		{"**On `origin/host-fixes`:** the lead pushed", "[b:On ][bc:origin/host-fixes][b::] the lead pushed"},
		{"**Still missing before the security re-review:**", "[b:Still missing before the security re-review:]"},
		{"plain **bold** and *italic* and _under_", "plain [b:bold] and [i:italic] and [i:under]"},
		{"***both***", "[bi:both]"},
		{"__strong__ text", "[b:strong] text"},
		{"*italic with `code`*", "[i:italic with ][ic:code]"},
		{"**bold [link](https://x.test/a)**", "[b:bold ][bl:link][bu: (https://x.test/a)]"},
		{"~~gone~~ and ~one~", "[s:gone] and ~one~"},
		// Code wins: nothing inside a code span is markup.
		{"`**not bold**` and `a_b_c`", "[c:**not bold**] and [c:a_b_c]"},
		{"``code with ` tick``", "[c:code with ` tick]"},
		{"`` `ticked` ``", "[c:`ticked`]"},
		{"unmatched `tick and **bold**", "unmatched `tick and [b:bold]"},
		// Escapes.
		{`\*not italic\* and \` + "`" + `not code\` + "`", "*not italic* and `not code`"},
		{`a \_b\_ \[c](d) \\`, `a _b_ [c](d) \`},
		{`keep \d and \n`, `keep \d and \n`},
		// Underscores inside words are not emphasis.
		{"snake_case_name and __init__.py", "snake_case_name and [b:init].py"},
		{"file_name_here_ok", "file_name_here_ok"},
		{"2*3*4 = 24", "2[i:3]4 = 24"},
		{"a * b * c", "a * b * c"},
		{"**unclosed bold", "**unclosed bold"},
		{"**a** **b**", "[b:a] [b:b]"},
		// Links.
		{"see [the docs](https://example.test/docs) now", "see [l:the docs][u: (https://example.test/docs)] now"},
		{"[https://x.test](https://x.test)", "[l:https://x.test]"},
		{"![alt text](img.png)", "[l:alt text][u: (img.png)]"},
		{"[a `b]` c](u)", "[l:a ][cl:b]][l: c][u: (u)]"},
		{"[t](u \"title\")", "[l:t][u: (u)]"},
		{"[not a link] and [x]", "[not a link] and [x]"},
		{"<https://example.test/x>", "[l:https://example.test/x]"},
		{"a < b > c", "a < b > c"},
		{"[**bold** link](u)", "[bl:bold][l: link][u: (u)]"},
		{"[[nested]](u)", "[l:[nested]][u: (u)]"},
		{"*[a*](u)", "*[l:a*][u: (u)]"},
		{"*x [*a*](u) y*", "[i:x ][il:a][iu: (u)][i: y]"},
		{"*a **b** c*", "[i:a ][bi:b][i: c]"},
		{"**a *b* c**", "[b:a ][bi:b][b: c]"},
	}
	for _, c := range cases {
		if got := spanString(parseInline(c.in)); got != c.want {
			t.Errorf("parseInline(%q)\n got %s\nwant %s", c.in, got, c.want)
		}
	}
}

// TestScreenshotLinesHaveNoAsterisks is the reported bug: bold holding
// inline code showed its asterisks.
func TestScreenshotLinesHaveNoAsterisks(t *testing.T) {
	m := Model{st: newStyles(theme.Deepharness)}
	for _, in := range []string{
		"**On `dev` (all green):** the HTML pages with the signed golden set (resize, animation, uninstall-as-command), the host with its bridge, the review report, and the old QML removed.",
		"**On `origin/host-fixes`:** the lead pushed drey-hatch's committed work, and nothing was uncommitted.",
		"**Still missing before the security re-review:**",
	} {
		for _, w := range []int{80, 40, 20} {
			for _, l := range m.markdownLines(in, w) {
				if strings.Contains(ansi.Strip(l), "*") {
					t.Errorf("width %d: literal asterisk in %q", w, ansi.Strip(l))
				}
				if ansi.StringWidth(l) > w {
					t.Errorf("width %d: line is %d wide: %q", w, ansi.StringWidth(l), ansi.Strip(l))
				}
			}
		}
	}
	// The code inside the bold is bold too, and in the code colour.
	out := m.markdownLines("**On `dev`:**", 80)[0]
	if want := m.st.code.Bold(true).Render("dev"); !strings.Contains(out, want) {
		t.Errorf("code in bold should be bold code %q: %q", want, out)
	}
}

func TestMarkdownWrapping(t *testing.T) {
	m := Model{st: newStyles(theme.Deepharness)}
	// Bold runs across the wrap: each line opens and closes its own
	// styling, so nothing bleeds into the pane's border.
	lines := m.markdownLines("**one two three four five six seven**", 12)
	if len(lines) < 3 {
		t.Fatalf("want a wrap: %q", stripAll(lines))
	}
	bold := m.st.fg.Bold(true)
	for _, l := range lines {
		if ansi.StringWidth(l) > 12 {
			t.Errorf("too wide: %q", ansi.Strip(l))
		}
		if l != bold.Render(ansi.Strip(l)) {
			t.Errorf("each wrapped line should be bold on its own: %q", l)
		}
	}
	// Wide characters are counted by display width and never split.
	for _, in := range []string{
		"漢字かな交じり文の長い行を折り返すテストです",
		"emoji 🎉🎉🎉🎉🎉🎉🎉🎉🎉🎉🎉🎉 run",
		"`" + strings.Repeat("x", 50) + "`",
		"**" + strings.Repeat("長", 30) + "**",
	} {
		lines := m.markdownLines(in, 15)
		var joined strings.Builder
		for _, l := range lines {
			if w := ansi.StringWidth(l); w > 15 {
				t.Errorf("%q: line is %d wide: %q", in, w, ansi.Strip(l))
			}
			joined.WriteString(ansi.Strip(l))
		}
		want := strings.NewReplacer("`", "", "*", "", " ", "").Replace(in)
		if got := strings.ReplaceAll(joined.String(), " ", ""); got != want {
			t.Errorf("%q: text lost in the wrap: %q", in, got)
		}
	}
	// A list item's wrapped lines hang under its text.
	got := stripAll(m.markdownLines("- alpha beta gamma delta", 14))
	if want := []string{"• alpha beta", "  gamma delta"}; !equalLines(got, want) {
		t.Errorf("hanging indent = %q, want %q", got, want)
	}
	got = stripAll(m.markdownLines("> alpha beta gamma delta", 14))
	if want := []string{"│ alpha beta", "│ gamma delta"}; !equalLines(got, want) {
		t.Errorf("quote wrap = %q, want %q", got, want)
	}
}

func TestMarkdownBlocks(t *testing.T) {
	m := Model{st: newStyles(theme.Deepharness)}
	table := "| Name | State | Note |\n|:-----|:-----:|-----:|\n| `dev` | **green** | ok |\n| host-fixes | red | a pipe \\| here |"
	got := stripAll(m.markdownLines(table, 60))
	want := []string{
		"Name       │ State │          Note",
		"───────────┼───────┼──────────────",
		"dev        │ green │            ok",
		"host-fixes │  red  │ a pipe | here",
	}
	for i := range want {
		want[i] = strings.TrimRight(want[i], " ")
	}
	for i := range got {
		got[i] = strings.TrimRight(got[i], " ")
	}
	if !equalLines(got, want) {
		t.Errorf("table =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// Too wide: the columns share the width and wrap their cells.
	for _, l := range m.markdownLines("| a | b |\n|---|---|\n| "+strings.Repeat("word ", 12)+"| short |", 30) {
		if w := ansi.StringWidth(l); w > 30 {
			t.Errorf("table line %d wide: %q", w, ansi.Strip(l))
		}
	}
	// Too narrow for columns: a row to a line.
	got = stripAll(m.markdownLines("| a | b | c | d |\n|-|-|-|-|\n| 1 | 2 | 3 | 4 |", 12))
	if want := []string{"a · b · c · d", "1 · 2 · 3 · 4"}; len(got) < 2 || !strings.HasPrefix(got[0], "a · b") {
		t.Errorf("narrow table = %q, want rows like %q", got, want)
	}
	// A fence closes only on its own kind and length, and an unclosed one
	// (a reply still streaming) runs to the end.
	got = stripAll(m.markdownLines("````\n```\ninner\n````\nafter", 40))
	if want := []string{"┃ ```", "┃ inner", "after"}; !equalLines(got, want) {
		t.Errorf("nested fence = %q, want %q", got, want)
	}
	got = stripAll(m.markdownLines("~~~sh\necho **hi**", 40))
	if want := []string{"┃ echo **hi**"}; !equalLines(got, want) {
		t.Errorf("open fence = %q, want %q", got, want)
	}
	// A known language is coloured by token; the text is unchanged.
	gocode := m.markdownLines("```go\nfunc main() { return \"x\" } // done\n```", 80)
	if len(gocode) != 1 || ansi.Strip(gocode[0]) != "┃ func main() { return \"x\" } // done" {
		t.Errorf("go block = %q", stripAll(gocode))
	}
	if !strings.Contains(gocode[0], m.st.bright.Bold(true).Render("func")) {
		t.Errorf("keyword not highlighted: %q", gocode[0])
	}
	// An indented fence inside a list item.
	got = stripAll(m.markdownLines("- step\n  ```\n  make\n  ```", 40))
	if want := []string{"• step", "┃ make"}; !equalLines(got, want) {
		t.Errorf("indented fence = %q, want %q", got, want)
	}
}

// TestMarkdownSoftBreaks: a paragraph's single line breaks join it, so
// emphasis spans them; hard breaks, blank lines and new blocks do not.
func TestMarkdownSoftBreaks(t *testing.T) {
	m := Model{st: newStyles(theme.Deepharness)}
	for _, c := range []struct {
		name, in string
		want     []string
	}{
		{"bold across lines", "**bold\ncontinues** here", []string{"bold continues here"}},
		{"plain paragraph", "one\ntwo\nthree", []string{"one two three"}},
		{"two spaces break", "one  \ntwo", []string{"one", "two"}},
		{"backslash breaks", "one\\\ntwo", []string{"one", "two"}},
		{"backslash at the end stays", "the path C:\\", []string{"the path C:\\"}},
		{"blank line", "one\n\ntwo", []string{"one", "", "two"}},
		{"list item continues", "- item\n  goes on\n- next", []string{"• item goes on", "• next"}},
		{"list after a paragraph", "Steps:\n- a\n- b", []string{"Steps:", "• a", "• b"}},
		{"heading stays", "# Title\nbody", []string{"Title", "body"}},
		{"quote joins", "> one\n> two\nafter", []string{"│ one two", "after"}},
		{"fence after text", "see\n```\ncode\n```", []string{"see", "┃ code"}},
		{"table after text", "rows:\n| a | b |\n|---|---|\n| 1 | 2 |", []string{"rows:", "a │ b", "──┼──", "1 │ 2"}},
	} {
		got := stripAll(m.markdownLines(c.in, 60))
		for i := range got {
			got[i] = strings.TrimRight(got[i], " ")
		}
		if !equalLines(got, c.want) {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	// A long paragraph of short lines joins in linear time.
	budget := time.Second
	if raceDetector {
		budget = 20 * time.Second // only a hang fails here
	}
	start := time.Now()
	m.renderMarkdown(strings.Repeat("word\n", 100000), 80)
	if d := time.Since(start); d > budget {
		t.Errorf("joining 100000 soft breaks took %v", d)
	}
	if out := m.markdownLines("**bold\ncontinues**", 60); len(out) != 1 || !strings.Contains(out[0], m.st.fg.Bold(true).Render("bold continues")) {
		t.Errorf("bold across a soft break is not bold: %q", out)
	}
}

// TestMarkdownSanitises: escapes in the text never reach the output; only
// Den's own SGR does.
func TestMarkdownSanitises(t *testing.T) {
	m := Model{st: newStyles(theme.Deepharness)}
	in := "**bold \x1b]52;c;aGk=\x07`co\x1b[31mde`** \x1b]8;;https://evil.test\x1b\\\\link\x1b]8;;\x1b\\\\ \u202eabc\n```\n\x1b[2Jcode\n```"
	for _, l := range m.markdownLines(in, 80) {
		rest := l
		for {
			i := strings.IndexByte(rest, 0x1b)
			if i < 0 {
				break
			}
			rest = rest[i:]
			n := sgrLenTest(rest)
			if n == 0 {
				t.Fatalf("non-SGR escape in %q", l)
			}
			rest = rest[n:]
		}
		if strings.ContainsRune(l, '\u202e') {
			t.Errorf("bidi override survived: %q", l)
		}
	}
}

// sgrLenTest is the length of the SGR sequence s starts with, 0 if it is
// not one.
func sgrLenTest(s string) int {
	if !strings.HasPrefix(s, "\x1b[") {
		return 0
	}
	for i := 2; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9' || c == ';' || c == ':':
		case c == 'm':
			return i + 1
		default:
			return 0
		}
	}
	return 0
}

// TestParseInlineIsLinear: a long hostile line renders quickly.
func TestParseInlineIsLinear(t *testing.T) {
	m := Model{st: newStyles(theme.Deepharness)}
	budget := time.Second
	if raceDetector {
		budget = 20 * time.Second // only a hang fails here
	}
	for _, in := range []string{
		strings.Repeat("*a _b ", 20000),
		strings.Repeat("**", 20000) + "x",
		strings.Repeat("[", 5000) + strings.Repeat("`", 5000),
		strings.Repeat("*a ", 10000) + strings.Repeat("b* ", 10000),
	} {
		start := time.Now()
		m.markdownLines(in, 80)
		t.Logf("%.12q %s", in, time.Since(start))
		if d := time.Since(start); d > budget {
			t.Errorf("%.20q… took %s", in, d)
		}
	}
}

func stripAll(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = ansi.Strip(l)
	}
	return out
}

func equalLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestDiffLines(t *testing.T) {
	m := Model{st: newStyles(theme.Deepharness)}
	hunks := []transcript.Hunk{{OldStart: 39, NewStart: 39, Lines: []string{" Bodies:", "-\tCode: \"a\",", "+\tCode: \"b\",", "+\tMore: 1,", " }"}}}
	got := m.diffLines(hunks, 40)
	want := []string{"  39   Bodies:", "  40 -     Code: \"a\",", "  40 +     Code: \"b\",", "  41 +     More: 1,", "  42   }"}
	if len(got) != len(want) {
		t.Fatalf("got %d lines: %q", len(got), got)
	}
	for i := range want {
		if strings.TrimRight(ansi.Strip(got[i]), " ") != strings.TrimRight(want[i], " ") {
			t.Errorf("line %d = %q, want %q", i, ansi.Strip(got[i]), want[i])
		}
		if w := ansi.StringWidth(got[i]); w > 40 {
			t.Errorf("line %d is %d wide", i, w)
		}
	}
	if w := ansi.StringWidth(got[1]); w != 40 {
		t.Errorf("changed lines fill the width, got %d", w)
	}
}

func TestCodeTakesTheAgentColour(t *testing.T) {
	m := Model{st: newStyles(theme.Deepharness), agents: sample(), store: inbox.New()}
	m.colors = map[string]string{"0123456789abcdef": "#ea9e9e"}
	a := m.agents[0]
	out := m.inAgentColor(a).markdownLines("run `go test` now", 80)[0]
	if !strings.Contains(out, "234;158;158") {
		t.Errorf("inline code should carry the agent colour #ea9e9e: %q", out)
	}
	block := m.inAgentColor(a).codeBlock("ls", 20)
	if !strings.Contains(block[0], "234;158;158") {
		t.Errorf("code block should carry the agent colour: %q", block[0])
	}
}
