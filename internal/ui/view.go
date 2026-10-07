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
	out.WriteString(ansi.Truncate(m.topBar(w), w, "…") + "\n")
	out.WriteString(ansi.Truncate(m.tabs(w), w, "…") + "\n")

	// Rows: top bar, tabs, panes, [completions], prompt, hints.
	popup := m.completionRows(w)
	paneH := h - 4 - len(popup)
	if paneH < 6 {
		paneH = 6
	}
	out.WriteString(m.panes(w, paneH) + "\n")
	for _, r := range popup {
		out.WriteString(ansi.Truncate(r, w, "…") + "\n")
	}
	out.WriteString(ansi.Truncate(m.prompt(w), w, "…") + "\n")
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

func (m Model) topBar(w int) string {
	s := m.st
	needs, working, idle, unknown, _ := m.counts()
	parts := []string{fmt.Sprintf("%d agents", len(m.agents))}
	if needs > 0 {
		parts = append(parts, s.attn.Render(fmt.Sprintf("%d need you", needs)))
	}
	parts = append(parts, fmt.Sprintf("%d working", working), fmt.Sprintf("%d idle", idle))
	if unknown > 0 {
		parts = append(parts, fmt.Sprintf("%d unknown", unknown))
	}
	left := s.brand.Render("den") + "  " + s.dim.Render(strings.Join(parts, " · "))
	right := s.dim.Render(time.Now().Format("15:04"))
	if m.flash != "" {
		if m.flashErr {
			right = s.err.Render(m.flash) + "  " + right
		} else {
			right = s.ok.Render(m.flash) + "  " + right
		}
	}
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
	inboxCount := ""
	if n := m.store.Waiting(); n > 0 {
		inboxCount = " " + s.badgeN.Render(fmt.Sprint(n))
	}
	return "  " + style(tabFleet).Render("1 Fleet") + "   " + style(tabInbox).Render("2 Inbox") + inboxCount + "   " + style(tabOrch).Render("3 Orchestrator") + orchStatus + "   " + s.tabOff.Render("? help")
}

func (m Model) panes(w, h int) string {
	if m.tab == tabInbox {
		return m.box("Inbox", m.inboxPane(w-2, h-2), w, h, true)
	}
	if m.tab == tabOrch {
		return m.box("Orchestrator", m.orchPane(w-2, h-2), w, h, true)
	}
	// Column plan: agents | inbox | detail, collapsing on narrow terminals.
	switch {
	case w < 70:
		return m.box("Agents", m.agentsPane(w-2, h-2), w, h, m.focus == paneAgents)
	case w < 110:
		lw := clamp(w*40/100, 34, 60)
		rw := w - lw
		a := m.box("Agents", m.agentsPane(lw-2, h-2), lw, h, m.focus == paneAgents)
		d := m.box(m.detailTitle(), m.detailPane(rw-2, h-2), rw, h, m.focus != paneAgents)
		return lipgloss.JoinHorizontal(lipgloss.Top, a, d)
	default:
		lw := clamp(w*28/100, 36, 52)
		rw := clamp(w*30/100, 38, 60)
		cw := w - lw - rw
		a := m.box("Agents", m.agentsPane(lw-2, h-2), lw, h, m.focus == paneAgents)
		i := m.box("Inbox", m.inboxPane(cw-2, h-2), cw, h, m.focus == paneInbox)
		d := m.box(m.detailTitle(), m.detailPane(rw-2, h-2), rw, h, m.focus == paneDetail)
		return lipgloss.JoinHorizontal(lipgloss.Top, a, i, d)
	}
}

func (m Model) detailTitle() string {
	if a, ok := m.selected(); ok {
		return a.Name
	}
	return "Agent"
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
		return "\n " + s.dim.Render("No Claude Code sessions running.")
	}
	var b strings.Builder
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
			b.WriteString(" " + s.section.Render(fmt.Sprintf("%s · %d", title, counts[a.Status])) + "\n")
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
			role = muted.Render("  " + strings.ToUpper(a.Labels.Role))
		}
		l1 := marker + glyph + fg.Render(" ") + name + role

		taskMark := "▸ "
		if a.Status == fleet.Idle || a.Status == fleet.Gone {
			taskMark = "↳ "
		}
		taskStyle := dim
		if selected {
			taskStyle = fg
		}
		l2 := marker + fg.Render("  ") + taskStyle.Render(ansi.Truncate(taskMark+a.Task(), w-4, "…"))

		meta := a.Where()
		if a.Status != fleet.Gone {
			meta += "  " + humanAge(a.Since())
		}
		l3 := marker + fg.Render("  ") + dim.Render(ansi.Truncate(meta, w-4, "…"))
		if a.HasUsage {
			ctx := fmt.Sprintf("ctx %d%%", a.Usage.Percent())
			cs := dim
			if a.Usage.Percent() >= 90 {
				cs = err
			} else if a.Usage.Percent() >= 75 {
				cs = attn
			}
			if room := w - 3 - ansi.StringWidth(meta) - len(ctx) - 2; room >= 0 {
				l3 = marker + fg.Render("  ") + dim.Render(meta) + fg.Render(strings.Repeat(" ", room+2)) + cs.Render(ctx)
			}
		}
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
	var b strings.Builder
	active := m.inboxActive()
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
		case it.Kind == inbox.Done && it.State != inbox.Resolved:
			stripe, kindStyle = ok.Render("▌"), ok
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
		name := fg.Render(m.nameFor(it.SessionID))
		if selected {
			name = bright.Render(m.nameFor(it.SessionID))
		}
		sp := fg.Render(" ")
		head := stripe + sp + name + sp + sp + kindStyle.Render(strings.ToUpper(it.Kind.String())) + sp + sp + muted.Render(humanAge(time.Since(it.At)))
		rows := []string{head}
		body := fg
		if it.State == inbox.Resolved || it.Kind >= inbox.Done {
			body = dim
		}
		titleLines := strings.Split(ansi.Wrap(it.Title, w-4, ""), "\n")
		bodyLines := it.Body
		if it.Kind >= inbox.Done || it.State == inbox.Resolved {
			// Notices are a glance, not a transcript: two lines of title, one of body.
			if len(titleLines) > 2 {
				titleLines = titleLines[:2]
				titleLines[1] = ansi.Truncate(titleLines[1], w-6, "…")
			}
			if len(bodyLines) > 1 {
				bodyLines = bodyLines[:1]
			}
		}
		for _, l := range titleLines {
			rows = append(rows, stripe+sp+body.Render(l))
		}
		for _, l := range bodyLines {
			rows = append(rows, stripe+sp+dim.Render(ansi.Truncate(l, w-4, "…")))
		}
		for j, o := range it.Options {
			rows = append(rows, stripe+sp+bright.Render(fmt.Sprint(j+1))+sp+fg.Render(ansi.Truncate(o, w-6, "…")))
		}
		key := func(k, label string) string { return bright.Render(k) + dim.Render(" "+label) }
		switch {
		case it.Kind == inbox.Sent:
			rows = append(rows, stripe+sp+dim.Render(it.Resolved)+sp+sp+key("x", "dismiss"))
		case it.Answerable():
			acts := key("y", "allow") + sp + sp + key("a", "always this session") + sp + sp + key("n", "deny") + sp + sp + key("o", "open terminal")
			if it.Kind == inbox.Plan {
				acts = key("y", "approve") + sp + sp + key("n", "ask for changes") + sp + sp + key("o", "read it in the terminal")
			}
			rows = append(rows, stripe+sp+acts)
		case it.State == inbox.InTerminal:
			why := "answer in the terminal"
			if it.Resolved != "" {
				why = it.Resolved + " · answer in the terminal"
			}
			rows = append(rows, stripe+sp+dim.Render(why)+sp+sp+key("o", "open"))
		case it.State == inbox.Resolved:
			rows = append(rows, stripe+sp+muted.Render(it.Resolved))
		default:
			rows = append(rows, stripe+sp+key("o", "open")+sp+sp+key("x", "dismiss"))
		}
		for _, r := range rows {
			b.WriteString(fill(r, w, fg) + "\n")
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m Model) detailPane(w, h int) string {
	s := m.st
	a, ok := m.selected()
	if !ok {
		return "\n " + s.dim.Render("Select an agent.")
	}
	var b strings.Builder
	line := func(str string) { b.WriteString(" " + ansi.Truncate(str, w-2, "…") + "\n") }
	wrap := func(prefix, text string, style lipgloss.Style) {
		for i, l := range strings.Split(ansi.Wrap(text, w-2-ansi.StringWidth(prefix), ""), "\n") {
			p := prefix
			if i > 0 {
				p = strings.Repeat(" ", ansi.StringWidth(prefix))
			}
			b.WriteString(" " + style.Render(p+l) + "\n")
		}
	}
	role := ""
	if a.Labels.Role != "" {
		role = "  " + s.roleChip.Render(strings.ToUpper(a.Labels.Role))
	}
	line(s.bright.Bold(true).Render(a.Name) + role)
	b.WriteString("\n")
	wrap("▸ ", a.Task(), s.fg)
	for i, h := range a.Labels.History {
		if i >= 3 {
			break
		}
		prefix := "↳ earlier: "
		if i > 0 {
			prefix = "           "
		}
		wrap(prefix, h, s.dim)
	}
	b.WriteString("\n")
	kv := func(k, v string) {
		if v == "" {
			return
		}
		line(s.dim.Render(fmt.Sprintf("%-9s", k)) + s.fg.Render(v))
	}
	kv("status", a.Status.String()+" · "+humanAge(a.Since()))
	if a.Model != "" {
		kv("model", prettyModel(a.Model))
	}
	if a.HasUsage {
		pct := a.Usage.Percent()
		barW := clamp(w-30, 6, 16)
		full := pct * barW / 100
		fill := s.bright
		if pct >= 90 {
			fill = s.err
		} else if pct >= 75 {
			fill = s.attn
		}
		bar := fill.Render(strings.Repeat("█", full)) + s.muted.Render(strings.Repeat("░", barW-full))
		left := (a.Usage.Window - a.Usage.Tokens) / 1000
		if left < 0 {
			left = 0
		}
		kv("context", fmt.Sprintf("%s %d%% · %dk left", bar, pct, left))
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
		for _, e := range a.Tail {
			switch e.Kind {
			case "you":
				line(s.muted.Render("you › ") + s.dim.Render(e.Text))
			case "tool":
				line(s.ok.Render("● ") + s.dim.Render(e.Text))
			default:
				line(s.fg.Render(e.Text))
			}
		}
	}
	b.WriteString("\n")
	line(s.key.Render("Enter") + s.keyLabel.Render(" focus terminal  ") + s.key.Render("@") + s.keyLabel.Render(" message  ") + s.key.Render("r") + s.keyLabel.Render(" rescan"))
	return strings.TrimRight(b.String(), "\n")
}

// completionRows lists matching agents while an @mention is being typed.
func (m Model) completionRows(w int) []string {
	if !m.input.Focused() {
		return nil
	}
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

func (m Model) prompt(w int) string {
	s := m.st
	if m.input.Focused() {
		m.input.SetWidth(max(10, w-4))
		return " " + m.input.View()
	}
	if m.help {
		return " " + s.fg.Render("j k") + s.dim.Render(" move  ") + s.fg.Render("g G") + s.dim.Render(" first/last  ") +
			s.fg.Render("Enter") + s.dim.Render(" focus terminal  ") + s.fg.Render("Tab") + s.dim.Render(" next pane  ") +
			s.fg.Render("r") + s.dim.Render(" rescan  ") + s.fg.Render("q") + s.dim.Render(" quit")
	}
	if it := m.topAnswerable(); it != nil && !m.inboxActive() {
		return " " + s.attn.Render("›") + " " + s.fg.Render(m.nameFor(it.SessionID)+": "+it.Title) + "  " + s.key.Render("y") + s.keyLabel.Render(" allow ") + s.key.Render("n") + s.keyLabel.Render(" deny ") + s.key.Render("a") + s.keyLabel.Render(" always")
	}
	return " " + s.muted.Render("›") + " " + s.muted.Render("@ to message an agent · m to ask the orchestrator · plain text goes to the orchestrator")
}

func (m Model) hints(w int) string {
	s := m.st
	items := [][2]string{{"j k", "move"}, {"Enter", "focus terminal"}, {"y n a", "answer"}, {"@ m", "message"}, {"1 2 3", "tabs"}, {"Tab", "pane"}, {"?", "help"}, {"q", "quit"}}
	if m.tab == tabOrch {
		items = [][2]string{{"m", "ask"}, {"@", "message one agent"}, {"j k", "scroll"}, {"1", "fleet"}, {"?", "help"}, {"q", "quit"}}
	}
	if m.input.Focused() {
		items = [][2]string{{"Tab", "complete name"}, {"↑ ↓", "pick"}, {"Enter", "send"}, {"Esc", "cancel"}}
	}
	var parts []string
	for _, it := range items {
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
