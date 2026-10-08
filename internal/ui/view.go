package ui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
)

func (m Model) View() tea.View {
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

	// Rows: header, panes, [completions], prompt (3 rows with rules), hints.
	popup := m.completionRows(w)
	paneH := h - len(header) - 4 - len(popup)
	if paneH < 6 {
		paneH = 6
	}
	out.WriteString(m.panes(w, paneH) + "\n")
	for _, r := range popup {
		out.WriteString(ansi.Truncate(r, w, "…") + "\n")
	}
	for _, r := range m.prompt(w) {
		out.WriteString(ansi.Truncate(r, w, "…") + "\n")
	}
	out.WriteString(ansi.Truncate(m.hints(w), w, "…"))

	v := tea.NewView(out.String())
	v.AltScreen = true
	return v
}

func (m Model) counts() (needs, working, idle, unknown, gone int) {
	for _, a := range m.agents {
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
	fleetParts := []string{fmt.Sprintf("%d agents", len(m.agents))}
	if needs > 0 {
		fleetParts = append(fleetParts, s.attn.Render(fmt.Sprintf("%d need you", needs)))
	}
	fleetParts = append(fleetParts, fmt.Sprintf("%d working", working), fmt.Sprintf("%d idle", idle))
	if unknown > 0 {
		fleetParts = append(fleetParts, fmt.Sprintf("%d unknown", unknown))
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
	linksLine := s.dim.Render(strings.Join(links, " · "))

	var sys []string
	if m.hooksReady {
		sys = append(sys, "hooks on")
	} else {
		sys = append(sys, s.attn.Render("hooks off · den install-hooks"))
	}
	if m.srvErr != nil {
		sys = append(sys, s.err.Render("bridge: "+m.srvErr.Error()))
	}
	sysLine := s.muted.Render(strings.Join(sys, " · "))

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
		left := s.brand.Render("den") + "  " + fleetLine
		right := clock
		if flash != "" {
			right = flash + "  " + clock
		}
		return []string{justify(left, right, w), ansi.Truncate(m.tabs(w), w, "…")}
	}
	logo := make([]string, len(wordmark))
	for i, r := range wordmark {
		logo[i] = "  " + s.bright.Render(r) + "    "
	}
	rows := []string{
		"",
		justify(logo[0]+fleetLine, clock+"  ", w),
		justify(logo[1]+linksLine, "", w),
		justify(logo[2]+sysLine, flash+"  ", w),
		"",
		m.tabs(w),
	}
	return rows
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
	orchStatus := ""
	if m.orch.status != "" {
		orchStatus = " " + s.muted.Render(m.orch.status)
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
	left := "  " + style(tabFleet).Render("Fleet") + "   " + style(tabInbox).Render("Inbox") + inboxCount + "   " + style(tabOrch).Render("Orchestrator") + orchStatus
	return justify(left, s.keycap.Render("Tab ⇥")+"  ", w)
}

func (m Model) panes(w, h int) string {
	if m.tab == tabInbox {
		return m.box("Inbox", m.inboxPane(w-2, h-2), w, h, true)
	}
	if m.tab == tabOrch {
		return m.box("Orchestrator", m.orchPane(w-2, h-2), w, h, true)
	}
	// Fleet: agents on the left, the selected agent on the right.
	if w < 72 {
		return m.box("Agents", m.agentsPane(w-2, h-2), w, h, true)
	}
	lw := clamp(w*42/100, 36, 64)
	rw := w - lw
	a := m.box("Agents", m.agentsPane(lw-2, h-2), lw, h, m.focus == paneAgents)
	d := m.box(m.detailTitle(), m.detailPane(rw-2, h-2), rw, h, m.focus == paneDetail)
	return lipgloss.JoinHorizontal(lipgloss.Top, a, d)
}

func (m Model) detailTitle() string {
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
		return "\n  " + s.dim.Render("No Claude Code sessions running.")
	}
	var b strings.Builder
	b.WriteString("\n")
	var last fleet.Status = -1
	counts := map[fleet.Status]int{}
	for _, a := range m.agents {
		counts[a.Status]++
	}
	for i, a := range m.agents {
		if a.Status != last {
			if last != -1 {
				b.WriteString("\n")
			}
			title := strings.ToUpper(a.Status.String())
			b.WriteString("  " + s.section.Render(fmt.Sprintf("%s · %d", title, counts[a.Status])) + "\n")
			last = a.Status
		}
		selected := i == m.cursor
		fg, dim, muted, bright, _, attn, err, marker := m.rowStyles(selected)
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
		role := ""
		if a.Labels.Role != "" {
			role = fg.Foreground(lipgloss.Color(m.agentColor(a))).Render("  " + strings.ToUpper(a.Labels.Role))
		}
		l1 := marker + fg.Render(" ") + glyph + fg.Render(" ") + name + role

		taskMark := "▸ "
		if a.Status == fleet.Idle || a.Status == fleet.Gone {
			taskMark = "↳ "
		}
		taskStyle := dim
		if selected {
			taskStyle = fg
		}
		l2 := marker + fg.Render("   ") + taskStyle.Render(ansi.Truncate(taskMark+a.Task(), w-5, "…"))

		// Third line: age · harness · model · ctx, all inline. Where it runs
		// lives in the details pane.
		var meta []string
		if a.Status != fleet.Gone {
			meta = append(meta, humanAge(a.Since()))
		}
		meta = append(meta, "Claude Code")
		if pm := prettyModel(a.Model); pm != "" {
			meta = append(meta, pm)
		}
		l3 := marker + fg.Render("   ") + dim.Render(strings.Join(meta, " · "))
		if a.HasUsage {
			cs := dim
			if a.Usage.Percent() >= 90 {
				cs = err
			} else if a.Usage.Percent() >= 75 {
				cs = attn
			}
			l3 += dim.Render(" · ") + cs.Render(fmt.Sprintf("ctx %d%%", a.Usage.Percent()))
		}
		l3 = ansi.Truncate(l3, w, "…")
		for _, r := range []string{l1, l2, l3} {
			b.WriteString(fill(r, w, fg) + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
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
	var blocks [][]string
	const avatarW = 8
	for i, it := range items {
		selected := active && i == m.inboxCur
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
		sp := fg.Render(" ")
		color := m.colorFor(it.SessionID)
		name := fg.Foreground(lipgloss.Color(color)).Bold(true).Render(m.nameFor(it.SessionID))
		role := ""
		if it.Role != "" {
			role = sp + s.chip(color).Render(strings.ToUpper(it.Role))
		}
		head := name + role + sp + sp + kindStyle.Render(strings.ToUpper(it.Kind.String())) + sp + sp + muted.Render(humanAge(time.Since(it.At)))

		textW := w - 4 - avatarW - 1
		var text []string
		text = append(text, head)
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
			lines := strings.Split(ansi.Wrap(it.Title, textW, ""), "\n")
			if len(lines) > 2 {
				lines = lines[:2]
				lines[1] = ansi.Truncate(lines[1], textW-2, "…")
			}
			for _, l := range lines {
				text = append(text, body.Render(l))
			}
		}
		bodyLines := it.Body
		if (it.Kind >= inbox.Done || it.State == inbox.Resolved) && len(bodyLines) > 1 {
			bodyLines = bodyLines[:1]
		}
		for _, l := range bodyLines {
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
		case it.Answerable():
			acts := key("^Y", "allow") + sp + sp + key("/always", "this session") + sp + sp + key("^N", "deny") + sp + sp + key("/open", "terminal")
			if it.Kind == inbox.Plan {
				acts = key("^Y", "approve") + sp + sp + key("^N", "ask for changes") + sp + sp + key("/open", "read it in the terminal")
			}
			text = append(text, acts)
		case it.State == inbox.InTerminal:
			why := "answer in the terminal"
			if it.Resolved != "" {
				why = it.Resolved + " · answer in the terminal"
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
		avatar := m.smallCritter(it.SessionID, selected)
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
		blocks = append(blocks, append(rows, ""))
	}
	// Keep the selected item in view: drop whole blocks from the top until
	// the selection fits in h rows.
	start := 0
	if h > 0 && active {
		cur := clamp(m.inboxCur, 0, len(blocks)-1)
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

func (m Model) detailPane(w, h int) string {
	s := m.st
	a, ok := m.selected()
	if !ok {
		return "\n " + s.dim.Render("Select an agent.")
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
	sub += a.Session.ShortCWD()
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
	kv("status", a.Status.String()+" · "+humanAge(a.Since()))
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
	kv("where", a.Where())
	if a.HasGit {
		kv("cwd", a.Session.ShortCWD())
	}
	kv("window", a.Place())
	if a.Title != "" {
		kv("title", a.Title)
	}
	kv("started", humanAge(time.Since(a.Session.Started()))+" ago")
	kv("pid", fmt.Sprintf("%d · claude %s", a.Session.PID, a.Session.Version))
	kv("session", a.Session.SessionID[:8]+" · "+a.Session.Name)
	if len(a.Tail) > 0 {
		b.WriteString("\n")
		line(s.muted.Render("recent"))
		// Each entry wraps in full under a hanging indent; the pane scrolls.
		entry := func(prefix string, pstyle, tstyle lipgloss.Style, text string) {
			indent := strings.Repeat(" ", ansi.StringWidth(prefix))
			width := max(12, w-4-ansi.StringWidth(prefix))
			first := true
			for _, raw := range strings.Split(text, "\n") {
				for _, l := range strings.Split(ansi.Wrap(raw, width, ""), "\n") {
					if first {
						line(pstyle.Render(prefix) + tstyle.Render(l))
						first = false
						continue
					}
					line(indent + tstyle.Render(l))
				}
			}
		}
		for _, e := range a.Tail {
			switch e.Kind {
			case "you":
				entry("you › ", s.muted, s.dim, e.Text)
			case "tool":
				name, rest, _ := strings.Cut(e.Text, " ")
				entry("● "+name+" ", s.ok, s.dim, rest)
			default:
				entry("", s.fg, s.fg, e.Text)
			}
		}
	}
	b.WriteString("\n")
	line(s.key.Render("Enter") + s.keyLabel.Render(" focus terminal  ") + s.key.Render("@"+a.Name) + s.keyLabel.Render(" to message"))
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	if m.detailScroll > 0 && len(lines) > h {
		off := min(m.detailScroll, len(lines)-h)
		lines = lines[off:]
	}
	return strings.Join(lines, "\n")
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
		line := marker + fg.Render("  ") + bright.Render(a.Name) + fg.Render("  ") + muted.Render(strings.ToLower(a.Labels.Role)) + fg.Render("  ") + dim.Render(a.Status.String())
		if i == m.complIdx%len(compl) {
			line = fill(line, min(w, 60), fg)
		}
		rows = append(rows, line)
	}
	_ = s
	return rows
}

// prompt draws the command line between two rules, the way Claude Code does.
func (m Model) prompt(w int) []string {
	m.input.SetWidth(max(10, w-4))
	rule := m.st.border.Render(strings.Repeat("─", max(0, w)))
	return []string{rule, " " + m.input.View(), rule}
}

func (m Model) hints(w int) string {
	s := m.st
	var items [][2]string
	switch {
	case m.help:
		items = [][2]string{{"/allow /deny /always", "answer the selected prompt"}, {"/open", "focus terminal"}, {"/dismiss", "drop a notice"}, {"/rescan", ""}, {"/fleet /inbox /orch", "switch view"}, {"/quit", ""}, {"Esc", "clear line"}}
	case len(completions(m.input.Value(), m.agents)) > 0:
		items = [][2]string{{"Tab", "complete name"}, {"↑ ↓", "pick"}, {"Enter", "send"}, {"Esc", "clear"}}
	case m.tab == tabInbox:
		items = [][2]string{{"↑ ↓", "select"}, {"^Y", "allow"}, {"^N", "deny"}, {"/always", "allow again"}, {"Enter", "send · empty: open terminal"}, {"Tab", "view"}, {"/help", ""}, {"^C", "quit"}}
	case m.tab == tabOrch:
		items = [][2]string{{"Enter", "send"}, {"@Name", "one agent"}, {"↑ ↓", "scroll"}, {"Tab", "view"}, {"/help", ""}, {"^C", "quit"}}
	case m.focus == paneDetail:
		items = [][2]string{{"↑ ↓", "scroll"}, {"←", "agents"}, {"Enter", "send · empty: focus terminal"}, {"Tab", "view"}, {"/help", ""}, {"^C", "quit"}}
	default:
		items = [][2]string{{"↑ ↓", "select"}, {"→", "details"}, {"Enter", "send · empty: focus terminal"}, {"^Y ^N", "answer"}, {"Tab", "view"}, {"/help", ""}, {"^C", "quit"}}
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

func humanAge(d time.Duration) string {
	switch {
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
