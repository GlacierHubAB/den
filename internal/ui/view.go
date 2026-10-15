package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/remote"
	"github.com/GlacierHubAB/den/internal/review"
	"github.com/GlacierHubAB/den/internal/sanitize"
	"github.com/GlacierHubAB/den/internal/theme"
	"github.com/GlacierHubAB/den/internal/transcript"
	"github.com/GlacierHubAB/den/internal/tree"
	"github.com/GlacierHubAB/den/internal/usage"
)

func (m Model) View() tea.View {
	m.hl = newHLBudget()
	w, h := m.width, m.height
	if w <= 0 {
		w = 120
	}
	if h <= 0 {
		h = 40
	}
	var out strings.Builder
	header := m.header(w)
	for _, r := range header {
		out.WriteString(ansi.Truncate(r, w, "…") + "\n")
	}

	// Rows: header, panes, [completions], prompt (rules, attachments, the
	// command line at its current height), hints. The panes take what is
	// left, so a growing command line pushes them up rather than over them.
	popup := m.completionRows(w)
	prompt := m.prompt(w)
	_, paneH := m.paneArea()
	out.WriteString(m.panes(w, paneH) + "\n")
	for _, r := range popup {
		out.WriteString(ansi.Truncate(r, w, "…") + "\n")
	}
	for _, r := range prompt {
		out.WriteString(ansi.Truncate(r, w, "…") + "\n")
	}
	out.WriteString(ansi.Truncate(m.hints(w), w, "…"))

	content := out.String()
	if m.sel.active {
		content = strings.Join(m.highlightSelection(strings.Split(content, "\n")), "\n")
	}
	// Outside text is sanitised where it comes in; this keeps Den's own
	// colours and drops any other sequence that got past that, so nothing
	// but styling reaches the terminal (the renderer passes SGR and OSC 8
	// hyperlinks through as they are).
	content = sanitize.Styled(content)
	v := tea.NewView(content)
	v.AltScreen = true
	if m.tab == tabReview || m.tab == tabOrch || m.tab == tabOverview {
		// Clicks and drags pick lines in the diff, and select text inside
		// one pane on the chat views. The Fleet and Inbox views keep the
		// terminal's own text selection.
		v.MouseMode = tea.MouseModeCellMotion
	}
	// Ask for disambiguated keys so Shift+Enter is not plain Enter.
	v.KeyboardEnhancements = tea.KeyboardEnhancements{ReportAlternateKeys: true}
	return v
}

func (m Model) counts() (needs, working, idle, unknown, gone int) {
	for _, a := range m.agents {
		if a.Stale {
			continue // last known only: its host row says so
		}
		switch a.Status {
		case fleet.NeedsYou:
			needs++
		case fleet.Working:
			working++
		case fleet.Idle:
			idle++
		case fleet.Gone:
			gone++
		default:
			unknown++
		}
	}
	return
}

// wordmark is the three-row block-letter logo.
var wordmark = []string{
	"█▀▀▄ █▀▀▀ █▄  █",
	"█  █ █▀▀  █ ▀▄█",
	"█▄▄▀ █▄▄▄ █   █",
}

// header draws the wordmark with fleet information beside it, padded above
// and below, then the view tabs. Narrow terminals fall back to one line.
func (m Model) header(w int) []string {
	s := m.st
	needs, working, idle, unknown, _ := m.counts()
	stale := 0
	for _, a := range m.agents {
		if a.Stale {
			stale++
		}
	}
	fleetParts := []string{agentsWord(len(m.agents) - stale)}
	if needs > 0 {
		fleetParts = append(fleetParts, s.attn.Render(fmt.Sprintf("%d need you", needs)))
	}
	fleetParts = append(fleetParts, fmt.Sprintf("%d working", working), fmt.Sprintf("%d idle", idle))
	if unknown > 0 {
		fleetParts = append(fleetParts, fmt.Sprintf("%d unknown", unknown))
	}
	if stale > 0 {
		fleetParts = append(fleetParts, fmt.Sprintf("%d last known on hosts offline", stale))
	}
	fleetLine := s.dim.Render(strings.Join(fleetParts, " · "))

	var links []string
	switch waiting, unseen := m.store.Waiting(), m.store.Unseen(); {
	case waiting > 0:
		links = append(links, s.attn.Render(fmt.Sprintf("inbox %d waiting", waiting)))
	case unseen > 0:
		links = append(links, fmt.Sprintf("inbox %d new", unseen))
	default:
		links = append(links, "inbox clear")
	}
	if m.orch.status != "" {
		links = append(links, "orchestrator "+m.orch.status)
	} else {
		links = append(links, "orchestrator off")
	}
	if m.missions != nil {
		if n := m.missions.Running(); n > 0 {
			links = append(links, fmt.Sprintf("%d missions running", n))
		}
	}
	switch label := usage.Label(m.use.reading, m.use.ok, m.use.st, time.Now()); {
	case !usage.Shown(m.use.ok, m.use.st):
		// Never a reading, a figure or a stop: nothing to say.
	case m.use.st.Stopped:
		links = append(links, s.err.Render(label))
	case m.use.ok && m.use.reading.Percent >= 80:
		links = append(links, s.attn.Render(label))
	default:
		links = append(links, label) // "usage unknown" once a reading has gone stale
	}
	linksLine := s.dim.Render(strings.Join(links, " · "))

	// Each part has a rank: when the line is too long, the highest ranks
	// go first. The codes, the bridge's error, the API chip's first part
	// and the console are never left out. The codes come first, so that
	// when even those do not fit, the "…" falls on the others (shown
	// whole in /api), never on a code, which Den shows nowhere else.
	var sys []headerPart
	codes := m.liveCodes()
	for _, c := range codes {
		sys = append(sys, headerPart{s.attn.Render(c.line), 0})
	}
	if m.hooksReady {
		sys = append(sys, headerPart{"hooks on", 2})
	} else {
		sys = append(sys, headerPart{s.attn.Render("hooks off · den install-hooks"), 1})
	}
	if m.srvErr != nil {
		sys = append(sys, headerPart{s.err.Render("bridge: " + m.srvErr.Error()), 0})
	}
	for i, p := range m.apiChipParts() {
		rank := 3
		if i == 0 {
			rank = 0
		}
		sys = append(sys, headerPart{p, rank})
	}
	if chip := m.consoleChip(); chip != "" {
		sys = append(sys, headerPart{chip, 0})
	}

	clock := s.dim.Render(time.Now().Format("15:04"))
	flash := ""
	if m.flash != "" {
		if m.flashErr {
			flash = s.err.Render(m.flash)
		} else {
			flash = s.ok.Render(m.flash)
		}
	}
	if w < 72 {
		brand := s.brand.Render("den") + "  "
		right := clock
		if len(codes) > 0 {
			// A live code takes the line, whole or as its note, ahead of
			// the fleet and the flash: the human always sees one is live.
			parts := make([]string, len(codes))
			used := 0
			for i, c := range codes {
				parts[i] = c.line
				if used+ansi.StringWidth(c.line) > headerRoom(brand, right, w) {
					parts[i] = c.hidden
				}
				used += ansi.StringWidth(parts[i]) + 3
			}
			return []string{headerRow(brand, s.attn.Render(strings.Join(parts, " · ")), right, w), ansi.Truncate(m.tabs(w), w, "…")}
		}
		if flash != "" {
			right = ansi.Truncate(flash, max(1, w-ansi.StringWidth(brand)-ansi.StringWidth(clock)-3), "…") + "  " + clock
		}
		return []string{headerRow(brand, fleetLine, right, w), ansi.Truncate(m.tabs(w), w, "…")}
	}
	logo := make([]string, len(wordmark))
	for i, r := range m.logoRows() {
		logo[i] = "  " + r + "    "
	}
	if flash != "" {
		// The flash gives way to the parts never left out (the pairing
		// code first): it is cut, or left out when no room is left.
		room := w - ansi.StringWidth(logo[2]) - 3
		if must := ansi.StringWidth(fitParts(sys, 0)); must > 0 {
			room -= must + 1
		}
		if room > 0 {
			flash = ansi.Truncate(flash, room, "…")
		} else {
			flash = ""
		}
	}
	// A code that does not fit whole is never shown in part: its note
	// says where it went instead. Codes are fitted in order, so a later
	// one that no longer fits after an earlier one shows its note.
	used := 0
	for i, c := range codes {
		shown := c.line
		if used+ansi.StringWidth(c.line) > headerRoom(logo[2], flash+"  ", w) {
			shown = c.hidden
			sys[i].text = s.attn.Render(c.hidden)
		}
		used += ansi.StringWidth(shown) + 3
	}
	rows := []string{
		"",
		headerRow(logo[0], fleetLine, clock+"  ", w),
		headerRow(logo[1], linksLine, "", w),
		// Whole parts that fit the room headerRow leaves beside the flash;
		// headerRow cuts only when the parts never left out do not fit.
		headerRow(logo[2], s.muted.Render(fitParts(sys, headerRoom(logo[2], flash+"  ", w))), flash+"  ", w),
		"",
		m.tabs(w),
	}
	return rows
}

// logoRows draws the wordmark with depth: the block letters are split into
// half-cell pixel rows and shaded from dark grey at the foot through two
// lighter greys to white at the top, so most of the mark is grey and the
// light sits on its upper edge.
func (m Model) logoRows() []string {
	p := m.st.pal
	// Six pixel rows, top first: white, then three greys stepping darker.
	bands := []string{p.Bright, p.Fg, p.Fg, theme.Tint(p.Fg, p.FgDim), p.FgDim, p.FgDim}
	out := make([]string, len(wordmark))
	for i, r := range wordmark {
		top, bottom := bands[2*i], bands[2*i+1]
		var b strings.Builder
		for _, ch := range r {
			switch ch {
			case '█':
				b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(top)).Background(lipgloss.Color(bottom)).Render("▀"))
			case '▀':
				b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(top)).Render("▀"))
			case '▄':
				b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(bottom)).Render("▄"))
			default:
				b.WriteRune(ch)
			}
		}
		out[i] = b.String()
	}
	return out
}

// The notes that stand in for a code too wide for Den's header.
const (
	codeHidden        = "Pairing code live: widen Den to see it"
	consoleCodeHidden = "Console code live: widen Den to see it"
)

// liveCode is a code Den shows in its header, and its note.
type liveCode struct{ line, hidden string }

// liveCodes are the codes live now: the app pairing code, then the
// console code.
func (m Model) liveCodes() []liveCode {
	var out []liveCode
	if l := m.codeLine(); l != "" {
		out = append(out, liveCode{l, codeHidden})
	}
	if l := m.consoleCodeLine(); l != "" {
		out = append(out, liveCode{l, consoleCodeHidden})
	}
	return out
}

// headerRow is a header line: the logo, then text, then right at the end
// of the row. When they do not all fit, the text gives way (cut with "…"),
// so a flash or the clock is never pushed past the edge.
func headerRow(logo, text, right string, w int) string {
	room := headerRoom(logo, right, w)
	if ansi.StringWidth(text) > room {
		text = ansi.Truncate(text, max(0, room), "…")
	}
	return justify(logo+text, right, w)
}

// headerRoom is the width headerRow leaves for the text between logo and
// right.
func headerRoom(logo, right string, w int) int {
	return w - ansi.StringWidth(logo) - ansi.StringWidth(right) - 1
}

// headerPart is one part of a header line and its rank: the higher, the
// sooner it is left out when the line is too long; 0 never is.
type headerPart struct {
	text string
	rank int
}

// fitParts joins parts with " · " in at most w columns, leaving out whole
// parts, the highest rank first and then the last of a rank, until they
// fit; nothing is cut inside a word. Parts of rank 0 always stay, even if
// the line is then too long for w.
func fitParts(parts []headerPart, w int) string {
	keep := append([]headerPart(nil), parts...)
	join := func() string {
		out := make([]string, len(keep))
		for i, p := range keep {
			out[i] = p.text
		}
		return strings.Join(out, " · ")
	}
	for ansi.StringWidth(join()) > w {
		drop := -1
		for i, p := range keep {
			if p.rank > 0 && (drop < 0 || p.rank >= keep[drop].rank) {
				drop = i
			}
		}
		if drop < 0 {
			break
		}
		keep = append(keep[:drop], keep[drop+1:]...)
	}
	return join()
}

// justify spaces left and right apart to fill w.
func justify(left, right string, w int) string {
	gap := w - ansi.StringWidth(left) - ansi.StringWidth(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m Model) tabs(w int) string {
	s := m.st
	style := func(t tab) lipgloss.Style {
		if m.tab == t {
			return s.tabOn
		}
		return s.tabOff
	}
	// The counter is what you have not dealt with: unseen notices and
	// anything still waiting. Amber when an answer is needed.
	inboxCount := ""
	if n := m.store.Unseen(); n > 0 {
		badge := s.badgeNew
		if m.store.Waiting() > 0 {
			badge = s.badgeN
		}
		inboxCount = " " + badge.Render(fmt.Sprint(n))
	}
	left := "  " + style(tabOverview).Render("Overview") + "   " + style(tabFleet).Render("Fleet") + "   " + style(tabInbox).Render("Inbox") + inboxCount + "   " + style(tabReview).Render("Review")
	// The Orchestrator view has no tab: Ctrl+O opens it. Its keycap lights
	// up while it is open, with the orchestrator's state beside it.
	orchKey := s.keycap.Render("^O orchestrator")
	if m.tab == tabOrch {
		orchKey = s.keycapOn.Render("^O orchestrator")
	}
	if m.orch.status != "" {
		orchKey = s.muted.Render(m.orch.status) + "  " + orchKey
	}
	return justify(left, orchKey+"  "+s.keycap.Render("Tab ⇥")+"  ", w)
}

func (m Model) panes(w, h int) string {
	if m.tab == tabInbox {
		title := "Inbox"
		if n := m.inboxWaiting(); n > 0 {
			title += fmt.Sprintf(" · %d waiting, not selected", n)
		}
		return m.box(title, m.inboxPane(w-2, h-2), w, h, true)
	}
	if m.tab == tabOrch {
		return m.box("Orchestrator", m.orchPane(w-2, h-2), w, h, true)
	}
	if m.tab == tabReview {
		return m.stripBlock(w) + "\n" + m.reviewPanes(w, h-stripRows)
	}
	if m.tab == tabOverview {
		return m.overviewPanes(w, h)
	}
	if m.tree.mapView {
		return m.box("Map", m.mapPane(w-2, h-2), w, h, true)
	}
	// Fleet: agents on the left, the selected agent on the right.
	if w < 72 {
		return m.box("Agents", m.agentsPane(w-2, h-2), w, h, true)
	}
	lw := clamp(w*42/100, 36, 64)
	rw := w - lw
	a := m.box("Agents", m.agentsPane(lw-2, h-2), lw, h, m.focus == paneAgents)
	detail := m.detailPane(rw-2, h-2)
	if m.remote.listing {
		detail = strings.Join(m.remoteLines(rw-2), "\n")
	}
	d := m.box(m.detailTitle(), detail, rw, h, m.focus == paneDetail)
	return lipgloss.JoinHorizontal(lipgloss.Top, a, d)
}

func (m Model) detailTitle() string {
	if m.remote.listing {
		return "Remote hosts"
	}
	return "Details"
}

// box draws a pane with its title set into the top border, the way TUIs do.
func (m Model) box(title, content string, w, h int, focused bool) string {
	s := m.st
	bs := s.border
	ts := s.paneTitle
	if focused {
		bs, ts = s.borderFocus, s.paneTitleFocus
	}
	inner := w - 2
	if inner < 1 {
		inner = 1
	}
	t := " " + title + " "
	if ansi.StringWidth(t) > inner-2 {
		t = ansi.Truncate(t, max(0, inner-2), "…")
	}
	top := bs.Render("┌─") + ts.Render(t) + bs.Render(strings.Repeat("─", max(0, inner-ansi.StringWidth(t)-1))+"┐")
	bottom := bs.Render("└" + strings.Repeat("─", inner) + "┘")

	lines := strings.Split(content, "\n")
	if len(lines) > h-2 {
		lines = lines[:h-2]
	}
	var b strings.Builder
	b.WriteString(top + "\n")
	for i := 0; i < h-2; i++ {
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		line = ansi.Truncate(line, inner, "…")
		pad := inner - ansi.StringWidth(line)
		if pad < 0 {
			pad = 0
		}
		b.WriteString(bs.Render("│") + line + strings.Repeat(" ", pad) + bs.Render("│") + "\n")
	}
	b.WriteString(bottom)
	return b.String()
}

func (m Model) glyph(st fleet.Status) string {
	s := m.st
	switch st {
	case fleet.Working:
		return s.bright.Render("●")
	case fleet.NeedsYou:
		return s.attn.Render("◐")
	case fleet.Idle:
		return s.dim.Render("○")
	case fleet.Gone:
		return s.err.Render("✕")
	}
	return s.muted.Render("?")
}

// rowStyles returns the text styles for a list row. A selected row carries
// the selection background in every style, so nested colour resets never
// break the highlight, and gets a bright left marker.
func (m Model) rowStyles(selected bool) (fg, dim, muted, bright, ok, attn, err lipgloss.Style, marker string) {
	s := m.st
	fg, dim, muted, bright, ok, attn, err = s.fg, s.dim, s.muted, s.bright, s.ok, s.attn, s.err
	marker = " "
	if !selected {
		return
	}
	bg := lipgloss.Color(s.pal.Sel)
	fg, dim, muted, bright = fg.Background(bg), dim.Background(bg), muted.Background(bg), bright.Background(bg)
	ok, attn, err = ok.Background(bg), attn.Background(bg), err.Background(bg)
	marker = s.bright.Background(bg).Render("▎")
	return
}

// fill pads a styled line to width w with the given style's background.
func fill(line string, w int, pad lipgloss.Style) string {
	n := w - ansi.StringWidth(line)
	if n <= 0 {
		return line
	}
	return line + pad.Render(strings.Repeat(" ", n))
}

func (m Model) agentsPane(w, h int) string {
	s := m.st
	if m.err != nil {
		return s.err.Render(ansi.Wrap("scan failed: "+m.err.Error(), w-2, ""))
	}
	if len(m.agents) == 0 {
		out := "\n  " + s.dim.Render("No Claude Code sessions running.")
		if rows := m.idleHostRows(w); len(rows) > 0 {
			out += "\n\n" + strings.Join(rows, "\n")
		}
		return out
	}
	var b strings.Builder
	b.WriteString("\n")
	// Sections: each tree under its root, then the rest by status. Rows
	// inside a folded branch are skipped.
	last := ""
	counts := map[string]int{}
	for _, a := range m.agents {
		counts[m.sectionOf(a)]++
	}
	for i, a := range m.agents {
		meta := m.rowFor(a.Session.SessionID)
		if meta.hidden {
			continue
		}
		if sec := m.sectionOf(a); sec != last {
			if last != "" {
				b.WriteString("\n")
			}
			if name, ok := projectHeader(sec); ok {
				b.WriteString("  " + s.section.Render(fmt.Sprintf("%s · %d", strings.ToUpper(name), counts[sec])) + "\n")
			} else if host, ok := strings.CutPrefix(sec, "@"); ok {
				for _, r := range m.hostRows(host, w) {
					b.WriteString(r + "\n")
				}
			} else {
				b.WriteString("  " + s.section.Render(fmt.Sprintf("%s · %d", strings.ToUpper(sec), counts[sec])) + "\n")
			}
			last = sec
			if sec == "orchestrator" {
				b.WriteString(fill(m.orchestratorRow(w), w, s.fg) + "\n")
			}
		}
		selected := i == m.cursor
		fg, dim, muted, bright, _, attn, err, marker := m.rowStyles(selected)
		guide, cont := muted.Render(meta.guide), muted.Render(meta.cont)
		indentW := ansi.StringWidth(meta.cont)
		var glyph string
		switch a.Status {
		case fleet.Working:
			glyph = bright.Render("●")
		case fleet.NeedsYou:
			glyph = attn.Render("◐")
		case fleet.Idle:
			glyph = dim.Render("○")
		case fleet.Gone:
			glyph = err.Render("✕")
		default:
			glyph = muted.Render("?")
		}
		name := fg.Render(a.Name)
		if selected {
			name = bright.Render(a.Name)
		}
		if a.Host != "" {
			name += muted.Render(hostChip(a))
		}
		twin := m.twinNote(a.Session.SessionID)
		role := ""
		if a.Labels.Role != "" {
			role = fg.Foreground(lipgloss.Color(m.agentColor(a))).Render("  " + strings.ToUpper(a.Labels.Role))
		}
		last, stale := m.lastKnown(a)
		if stale {
			// Dimmed through and through: the host is not live.
			glyph, name = muted.Render("·"), dim.Render(a.Name+hostChip(a))
			if a.Labels.Role != "" {
				role = muted.Render("  " + strings.ToUpper(a.Labels.Role))
			}
		}
		l1 := marker + fg.Render(" ") + guide + glyph + fg.Render(" ") + name + role
		if twin != "" {
			l1 += attn.Render("  " + twin)
		}
		if d := m.directsLabel(a.Session.SessionID, dim, attn); d != "" {
			l1 += fg.Render("   ") + d
		}
		l1 = ansi.Truncate(l1, w, "…")

		taskMark := "▸ "
		if a.Status == fleet.Idle || a.Status == fleet.Gone {
			taskMark = "↳ "
		}
		taskStyle := dim
		if selected && !stale {
			taskStyle = fg
		}
		if stale {
			taskStyle = muted
		}
		l2 := marker + fg.Render(" ") + cont + fg.Render("  ") + taskStyle.Render(ansi.Truncate(taskMark+a.Task(), w-5-indentW, "…"))

		// Third line: age · harness · model · ctx, all inline. Where it runs
		// lives in the details pane.
		ctx := ""
		cs := dim
		if a.HasUsage {
			ctx = fmt.Sprintf("ctx %d%%", a.Usage.Percent())
			if a.Usage.Percent() >= 90 {
				cs = err
			} else if a.Usage.Percent() >= 75 {
				cs = attn
			}
		}
		age := ""
		if a.Status != fleet.Gone {
			age = agentAge(a, a.Session.StatusChanged())
		}
		if stale {
			age, ctx = "last known: "+last.String(), ""
		}
		// Narrow panes drop the harness first, then the model; age and
		// context always fit.
		var l3 string
		for _, parts := range [][]string{
			{age, "Claude Code", prettyModel(a.Model)},
			{age, prettyModel(a.Model)},
			{age},
		} {
			var bits []string
			for _, p := range parts {
				if p != "" {
					bits = append(bits, p)
				}
			}
			l3 = marker + fg.Render(" ") + cont + fg.Render("  ") + dim.Render(strings.Join(bits, " · "))
			if ctx != "" {
				if len(bits) > 0 {
					l3 += dim.Render(" · ")
				}
				l3 += cs.Render(ctx)
			}
			if ansi.StringWidth(l3) <= w {
				break
			}
		}
		l3 = ansi.Truncate(l3, w, "…")
		for _, r := range []string{l1, l2, l3} {
			b.WriteString(fill(r, w, fg) + "\n")
		}
	}
	if rows := m.idleHostRows(w); len(rows) > 0 {
		b.WriteString("\n" + strings.Join(rows, "\n"))
	}
	return strings.TrimRight(b.String(), "\n")
}

// openItem is the request waiting on the human for a session, if any.
func (m Model) openItem(sessionID string) *inbox.Item {
	for _, it := range m.store.Items() {
		if itemKey(it) == sessionID && it.State != inbox.Resolved && (it.Kind <= inbox.Question || it.DenAsks()) {
			return it
		}
	}
	return nil
}

// agentOrStub is the agent for a session, or a stub carrying its colour
// when the session is no longer in the fleet.
func (m Model) agentOrStub(sessionID string) fleet.Agent {
	if a, ok := m.agentFor(sessionID); ok {
		return a
	}
	a := fleet.Agent{}
	a.Session.SessionID = sessionID
	return a
}

// wrapCode wraps one line of a command or diff to the width, continuation
// lines indented, in the code style.
func (m Model) wrapCode(l string, w int, code lipgloss.Style) []string {
	var out []string
	for i, part := range strings.Split(ansi.Wrap(l, max(8, w), ""), "\n") {
		if i > 0 {
			part = "    " + part
		}
		out = append(out, code.Render(part))
	}
	return out
}

// requestLines spells out an open request: the kind and tool, the agent's
// intent, the full command, diff or arguments wrapped, then the keys.
func (m Model) requestLines(it *inbox.Item, a fleet.Agent, w int) []string {
	s := m.st
	head := s.attn.Render(strings.ToUpper(it.Kind.String()))
	if it.Tool != "" {
		head += s.dim.Render(" · ") + s.fg.Render(it.Tool)
	}
	head += s.dim.Render(" · ") + s.dim.Render(it.Title)
	out := []string{head}
	if twin := m.twinNote(itemKey(it)); twin != "" {
		out = append(out, s.attn.Render(twin))
	}
	if it.Intent != "" {
		for _, l := range strings.Split(ansi.Wrap(it.Intent, w, ""), "\n") {
			out = append(out, s.fg.Render(l))
		}
	}
	code := m.inAgentColor(a).st.code
	for _, l := range it.Body {
		out = append(out, m.wrapCode(l, w, code)...)
	}
	for j, o := range it.Options {
		out = append(out, s.bright.Render(fmt.Sprint(j+1))+" "+s.fg.Render(ansi.Truncate(o, w-2, "…")))
	}
	key := func(k, label string) string { return s.key.Render(k) + s.keyLabel.Render(" "+label) }
	if t := m.answerTarget(); it.Answerable() && t != it {
		// ^Y and ^N act on another request here: no keys on this one.
		note := "waiting · select it in the Inbox to answer"
		if t != nil {
			note = "waiting · ^Y and ^N answer " + m.nameFor(itemKey(t)) + "'s request first; select this one in the Inbox"
		}
		return append(out, s.dim.Render(note))
	}
	switch {
	case it.AsksInDen():
		out = append(out, key(optionKeys(it), "or your own answer, Enter (Inbox)")+"  "+key("^N", "answer in the terminal")+"  "+key("/open", "terminal"))
	case it.Answerable() && it.Kind == inbox.Plan:
		out = append(out, key("^Y", "approve")+"  "+key("^N", "ask for changes")+"  "+key("/open", "read it in the terminal"))
	case it.Answerable() && it.Kind == inbox.Close:
		out = append(out, key("^Y", "close them")+"  "+key("^N", "keep them"))
	case it.Answerable() && it.Kind == inbox.Skills:
		out = append(out, key("^Y", "install them")+"  "+key("^N", "decline"))
	case it.Answerable() && it.Kind == inbox.Budget:
		out = append(out, key("^Y", "keep going")+"  "+key("^N", "stop the mission"))
	case it.Answerable() && it.Kind == inbox.MissionUpdate:
		out = append(out, key("^Y", "apply it")+"  "+key("^N", "leave it"))
	case it.Answerable() && it.Kind == inbox.MissionMove:
		out = append(out, key("^Y", "move it")+"  "+key("^N", "keep it where it is"))
	case it.Answerable() && it.Kind == inbox.Restore:
		out = append(out, key("^Y", "reopen")+"  "+key("^N", "leave closed"))
	case it.Answerable() && it.Kind == inbox.Merge:
		out = append(out, key("^Y", "merge it")+"  "+key("^N", "not now"))
	case it.Answerable() && it.Kind == inbox.Cleanup:
		out = append(out, key("^Y", "remove the worktree")+"  "+key("^N", "keep it"))
	case it.Answerable() && it.Kind == inbox.Install:
		out = append(out, key("^Y", "install it")+"  "+key("^N", "decline"))
	case it.Answerable() && it.Kind == inbox.Message:
		out = append(out, key("^Y", "send it")+"  "+key("^N", "do not send"))
	case it.Answerable() && it.Kind == inbox.Guidance:
		out = append(out, key("^Y", "load it")+"  "+key("^N", "leave it off"))
	case it.Answerable() && it.Kind == inbox.UsageResume:
		out = append(out, key("^Y", "let the fleet go on")+"  "+key("^N", "stay stopped"))
	case it.Answerable() && it.Kind == inbox.UsageSet:
		out = append(out, key("^Y", "take the figure")+"  "+key("^N", "keep yours"))
	case it.Answerable() && it.Kind == inbox.APIOn:
		out = append(out, key("^Y", "turn it on")+"  "+key("^N", "leave it off"))
	case it.Answerable() && it.Kind == inbox.Grant:
		out = append(out, key("^Y", "grant it")+"  "+key("^N", "deny"))
	case it.Answerable() && it.Kind == inbox.Pair:
		out = append(out, key("^Y", "pair it")+"  "+key("^N", "refuse"))
	case it.Answerable() && it.Kind == inbox.Console:
		out = append(out, key("^Y", "make it your console")+"  "+key("^N", "refuse"))
	case it.Answerable() && it.Kind == inbox.ControlAsk:
		line := key("^Y", "allow it once") + "  " + key("^N", "deny")
		if _, ok := m.ctl.always[it]; ok {
			line += "  " + key("/always", "and from now on (read the card)")
		}
		out = append(out, line)
	case it.Answerable():
		out = append(out, key("^Y", "allow")+"  "+key("/always", "this session")+"  "+key("^N", "deny")+"  "+key("/open", "terminal"))
	case hostOf(itemKey(it)) != "":
		out = append(out, s.dim.Render("answer on "+hostOf(itemKey(it)))+"  "+key("/open", "how to reach it"))
	default:
		out = append(out, s.dim.Render("answer in the terminal")+"  "+key("/open", ""))
	}
	return out
}

// orchestratorRow heads the orchestrator's section of the agents pane: it
// is not a fleet row, so it is one line and cannot be selected.
func (m Model) orchestratorRow(w int) string {
	s := m.st
	status := m.orch.status
	if status == "" {
		status = "off"
	}
	line := "  " + m.orchStyle().Render("● orchestrator") + s.dim.Render("  "+status)
	if d := m.directsLabel(tree.Orchestrator, s.dim, s.attn); d != "" {
		line += s.dim.Render("   ") + d
	}
	return ansi.Truncate(line, w, "…")
}

func (m Model) inboxPane(w, h int) string {
	s := m.st
	items := m.store.Items()
	if m.srvErr != nil {
		return "\n " + s.err.Render(ansi.Wrap("Hook bridge unavailable: "+m.srvErr.Error(), w-2, ""))
	}
	if len(items) == 0 {
		lines := []string{"", " " + s.dim.Render("Nothing is waiting on you.")}
		if !m.hooksReady {
			lines = append(lines, "")
			for _, l := range strings.Split(ansi.Wrap("Run `den install-hooks` once so permission prompts, questions and finished turns from every agent land here. Sessions started afterwards report in.", w-2, ""), "\n") {
				lines = append(lines, " "+s.muted.Render(l))
			}
		}
		return strings.Join(lines, "\n")
	}
	active := m.inboxActive()
	// The row drawn as selected, and the only one with answer keys, is the
	// item ^Y and ^N act on: found by its id, never by a row number.
	bound, cur := m.inboxSelected(), -1
	var blocks [][]string
	for i, it := range items {
		selected := active && it == bound
		if selected {
			cur = i
		}
		blocks = append(blocks, append(m.inboxBlock(it, selected, w), ""))
	}
	// A selected item taller than the pane is all the pane shows, scrolled
	// under a marker; ^Y waits until it was read to its end (inbox_body.go).
	if h > 0 && active && cur >= 0 && len(blocks[cur])-1 > h {
		return strings.Join(m.bodyWindow(blocks[cur][:len(blocks[cur])-1], h, w), "\n")
	}
	// Keep the selected item in view: drop whole blocks from the top until
	// the selection fits in h rows.
	start := 0
	if h > 0 && active && cur >= 0 {
		for start < cur {
			used := 0
			for _, bl := range blocks[start : cur+1] {
				used += len(bl)
			}
			if used <= h {
				break
			}
			start++
		}
	}
	var lines []string
	for _, bl := range blocks[start:] {
		lines = append(lines, bl...)
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

// inboxAvatarW is the width of the critter beside an inbox item.
const inboxAvatarW = 8

// inboxBlock is one inbox item as the Inbox pane draws it, w wide, without
// the blank row that follows it.
func (m Model) inboxBlock(it *inbox.Item, selected bool, w int) []string {
	s := m.st
	const avatarW = inboxAvatarW
	fg, dim, muted, bright, ok, attn, err, _ := m.rowStyles(selected)
	stripe := muted.Render("▏")
	kindStyle := muted
	switch {
	case it.Answerable():
		stripe, kindStyle = attn.Render("▌"), attn
	case it.State == inbox.InTerminal && it.Kind <= inbox.Question:
		stripe, kindStyle = attn.Render("▏"), attn
	case it.Kind == inbox.Done:
		stripe, kindStyle = ok.Render("▏"), ok
		if !it.Seen {
			stripe = ok.Render("▌")
		}
	case it.Kind == inbox.Failed && it.State != inbox.Resolved:
		stripe, kindStyle = err.Render("▌"), err
	case it.Kind == inbox.Stalled && it.State != inbox.Resolved:
		stripe, kindStyle = attn.Render("▏"), attn
	case (it.Kind == inbox.Usage || it.Kind == inbox.ConsoleNote) && it.State != inbox.Resolved:
		stripe, kindStyle = err.Render("▌"), err
	case it.Kind == inbox.Sent:
		switch {
		case strings.HasPrefix(it.Resolved, "failed"):
			stripe, kindStyle = err.Render("▏"), err
		case it.State == inbox.Resolved:
			stripe, kindStyle = ok.Render("▏"), dim
		default:
			stripe, kindStyle = attn.Render("▏"), attn
		}
	}
	if it.Unread {
		stripe = bright.Render("▌")
	}
	sp := fg.Render(" ")
	color := m.colorFor(itemKey(it))
	name := fg.Foreground(lipgloss.Color(color)).Bold(true).Render(m.nameFor(itemKey(it)))
	if host := hostOf(itemKey(it)); host != "" {
		name += muted.Render(" @" + host)
	}
	role := ""
	if it.Role != "" {
		role = sp + s.chip(color).Render(strings.ToUpper(it.Role))
	}
	head := name + role + sp + sp + kindStyle.Render(strings.ToUpper(it.Kind.String())) + sp + sp + muted.Render(humanAge(time.Since(it.At)))

	textW := w - 4 - avatarW - 1
	var text []string
	text = append(text, head)
	if via := m.viaLine(itemKey(it)); via != "" {
		text = append(text, ansi.Truncate(via, textW, "…"))
	}
	if twin := m.twinNote(itemKey(it)); twin != "" {
		text = append(text, attn.Render(ansi.Truncate(twin, textW, "…")))
	}
	body := fg
	if it.State == inbox.Resolved || it.Kind >= inbox.Done {
		body = dim
	}
	// Headline: for a finished turn, what the agent was working on; for
	// everything else, what is being asked. Then the one-line gist.
	switch {
	case it.Kind == inbox.Done && it.Task != "":
		text = append(text, fg.Render(ansi.Truncate(it.Task, textW, "…")))
		text = append(text, dim.Render(ansi.Truncate(it.Title, textW, "…")))
	default:
		title := it.Title
		if it.Kind == inbox.Idle {
			title = m.idleTitle(itemKey(it), title)
		}
		lines := strings.Split(ansi.Wrap(title, textW, ""), "\n")
		if len(lines) > 2 {
			lines = lines[:2]
			lines[1] = ansi.Truncate(lines[1], textW-2, "…")
		}
		for _, l := range lines {
			text = append(text, body.Render(l))
		}
	}
	// An open request shows the whole thing being decided, wrapped;
	// history keeps one line of it.
	open := it.State != inbox.Resolved && (it.Kind <= inbox.Question || it.DenAsks())
	if open && it.Intent != "" {
		for _, l := range strings.Split(ansi.Wrap(it.Intent, textW, ""), "\n") {
			text = append(text, fg.Render(l))
		}
	}
	bodyLines := it.Body
	if !open && len(bodyLines) > 1 {
		bodyLines = bodyLines[:1]
	}
	for _, l := range bodyLines {
		if open {
			text = append(text, m.wrapCode(l, textW, m.inAgentColor(m.agentOrStub(itemKey(it))).st.code)...)
			continue
		}
		text = append(text, dim.Render(ansi.Truncate(l, textW, "…")))
	}
	for j, o := range it.Options {
		text = append(text, bright.Render(fmt.Sprint(j+1))+sp+fg.Render(ansi.Truncate(o, textW-2, "…")))
	}
	key := func(k, label string) string {
		if label == "" {
			return bright.Render(k)
		}
		return bright.Render(k) + dim.Render(" "+label)
	}
	switch {
	case it.Kind == inbox.Sent:
		text = append(text, dim.Render(it.Resolved)+sp+sp+key("/dismiss", ""))
	case it.Answerable() && !selected:
		why := "waiting"
		if it.At.After(m.inboxAt) {
			why = "new request"
		}
		text = append(text, attn.Render(why)+dim.Render(" · select it to answer (↑ ↓)"))
	case it.Answerable():
		acts := key("^Y", "allow") + sp + sp + key("/always", "this session") + sp + sp + key("^N", "deny") + sp + sp + key("/open", "terminal")
		if it.Kind == inbox.Plan {
			acts = key("^Y", "approve") + sp + sp + key("^N", "ask for changes") + sp + sp + key("/open", "read it in the terminal")
		}
		if it.Kind == inbox.Close {
			acts = key("^Y", "close them") + sp + sp + key("^N", "keep them")
		}
		if it.Kind == inbox.Skills {
			acts = key("^Y", "install them") + sp + sp + key("^N", "decline")
		}
		if it.Kind == inbox.Budget {
			acts = key("^Y", "keep going") + sp + sp + key("^N", "stop the mission")
		}
		if it.Kind == inbox.MissionUpdate {
			acts = key("^Y", "apply it") + sp + sp + key("^N", "leave it")
		}
		if it.Kind == inbox.MissionMove {
			acts = key("^Y", "move it") + sp + sp + key("^N", "keep it where it is")
		}
		if it.Kind == inbox.Restore {
			acts = key("^Y", "reopen") + sp + sp + key("^N", "leave closed")
		}
		if it.Kind == inbox.Merge {
			acts = key("^Y", "merge it") + sp + sp + key("^N", "not now")
		}
		if it.Kind == inbox.Cleanup {
			acts = key("^Y", "remove the worktree") + sp + sp + key("^N", "keep it")
		}
		if it.Kind == inbox.Install {
			acts = key("^Y", "install it") + sp + sp + key("^N", "decline")
		}
		if it.Kind == inbox.Message {
			acts = key("^Y", "send it") + sp + sp + key("^N", "do not send")
		}
		if it.Kind == inbox.Guidance {
			acts = key("^Y", "load it") + sp + sp + key("^N", "leave it off")
		}
		if it.Kind == inbox.UsageResume {
			acts = key("^Y", "let the fleet go on") + sp + sp + key("^N", "stay stopped")
		}
		if it.Kind == inbox.UsageSet {
			acts = key("^Y", "take the figure") + sp + sp + key("^N", "keep yours")
		}
		if it.Kind == inbox.APIOn {
			acts = key("^Y", "turn it on") + sp + sp + key("^N", "leave it off")
		}
		if it.Kind == inbox.Grant {
			acts = key("^Y", "grant it") + sp + sp + key("^N", "deny")
		}
		if it.Kind == inbox.Pair {
			acts = key("^Y", "pair it") + sp + sp + key("^N", "refuse")
		}
		if it.Kind == inbox.Console {
			acts = key("^Y", "make it your console") + sp + sp + key("^N", "refuse")
		}
		if it.Kind == inbox.ControlAsk {
			acts = key("^Y", "allow it once") + sp + sp + key("^N", "deny")
		}
		if it.AsksInDen() {
			acts = key(optionKeys(it), "or your own answer, Enter") + sp + sp + key("^N", "answer in the terminal") + sp + sp + key("/open", "terminal")
		}
		text = append(text, acts)
	case it.State == inbox.InTerminal:
		why := "answer in the terminal"
		if host := hostOf(itemKey(it)); host != "" {
			why = "answer it in the terminal on " + host
			if h, ok := m.hostStatus(host); ok && h.State == remote.AttachedElsewhere {
				// That Den may hold it for an answer: not only the terminal's.
				why = "another Den controls " + host + ": answer it there or in the terminal"
			}
		}
		if it.Resolved != "" {
			why = it.Resolved + " · " + why
		}
		text = append(text, dim.Render(why)+sp+sp+key("/open", ""))
	case it.Kind == inbox.Done:
		text = append(text, key("/open", "")+sp+sp+key("/dismiss", ""))
	case it.State == inbox.Resolved:
		text = append(text, muted.Render(it.Resolved))
	default:
		text = append(text, key("/open", "")+sp+sp+key("/dismiss", ""))
	}
	// Avatar on the left, 8 columns by 4 rows; the text runs beside it.
	avatar := m.smallCritter(itemKey(it), selected)
	n := max(len(text), len(avatar))
	rows := make([]string, 0, n+1)
	for r := 0; r < n; r++ {
		left := fg.Render(strings.Repeat(" ", avatarW))
		if r < len(avatar) {
			left = avatar[r]
		}
		right := ""
		if r < len(text) {
			right = text[r]
		}
		rows = append(rows, fill(stripe+sp+left+sp+right, w, fg))
	}
	return rows
}

func (m Model) detailPane(w, h int) string {
	lines, _, _ := m.detailLines(w)
	if lines == nil {
		return "\n " + m.st.dim.Render("Select an agent.")
	}
	if m.detailScroll > 0 && len(lines) > h {
		off := min(m.detailScroll, len(lines)-h)
		lines = lines[off:]
	}
	return strings.Join(lines, "\n")
}

// detailLines is the detail pane's content for the selected agent, w wide,
// before scrolling, and the rows [reqFrom, reqTo) its open request takes
// (both 0 with none). nil when no agent is selected.
func (m Model) detailLines(w int) (lines []string, reqFrom, reqTo int) {
	s := m.st
	a, ok := m.selected()
	if !ok {
		return nil, 0, 0
	}
	var b strings.Builder
	const indent = "  "
	line := func(str string) { b.WriteString(indent + ansi.Truncate(str, w-4, "…") + "\n") }
	role := ""
	if a.Labels.Role != "" {
		role = "  " + s.chip(m.agentColor(a)).Render(strings.ToUpper(a.Labels.Role))
	}
	// Identity on top, then the critter with its task beside it.
	b.WriteString("\n")
	line(s.bright.Bold(true).Render(a.Name) + role)
	if ch := m.chainLine(a.Session.SessionID, true); ch != "" {
		line(ch)
	}
	b.WriteString("\n")
	const spriteW = 16
	var head []string
	textW := w - 4 - spriteW - 2
	if textW < 16 {
		textW = w - 4
	}
	wrapTo := func(prefix, text string, style lipgloss.Style, dst *[]string) {
		for i, l := range strings.Split(ansi.Wrap(text, max(8, textW-ansi.StringWidth(prefix)), ""), "\n") {
			p := prefix
			if i > 0 {
				p = strings.Repeat(" ", ansi.StringWidth(prefix))
			}
			*dst = append(*dst, style.Render(p+l))
		}
	}
	wrapTo("▸ ", a.Task(), s.fg, &head)
	for i, h := range a.Labels.History {
		if i >= 3 {
			break
		}
		prefix := "↳ earlier: "
		if i > 0 {
			prefix = "           "
		}
		wrapTo(prefix, h, s.dim, &head)
	}
	sub := prettyModel(a.Model)
	if sub != "" {
		sub += " · "
	}
	if a.Host != "" {
		sub += "on " + a.Host
	} else {
		sub += sanitize.Line(a.Session.ShortCWD())
	}
	head = append(head, s.dim.Render(ansi.Truncate(sub, textW, "…")))
	if textW == w-4 {
		for _, l := range head {
			line(l)
		}
	} else {
		sprite := m.critterRows(a)
		n := max(len(sprite), len(head))
		for i := 0; i < n; i++ {
			left := strings.Repeat(" ", spriteW)
			if i < len(sprite) {
				left = sprite[i]
			}
			right := ""
			if i < len(head) {
				right = head[i]
			}
			b.WriteString(indent + left + "  " + right + "\n")
		}
	}
	b.WriteString("\n")
	kv := func(k, v string) {
		if v == "" {
			return
		}
		line(s.dim.Render(fmt.Sprintf("%-9s", k)) + s.fg.Render(v))
	}
	if last, stale := m.lastKnown(a); stale {
		kv("status", "last known: "+last.String()+" · "+agentAge(a, a.Session.StatusChanged()))
	} else {
		kv("status", a.Status.String()+" · "+agentAge(a, a.Session.StatusChanged()))
	}
	if it := m.openItem(a.Session.SessionID); it != nil {
		b.WriteString("\n")
		reqFrom = strings.Count(b.String(), "\n")
		for _, l := range m.requestLines(it, a, w-4) {
			line(l)
		}
		reqTo = strings.Count(b.String(), "\n")
	}
	if kids := m.childrenOf(a.Session.SessionID); len(kids) > 0 {
		b.WriteString("\n")
		line(s.muted.Render("directs"))
		for _, l := range m.childLines(a.Session.SessionID, w-4, false) {
			line(l)
		}
		b.WriteString("\n")
	}
	var gauges [][]string
	if a.HasUsage {
		left := (a.Usage.Window - a.Usage.Tokens) / 1000
		if left < 0 {
			left = 0
		}
		gauges = append(gauges, m.gauge("Context", a.Usage.Percent(), fmt.Sprintf("%dk left", left), 18))
	}
	if a.HasLimit {
		gauges = append(gauges, m.limitGauges(a.Limits, 18)...)
		if a.Limits.USD != nil {
			gauges = append(gauges, []string{s.dim.Render("Spent"), s.fg.Render(fmt.Sprintf("$%.2f", *a.Limits.USD)), s.dim.Render("this session")})
		}
	}
	if len(gauges) > 0 {
		cols := max(1, min(len(gauges), (w-4)/20))
		for i := 0; i < len(gauges); i += cols {
			group := gauges[i:min(i+cols, len(gauges))]
			for r := 0; r < 3; r++ {
				var row []string
				for _, g := range group {
					cell := g[r]
					pad := 20 - ansi.StringWidth(cell)
					if pad < 0 {
						pad = 0
					}
					row = append(row, cell+strings.Repeat(" ", pad))
				}
				line(strings.TrimRight(strings.Join(row, ""), " "))
			}
			b.WriteString("\n")
		}
	}
	for _, r := range m.agentCostLines(a) {
		kv(r[0], r[1])
	}
	if a.Host != "" {
		// A remote agent carries metadata only, unless its host's policy is
		// full: then its folder on the host, its title and its recent
		// activity cross too. Never a window or a pid.
		kv("where", a.Where())
		for _, r := range m.remoteDetail(a) {
			kv(r[0], r[1])
		}
		kv("cwd", a.RemoteCWD)
		kv("title", a.Title)
		if age := agentAge(a, a.Session.Started()); age != "?" {
			kv("started", age+" ago")
		}
		sid, _ := splitKey(a.Session.SessionID)
		kv("session", sanitize.Line(sid[:min(8, len(sid))]+" · "+a.Session.Name))
		if len(a.Tail) > 0 {
			b.WriteString("\n")
			line(s.muted.Render("recent"))
			for _, l := range m.activityLines(a.Tail, a, w-4, true) {
				line(l)
			}
		}
		b.WriteString("\n")
		line(s.key.Render("Enter") + s.keyLabel.Render(" how to reach it: ssh -t "+m.hostTarget(a.Host)))
		return strings.Split(strings.TrimRight(b.String(), "\n"), "\n"), reqFrom, reqTo
	}
	kv("where", a.Where())
	if a.HasGit {
		kv("cwd", sanitize.Line(a.Session.ShortCWD()))
	}
	kv("window", a.Place())
	if a.Title != "" {
		kv("title", a.Title)
	}
	kv("started", humanAge(time.Since(a.Session.Started()))+" ago")
	kv("pid", fmt.Sprintf("%d · claude %s", a.Session.PID, a.Session.Version))
	kv("session", sanitize.Line(a.Session.SessionID[:8]+" · "+a.Session.Name))
	if len(a.Tail) > 0 {
		b.WriteString("\n")
		line(s.muted.Render("recent"))
		for _, l := range m.activityLines(a.Tail, a, w-4, true) {
			line(l)
		}
	}
	b.WriteString("\n")
	line(s.key.Render("Enter") + s.keyLabel.Render(" focus terminal  ") + s.key.Render(mentionText(a)) + s.keyLabel.Render(" to message"))
	return strings.Split(strings.TrimRight(b.String(), "\n"), "\n"), reqFrom, reqTo
}

// completionRows lists matching agents while an @mention is being typed.
func (m Model) completionRows(w int) []string {
	compl := completions(m.input.Value(), m.agents)
	if len(compl) == 0 {
		return nil
	}
	if len(compl) > 5 {
		compl = compl[:5]
	}
	s := m.st
	rows := make([]string, 0, len(compl))
	for i, a := range compl {
		fg, dim, muted, bright, _, _, _, marker := m.rowStyles(i == m.complIdx%len(compl))
		line := marker + fg.Render("  ") + bright.Render(a.Name) + dim.Render(hostChip(a)) + fg.Render("  ") + muted.Render(strings.ToLower(a.Labels.Role)) + fg.Render("  ") + dim.Render(a.Status.String())
		if i == m.complIdx%len(compl) {
			line = fill(line, min(w, 60), fg)
		}
		rows = append(rows, line)
	}
	_ = s
	return rows
}

// prompt draws the command line between two rules, the way Claude Code
// does: attachments first (thumbnails when the terminal draws them, the
// chip and file name always), then the text at its current height.
func (m Model) prompt(w int) []string {
	s := m.st
	rule := s.border.Render(strings.Repeat("─", max(0, w)))
	rows := []string{rule}
	if list := m.referenced(m.input.Value()); len(list) > 0 {
		if m.kitty {
			strip := make([]string, thumbRows)
			for _, a := range list {
				for r, tr := range thumbRowsFor(a) {
					label := ""
					if r == 0 {
						label = " " + s.dim.Render(a.Chip()+" "+filepath.Base(a.Path))
					}
					strip[r] += " " + tr + label + "  "
				}
			}
			rows = append(rows, strip...)
		} else {
			var chips []string
			for _, a := range list {
				chips = append(chips, s.dim.Render(a.Chip()+" "+filepath.Base(a.Path)))
			}
			rows = append(rows, " "+strings.Join(chips, "   "))
		}
	}
	if label := m.selectionLabel(); label != "" {
		verb := "comment on "
		if m.rev.editing != "" {
			verb = "editing the comment on "
		}
		rows = append(rows, " "+s.attn.Render("✎ ")+s.dim.Render(verb)+s.fg.Render(label)+s.muted.Render(" · type, then Enter · @Name and /commands still work"))
	}
	for _, l := range strings.Split(m.input.View(), "\n") {
		rows = append(rows, " "+l)
	}
	return append(rows, rule)
}

func (m Model) hints(w int) string {
	s := m.st
	var items [][2]string
	switch {
	case m.help:
		items = [][2]string{{"/allow /deny /always", "answer the selected prompt"}, {"/answer <text>", "answer a question held in Den"}, {"/open", "focus terminal"}, {"/dismiss", "drop a notice"}, {"/unread", "keep the selected item unread"}, {"/close [idle|<name>] [--force]", "close agents"}, {"/reopen [--fork] <name>", "reopen a closed agent"}, {"/copy", "last command block"}, {"/rescan", ""}, {"/fleet /inbox /orch /review /map", "switch view"}, {"/fleet project · flat", "the Fleet by project, or not"}, {"/project <name> · add <dir> · drop", ""}, {"/remote [connect <host>]", "remote hosts"}, {"/mission <goal> · move <project>", ""}, {"/template [<name> <target>|save <name>]", "missions from a saved shape"}, {"/quit", ""}, {"⇧Enter Alt+Enter ^J", "newline"}, {"^V", "paste image"}, {"Esc", "clear line"}}
	case len(completions(m.input.Value(), m.agents)) > 0:
		items = [][2]string{{"Tab", "complete name"}, {"↑ ↓", "pick"}, {"Enter", "send"}, {"Esc", "clear"}}
	case m.tab == tabOverview && m.ov.proposal != "":
		items = [][2]string{{"^Y", "start"}, {"^N", "drop"}, {"Enter", "refine"}, {"^E click", "actions in full"}, {"Esc", "keep for later"}, {"← →", "project"}, {"Tab", "view"}}
	case m.tab == tabOverview && m.ov.open != "":
		items = [][2]string{{"↑ ↓", "scroll"}, {"^Y ^N", "answer"}, {"Enter", "message the mission"}, {"^E click", "actions in full"}, {"drag", "select · click a block: copy"}, {"/pause /resume /stop /budget", ""}, {"Esc", "back"}, {"Tab", "view"}}
	case m.tab == tabOverview:
		items = [][2]string{{"← →", "project"}, {"↑ ↓", "select"}, {"Enter", "mission · empty: open"}, {"^Y ^N", "answer"}, {"/template", "saved mission shapes"}, {"/pause /stop", ""}, {"^O", "orchestrator"}, {"Tab", "view"}, {"^C", "quit"}}
	case m.tab == tabInbox:
		items = [][2]string{{"↑ ↓", "select"}, {"^Y", "allow"}, {"^N", "deny"}, {"/always", "allow again"}, {"Enter", "send · empty: open terminal"}, {"^X", "close agent"}, {"^O", "orchestrator"}, {"Tab", "view"}, {"/help", ""}, {"^C", "quit"}}
	case m.tab == tabReview:
		items = [][2]string{{"← →", "panes"}, {"↑ ↓", "line"}, {"⇧↑ ⇧↓ drag", "range"}, {"type Enter", "comment"}, {"Enter", "empty: edit comment"}, {"/delete", ""}, {"/send [name|orchestrator]", "send drafts"}, {"Tab", "view"}, {"^C", "quit"}}
		if w := m.curWork(); w != nil && w.Worktree && w.Host == "" { // remote worktrees are merged on their host
			items = append(items[:7:7], [2]string{"/merge", "ask to merge " + w.Branch}, [2]string{"/cleanup", "remove its worktree"}, items[7], items[8])
		}
	case m.tab == tabOrch:
		items = [][2]string{{"Enter", "send"}, {"⇧Enter", "newline"}, {"@Name", "one agent"}, {"↑ ↓", "cards, actions"}, {"drag", "select · click a block: copy"}, {"^V", "paste image"}, {"^O", "back"}, {"/help", ""}, {"^C", "quit"}}
	case m.tree.mapView:
		items = [][2]string{{"↑ ↓", "walk"}, {"← →", "parent · child"}, {"Enter", "send · empty: focus terminal"}, {"^T", "list"}, {"^O", "orchestrator"}, {"Tab", "view"}, {"^C", "quit"}}
	case m.focus == paneDetail:
		items = [][2]string{{"↑ ↓", "scroll"}, {"←", "agents"}, {"Enter", "send · empty: focus terminal"}, {"Tab", "view"}, {"/help", ""}, {"^C", "quit"}}
	default:
		items = [][2]string{{"↑ ↓", "select"}, {"→", "details"}, {"Enter", "send · empty: focus terminal"}, {"^Y ^N", "answer"}, {"^X", "close"}, {"^T", "map"}, {"^O", "orchestrator"}, {"Tab", "view"}, {"/help", ""}, {"^C", "quit"}}
	}
	var parts []string
	for _, it := range items {
		if it[1] == "" {
			parts = append(parts, s.key.Render(it[0]))
			continue
		}
		parts = append(parts, s.key.Render(it[0])+" "+s.keyLabel.Render(it[1]))
	}
	return " " + strings.Join(parts, "   ")
}

// maxShownAge is the oldest age Den shows for a remote agent; anything
// older is an instant it does not know (zero) or one no agent could have.
const maxShownAge = 366 * 24 * time.Hour

// agentAge is how long ago at was, for agent a. A remote agent's instant
// that is unknown (the host sent no age) or not plausible reads "?": a
// host's figures never show as an absurd age.
func agentAge(a fleet.Agent, at time.Time) string {
	d := time.Since(at)
	if a.Host != "" && (at.UnixMilli() <= 0 || d > maxShownAge) {
		return "?"
	}
	return humanAge(d)
}

func humanAge(d time.Duration) string {
	switch {
	case d < 0:
		return "0s" // an instant ahead of this clock: never a negative age
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// shortPath shows a file the way the agent would say it: relative to its
// working directory, or under ~.
func shortPath(path, cwd string) string {
	if cwd != "" && strings.HasPrefix(path, cwd+"/") {
		return strings.TrimPrefix(path, cwd+"/")
	}
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, home+"/") {
		return "~/" + strings.TrimPrefix(path, home+"/")
	}
	return path
}

// inAgentColor returns a copy of the model whose code highlighting uses the
// agent's colour instead of the theme's code colour.
func (m Model) inAgentColor(a fleet.Agent) Model {
	m.st.code = lipgloss.NewStyle().Foreground(lipgloss.Color(m.agentColor(a)))
	return m
}

// activityLines renders transcript entries the way the detail pane shows
// them: prompts and replies under a hanging indent with light markdown,
// tool calls as the tool and what it was for with the call as a code block,
// diffs and output underneath. Code takes the agent's colour. Entries are
// separated by a blank line; newestFirst reverses the order.
func (m Model) activityLines(tail []transcript.Entry, a fleet.Agent, textW int, newestFirst bool) []string {
	s := m.st
	m = m.inAgentColor(a)
	// A remote host's text is never handed to a lexer: the bytes are the
	// host's to choose, and a lexer can be made slow.
	m.plainCode = m.plainCode || a.Host != ""
	// Nor are names coloured: these are the agent's own words, and a
	// coloured @Name is Den's word that the agent is in the fleet.
	m.quietMentions = true
	textW = max(12, textW)
	var out []string
	prose := func(prefix string, pstyle lipgloss.Style, text string, plain bool) {
		indent := strings.Repeat(" ", ansi.StringWidth(prefix))
		width := max(12, textW-ansi.StringWidth(prefix))
		var body []string
		if plain {
			body = m.plainLines(text, width, s.dim)
		} else {
			body = m.markdownLines(text, width)
		}
		for i, l := range body {
			if i == 0 {
				out = append(out, pstyle.Render(prefix)+l)
				continue
			}
			out = append(out, indent+l)
		}
	}
	for n := range tail {
		i := n
		if newestFirst {
			i = len(tail) - 1 - n
		}
		e := tail[i]
		switch e.Kind {
		case "you":
			prose("you › ", s.muted, e.Text, true)
		case "tool":
			head := s.ok.Render("● ") + s.fg.Bold(true).Render(e.Tool)
			if e.Text != "" {
				head += "  " + s.dim.Render(ansi.Truncate(e.Text, max(8, textW-ansi.StringWidth(e.Tool)-4), "…"))
			}
			out = append(out, head)
			if e.Code != "" && e.Tool != "Edit" && e.Tool != "Write" && e.Tool != "MultiEdit" {
				for _, l := range m.codeBlockN(e.Code, textW-2, 3) {
					out = append(out, "  "+l)
				}
			}
			for _, c := range e.Changes {
				added, removed := c.Counts()
				what := fmt.Sprintf("Updated %s (+%d -%d)", shortPath(c.Path, a.Session.CWD), added, removed)
				switch {
				case c.Hidden():
					what = shortPath(c.Path, a.Session.CWD) + ": " + review.UnsafeNote
				case c.Deleted:
					what = "Deleted " + shortPath(c.Path, a.Session.CWD)
				case c.Created:
					what = fmt.Sprintf("Wrote %s (%d lines)", shortPath(c.Path, a.Session.CWD), added)
				}
				out = append(out, "  "+s.muted.Render("⎿ ")+s.dim.Render(what))
				if c.Deleted || c.Hidden() {
					continue
				}
				for _, l := range m.diffLines(c.Hunks, textW-4) {
					out = append(out, "    "+l)
				}
			}
			if e.Output != "" && len(e.Changes) == 0 {
				first := true
				for _, raw := range strings.Split(e.Output, "\n") {
					for _, l := range strings.Split(ansi.Wrap(raw, max(8, textW-4), ""), "\n") {
						if first {
							out = append(out, "  "+s.muted.Render("⎿ ")+s.dim.Render(l))
							first = false
							continue
						}
						out = append(out, "    "+s.dim.Render(l))
					}
				}
			}
		default:
			prose("", s.fg, e.Text, false)
		}
		if n < len(tail)-1 {
			out = append(out, "")
		}
	}
	return out
}
