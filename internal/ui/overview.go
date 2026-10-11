package ui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/mission"
)

// overviewState is the Overview tab: which mission row is selected, which
// mission is open, and the proposal waiting for ^Y.
type overviewState struct {
	cur      int
	open     string // mission id shown in full
	proposal string // mission id of the pending proposal
	scroll   int    // lines from the bottom of the open mission or proposal
}

// projectStrip is the row under the tabs that names the projects and the
// pick, shown on the Overview and Review views.
func (m Model) projectStrip(w int) string {
	s := m.st
	if len(m.proj.list) == 0 {
		return ansi.Truncate(" "+s.dim.Render("no project yet: projects appear when an agent edits files in a repository"), w, "…")
	}
	var parts []string
	parts = append(parts, s.section.Render("PROJECT"))
	for _, p := range m.proj.list {
		label := fmt.Sprintf("%s %d", p.Name, len(p.Agents))
		if p.Root == m.proj.cur {
			parts = append(parts, s.keycapOn.Render(label))
		} else {
			parts = append(parts, s.keycap.Render(label))
		}
	}
	if m.proj.loose > 0 {
		parts = append(parts, s.muted.Render(fmt.Sprintf("no project %d", m.proj.loose)))
	}
	left := " " + strings.Join(parts, "  ")
	right := ""
	if p, ok := m.curProject(); ok {
		right = shortPath(p.Root, "")
		if p.Branch != "" {
			right += " · " + p.Branch
		}
		if n := len(m.missions.ForProject(p.Root)); n > 0 {
			right += fmt.Sprintf(" · %d missions", n)
		}
		right = s.dim.Render(right) + " "
	}
	// The path is a nicety: it goes before the chips get cut.
	if ansi.StringWidth(left)+ansi.StringWidth(right)+2 > w {
		right = ""
	}
	return ansi.Truncate(justify(left, right, w), w, "…")
}

// stripRows is the project strip with a blank line above and below, so it
// sits apart from the tabs and the panes.
const stripRows = 3

func (m Model) stripBlock(w int) string {
	return "\n" + m.projectStrip(w) + "\n"
}

// overviewPanes lays the tab out: the strip, the project's agents on the
// left and its missions on the right.
func (m Model) overviewPanes(w, h int) string {
	strip := m.stripBlock(w)
	h -= stripRows
	if h < 4 {
		return strip
	}
	if w < 72 {
		return strip + "\n" + m.box(m.missionsTitle(), m.missionsPane(w-2, h-2), w, h, true)
	}
	lw := clamp(w*42/100, 36, 64)
	rw := w - lw
	title := "Agents"
	if p, ok := m.curProject(); ok {
		title = "Agents on " + p.Name
	}
	left := m.box(title, m.projectAgentsPane(lw-2, h-2), lw, h, false)
	right := m.box(m.missionsTitle(), m.missionsPane(rw-2, h-2), rw, h, true)
	return strip + "\n" + lipgloss.JoinHorizontal(lipgloss.Top, left, right)
}

func (m Model) missionsTitle() string {
	if ms := m.missions.Get(m.ov.open); ms != nil {
		return "Mission · " + ms.Name
	}
	if m.ov.proposal != "" {
		return "Mission proposal"
	}
	return "Missions"
}

// projectAgentsPane is the agents pane narrowed to the project, without a
// cursor: the Fleet view selects agents, this one shows who is here.
func (m Model) projectAgentsPane(w, h int) string {
	agents := m.projectAgents()
	if len(agents) == 0 {
		return "\n  " + m.st.dim.Render("Nobody is working here right now.")
	}
	mo := m
	mo.agents = agents
	mo.cursor = -1
	mo.tree.mapView = false
	out := mo.agentsPane(w, h)
	lines := strings.Split(out, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	return strings.Join(lines, "\n")
}

// missionsPane shows the proposal while one is pending, the open mission
// in full, or the project's missions as rows. A proposal or a mission ends
// in its conversation, so like the chat it follows the tail, honouring
// scroll-back.
func (m Model) missionsPane(w, h int) string {
	var lines []string
	switch {
	case m.ov.proposal != "":
		lines = m.proposalLines(w)
	case m.ov.open != "":
		lines = m.missionLines(w)
	default:
		lines = m.missionRows(w)
		if len(lines) > h {
			lines = lines[:h]
		}
		return strings.Join(lines, "\n")
	}
	if len(lines) > h {
		start := clamp(len(lines)-h-m.ov.scroll, 0, len(lines)-h)
		lines = lines[start : start+h]
	}
	return strings.Join(lines, "\n")
}

// missionRows lists the project's missions, three lines each.
func (m Model) missionRows(w int) []string {
	s := m.st
	list := m.missions.ForProject(m.proj.cur)
	if len(list) == 0 {
		var out []string
		out = append(out, "")
		for _, l := range strings.Split(ansi.Wrap("No missions here yet. Describe a goal on the command line and the orchestrator will propose how to run it: once, as a loop, as a fan-out across agents, or on a schedule. Nothing starts until you press ^Y.", w-4, ""), "\n") {
			out = append(out, "  "+s.dim.Render(l))
		}
		return out
	}
	var out []string
	out = append(out, "")
	for i, ms := range list {
		fg, dim, muted, bright, ok, attn, errS, marker := m.rowStyles(i == m.ov.cur)
		state := m.missionState(ms)
		var st string
		switch state {
		case mission.Running:
			st = ok.Render("● running · " + humanAge(time.Since(ms.Updated)))
		case mission.Waiting:
			st = attn.Render("◐ needs you")
		case mission.Paused:
			st = dim.Render("‖ paused")
		case mission.Done:
			st = ok.Render("✓ done · " + humanAge(time.Since(ms.Updated)))
		case mission.Failed:
			st = errS.Render("✕ failed")
		case mission.Proposed:
			st = muted.Render("· proposed")
		}
		name := fg.Render(ms.Name)
		if i == m.ov.cur {
			name = bright.Render(ms.Name)
		}
		head := marker + fg.Render(" ") + fg.Render(ms.Shape.Glyph()) + fg.Render(" ") + name
		l1 := justify(ansi.Truncate(head, max(8, w-ansi.StringWidth(st)-2), "…"), st, w)
		l2 := marker + fg.Render("   ") + dim.Render(ansi.Truncate(m.missionBrief(ms), w-4, "…"))
		l3 := marker + fg.Render("   ") + dim.Render(ansi.Truncate(m.missionNow(ms), w-4, "…"))
		for _, l := range []string{l1, l2, l3} {
			out = append(out, fill(l, w, fg))
		}
		out = append(out, "")
	}
	return out
}

// missionBrief is the row's second line: shape, agents and done-when.
func (m Model) missionBrief(ms *mission.Mission) string {
	parts := []string{ms.Shape.Label()}
	if ms.Cadence != "" {
		parts[0] += " " + ms.Cadence
	}
	if len(ms.Agents) > 0 {
		parts = append(parts, strings.Join(ms.Agents, ", "))
	}
	if n := len(ms.Team); n == 1 {
		parts = append(parts, ms.Team[0].Role)
	} else if n > 1 {
		parts = append(parts, fmt.Sprintf("team of %d", n))
	}
	if ms.DoneWhen != "" {
		parts = append(parts, "done when "+ms.DoneWhen)
	}
	return strings.Join(parts, " · ")
}

// missionNow is the row's third line: what is happening in the current
// run, the last run's summary, or the next scheduled time.
func (m Model) missionNow(ms *mission.Mission) string {
	if r := ms.CurrentRun(); r != nil {
		cards := m.missionCards(ms, r)
		var working, done, needs []string
		for _, d := range cards {
			switch d.State {
			case cardNeedsYou:
				needs = append(needs, d.Agent)
			case cardDone:
				done = append(done, d.Agent)
			case cardWorking, cardQueued:
				working = append(working, d.Agent)
			}
		}
		switch {
		case len(needs) > 0:
			return fmt.Sprintf("run %d · %s needs you · %d of %d done", r.N, strings.Join(needs, ", "), len(done), len(cards))
		case len(cards) == 0:
			return fmt.Sprintf("run %d · dispatching", r.N)
		default:
			return fmt.Sprintf("run %d · %d of %d done · %s working", r.N, len(done), len(cards), strings.Join(working, ", "))
		}
	}
	if n := len(ms.Runs); n > 0 && ms.Runs[n-1].Summary != "" {
		last := fmt.Sprintf("run %d · %s", ms.Runs[n-1].N, ms.Runs[n-1].Summary)
		if ms.Shape == mission.Schedule && !ms.NextRun.IsZero() && ms.State == mission.Running {
			last = "next " + untilShort(ms.NextRun) + " · " + last
		}
		return last
	}
	if ms.Shape == mission.Schedule && !ms.NextRun.IsZero() {
		return "next " + untilShort(ms.NextRun)
	}
	if ms.Reason != "" {
		return ms.Reason
	}
	return ""
}

func untilShort(t time.Time) string {
	d := time.Until(t)
	if d <= 0 {
		return "now"
	}
	if d < 48*time.Hour {
		return "in " + humanAge(d)
	}
	return t.Format("Mon 15:04")
}

// fieldLines spells out a mission's fields.
func (m Model) fieldLines(ms *mission.Mission, w int) []string {
	s := m.st
	var out []string
	kv := func(k, v string, style lipgloss.Style) {
		if v == "" {
			return
		}
		label := s.dim.Render(fmt.Sprintf("  %-12s", k))
		for i, l := range strings.Split(ansi.Wrap(v, max(10, w-14), ""), "\n") {
			if i > 0 {
				label = strings.Repeat(" ", 14)
			}
			out = append(out, label+style.Render(l))
		}
	}
	kv("mission", ms.Mission, s.fg)
	kv("done when", ms.DoneWhen, s.fg)
	if len(ms.Constraints) > 0 {
		var tags []string
		for _, c := range ms.Constraints {
			tags = append(tags, s.keycap.Render(c))
		}
		out = append(out, s.dim.Render(fmt.Sprintf("  %-12s", "constraints"))+strings.Join(tags, " "))
	}
	shape := s.bright.Render(ms.Shape.Glyph() + " " + ms.Shape.Label())
	if ms.Cadence != "" {
		shape += s.fg.Render(" · " + ms.Cadence)
	}
	if ms.Reason != "" {
		shape += s.dim.Render(" · " + ms.Reason)
	}
	out = append(out, ansi.Truncate(s.dim.Render(fmt.Sprintf("  %-12s", "shape"))+shape, w, "…"))
	if len(ms.Agents) > 0 {
		var names []string
		for _, n := range ms.Agents {
			style := s.fg
			for _, a := range m.agents {
				if a.Name == n {
					style = lipgloss.NewStyle().Foreground(lipgloss.Color(m.agentColor(a)))
				}
			}
			names = append(names, style.Render(n))
		}
		out = append(out, s.dim.Render(fmt.Sprintf("  %-12s", "agents"))+strings.Join(names, s.dim.Render(", ")))
	}
	if len(ms.Team) > 0 {
		label := s.dim.Render(fmt.Sprintf("  %-12s", "team"))
		for i, l := range m.teamLines(ms.Team) {
			if i > 0 {
				label = strings.Repeat(" ", 14)
			}
			out = append(out, ansi.Truncate(label+l, w, "…"))
		}
	}
	return out
}

// teamLines draws the roles to start as a tree, leads above their people,
// each with its model.
func (m Model) teamLines(team []mission.Member) []string {
	s := m.st
	var out []string
	seen := map[string]bool{}
	var walk func(members []mission.Member, prefix string)
	walk = func(members []mission.Member, prefix string) {
		for i, t := range members {
			key := strings.ToLower(t.Role)
			if seen[key] {
				continue
			}
			seen[key] = true
			guide, cont := "", ""
			if prefix != "" || len(out) > 0 || len(mission.Leads(team)) > 1 {
				if i == len(members)-1 {
					guide, cont = prefix+"└─ ", prefix+"   "
				} else {
					guide, cont = prefix+"├─ ", prefix+"│  "
				}
			}
			line := s.dim.Render(guide) + s.fg.Render(t.Role)
			if t.Model != "" {
				line += s.dim.Render(" · ") + s.bright.Render(t.Model)
			}
			out = append(out, line)
			walk(mission.Under(team, t.Role), cont)
		}
	}
	walk(mission.Leads(team), "")
	// Roles whose lead is not in the team still show, flat.
	for _, t := range team {
		if !seen[strings.ToLower(t.Role)] {
			seen[strings.ToLower(t.Role)] = true
			line := s.fg.Render(t.Role)
			if t.Model != "" {
				line += s.dim.Render(" · ") + s.bright.Render(t.Model)
			}
			out = append(out, line)
		}
	}
	return out
}

// proposalLines is the orchestrator's proposal, waiting for ^Y.
func (m Model) proposalLines(w int) []string {
	s := m.st
	ms := m.missions.Get(m.ov.proposal)
	if ms == nil {
		return nil
	}
	var out []string
	out = append(out, "")
	if ms.Mission != "" {
		head := "  " + s.bright.Bold(true).Render(ms.Name) + "  " + s.chip(m.st.pal.Bright).Render("PROPOSAL")
		out = append(out, ansi.Truncate(head, w, "…"))
		out = append(out, "")
		out = append(out, m.fieldLines(ms, w)...)
		out = append(out, "")
	}
	out = append(out, m.threadLines(ms, w)...)
	if ms.Mission == "" {
		out = append(out, "  "+s.key.Render("^N")+s.keyLabel.Render(" drop")+"   "+s.key.Render("Esc")+s.keyLabel.Render(" keep it for later"))
		return out
	}
	out = append(out, "  "+s.key.Render("^Y")+s.keyLabel.Render(" start")+"   "+s.key.Render("^N")+s.keyLabel.Render(" drop")+"   "+s.dim.Render("or type to refine: \"make it a schedule\", \"use Mossy\""))
	return out
}

// threadLines is the mission's conversation with the orchestrator, drawn
// the way the Orchestrator tab draws it. Cards show under their runs, not
// here. A proposal from before threads were kept shows the words it
// started from.
func (m Model) threadLines(ms *mission.Mission, w int) []string {
	lines, _, _ := m.chatLines(w, func(e chatEntry) bool { return e.mission == ms.ID && e.card == nil })
	if len(lines) == 0 && ms.Text != "" {
		lines = append(m.plainLines("you › "+ms.Text, w-4, m.st.dim), "")
	}
	return lines
}

// missionLines is an open mission: its fields, its runs with their cards,
// newest run first, then its conversation.
func (m Model) missionLines(w int) []string {
	s := m.st
	ms := m.missions.Get(m.ov.open)
	if ms == nil {
		return nil
	}
	now := time.Now()
	var out []string
	out = append(out, "")
	state := m.missionState(ms)
	st := s.dim.Render(string(state))
	switch state {
	case mission.Running:
		st = s.ok.Render("● running · " + humanAge(now.Sub(ms.Created)))
	case mission.Waiting:
		st = s.attn.Render("◐ needs you")
	case mission.Done:
		st = s.ok.Render("✓ done")
	case mission.Failed:
		st = s.err.Render("✕ failed")
	case mission.Paused:
		st = s.dim.Render("‖ paused")
	}
	out = append(out, justify("  "+s.bright.Bold(true).Render(ms.Name), st+" ", w))
	out = append(out, "")
	out = append(out, m.fieldLines(ms, w)...)
	for i := len(ms.Runs) - 1; i >= 0; i-- {
		r := ms.Runs[i]
		out = append(out, "")
		when := r.StartedAt.Format("15:04")
		if !r.EndedAt.IsZero() {
			when += " → " + r.EndedAt.Format("15:04")
		}
		head := s.dim.Render(fmt.Sprintf("  run %d · %s", r.N, when))
		trail := ""
		if r.Summary != "" {
			trail = s.dim.Render(ansi.Truncate(r.Summary, max(10, w-30), "…")) + " "
		}
		out = append(out, ansi.Truncate(justify(head, trail, w), w, "…"))
		if i == len(ms.Runs)-1 || r.EndedAt.IsZero() {
			for _, d := range m.missionCards(ms, &r) {
				out = append(out, "")
				for _, l := range m.cardLines(d, w-2, false, now) {
					out = append(out, " "+l)
				}
			}
		}
	}
	out = append(out, "")
	out = append(out, m.threadLines(ms, w)...)
	out = append(out, "  "+s.key.Render("Esc")+s.keyLabel.Render(" back")+"   "+s.key.Render("/pause /resume /stop")+"   "+s.dim.Render("type to talk to the mission's agents through the orchestrator"))
	return out
}
