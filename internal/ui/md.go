package ui

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/sanitize"
	"github.com/GlacierHubAB/den/internal/theme"
	"github.com/GlacierHubAB/den/internal/transcript"
)

// codeBlock draws lines as a block: a bar down the left and the code in the
// code colour, wrapped to width.
func (m Model) codeBlock(text string, width int) []string {
	return m.codeBlockN(text, width, 0)
}

// codeBlockN is codeBlock capped at n wrapped lines (0 for no cap), ending
// in an ellipsis when cut; a call reads as a glance, not a listing.
func (m Model) codeBlockN(text string, width, n int) []string {
	s := m.st
	inner := max(8, width-2)
	var out []string
	for _, raw := range strings.Split(strings.ReplaceAll(text, "\t", "    "), "\n") {
		for _, l := range fitLines(strings.Split(ansi.Wrap(raw, inner, ""), "\n"), inner) {
			if n > 0 && len(out) == n {
				last := out[n-1]
				out[n-1] = ansi.Truncate(last, inner+2-1, "") + s.muted.Render("…")
				return out
			}
			out = append(out, s.muted.Render("┃ ")+s.code.Render(l))
		}
	}
	return out
}

// fitLines cuts any line still wider than w (a wide space ansi.Wrap does
// not break at, say) into pieces that fit.
func fitLines(lines []string, w int) []string {
	var out []string
	for _, l := range lines {
		if ansi.StringWidth(l) <= w {
			out = append(out, l)
			continue
		}
		for _, piece := range wrapPlain([]mdSpan{{text: l}}, w, true) {
			var b strings.Builder
			for _, sp := range piece {
				b.WriteString(sp.text)
			}
			out = append(out, b.String())
		}
	}
	return out
}

// diffLines renders hunks the way Claude Code does: a line-number gutter,
// the sign, and removed or added lines on a tinted background filled to
// width. Context lines stay dim.
func (m Model) diffLines(hunks []transcript.Hunk, width int) []string {
	s := m.st
	p := s.pal
	del := lipgloss.NewStyle().Foreground(lipgloss.Color(p.Err)).Background(lipgloss.Color(theme.Tint(p.Err, p.Bg)))
	add := lipgloss.NewStyle().Foreground(lipgloss.Color(p.Ok)).Background(lipgloss.Color(theme.Tint(p.Ok, p.Bg)))
	total := 0
	for _, h := range hunks {
		total += len(h.Lines)
	}
	var out []string
	shown := 0
	for hi, h := range hunks {
		if hi > 0 {
			out = append(out, s.muted.Render("…"))
		}
		oldN, newN := h.OldStart, h.NewStart
		for _, l := range h.Lines {
			if l == "" {
				l = " "
			}
			sign, text := l[:1], l[1:]
			var num int
			style := s.dim
			switch sign {
			case "-":
				num, style = oldN, del
				oldN++
			case "+":
				num, style = newN, add
				newN++
			default:
				num = newN
				oldN++
				newN++
			}
			gutter := fmt.Sprintf("%4d %s ", num, sign)
			if sign == " " {
				gutter = fmt.Sprintf("%4d   ", num)
			}
			body := ansi.Truncate(strings.ReplaceAll(text, "\t", "    "), max(4, width-len(gutter)), "…")
			row := gutter + body
			if pad := width - ansi.StringWidth(row); pad > 0 && sign != " " {
				row += strings.Repeat(" ", pad)
			}
			out = append(out, style.Render(row))
			shown++
			if shown == maxDiffLines && total > maxDiffLines {
				out = append(out, s.muted.Render(fmt.Sprintf("… %d more lines", total-shown)))
				return out
			}
		}
	}
	return out
}

// maxDiffLines caps one file's diff in the pane; the terminal has the rest.
const maxDiffLines = 30

// plainLines wraps text to width in one style, keeping its line breaks.
func (m Model) plainLines(text string, width int, style lipgloss.Style) []string {
	var out []string
	for _, raw := range strings.Split(text, "\n") {
		for _, l := range strings.Split(ansi.Wrap(raw, width, ""), "\n") {
			out = append(out, style.Render(l))
		}
	}
	return out
}

// markdownLines renders a reply with the light markdown treatment, wrapped
// to width: headings bright, quotes behind a bar, lists with their nesting,
// tables in aligned columns, fenced code as a block (coloured when its
// language is known), and inline code, emphasis and links styled. The
// text is sanitised first, so the only escapes in the result are Den's.
func (m Model) markdownLines(text string, width int) []string {
	width = max(1, width)
	if m.hl == nil {
		m.hl = newHLBudget()
	}
	key := m.mdKey(text, width)
	if lines, ok := mdCache.get(key); ok {
		return lines
	}
	short, left := m.hl.short, m.hl.left
	out := m.renderMarkdown(text, width)
	// A text that found the frame's budget already spent is not kept: a
	// later frame, with the rest cached, colours it. One that spent some
	// is kept as drawn, so its colouring is paid for once.
	if m.hl.short == short || m.hl.left < left {
		mdCache.put(key, out)
	}
	return append([]string(nil), out...)
}

// renderMarkdown is markdownLines without the cache.
func (m Model) renderMarkdown(text string, width int) []string {
	mdRendered.Add(1)
	src := strings.Split(sanitize.Text(text), "\n")
	var out []string
	for i := 0; i < len(src); i++ {
		line := src[i]
		if f := mdFence.FindStringSubmatch(line); f != nil {
			indent, mark, lang := len(f[1]), f[2], f[3]
			var code []string
			for i++; i < len(src); i++ {
				if c := mdFence.FindStringSubmatch(src[i]); c != nil && c[2][0] == mark[0] && len(c[2]) >= len(mark) && c[3] == "" {
					break
				}
				code = append(code, trimIndent(src[i], indent))
			}
			out = append(out, m.fencedBlock(strings.Join(code, "\n"), lang, width)...)
			continue
		}
		if i+1 < len(src) && strings.Contains(line, "|") && strings.Contains(src[i+1], "|") && mdTableSep.MatchString(src[i+1]) {
			rows := [][]string{splitRow(line)}
			aligns := tableAligns(src[i+1])
			for i += 2; i < len(src) && strings.Contains(src[i], "|") && strings.TrimSpace(src[i]) != ""; i++ {
				rows = append(rows, splitRow(src[i]))
			}
			i--
			out = append(out, m.tableLines(rows, aligns, width)...)
			continue
		}
		// A paragraph's soft line breaks join it into one line, as in
		// CommonMark, so emphasis can span them; a hard break (two
		// trailing spaces, or a backslash) keeps the line.
		if d, ok := paragraphLine(line); ok {
			parts := []string{line}
			for i+1 < len(src) && !hardBreak(parts[len(parts)-1]) && continues(d, src, i+1) {
				_, next := quoteSplit(src[i+1])
				parts[len(parts)-1] = strings.TrimRight(parts[len(parts)-1], " \t")
				parts = append(parts, strings.TrimLeft(next, " \t"))
				i++
			}
			// A backslash break is markup only when a line follows it.
			if last := parts[len(parts)-1]; i+1 < len(src) && strings.HasSuffix(last, "\\") && hardBreak(last) && continues(d, src, i+1) {
				parts[len(parts)-1] = strings.TrimSuffix(last, "\\")
			}
			line = strings.Join(parts, " ")
		}
		out = append(out, m.blockLine(line, width)...)
	}
	return out
}

// quoteSplit is a line's quote depth and what follows its > marks.
func quoteSplit(line string) (depth int, rest string) {
	for {
		t := strings.TrimLeft(line, " ")
		if len(line)-len(t) > 3 || !strings.HasPrefix(t, ">") {
			return depth, line
		}
		depth++
		line = strings.TrimPrefix(t[1:], " ")
	}
}

// hardBreak says the line ends in a hard line break.
func hardBreak(line string) bool {
	return strings.HasSuffix(line, "  ") || strings.HasSuffix(line, "\\") && !strings.HasSuffix(line, "\\\\")
}

// paragraphLine says line is text that can run on to the next line (a
// paragraph or a list item), and its quote depth.
func paragraphLine(line string) (depth int, ok bool) {
	d, cur := quoteSplit(strings.ReplaceAll(line, "\t", "    "))
	if strings.TrimSpace(cur) == "" || mdRule.MatchString(cur) || mdHeading.MatchString(cur) {
		return 0, false
	}
	return d, true
}

// continues says src[j] carries on a paragraph at quote depth d: text at
// the same depth that starts no block of its own.
func continues(d int, src []string, j int) bool {
	nd, next := quoteSplit(strings.ReplaceAll(src[j], "\t", "    "))
	if nd != d || strings.TrimSpace(next) == "" {
		return false
	}
	if mdFence.MatchString(next) || mdRule.MatchString(next) || mdHeading.MatchString(next) || mdList.MatchString(next) {
		return false
	}
	// Nor the first row of a table.
	return !(j+1 < len(src) && strings.Contains(next, "|") && mdTableSep.MatchString(src[j+1]))
}

var (
	mdFence    = regexp.MustCompile("^( {0,3})(`{3,}|~{3,})[ \t]*([^`\\s]*)[^`]*$")
	mdRule     = regexp.MustCompile(`^ {0,3}(?:(?:-[ \t]*){3,}|(?:\*[ \t]*){3,}|(?:_[ \t]*){3,})$`)
	mdHeading  = regexp.MustCompile(`^ {0,3}(#{1,6})(?:[ \t]+(.*?))?(?:[ \t]+#+)?[ \t]*$`)
	mdList     = regexp.MustCompile(`^([ \t]*)([-*+•]|\d{1,9}[.)])[ \t]+(.*)$`)
	mdTableSep = regexp.MustCompile(`^[ \t]*\|?[ \t]*:?-+:?[ \t]*(\|[ \t]*:?-+:?[ \t]*)*\|?[ \t]*$`)
)

// blockLine renders one line outside fences and tables.
func (m Model) blockLine(line string, width int) []string {
	s := m.st
	line = strings.ReplaceAll(line, "\t", "    ")
	if strings.TrimSpace(line) == "" {
		return []string{""}
	}
	// Quotes nest: each > is a bar, and what follows is a line of its own.
	depth, line := quoteSplit(line)
	base := s.fg
	var bar string
	if depth > 0 {
		base = s.dim
		bar = s.muted.Render(strings.Repeat("│ ", depth))
	}
	barW := 2 * depth
	switch {
	case mdRule.MatchString(line):
		return []string{bar + s.muted.Render(strings.Repeat("─", max(4, min(width-barW, 24))))}
	case mdHeading.MatchString(line):
		h := mdHeading.FindStringSubmatch(line)
		return m.wrapSpans(m.mentionSpans(parseInline(h[2]), depth > 0), s.bright.Bold(true), width, bar, bar, barW)
	}
	if l := mdList.FindStringSubmatch(line); l != nil && !mdRule.MatchString(line) {
		level := min(ansi.StringWidth(l[1])/2, 6)
		marker := l[2]
		if !unicode.IsDigit(rune(marker[0])) {
			marker = []string{"•", "◦", "▪"}[level%3]
		}
		lead := strings.Repeat("  ", level)
		first := bar + lead + s.dim.Render(marker) + " "
		w := barW + len(lead) + ansi.StringWidth(marker) + 1
		return m.wrapSpans(m.mentionSpans(parseInline(l[3]), depth > 0), base, width, first, bar+strings.Repeat(" ", w-barW), w)
	}
	// A paragraph keeps a little of its indent, so a list item's second
	// paragraph still sits under it.
	t := strings.TrimLeft(line, " ")
	indent := strings.Repeat(" ", min(len(line)-len(t), 8, width/3))
	return m.wrapSpans(m.mentionSpans(parseInline(t), depth > 0), base, width, bar+indent, bar+indent, barW+len(indent))
}

// spanStyle is the style for a span in base text.
func (m Model) spanStyle(a mdAttr, base lipgloss.Style) lipgloss.Style {
	st := base
	switch {
	case a.code:
		st = m.st.code
	case a.url:
		st = m.st.muted
	case a.mention != "":
		// The agent's own colour, the way its card and inbox items show it.
		st = lipgloss.NewStyle().Foreground(lipgloss.Color(m.colorFor(a.mention))).Bold(true)
	}
	if a.bold {
		st = st.Bold(true)
	}
	if a.italic {
		st = st.Italic(true)
	}
	return st
}

// renderSpan styles one run of text. Underline (a link) and strikethrough
// are set around the whole run here: lipgloss draws them a rune at a
// time, which splits an emoji sequence and widens the line.
func (m Model) renderSpan(a mdAttr, base lipgloss.Style, text string) string {
	out := m.spanStyle(a, base).Render(text)
	var sgr string
	if a.link && !a.code && !a.url {
		sgr += "\x1b[4m"
	}
	if a.strike {
		sgr += "\x1b[9m"
	}
	if sgr == "" {
		return out
	}
	// The run's own style ends with a reset, which clears these too.
	return sgr + out + "\x1b[m"
}

// renderSpans styles a line's spans; each line stands alone, so markup
// carried over a wrap opens again on the next line and nothing leaks.
func (m Model) renderSpans(spans []mdSpan, base lipgloss.Style) string {
	var b strings.Builder
	for i := 0; i < len(spans); {
		j, text := i+1, spans[i].text
		for ; j < len(spans) && spans[j].a == spans[i].a; j++ {
			text += spans[j].text
		}
		b.WriteString(m.renderSpan(spans[i].a, base, text))
		i = j
	}
	return b.String()
}

// wrapSpans wraps spans to width at spaces, by display width, breaking a
// word only when it is wider than a line. first leads the first line and
// cont the others; both are lead columns wide.
func (m Model) wrapSpans(spans []mdSpan, base lipgloss.Style, width int, first, cont string, lead int) []string {
	var out []string
	for i, l := range wrapPlain(spans, max(4, width-lead), false) {
		p := cont
		if i == 0 {
			p = first
		}
		out = append(out, p+m.renderSpans(l, base))
	}
	return out
}

// wrapPlain wraps spans to lines no wider than width. Words break at
// spaces, and the spaces at a break are dropped; a word wider than a line
// is cut by display width, never inside a character. hard cuts at width
// and keeps every space, for code.
func wrapPlain(spans []mdSpan, width int, hard bool) [][]mdSpan {
	type word struct {
		parts []mdSpan
		w     int
		space bool
	}
	// Split into alternating runs of spaces and of the rest.
	var words []word
	for _, sp := range spans {
		for rest := sp.text; rest != ""; {
			sp1 := rest[0] == ' '
			j := 0
			for j < len(rest) && (rest[j] == ' ') == sp1 && !hard {
				j++
			}
			if hard {
				j = len(rest)
			}
			piece := mdSpan{rest[:j], sp.a}
			rest = rest[j:]
			if k := len(words) - 1; k >= 0 && (words[k].space == sp1 || hard) {
				words[k].parts = append(words[k].parts, piece)
				words[k].w += ansi.StringWidth(piece.text)
				continue
			}
			words = append(words, word{[]mdSpan{piece}, ansi.StringWidth(piece.text), sp1 && !hard})
		}
	}
	var lines [][]mdSpan
	var cur []mdSpan
	curW := 0
	var gap []mdSpan
	gapW := 0
	newline := func() {
		lines = append(lines, cur)
		cur, curW, gap, gapW = nil, 0, nil, 0
	}
	for _, w := range words {
		if w.space {
			if curW > 0 {
				gap, gapW = append(gap, w.parts...), gapW+w.w
			}
			continue
		}
		if curW > 0 && curW+gapW+w.w > width {
			newline()
		}
		if curW > 0 {
			cur, curW = append(cur, gap...), curW+gapW
		}
		gap, gapW = nil, 0
		for _, p := range w.parts {
			for p.text != "" {
				// Measure and cut only as much as the room could hold, so
				// a long word costs its length, not its length per line.
				room := width - curW
				prefix := runePrefix(p.text, 32*(room+1))
				if len(prefix) == len(p.text) {
					if pw := ansi.StringWidth(p.text); curW+pw <= width {
						cur, curW = append(cur, p), curW+pw
						break
					}
				}
				head := ansi.Truncate(prefix, room, "")
				if head == "" && curW == 0 { // one character wider than the line
					head = string([]rune(p.text)[:1])
				}
				if head != "" {
					cur, curW = append(cur, mdSpan{head, p.a}), curW+ansi.StringWidth(head)
					p.text = p.text[len(head):]
				}
				if head == "" || len(head) < len(prefix) { // the line is full
					newline()
				}
			}
		}
	}
	if curW > 0 || len(lines) == 0 {
		lines = append(lines, cur)
	}
	return lines
}

// Some chroma lexers are quadratic on hostile input (a Rust raw string
// opener repeated takes seconds at 64 KB, about 45 ms at 4 KB), and the
// view is drawn on every key press. So a block is coloured only when it is small, the colouring
// in one frame is capped, and what is drawn is kept.
const (
	hlBlockMax  = 4 << 10  // the largest block coloured
	hlFrameMax  = 16 << 10 // the code coloured in one frame
	mdCacheMax  = 256      // rendered texts kept
	mdCacheSize = 8 << 20  // and their total size
)

// hlBudget is the colouring one frame may still do. short is set once a
// block was drawn plain for want of it.
type hlBudget struct {
	left  int
	short bool
}

func newHLBudget() *hlBudget { return &hlBudget{left: hlFrameMax} }

func (b *hlBudget) take(n int) bool {
	if n > b.left {
		b.short = true
		return false
	}
	b.left -= n
	return true
}

// mdLexed counts fenced blocks handed to chroma, and mdRendered texts
// laid out afresh, for the tests.
var mdLexed, mdRendered atomic.Int64

// mdKey identifies a rendering: the text, the width, and everything about
// the model that changes how it looks.
func (m Model) mdKey(text string, width int) string {
	h := sha256.Sum256([]byte(text))
	return fmt.Sprintf("%x|%d|%v|%v|%v|%s", h, width, m.st.pal, m.st.code.GetForeground(), m.plainCode, m.mentionKey())
}

// mdCache keeps rendered markdown, so a long chat or a detail pane full of
// replies is laid out once, not on every frame.
var mdCache = &renderCache{m: map[string][]string{}}

type renderCache struct {
	sync.Mutex
	m    map[string][]string
	size int
}

func (c *renderCache) get(key string) ([]string, bool) {
	c.Lock()
	defer c.Unlock()
	lines, ok := c.m[key]
	if !ok {
		return nil, false
	}
	return append([]string(nil), lines...), true
}

func (c *renderCache) put(key string, lines []string) {
	n := 0
	for _, l := range lines {
		n += len(l)
	}
	if n > mdCacheSize/4 {
		return // too big to be worth keeping
	}
	c.Lock()
	defer c.Unlock()
	if _, ok := c.m[key]; ok {
		return
	}
	for k, old := range c.m { // map order is random: evict any
		if len(c.m) < mdCacheMax && c.size+n <= mdCacheSize {
			break
		}
		for _, l := range old {
			c.size -= len(l)
		}
		delete(c.m, k)
	}
	c.m[key] = append([]string(nil), lines...)
	c.size += n
}

// runePrefix is at most n bytes of s, cut at a rune boundary.
func runePrefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// fencedBlock draws a fenced block: in the code colour, or coloured by
// token when chroma knows the language.
func (m Model) fencedBlock(code, lang string, width int) []string {
	if lang == "" || m.plainCode || len(code) > hlBlockMax {
		return m.codeBlock(code, width)
	}
	lexer := lexers.Get(lang)
	if lexer == nil {
		return m.codeBlock(code, width)
	}
	if !m.hl.take(len(code)) {
		return m.codeBlock(code, width)
	}
	mdLexed.Add(1)
	it, err := chroma.Coalesce(lexer).Tokenise(nil, code)
	if err != nil {
		return m.codeBlock(code, width)
	}
	s := m.st
	styleOf := map[synClass]lipgloss.Style{
		synKeyword: s.bright.Bold(true), synHeading: s.bright.Bold(true),
		synFunc: s.code, synType: s.code, synLiteral: s.attn, synComment: s.dim.Italic(true),
	}
	// Each line's spans, and beside them the class of each.
	var lines [][]mdSpan
	var classes [][]synClass
	cur, cls := []mdSpan{}, []synClass{}
	for _, tok := range it.Tokens() {
		c := synClassOf(tok.Type)
		for pi, part := range strings.Split(strings.ReplaceAll(tok.Value, "\t", "    "), "\n") {
			if pi > 0 {
				lines, classes = append(lines, cur), append(classes, cls)
				cur, cls = []mdSpan{}, []synClass{}
			}
			if part != "" {
				cur, cls = append(cur, mdSpan{text: part}), append(cls, c)
			}
		}
	}
	if len(cur) > 0 {
		lines, classes = append(lines, cur), append(classes, cls)
	}
	inner := max(8, width-2)
	var out []string
	for li, l := range lines {
		// Wrapping can split a span; walk the pieces back to their class.
		k, used := 0, 0
		for _, wl := range wrapPlain(l, inner, true) {
			var b strings.Builder
			for _, piece := range wl {
				for piece.text != "" && k < len(l) {
					take := min(len(piece.text), len(l[k].text)-used)
					st, ok := styleOf[classes[li][k]]
					if !ok {
						st = s.fg
					}
					b.WriteString(st.Render(piece.text[:take]))
					piece.text = piece.text[take:]
					used += take
					if used == len(l[k].text) {
						k, used = k+1, 0
					}
				}
			}
			out = append(out, s.muted.Render("┃ ")+b.String())
		}
	}
	return out
}

// trimIndent removes up to n leading spaces.
func trimIndent(s string, n int) string {
	for n > 0 && strings.HasPrefix(s, " ") {
		s, n = s[1:], n-1
	}
	return s
}

// splitRow splits a table row into its cells at pipes that are not escaped
// or inside code.
func splitRow(line string) []string {
	t := strings.TrimSpace(line)
	t = strings.TrimPrefix(t, "|")
	if strings.HasSuffix(t, "|") && !strings.HasSuffix(t, `\|`) {
		t = t[:len(t)-1]
	}
	ix := newLineIndex(t)
	var cells []string
	var b strings.Builder
	for i := 0; i < len(t); i++ {
		switch {
		case t[i] == '\\' && i+1 < len(t) && t[i+1] == '|':
			b.WriteByte('|')
			i++
		case t[i] == '`':
			n := runLen(t, i, '`')
			end := ix.closingTicks(i+n, n)
			if end < 0 {
				end = i
			}
			b.WriteString(t[i : end+n])
			i = end + n - 1
		case t[i] == '|':
			cells = append(cells, strings.TrimSpace(b.String()))
			b.Reset()
		default:
			b.WriteByte(t[i])
		}
	}
	return append(cells, strings.TrimSpace(b.String()))
}

// tableAligns reads the alignment row: 'l', 'c' or 'r' per column.
func tableAligns(sep string) []byte {
	var out []byte
	for _, c := range splitRow(sep) {
		switch {
		case strings.HasPrefix(c, ":") && strings.HasSuffix(c, ":"):
			out = append(out, 'c')
		case strings.HasSuffix(c, ":"):
			out = append(out, 'r')
		default:
			out = append(out, 'l')
		}
	}
	return out
}

// tableLines draws a table in aligned columns, the header bold over a
// rule. Columns that do not fit share the width and wrap their cells; a
// table too narrow even for that is drawn a row to a line.
func (m Model) tableLines(rows [][]string, aligns []byte, width int) []string {
	s := m.st
	cols := len(aligns)
	cells := make([][][]mdSpan, len(rows))
	natural := make([]int, cols)
	for r, row := range rows {
		cells[r] = make([][]mdSpan, cols)
		for c := 0; c < cols; c++ {
			if c < len(row) {
				cells[r][c] = m.mentionSpans(parseInline(row[c]), false)
			}
			w := 0
			for _, sp := range cells[r][c] {
				w += ansi.StringWidth(sp.text)
			}
			natural[c] = max(natural[c], w, 1)
		}
	}
	sepW := 3 * (cols - 1)
	room := width - sepW
	perRow := func() []string {
		var out []string
		for r, row := range rows {
			base := s.fg
			if r == 0 {
				base = base.Bold(true)
			}
			out = append(out, m.wrapSpans(m.mentionSpans(parseInline(strings.Join(row, " · ")), false), base, width, "", "", 0)...)
		}
		return out
	}
	if room < 4*cols {
		return perRow()
	}
	// Share the room: narrow columns keep their width, the rest split
	// what is left evenly.
	widths := append([]int(nil), natural...)
	for {
		total := 0
		for _, w := range widths {
			total += w
		}
		if total <= room {
			break
		}
		wide := 0
		left := room
		fair := room / cols
		for c := range widths {
			if natural[c] <= fair {
				left -= natural[c]
			} else {
				wide++
			}
		}
		share := max(3, left/max(1, wide))
		changed := false
		for c := range widths {
			if natural[c] > fair && widths[c] != share {
				widths[c], changed = share, true
			}
		}
		if !changed {
			break
		}
	}
	bar := s.muted.Render(" │ ")
	var out []string
	for r := range rows {
		base := s.fg
		if r == 0 {
			base = s.bright.Bold(true)
		}
		wrapped := make([][][]mdSpan, cols)
		height := 1
		for c := 0; c < cols; c++ {
			wrapped[c] = wrapPlain(cells[r][c], widths[c], false)
			height = max(height, len(wrapped[c]))
		}
		for h := 0; h < height; h++ {
			var b strings.Builder
			for c := 0; c < cols; c++ {
				if c > 0 {
					b.WriteString(bar)
				}
				var l []mdSpan
				if h < len(wrapped[c]) {
					l = wrapped[c][h]
				}
				w := 0
				for _, sp := range l {
					w += ansi.StringWidth(sp.text)
				}
				pad := max(0, widths[c]-w)
				lp := 0
				switch aligns[c] {
				case 'r':
					lp = pad
				case 'c':
					lp = pad / 2
				}
				b.WriteString(strings.Repeat(" ", lp) + m.renderSpans(l, base))
				if c < cols-1 {
					b.WriteString(strings.Repeat(" ", pad-lp))
				}
			}
			out = append(out, b.String())
		}
		if r == 0 {
			var b strings.Builder
			for c, w := range widths {
				if c > 0 {
					b.WriteString("─┼─")
				}
				b.WriteString(strings.Repeat("─", w))
			}
			out = append(out, s.muted.Render(b.String()))
		}
	}
	// Cells the columns could not hold (a character wider than its
	// column) would push the row past the pane: draw it a row to a line.
	for _, l := range out {
		if ansi.StringWidth(l) > width {
			return perRow()
		}
	}
	return out
}
