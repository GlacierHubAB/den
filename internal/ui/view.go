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

	// Rows: top bar, tabs, panes, prompt, hints.
	paneH := h - 4
	if paneH < 6 {
		paneH = 6
	}
	out.WriteString(m.panes(w, paneH) + "\n")
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
	inboxLabel := "2 Inbox"
	if n := m.store.Waiting(); n > 0 {
		inboxLabel += " " + s.badgeN.Render(fmt.Sprint(n))
	}
	style := func(t tab) lipgloss.Style {
		if m.tab == t {
			return s.tabOn
		}
		return s.tabOff
	}
	return "  " + style(tabFleet).Render("1 Fleet") + "   " + style(tabInbox).Render(inboxLabel) + "   " + s.tabOff.Render("3 Orchestrator") + "   " + s.tabOff.Render("? help")
}

func (m Model) panes(w, h int) string {
	if m.tab == tabInbox {
		return m.box("Inbox", m.inboxPane(w-2, h-2), w, h, true)
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
		role := ""
		if a.Labels.Role != "" {
			role = "  " + s.muted.Render(strings.ToUpper(a.Labels.Role))
		}
		name := s.fg.Render(a.Name)
		if i == m.cursor {
			name = s.bright.Render(a.Name)
		}
		l1 := fmt.Sprintf(" %s %s%s", m.glyph(a.Status), name, role)
		marker := "▸ "
		if a.Status == fleet.Idle || a.Status == fleet.Gone {
			marker = "↳ "
		}
		l2 := "   " + s.fg.Render(ansi.Truncate(marker+a.Task(), w-4, "…"))
		if i != m.cursor {
			l2 = "   " + s.dim.Render(ansi.Truncate(marker+a.Task(), w-4, "…"))
		}
		meta := a.Where()
		age := humanAge(a.Since())
		if a.Status != fleet.Gone {
			meta += "  " + age
		}
		l3 := "   " + s.dim.Render(ansi.Truncate(meta, w-4, "…"))
		rows := []string{l1, l2, l3}
		for j, r := range rows {
			if i == m.cursor {
				pad := w - ansi.StringWidth(r)
				if pad < 0 {
					pad = 0
				}
				r = s.sel.Render(r + strings.Repeat(" ", pad))
			}
			rows[j] = r
		}
		b.WriteString(strings.Join(rows, "\n") + "\n")
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
		stripe := s.muted.Render("▏")
		kindStyle := s.muted
		switch {
		case it.Answerable():
			stripe, kindStyle = s.attn.Render("▌"), s.attn
		case it.State == inbox.InTerminal && it.Kind <= inbox.Question:
			stripe, kindStyle = s.attn.Render("▏"), s.attn
		case it.Kind == inbox.Done && it.State != inbox.Resolved:
			stripe, kindStyle = s.ok.Render("▌"), s.ok
		}
		name := s.fg.Render(m.nameFor(it.SessionID))
		if active && i == m.inboxCur {
			name = s.bright.Render(m.nameFor(it.SessionID))
		}
		head := fmt.Sprintf("%s %s  %s  %s", stripe, name, kindStyle.Render(strings.ToUpper(it.Kind.String())), s.muted.Render(humanAge(time.Since(it.At))))
		rows := []string{head}
		body := s.fg
		if it.State == inbox.Resolved || it.Kind >= inbox.Done {
			body = s.dim
		}
		for _, l := range strings.Split(ansi.Wrap(it.Title, w-4, ""), "\n") {
			rows = append(rows, stripe+" "+body.Render(l))
		}
		for _, l := range it.Body {
			rows = append(rows, stripe+" "+s.dim.Render(ansi.Truncate(l, w-4, "…")))
		}
		for j, o := range it.Options {
			rows = append(rows, stripe+" "+s.bright.Render(fmt.Sprint(j+1))+" "+s.fg.Render(ansi.Truncate(o, w-6, "…")))
		}
		switch {
		case it.Answerable():
			acts := s.key.Render("y") + s.keyLabel.Render(" allow  ") + s.key.Render("a") + s.keyLabel.Render(" always this session  ") + s.key.Render("n") + s.keyLabel.Render(" deny  ") + s.key.Render("o") + s.keyLabel.Render(" open terminal")
			if it.Kind == inbox.Plan {
				acts = s.key.Render("y") + s.keyLabel.Render(" approve  ") + s.key.Render("n") + s.keyLabel.Render(" ask for changes  ") + s.key.Render("o") + s.keyLabel.Render(" read it in the terminal")
			}
			rows = append(rows, stripe+" "+acts)
		case it.State == inbox.InTerminal:
			why := "answer in the terminal"
			if it.Resolved != "" {
				why = it.Resolved + " · answer in the terminal"
			}
			rows = append(rows, stripe+" "+s.dim.Render(why)+"  "+s.key.Render("o")+s.keyLabel.Render(" open"))
		case it.State == inbox.Resolved:
			rows = append(rows, stripe+" "+s.muted.Render(it.Resolved))
		default:
			rows = append(rows, stripe+" "+s.key.Render("o")+s.keyLabel.Render(" open  ")+s.key.Render("x")+s.keyLabel.Render(" dismiss"))
		}
		for j, r := range rows {
			if active && i == m.inboxCur {
				pad := w - ansi.StringWidth(r)
				if pad < 0 {
					pad = 0
				}
				r = s.sel.Render(r + strings.Repeat(" ", pad))
			}
			rows[j] = r
		}
		b.WriteString(strings.Join(rows, "\n") + "\n\n")
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
	b.WriteString("\n")
	line(s.key.Render("Enter") + s.keyLabel.Render(" focus terminal  ") + s.key.Render("r") + s.keyLabel.Render(" rescan"))
	return strings.TrimRight(b.String(), "\n")
}

func (m Model) prompt(w int) string {
	s := m.st
	if m.help {
		return " " + s.fg.Render("j k") + s.dim.Render(" move  ") + s.fg.Render("g G") + s.dim.Render(" first/last  ") +
			s.fg.Render("Enter") + s.dim.Render(" focus terminal  ") + s.fg.Render("Tab") + s.dim.Render(" next pane  ") +
			s.fg.Render("r") + s.dim.Render(" rescan  ") + s.fg.Render("q") + s.dim.Render(" quit")
	}
	if it := m.topAnswerable(); it != nil && !m.inboxActive() {
		return " " + s.attn.Render("›") + " " + s.fg.Render(m.nameFor(it.SessionID)+": "+it.Title) + "  " + s.key.Render("y") + s.keyLabel.Render(" allow ") + s.key.Render("n") + s.keyLabel.Render(" deny ") + s.key.Render("a") + s.keyLabel.Render(" always")
	}
	return " " + s.muted.Render("›") + " " + s.muted.Render("Messaging arrives in milestone 3. @Name will address one agent, plain text the orchestrator.")
}

func (m Model) hints(w int) string {
	s := m.st
	items := [][2]string{{"j k", "move"}, {"Enter", "focus terminal"}, {"y n a", "answer"}, {"1 2", "tabs"}, {"Tab", "pane"}, {"?", "help"}, {"q", "quit"}}
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
