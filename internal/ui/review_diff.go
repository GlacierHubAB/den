package ui

import (
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/review"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// diffLine is one line of a file's diff that the cursor can stand on.
type diffLine struct {
	kind  byte   // '+', '-' or ' '
	side  string // "new" or "old": which file the number belongs to
	num   int    // line number on that side
	oldN  int    // 0 when the line is not in the old file
	newN  int    // 0 when the line is not in the new file
	text  string // without the +/- marker
	owner string // session that wrote it, "" for context or nobody
	hunk  int
}

// diffRow is one rendered row of the diff pane.
type diffRow struct {
	text string
	line int // index into the diffLines, -1 for headers and comments
}

// diffLines flattens a file's hunks into the lines the cursor moves over.
func diffLines(f review.File) []diffLine {
	var out []diffLine
	for hi, h := range f.Hunks {
		oldN, newN := h.OldStart, h.NewStart
		for li, l := range h.Lines {
			if l == "" {
				continue
			}
			d := diffLine{kind: l[0], text: l[1:], hunk: hi}
			if hi < len(f.Owner) && li < len(f.Owner[hi]) {
				d.owner = f.Owner[hi][li]
			}
			switch l[0] {
			case '+':
				d.side, d.num, d.newN = "new", newN, newN
				newN++
			case '-':
				d.side, d.num, d.oldN = "old", oldN, oldN
				oldN++
			default:
				d.side, d.num, d.oldN, d.newN = "new", newN, oldN, newN
				oldN++
				newN++
			}
			out = append(out, d)
		}
	}
	return out
}

// selection returns the first and last selected line indexes.
func (m Model) selection() (lo, hi int) {
	lo, hi = m.rev.line, m.rev.line
	if m.rev.ranged {
		lo, hi = min(m.rev.anchor, m.rev.line), max(m.rev.anchor, m.rev.line)
	}
	return
}

// moveReview handles ↑ ↓ and paging in whichever Review pane has focus.
// extend grows the diff selection instead of moving a single cursor.
func (m *Model) moveReview(delta int) { m.moveReviewSel(delta, false) }

func (m *Model) moveReviewSel(delta int, extend bool) {
	switch m.rev.focus {
	case revWork:
		if next := clamp(m.rev.cur+delta, 0, max(0, len(m.rev.works)-1)); next != m.rev.cur {
			m.rev.cur, m.rev.fileCur = next, 0
			m.resetCursor()
		}
	case revFiles:
		if w := m.curWork(); w != nil {
			if next := clamp(m.rev.fileCur+delta, 0, max(0, len(w.Files)-1)); next != m.rev.fileCur {
				m.rev.fileCur = next
				m.resetCursor()
			}
		}
	case revDiff:
		f := m.curFile()
		if f == nil {
			return
		}
		n := len(diffLines(*f))
		if n == 0 {
			return
		}
		switch {
		case extend && !m.rev.ranged:
			m.rev.anchor, m.rev.ranged = m.rev.line, true
		case !extend:
			m.rev.ranged = false
		}
		m.rev.line = clamp(m.rev.line+delta, 0, n-1)
		m.followCursor()
	}
}

// resetCursor puts the cursor on the first changed line of a newly shown
// file, with the view at the top.
func (m *Model) resetCursor() {
	m.rev.scroll, m.rev.ranged, m.rev.dragging = 0, false, false
	m.rev.line = 0
	if f := m.curFile(); f != nil {
		for i, d := range diffLines(*f) {
			if d.kind != ' ' {
				m.rev.line = i
				break
			}
		}
	}
	m.followCursor()
}

// followCursor scrolls the diff so the cursor row is in view.
func (m *Model) followCursor() {
	lay := m.reviewLayout()
	rows := m.diffRowsDrawn(lay.diffW-2, 0, 0)
	h := lay.paneH - 2
	at := -1
	for i, r := range rows {
		if r.line == m.rev.line {
			at = i
			break
		}
	}
	if at < 0 || h <= 0 {
		return
	}
	// Keep a row of the comment block below the cursor visible too.
	switch {
	case at < m.rev.scroll+1:
		m.rev.scroll = max(0, at-1)
	case at > m.rev.scroll+h-2:
		m.rev.scroll = at - h + 2
	}
	m.rev.scroll = clamp(m.rev.scroll, 0, max(0, len(rows)-h))
}

// ownerColor is the colour of the agent behind a session, or "" when the
// session is not in the fleet any more.
func (m Model) ownerColor(session string) string {
	if session == "" {
		return ""
	}
	if a, ok := m.agentFor(session); ok {
		return m.agentColor(a)
	}
	return ""
}

// reviewLayout is where each Review pane sits on screen. Drawing and mouse
// hit-testing both use it, so a click always lands where the eye sees.
type reviewLayout struct {
	top, paneH int // first pane row on screen, and the panes' height
	diffOnly   bool
	lw         int // left column width
	workH      int // height of the Recent work box
	diffX      int // first column of the diff box
	diffW      int
	notesX     int // first column of the comments box, 0 when not shown
	notesW     int
}

func (m Model) reviewLayout() reviewLayout {
	w := m.width
	if w <= 0 {
		w = 120
	}
	top, h := m.paneArea()
	lay := reviewLayout{top: top, paneH: h}
	// Too narrow or too short for the side panes: the diff alone. Below 12
	// rows the two left boxes would get less than their borders need.
	if w < 72 || h < 12 {
		lay.diffOnly, lay.diffW = true, w
		return lay
	}
	lay.lw = clamp(w*28/100, 32, 46)
	lay.workH = min(max(3*len(m.rev.works)+4, 7), h/2)
	if len(m.rev.works) == 0 {
		lay.workH = h / 2
	}
	lay.diffX = lay.lw
	lay.diffW = w - lay.lw
	if lay.diffW >= 110 {
		lay.notesW = clamp(w*22/100, 32, 44)
		lay.diffW -= lay.notesW
		lay.notesX = lay.diffX + lay.diffW
	}
	return lay
}

func (m Model) reviewPanes(w, h int) string {
	lay := m.reviewLayout()
	if lay.diffOnly {
		return m.box(m.diffTitle(), m.diffPane(w-2, h-2), w, h, true)
	}
	filesH := h - lay.workH
	left := lipgloss.JoinVertical(lipgloss.Left,
		m.box("Recent work", m.workPane(lay.lw-2, lay.workH-2), lay.lw, lay.workH, m.rev.focus == revWork),
		m.box(m.filesTitle(), m.filesPane(lay.lw-2, filesH-2), lay.lw, filesH, m.rev.focus == revFiles),
	)
	parts := []string{left, m.box(m.diffTitle(), m.diffPane(lay.diffW-2, h-2), lay.diffW, h, m.rev.focus == revDiff)}
	if lay.notesW > 0 {
		parts = append(parts, m.box(m.notesTitle(), m.notesPane(lay.notesW-2, h-2), lay.notesW, h, false))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}

func (m Model) filesTitle() string {
	if w := m.curWork(); w != nil {
		return "Files · " + strings.SplitN(w.Repo, " · ", 2)[0]
	}
	return "Files"
}

func (m Model) diffTitle() string {
	if f := m.curFile(); f != nil {
		return sanitize.Code(f.Path)
	}
	return "Diff"
}

func (m Model) workPane(w, h int) string {
	s := m.st
	if len(m.rev.works) == 0 {
		var lines []string
		switch {
		case m.rev.loading || m.rev.loadedAt.IsZero():
			lines = append(lines, s.dim.Render(" Reading changes…"))
		default:
			note := m.rev.note
			if note == "" {
				note = "No agent has changed anything yet."
			}
			for _, l := range strings.Split(ansi.Wrap(note, max(10, w-2), ""), "\n") {
				lines = append(lines, s.dim.Render(" "+l))
			}
		}
		for _, e := range m.rev.errs {
			lines = append(lines, s.err.Render(" "+ansi.Truncate(e, w-2, "…")))
		}
		return strings.Join(append(lines, m.remoteWorkLines(w)...), "\n")
	}
	var lines []string
	for i, wk := range m.rev.works {
		sel := i == m.rev.cur
		fg, dim, muted, _, ok, attn, errS, _ := m.rowStyles(sel)
		pad := lipgloss.NewStyle()
		if sel {
			pad = m.st.sel
		}
		name, task, color, gone := "unknown session", "", s.pal.Muted, false
		if a, found := m.agentFor(wk.SessionID); found {
			name, task, color, gone = a.Name+hostChip(a), a.Task(), m.agentColor(a), a.Status == fleet.Gone
		}
		stripe := lipgloss.NewStyle().Foreground(lipgloss.Color(color))
		nameS := lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(true)
		if sel {
			stripe = stripe.Background(lipgloss.Color(s.pal.Sel))
			nameS = nameS.Background(lipgloss.Color(s.pal.Sel))
		}
		bar := stripe.Render("▌")
		meta := dim.Render(wk.Repo + " · ")
		if wk.Worktree && wk.Ahead > 0 {
			meta += attn.Render(commitWord(wk.Ahead)) + dim.Render(" · ")
		}
		meta += ok.Render(fmt.Sprintf("+%d", wk.Added)) + dim.Render(" ") + errS.Render(fmt.Sprintf("−%d", wk.Removed))
		if n := m.draftCount(wk.Root, wk.SessionID, ""); n > 0 {
			meta += dim.Render(" · ") + attn.Render(fmt.Sprintf("%d ✎", n))
		}
		if !wk.Last.IsZero() {
			meta += dim.Render(" · " + humanAge(time.Since(wk.Last)))
		}
		if gone {
			meta += dim.Render(" · ") + errS.Render("✕ gone")
		}
		if wk.Cut != "" {
			meta += dim.Render(" · ") + attn.Render("cut")
		}
		lines = append(lines,
			fill(bar+nameS.Render(" "+ansi.Truncate(name, w-3, "…")), w, pad),
			fill(bar+fg.Render(" "+ansi.Truncate(task, w-3, "…")), w, pad),
			fill(bar+muted.Render(" ")+ansi.Truncate(meta, w-3, "…"), w, pad),
		)
	}
	start := scrollStart(len(lines), 3*m.rev.cur, 3, h)
	lines = append(lines, m.remoteWorkLines(w)...)
	return strings.Join(lines[start:min(len(lines), start+h)], "\n")
}

// remoteWorkLines lists, greyed out under the work list, the remote agents
// with no work listed: why their diff stays on their host (its policy is
// not full, or it is offline), or that it is being read.
func (m Model) remoteWorkLines(w int) []string {
	s := m.st
	var out []string
	host := projectHost(m.proj.cur)
	listed := map[string]bool{}
	for _, wk := range m.rev.all {
		if wk.Host != "" {
			listed[wk.SessionID] = true
		}
	}
	for _, a := range m.agents {
		if a.Host == "" || a.Status == fleet.Gone || (host != "" && a.Host != host) || listed[a.Session.SessionID] {
			continue
		}
		if len(out) == 0 {
			out = append(out, "")
		}
		out = append(out, s.muted.Render(" "+ansi.Truncate(a.Name+hostChip(a), w-2, "…")))
		for _, l := range strings.Split(ansi.Wrap(m.remoteNote(a), max(10, w-2), ""), "\n") {
			out = append(out, s.muted.Render(" "+l))
		}
	}
	return out
}

// fileRows lists, for each row of the Files pane, the file it shows, or -1
// for a folder heading or a note.
func fileRows(wk *work) []int {
	var rows []int
	// A remote work's note comes first (filesPane draws it).
	if wk.Cut != "" {
		rows = append(rows, -1)
	}
	lastDir := "\x00"
	for i, f := range wk.Files {
		dir := filepath.Dir(f.Path)
		if dir != "." && dir != lastDir {
			rows = append(rows, -1)
		}
		lastDir = dir
		rows = append(rows, i)
	}
	return rows
}

func (m Model) filesPane(w, h int) string {
	s := m.st
	wk := m.curWork()
	if wk == nil {
		return ""
	}
	var lines []string
	if wk.Cut != "" {
		lines = append(lines, s.attn.Render(" "+ansi.Truncate(wk.Cut, w-2, "…")))
	}
	selLine := 0
	for _, i := range fileRows(wk) {
		if i < 0 {
			continue // headings are drawn just before their first file
		}
		f := wk.Files[i]
		dir, name := filepath.Split(sanitize.Code(f.Path))
		indent := ""
		if dir != "" {
			indent = "  "
			if i == 0 || filepath.Dir(wk.Files[i-1].Path) != filepath.Dir(f.Path) {
				lines = append(lines, s.muted.Render(" "+ansi.Truncate(dir, w-2, "…")))
			}
		}
		sel := i == m.rev.fileCur
		fg, dim, _, _, ok, attn, errS, _ := m.rowStyles(sel)
		pad := lipgloss.NewStyle()
		if sel {
			pad = s.sel
			selLine = len(lines)
		}
		sq := lipgloss.NewStyle().Foreground(lipgloss.Color(m.fileColor(f, wk.SessionID)))
		if sel {
			sq = sq.Background(lipgloss.Color(s.pal.Sel))
		}
		a, r := f.Counts()
		counts := ok.Render(fmt.Sprintf("+%d", a)) + dim.Render(" ") + errS.Render(fmt.Sprintf("−%d", r))
		switch {
		case f.Unsafe:
			counts = attn.Render("not shown")
		case f.Binary:
			counts = dim.Render("binary")
		case f.Deleted:
			counts = errS.Render("deleted")
		case f.Created && r == 0:
			counts = ok.Render(fmt.Sprintf("+%d", a)) + dim.Render(" new")
		}
		if n := m.draftCount(wk.Root, "", f.Path); n > 0 {
			counts += dim.Render(" ") + attn.Render(strconv.Itoa(n))
		}
		left := fg.Render(" "+indent) + sq.Render("■") + fg.Render(" "+name)
		room := w - ansi.StringWidth(counts) - 1
		left = ansi.Truncate(left, max(1, room-1), "…")
		gap := max(1, room-ansi.StringWidth(left))
		lines = append(lines, left+pad.Render(strings.Repeat(" ", gap))+counts+pad.Render(" "))
	}
	start := scrollStart(len(lines), selLine, 1, h)
	return strings.Join(lines[start:min(len(lines), start+h)], "\n")
}

// fileColor is the colour of the square in front of a file: the agent of
// this work when it wrote most of the file's changed lines, otherwise the
// one that did, grey when no agent did.
func (m Model) fileColor(f review.File, session string) string {
	count := map[string]int{}
	for _, own := range f.Owner {
		for _, o := range own {
			if o != "" {
				count[o]++
			}
		}
	}
	best, n := "", 0
	for o, c := range count {
		if c > n || c == n && o == session {
			best, n = o, c
		}
	}
	if c := m.ownerColor(best); c != "" {
		return c
	}
	return m.st.pal.Muted
}

func (m Model) diffPane(w, h int) string {
	s := m.st
	if m.curFile() == nil {
		if m.rev.loading && len(m.rev.works) == 0 {
			return s.dim.Render(" Reading changes…")
		}
		return s.dim.Render(" Pick some work on the left to see its diff.")
	}
	// Lay the rows out, then draw only the ones on screen.
	n := len(m.diffRowsDrawn(w, 0, 0))
	scroll := clamp(m.rev.scroll, 0, max(0, n-h))
	rows := m.diffRowsDrawn(w, scroll, scroll+h)
	var out []string
	for _, r := range rows[scroll:min(len(rows), scroll+h)] {
		out = append(out, r.text)
	}
	return strings.Join(out, "\n")
}

// diffRows renders the current file: a header, then each hunk as numbered
// lines with a stripe in the colour of the agent that wrote each change,
// the selection highlighted and comments drawn under the line they end on.
func (m Model) diffRows(w int) []diffRow { return m.diffRowsDrawn(w, 0, math.MaxInt) }

// diffRowsDrawn is diffRows with the code drawn only on rows from up to
// to; the other code rows keep their line but no text. Moving the cursor
// lays out the whole file but only draws a screenful.
func (m Model) diffRowsDrawn(w, from, to int) []diffRow {
	s := m.st
	f := m.curFile()
	wk := m.curWork()
	if f == nil || wk == nil {
		return nil
	}
	hdr := func(t string) diffRow { return diffRow{text: t, line: -1} }
	by := "by " + m.nameFor(wk.SessionID)
	if a, r := ownedCounts(*f, wk.SessionID); a+r > 0 {
		by += fmt.Sprintf(" · +%d −%d of it", a, r)
	}
	rows := []diffRow{hdr(s.bright.Render(" "+sanitize.Code(f.Path)) + s.dim.Render("  "+by)), hdr("")}
	switch {
	case f.Unsafe:
		// The name above is cleaned and may not be the file's real one.
		return append(rows, hdr(s.dim.Render(" Content "+review.UnsafeNote+".")))
	case f.Binary:
		return append(rows, hdr(s.dim.Render(" Binary file, not shown.")))
	case len(f.Hunks) == 0:
		return append(rows, hdr(s.dim.Render(" No line changes (mode or rename only).")))
	}

	lines := diffLines(*f)
	numW := 4
	for _, d := range lines {
		numW = max(numW, len(strconv.Itoa(max(d.oldN, d.newN))))
	}
	textW := max(1, w-2-2*numW-4)
	lo, hi := m.selection()
	focused := m.rev.focus == revDiff
	comments := m.fileComments(wk.Root, f.Path)
	bg := lipgloss.Color(s.pal.Sel)
	var syn *synEntry // lexed on the first row drawn

	hunk := -1
	for i, d := range lines {
		if d.hunk != hunk {
			if hunk >= 0 {
				rows = append(rows, hdr(""))
			}
			hunk = d.hunk
			h := f.Hunks[d.hunk]
			rows = append(rows, hdr(s.muted.Render(fmt.Sprintf(" @@ -%d +%d @@", h.OldStart, h.NewStart))))
		}
		if at := len(rows); at < from || at >= to {
			rows = append(rows, diffRow{line: i})
			rows = m.appendComments(rows, comments, d, w)
			continue
		}
		if syn == nil {
			synCache.Lock()
			defer synCache.Unlock()
			syn = m.synFor(*f, lines)
		}
		inSel := focused && i >= lo && i <= hi
		st := func(base lipgloss.Style) lipgloss.Style {
			if inSel {
				return base.Background(bg)
			}
			return base
		}
		stripe := st(lipgloss.NewStyle()).Render(" ")
		if d.kind != ' ' {
			c := m.ownerColor(d.owner)
			if c == "" {
				c = s.pal.Muted // a change no agent made
			}
			stripe = st(lipgloss.NewStyle().Foreground(lipgloss.Color(c))).Render("▌")
		}
		oldS, newS := "", ""
		if d.oldN > 0 {
			oldS = strconv.Itoa(d.oldN)
		}
		if d.newN > 0 {
			newS = strconv.Itoa(d.newN)
		}
		numStyle := s.muted
		if focused && i == m.rev.line {
			numStyle = s.bright
		}
		sp := st(lipgloss.NewStyle()).Render(" ")
		row := stripe + sp + st(numStyle).Render(fmt.Sprintf("%*s %*s", numW, oldS, numW, newS)) + sp + m.codeCell(syn, lines, i, textW, inSel)
		switch {
		case inSel:
			row = fill(row, w, s.sel)
		case d.kind != ' ':
			row = fill(row, w, lipgloss.NewStyle().Background(lipgloss.Color(lineBg(s.pal, d.kind))))
		}
		rows = append(rows, diffRow{text: row, line: i})
		rows = m.appendComments(rows, comments, d, w)
	}
	return rows
}

// appendComments adds the comments that end on line d below it.
func (m Model) appendComments(rows []diffRow, comments []review.Comment, d diffLine, w int) []diffRow {
	for _, c := range comments {
		if c.Side == d.side && c.End == d.num {
			for _, t := range m.commentRows(c, w) {
				rows = append(rows, diffRow{text: t, line: -1})
			}
		}
	}
	return rows
}

// commentRows draws a comment under its line: an amber bar while it is a
// draft, green once sent, with "open" until the quoted lines change and
// "addressed" after, then the body wrapped to the pane.
func (m Model) commentRows(c review.Comment, w int) []string {
	s := m.st
	barS, state := m.commentState(c)
	if c.ID == m.rev.editing {
		state = "editing"
	}
	indent := "      "
	bar := indent + barS.Render("┃") + " "
	where := "L" + strconv.Itoa(c.Start)
	if c.End != c.Start {
		where += "–" + strconv.Itoa(c.End)
	}
	to := ""
	if c.SessionID != "" {
		to = "  → " + m.nameFor(c.SessionID)
	}
	if c.Via != "" {
		to += " via " + c.Via
	}
	out := []string{bar + s.dim.Render("you · ") + barS.Render(state) + s.dim.Render(" · "+where+to)}
	bodyW := max(10, w-len(indent)-3)
	for _, l := range strings.Split(ansi.Wrap(c.Body, bodyW, ""), "\n") {
		out = append(out, bar+s.fg.Render(l))
	}
	return out
}

// fileComments are the comments on one file, oldest first.
func (m Model) fileComments(root, path string) []review.Comment {
	var out []review.Comment
	for _, c := range m.rev.comments {
		if c.Root == root && c.Path == path {
			out = append(out, c)
		}
	}
	return out
}

// draftCount counts drafts in a repository, narrowed to one agent or one
// file when those are given.
func (m Model) draftCount(root, session, path string) int {
	n := 0
	for _, c := range m.rev.comments {
		if c.Draft() && c.Root == root && (session == "" || c.SessionID == session) && (path == "" || c.Path == path) {
			n++
		}
	}
	return n
}

// commentState is the colour and word for where a comment stands.
func (m Model) commentState(c review.Comment) (lipgloss.Style, string) {
	s := m.st
	switch {
	case m.rev.sending[c.ID]:
		return s.attn, "sending"
	case c.Draft():
		return s.attn, "draft"
	case c.Open():
		return s.ok, "sent · open"
	}
	return s.ok, "sent · addressed"
}

func (m Model) notesTitle() string {
	drafts, open := 0, 0
	for _, c := range m.rev.comments {
		switch {
		case c.Draft():
			drafts++
		case c.Open():
			open++
		}
	}
	t := "Review"
	if drafts > 0 {
		t += fmt.Sprintf(" · %d draft%s", drafts, plural(drafts))
	}
	if open > 0 {
		t += fmt.Sprintf(" · %d open", open)
	}
	return t
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// notesPane lists comments grouped by the agent that gets them: drafts
// first, then what was sent, open before addressed. Recipients with drafts
// come first.
func (m Model) notesPane(w, h int) string {
	s := m.st
	bySession := map[string][]review.Comment{}
	var order []string
	for _, c := range m.rev.comments {
		if _, ok := bySession[c.SessionID]; !ok {
			order = append(order, c.SessionID)
		}
		bySession[c.SessionID] = append(bySession[c.SessionID], c)
	}
	if len(order) == 0 {
		return strings.Join([]string{
			s.dim.Render(" No comments yet."),
			"",
			s.muted.Render(ansi.Wrap(" Select lines in the diff and type to leave one.", w-1, "")),
		}, "\n")
	}
	rank := func(c review.Comment) int {
		switch {
		case c.Draft():
			return 0
		case c.Open():
			return 1
		}
		return 2
	}
	drafts := func(sid string) int {
		n := 0
		for _, c := range bySession[sid] {
			if c.Draft() {
				n++
			}
		}
		return n
	}
	sort.SliceStable(order, func(i, j int) bool {
		di, dj := drafts(order[i]) > 0, drafts(order[j]) > 0
		if di != dj {
			return di
		}
		if (order[i] == "") != (order[j] == "") {
			return order[j] == "" // lines no agent wrote last
		}
		return m.nameFor(order[i]) < m.nameFor(order[j])
	})
	var lines []string
	for gi, sid := range order {
		if gi > 0 {
			lines = append(lines, s.border.Render(" "+strings.Repeat("─", max(0, w-2))))
		}
		color := m.ownerColor(sid)
		if color == "" {
			color = s.pal.Muted
		}
		cs := bySession[sid]
		sort.SliceStable(cs, func(i, j int) bool { return rank(cs[i]) < rank(cs[j]) })
		nd := drafts(sid)
		head := lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(true).Render(" " + m.recipientName(sid))
		count := ""
		if nd > 0 {
			count = s.attn.Render(strconv.Itoa(nd))
		}
		lines = append(lines, head+strings.Repeat(" ", max(1, w-ansi.StringWidth(head)-ansi.StringWidth(count)-1))+count)
		_, live := m.reviewRecipient(sid)
		state := ""
		a, found := m.agentFor(sid)
		host := m.groupHost(reviewGroup{sessionID: sid, comments: cs})
		switch {
		case sid == "":
		case host != "" && !live:
			// Drafts on a remote agent's work wait for its host; they
			// never go to the orchestrator.
			state = m.remoteDraftsState(host)
		case !found || a.Status == fleet.Gone:
			state = "session ended"
		case !live:
			state = "live · no inbox socket"
		case a.Status == fleet.Working:
			state = "live · working"
		case a.Status == fleet.Idle:
			state = "live · idle"
		default:
			state = "live"
		}
		if !live && nd > 0 && host == "" {
			state = strings.TrimPrefix(state+" · /send orchestrator hands these on", " · ")
		}
		if state != "" {
			lines = append(lines, ansi.Truncate(s.dim.Render(" "+state), w, "…"))
		}
		for _, c := range cs {
			mark := s.attn.Render("•")
			if m.rev.sending[c.ID] {
				mark = s.attn.Render("→")
			} else if !c.Draft() {
				mark = s.ok.Render("→")
				if !c.Open() {
					mark = s.ok.Render("✓")
				}
			}
			where := sanitize.Code(filepath.Base(c.Path)) + ":" + strconv.Itoa(c.Start)
			body := s.fg.Render(strings.ReplaceAll(c.Body, "\n", " "))
			if !c.Draft() {
				body = s.dim.Render(strings.ReplaceAll(c.Body, "\n", " "))
			}
			lines = append(lines, ansi.Truncate(" "+mark+" "+s.muted.Render(where)+" "+body, w, "…"))
		}
	}
	return strings.Join(lines[:min(len(lines), h)], "\n")
}

// scrollStart is the first row shown when total rows scroll in a pane of
// height h so the block of n rows starting at sel stays in view.
func scrollStart(total, sel, n, h int) int {
	if total <= h || h <= 0 {
		return 0
	}
	start := 0
	if sel+n > h {
		start = sel + n - h
	}
	return clamp(start, 0, total-h)
}

// ---- comments ----

// commentAtCursor is the comment covering the cursor line, if any.
func (m Model) commentAtCursor() *review.Comment {
	f, wk := m.curFile(), m.curWork()
	if f == nil || wk == nil {
		return nil
	}
	lines := diffLines(*f)
	if m.rev.line >= len(lines) {
		return nil
	}
	d := lines[m.rev.line]
	for i := range m.rev.comments {
		c := &m.rev.comments[i]
		if c.Root == wk.Root && c.Path == f.Path && c.Side == d.side && c.Start <= d.num && d.num <= c.End {
			return c
		}
	}
	return nil
}

// selectionLabel names the selected lines for the command line, "" when
// the diff has no cursor to comment on.
func (m Model) selectionLabel() string {
	f := m.curFile()
	if f == nil || m.rev.focus != revDiff || m.tab != tabReview {
		return ""
	}
	lines := diffLines(*f)
	if len(lines) == 0 {
		return ""
	}
	lo, hi := m.selection()
	hi = min(hi, len(lines)-1)
	lo = min(lo, hi)
	a, b := lines[lo], lines[hi]
	label := sanitize.Code(filepath.Base(f.Path)) + " L" + strconv.Itoa(a.num)
	if hi > lo {
		label += "–" + strconv.Itoa(b.num)
	}
	return label
}

// addComment turns the command line into a comment on the selected lines,
// or saves the edit when one is open.
func (m *Model) addComment(body string) error {
	f, wk := m.curFile(), m.curWork()
	if f == nil || wk == nil {
		return fmt.Errorf("pick a file first")
	}
	if m.rev.editing != "" {
		for i := range m.rev.comments {
			if m.rev.comments[i].ID == m.rev.editing {
				m.rev.comments[i].Body = body
			}
		}
		m.rev.editing = ""
		return m.saveComments()
	}
	lines := diffLines(*f)
	if len(lines) == 0 {
		return fmt.Errorf("nothing to comment on in a binary or empty diff")
	}
	lo, hi := m.selection()
	hi = min(hi, len(lines)-1)
	lo = min(lo, hi)
	sel := lines[lo : hi+1]
	end := sel[len(sel)-1]
	// Number the range on the side of its last line; lines from the other
	// side (a removed line above added ones) are quoted but not numbered.
	start := end.num
	for _, d := range sel {
		if d.side == end.side {
			start = d.num
			break
		}
	}
	c := review.Comment{
		ID:        strconv.FormatInt(time.Now().UnixNano(), 36),
		Root:      wk.Root,
		Host:      wk.Host,
		Path:      f.Path,
		Side:      end.side,
		Start:     start,
		End:       end.num,
		Body:      body,
		SessionID: commentOwner(sel, wk.SessionID),
		Created:   time.Now(),
	}
	for _, d := range sel {
		c.Quote = append(c.Quote, string(d.kind)+d.text)
	}
	m.rev.comments = append(m.rev.comments, c)
	m.rev.ranged = false
	return m.saveComments()
}

// commentOwner is the agent most of the selected changed lines belong to;
// the work's own agent when the selection is context or no agent's lines.
func commentOwner(sel []diffLine, fallback string) string {
	count := map[string]int{}
	best, n := "", 0
	for _, d := range sel {
		if d.owner == "" {
			continue
		}
		count[d.owner]++
		if count[d.owner] > n {
			best, n = d.owner, count[d.owner]
		}
	}
	if best == "" {
		return fallback
	}
	return best
}

// deleteComment removes the comment under the cursor.
func (m *Model) deleteComment() bool {
	c := m.commentAtCursor()
	if c == nil {
		return false
	}
	id := c.ID
	kept := m.rev.comments[:0]
	for _, x := range m.rev.comments {
		if x.ID != id {
			kept = append(kept, x)
		}
	}
	m.rev.comments = kept
	if m.rev.editing == id {
		m.rev.editing = ""
	}
	m.saveComments()
	return true
}

// dropEdit abandons an open comment edit when you leave the Review view,
// clearing its text from the command line so Enter elsewhere cannot send it
// as a message.
func (m *Model) dropEdit() tea.Cmd {
	if m.rev.editing == "" {
		return nil
	}
	m.rev.editing = ""
	return m.clear()
}

// saveComments writes the comments on this machine's work. A comment on a
// remote agent's work quotes lines of a diff that crossed from its host
// under full, so it is kept for this run only, never written down.
func (m *Model) saveComments() error {
	local := make([]review.Comment, 0, len(m.rev.comments))
	for _, c := range m.rev.comments {
		if c.Host == "" {
			local = append(local, c)
		}
	}
	return review.SaveComments(m.rev.commentsPath, local)
}

// ---- mouse ----

// reviewMouse maps a click, drag or wheel to the pane under the pointer:
// click a work or file to open it, click a diff line to put the cursor on
// it and drag to select a range, wheel to move through whatever is there.
func (m *Model) reviewMouse(msg tea.Msg) {
	lay := m.reviewLayout()
	var mo tea.Mouse
	switch e := msg.(type) {
	case tea.MouseClickMsg:
		mo = tea.Mouse(e)
	case tea.MouseMotionMsg:
		mo = tea.Mouse(e)
	case tea.MouseReleaseMsg:
		m.rev.dragging = false
		return
	case tea.MouseWheelMsg:
		mo = tea.Mouse(e)
	default:
		return
	}
	row := mo.Y - lay.top - 1 // inside the box border
	inside := row >= 0 && row < lay.paneH-2
	inDiff := mo.X >= lay.diffX && mo.X < lay.diffX+lay.diffW
	inLeft := !lay.diffOnly && mo.X < lay.lw

	if _, wheel := msg.(tea.MouseWheelMsg); wheel {
		delta := 3
		if mo.Button == tea.MouseWheelUp {
			delta = -3
		}
		switch {
		case inDiff:
			m.rev.focus = revDiff
		case inLeft && row < lay.workH-1:
			m.rev.focus, delta = revWork, delta/3
		case inLeft:
			m.rev.focus, delta = revFiles, delta/3
		default:
			return
		}
		m.moveReview(delta)
		return
	}

	if _, motion := msg.(tea.MouseMotionMsg); motion {
		if !m.rev.dragging || !inDiff {
			return
		}
		if i, ok := m.diffLineAt(row, lay); ok && i != m.rev.line {
			if !m.rev.ranged {
				m.rev.anchor, m.rev.ranged = m.rev.line, true
			}
			m.rev.line = i
			m.followCursor()
		}
		return
	}

	if mo.Button != tea.MouseLeft || !inside {
		return
	}
	switch {
	case inDiff:
		m.rev.focus = revDiff
		if i, ok := m.diffLineAt(row, lay); ok {
			m.rev.line, m.rev.ranged, m.rev.dragging = i, false, true
			if mo.Mod&tea.ModShift != 0 {
				m.rev.ranged = true // shift-click extends from the old cursor
			}
		}
	case inLeft && row < lay.workH-2:
		m.rev.focus = revWork
		start := scrollStart(3*len(m.rev.works), 3*m.rev.cur, 3, lay.workH-2)
		if i := (start + row) / 3; i < len(m.rev.works) && i != m.rev.cur {
			m.rev.cur, m.rev.fileCur = i, 0
			m.resetCursor()
		}
	case inLeft && row > lay.workH-1:
		m.rev.focus = revFiles
		wk := m.curWork()
		if wk == nil {
			return
		}
		rows := fileRows(wk)
		filesH := lay.paneH - lay.workH - 2
		sel := 0
		for r, i := range rows {
			if i == m.rev.fileCur {
				sel = r
			}
		}
		r := scrollStart(len(rows), sel, 1, filesH) + row - lay.workH
		if r >= 0 && r < len(rows) && rows[r] >= 0 && rows[r] != m.rev.fileCur {
			m.rev.fileCur = rows[r]
			m.resetCursor()
		}
	}
}

// diffLineAt is the diff line shown at a row inside the diff box.
func (m Model) diffLineAt(row int, lay reviewLayout) (int, bool) {
	rows := m.diffRowsDrawn(lay.diffW-2, 0, 0)
	i := m.rev.scroll + row
	if i < 0 || i >= len(rows) || rows[i].line < 0 {
		return 0, false
	}
	return rows[i].line, true
}
