package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/review"
	"github.com/GlacierHubAB/den/internal/theme"
)

// spanClass finds the class of the first span on a line whose text is want.
func spanClass(spans []synSpan, want string) (synClass, bool) {
	for _, s := range spans {
		if strings.TrimSpace(s.text) == want {
			return s.cls, true
		}
	}
	return 0, false
}

func TestLexDiffGo(t *testing.T) {
	f := review.ParseUnified("diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n" +
		"@@ -1,4 +1,5 @@\n package x\n-/* old\n-   note */\n+/* new\n+   note */\n+func Run() string { return \"hi\" }\n x := 42\n")[0]
	lines := diffLines(f)
	spans := lexDiff(f.Path, lines)
	if spans == nil {
		t.Fatal("no spans for a Go file")
	}
	checks := []struct {
		line int
		text string
		want synClass
	}{
		{0, "package", synKeyword},
		{1, "/* old", synComment},
		{2, "note */", synComment}, // second line of a block comment, removed side
		{4, "note */", synComment}, // and on the added side
		{5, "func", synKeyword},
		{5, "Run", synFunc},
		{5, "string", synType},
		{5, `"hi"`, synLiteral},
		{6, "42", synLiteral},
	}
	for _, c := range checks {
		got, ok := spanClass(spans[c.line], c.text)
		if !ok || got != c.want {
			t.Errorf("line %d %q: class %v (found %v), want %v; spans %+v", c.line, c.text, got, ok, c.want, spans[c.line])
		}
	}
	// Every line's spans put its text back together.
	for i, d := range lines {
		var b strings.Builder
		for _, s := range spans[i] {
			b.WriteString(s.text)
		}
		if b.String() != d.text {
			t.Errorf("line %d: spans %q, text %q", i, b.String(), d.text)
		}
	}
}

func TestLexDiffUnknownType(t *testing.T) {
	f := review.ParseUnified("diff --git a/notes.zzqq b/notes.zzqq\n--- a/notes.zzqq\n+++ b/notes.zzqq\n@@ -1 +1 @@\n-a\n+func b\n")[0]
	if spans := lexDiff(f.Path, diffLines(f)); spans != nil {
		t.Errorf("unknown extension lexed: %+v", spans)
	}
	// It still renders, plain, with the changed lines tinted.
	m := Model{st: newStyles(theme.Deepharness)}
	lines := diffLines(f)
	e := &synEntry{}
	got := m.codeCell(e, lines, 1, 40, false)
	if ansi.Strip(got) != "+ func b" {
		t.Errorf("plain cell = %q", ansi.Strip(got))
	}
}

func TestCodeCellColours(t *testing.T) {
	f := review.ParseUnified("diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1,2 +1,2 @@\n-return 1\n+return 2\n return 3\n")[0]
	m := Model{st: newStyles(theme.Deepharness)}
	lines := diffLines(f)
	e := &synEntry{spans: lexDiff(f.Path, lines)}
	p := m.st.pal
	sgr := func(hex string, bg bool) string {
		c, _ := hexRGB(hex)
		k := 38
		if bg {
			k = 48
		}
		return fmt.Sprintf("%d;2;%d;%d;%d", k, c[0], c[1], c[2])
	}
	added := m.codeCell(e, lines, 1, 40, false)
	removed := m.codeCell(e, lines, 0, 40, false)
	context := m.codeCell(e, lines, 2, 40, false)
	selected := m.codeCell(e, lines, 1, 40, true)
	for _, c := range []struct {
		name, cell, want string
	}{
		{"added keyword is bright", added, sgr(p.Bright, false)},
		{"added line sits on a green tint", added, sgr(lineBg(p, '+'), true)},
		{"added sign is green", added, sgr(p.Ok, false)},
		{"removed line sits on a red tint", removed, sgr(lineBg(p, '-'), true)},
		{"removed keyword is faded", removed, sgr(mixHex(p.Bright, p.Bg, synFade), false)},
		{"context number is faded", context, sgr(mixHex(p.Attn, p.Bg, synFade), false)},
		{"selection wins over the tint", selected, sgr(p.Sel, true)},
	} {
		if !strings.Contains(c.cell, c.want) {
			t.Errorf("%s: %q lacks %s", c.name, c.cell, c.want)
		}
	}
	if strings.Contains(context, "48;2") {
		t.Errorf("context line has a background: %q", context)
	}
	if strings.Contains(selected, sgr(lineBg(p, '+'), true)) {
		t.Errorf("selected line keeps the tint: %q", selected)
	}
	// Long lines are cut to the width.
	if w := ansi.StringWidth(m.codeCell(&synEntry{}, []diffLine{{kind: '+', text: strings.Repeat("x", 100)}}, 0, 20, false)); w != 22 {
		t.Errorf("truncated width = %d", w)
	}
}

func TestSyntaxCacheReused(t *testing.T) {
	m := reviewModel(t)
	f := m.curFile()
	lines := diffLines(*f)
	synCache.Lock()
	a := m.synFor(*f, lines)
	b := m.synFor(*f, lines)
	synCache.Unlock()
	if a != b {
		t.Error("same file lexed twice")
	}
	m.diffRows(80)
	if !a.drawn[0] || a.width == 0 {
		t.Error("drawn bodies not kept")
	}
	// Another palette is another entry.
	m2 := m
	p := theme.Deepharness
	p.Fg = "#ffffff"
	m2.st = newStyles(p)
	synCache.Lock()
	c := m2.synFor(*f, lines)
	synCache.Unlock()
	if c == a {
		t.Error("palette change reused the old entry")
	}
}

// bigReview is a Review model on a new Go file of n lines.
func bigReview(n int) Model {
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/big.go b/big.go\nnew file mode 100644\n--- /dev/null\n+++ b/big.go\n@@ -0,0 +1,%d @@\n", n)
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "+func f%d(a int) string { return fmt.Sprint(a + %d) } // line %d\n", i, i, i)
	}
	files := review.ParseUnified(b.String())
	m := Model{st: newStyles(theme.Deepharness), agents: sample(), store: inbox.New(), tab: tabReview, width: 160, height: 45}
	m.input = newInput(m.st)
	m.rev = reviewState{works: []work{{SessionID: "s", Root: "/r", Repo: "r", Files: files}}, loadedAt: time.Now(), focus: revDiff}
	return m
}

// TestSyntaxLargeDiffStaysFast checks the property that keeps a big diff
// responsive: a diff under synMaxBytes is lexed once, on the first view, and
// every later key is served from the cache; a diff over it is never lexed at
// all. A wall-clock bound would only measure the machine (the race detector
// alone slows this several times over), so the test counts lexer runs
// instead. The benchmarks keep the timing.
func TestSyntaxLargeDiffStaysFast(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lines int
		lexed int64 // lexer runs for the whole session
	}{
		{"under the cap", 2000, 1},
		{"over the cap", 5000, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synCache.Lock()
			synCache.m = map[string]*synEntry{}
			synCache.Unlock()
			m := bigReview(tc.lines)
			before := synLexed.Load()
			start := time.Now()
			m.View()
			first := time.Since(start)
			if got := synLexed.Load() - before; got != tc.lexed {
				t.Fatalf("first view ran the lexer %d times, want %d", got, tc.lexed)
			}
			start = time.Now()
			for i := 0; i < 20; i++ {
				m.moveReview(1)
				m.View()
			}
			t.Logf("%d lines: first view %v, then %v per key", tc.lines, first, time.Since(start)/20)
			if got := synLexed.Load() - before; got != tc.lexed {
				t.Errorf("20 more keys ran the lexer %d more times; the diff is being re-lexed per key", got-tc.lexed)
			}
		})
	}
}

func BenchmarkReviewKeyLargeDiff(b *testing.B) {
	benchReviewKey(b, 2000)
}

// BenchmarkReviewKeyHugeDiff is a diff over synMaxBytes, drawn plain.
func BenchmarkReviewKeyHugeDiff(b *testing.B) {
	benchReviewKey(b, 5000)
}

func benchReviewKey(b *testing.B, lines int) {
	m := bigReview(lines)
	m.View()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.moveReview(1)
		m.View()
	}
}
