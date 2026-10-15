package ui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/cost"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/mission"
	"github.com/GlacierHubAB/den/internal/transcript"
)

// overviewState is the Overview tab: which mission row is selected, which
// mission is open, and the proposal waiting for ^Y.
type overviewState struct {
	// sel is the selected mission's id: the row follows it when the list
	// re-sorts. idx is its row when last moved, so a mission that goes
	// selects its neighbour; top is the first mission row in view.
	sel string
	idx int
	top int

	open     string // mission id shown in full
	proposal string // mission id of the pending proposal
	scroll   int    // the proposal: lines from the top; a transcript: lines from the bottom
	listing  bool   // the template list is showing in place of the missions
	// who is the selected row of the open mission's WHO DOES WHAT;
	// transcript is the session whose transcript shows in its place, with
	// the entries last read (trAt) for it.
	who        int
	transcript string
	trEntries  []transcript.Entry
	trAt       time.Time
	// moves are the /mission move questions in the inbox: item id → what
	// ^Y on it does.
	moves map[string]pendingMove
}

// Margins inside a missions pane: a blank column, the selection bar, a
// blank column, the content, two blank columns. The border never touches
// the band or the bar.
const (
	ovLeft  = 3
	ovRight = 2
)

// ovBody is the content width inside a pane of inner width w.
func ovBody(w int) int { return max(8, w-ovLeft-ovRight) }

// ovLine is one plain line of a missions pane: margins and the content
// padded to the pane's width.
func (m Model) ovLine(content string, w int) string {
	body := ovBody(w)
	return "   " + ansi.Truncate(content, body, "…") + strings.Repeat(" ", max(0, body-ansi.StringWidth(ansi.Truncate(content, body, "…")))) + "  "
}

// ovSelLine is a selected line: the band across the whole width, gaps
// included, and the bar in the second column.
func (m Model) ovSelLine(content string, w int, fg lipgloss.Style, marker string) string {
	body := ovBody(w)
	c := ansi.Truncate(content, body, "…")
	// The missions panes' bar is the full-height ▌ the design uses; the
	// shared row style draws the Fleet's thinner one.
	marker = strings.Replace(marker, "▎", "▌", 1)
	return fg.Render(" ") + marker + fg.Render(" ") + c + fg.Render(strings.Repeat(" ", max(0, body-ansi.StringWidth(c))+ovRight))
}

// justifyIn is justify with the gap drawn in pad's style, so a selected
// row's band has no hole between its two ends.
func justifyIn(left, right string, w int, pad lipgloss.Style) string {
	gap := w - ansi.StringWidth(left) - ansi.StringWidth(right)
	if gap < 1 {
		gap = 1
	}
	return left + pad.Render(strings.Repeat(" ", gap)) + right
}

// missionCounts is how many of a project's missions are live and how many
// of those need the human: the chip and the list use the same rules.
func (m Model) missionCounts(root string) (live, needs, done int) {
	for _, ms := range m.missions.ForProject(root) {
		switch {
		case ms.Live():
			live++
			if m.missionState(ms) == mission.Waiting {
				needs++
			}
		case ms.State == mission.Done:
			done++
		}
	}
	return live, needs, done
}

// chipCount is a project chip's mission mark: ◐ and how many need the
// human, else ● and how many are live, else nothing.
func (m Model) chipCount(root string) (mark string, needsYou bool) {
	live, needs, _ := m.missionCounts(root)
	switch {
	case needs > 0:
		return fmt.Sprintf("◐%d", needs), true
	case live > 0:
		return fmt.Sprintf("●%d", live), false
	}
	return "", false
}

// projectStrip is the row under the tabs that names the projects and the
// pick, shown on the Overview and Review views.
func (m Model) projectStrip(w int) string {
	s := m.st
	fleetTotal := ""
	if f := m.fleetCost(); f.Total() > 0 {
		fleetTotal = "fleet " + f.Line()
		if by := m.hostCostNote(); by != "" {
			fleetTotal += " (" + by + ")"
		}
	}
	if m.orch.cost > 0 {
		if fleetTotal != "" {
			fleetTotal += " + "
		}
		fleetTotal += "orchestrator " + cost.Dollars(m.orch.cost, false)
	}
	if len(m.proj.list) == 0 {
		left := " " + s.dim.Render("no project yet: projects appear when an agent edits files in a repository")
		if fleetTotal != "" && ansi.StringWidth(left)+ansi.StringWidth(fleetTotal)+3 <= w {
			return justify(left, s.dim.Render(fleetTotal)+" ", w)
		}
		return ansi.Truncate(left, w, "…")
	}
	var parts []string
	parts = append(parts, s.section.Render("PROJECT"))
	sel := lipgloss.Color(s.pal.Sel)
	for _, p := range m.proj.list {
		label := fmt.Sprintf("%s %d", p.Name, len(p.Agents))
		mark, needs := m.chipCount(p.Root)
		if p.Root == m.proj.cur {
			// Inside the picked chip the mark takes the chip's own ink: a
			// colour there reads as a second selection; the glyph says it.
			if mark != "" {
				label += " " + mark
			}
			parts = append(parts, s.keycapOn.Render(label))
			continue
		}
		if mark == "" {
			parts = append(parts, s.keycap.Render(label))
			continue
		}
		ms := s.ok
		if needs {
			ms = s.attn
		}
		bg := lipgloss.NewStyle().Background(sel)
		parts = append(parts, s.keycap.Padding(0).Render(" "+label+" ")+ms.Background(sel).Render(mark)+bg.Render(" "))
	}
	if m.proj.loose > 0 {
		parts = append(parts, s.muted.Render(fmt.Sprintf("no project %d", m.proj.loose)))
	}
	left := " " + strings.Join(parts, "  ")
	right := fleetTotal
	if p, ok := m.curProject(); ok {
		if right != "" {
			right += " · "
		}
		right += shortPath(p.Root, "")
		if p.Branch != "" {
			right += " · " + p.Branch
		}
		if live, _, done := m.missionCounts(p.Root); live+done > 0 {
			right += fmt.Sprintf(" · %d running · %d done", live, done)
		}
		right = s.dim.Render(right) + " "
	}
	// The path is a nicety: it goes before the chips get cut, and the fleet
	// total outlasts it.
	if ansi.StringWidth(left)+ansi.StringWidth(right)+2 > w {
		right = ""
		if fleetTotal != "" {
			right = s.dim.Render(fleetTotal) + " "
		}
		if ansi.StringWidth(left)+ansi.StringWidth(right)+2 > w {
			right = ""
		}
	}
	return ansi.Truncate(justify(left, right, w), w, "…")
}

// stripRows is the project strip with a blank line above and below, so it
// sits apart from the tabs and the panes.
const stripRows = 3

func (m Model) stripBlock(w int) string {
	return "\n" + m.projectStrip(w) + "\n"
}

// overviewFull reports whether the missions pane takes the whole width: an
// open mission, its transcript or a proposal. Esc brings the agents back.
func (m Model) overviewFull() bool { return m.ov.open != "" || m.ov.proposal != "" }

// agentsWidth is the agents pane's width beside the missions: a 30-column
// floor under 100 columns, so the list keeps room for its names.
func agentsWidth(w int) int {
	if w < 100 {
		return 30
	}
	return clamp(w*42/100, 36, 64)
}

// overviewPanes lays the tab out: the strip, the project's agents on the
// left and its missions on the right; an open mission, its transcript or
// a proposal takes the whole width, under the strip.
func (m Model) overviewPanes(w, h int) string {
	strip := m.stripBlock(w)
	h -= stripRows
	if h < 4 {
		return strip
	}
	if w < 72 || m.overviewFull() {
		return strip + "\n" + m.box(m.missionsTitle(), m.missionsPane(w-2, h-2), w, h, true)
	}
	lw := agentsWidth(w)
	rw := w - lw
	title := "Agents"
	if p, ok := m.curProject(); ok {
		title = "Agents on " + p.Name
	}
	left := m.box(title, m.projectAgentsPane(lw-2, h-2), lw, h, false)
	right := m.box(m.missionsTitle(), m.missionsPane(rw-2, h-2), rw, h, true)
	return strip + "\n" + lipgloss.JoinHorizontal(lipgloss.Top, left, right)
}

// listHeight is the missions list's inner height on the Overview as laid
// out now, for keeping the selection in view.
func (m Model) listHeight() int {
	_, ph := m.paneArea()
	return ph - stripRows - 2
}

func (m Model) missionsTitle() string {
	if m.ov.listing {
		return "Templates"
	}
	if ms := m.missions.Get(m.ov.open); ms != nil {
		if m.ov.transcript != "" {
			if a, ok := m.agentFor(m.ov.transcript); ok {
				return "Mission · " + ms.Name + " · " + a.Name
			}
		}
		return "Mission · " + ms.Name
	}
	if ms := m.missions.Get(m.ov.proposal); ms != nil && ms.Name != "" {
		return "Mission proposal · " + ms.Name
	}
	if m.ov.proposal != "" {
		return "Mission proposal"
	}
	if p, ok := m.curProject(); ok {
		return "Missions · " + p.Name
	}
	return "Missions"
}

// projectAgentsPane is the agents pane narrowed to the project, without a
// cursor: the Fleet view selects agents, this one shows who is here. A
// narrow pane drops the role tags first (the Fleet view has them).
func (m Model) projectAgentsPane(w, h int) string {
	agents := m.projectAgents()
	if len(agents) == 0 {
		return "\n  " + m.st.dim.Render("Nobody is working here right now.")
	}
	mo := m
	mo.agents = agents
	mo.cursor = -1
	mo.tree.mapView = false
	mo.noRoles = w < 34
	out := mo.agentsPane(w, h)
	lines := strings.Split(out, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	return strings.Join(lines, "\n")
}

// missionsPane shows the proposal while one is pending, the open mission
// or its owner's transcript, or the project's missions as rows.
func (m Model) missionsPane(w, h int) string {
	lines, _ := m.missionsPaneOwned(w, h)
	return strings.Join(lines, "\n")
}

// missionsPaneOwned is missionsPane's lines, each with the chat entry it
// draws (-1 for none: the mission views draw no chat).
func (m Model) missionsPaneOwned(w, h int) (lines []string, owners []int) {
	switch {
	case m.ov.listing:
		lines = m.templateLines(w)
		if len(lines) > h {
			lines = lines[:h]
		}
	case m.ov.proposal != "":
		lines = m.proposalView(w)
		if len(lines) > h {
			start := clamp(m.ov.scroll, 0, len(lines)-h)
			lines = lines[start : start+h]
		}
	case m.ov.open != "" && m.ov.transcript != "":
		lines = m.transcriptView(w, h)
	case m.ov.open != "":
		var sel int
		lines, sel = m.missionView(w)
		if len(lines) > h {
			// The header (a blank line, the name and state, the meta line)
			// stays put; the rest scrolls only as far as it must to keep
			// the selected WHO DOES WHAT row in view.
			const head = 3
			rest, room := lines[head:], max(1, h-head)
			start := 0
			if s := sel - head; s > room-2 {
				start = s - (room - 2)
			}
			start = clamp(start, 0, max(0, len(rest)-room))
			lines = append(append([]string(nil), lines[:head]...), rest[start:min(len(rest), start+room)]...)
		}
	default:
		lines = m.missionRows(w, h)
	}
	return lines, noOwners(len(lines))
}

// noOwners is n lines that draw no chat entry.
func noOwners(n int) []int {
	o := make([]int, n)
	for i := range o {
		o[i] = -1
	}
	return o
}

// toggleThreadActions expands every collapsed action in the open mission's
// thread, or collapses them all when every one is open. It reports whether
// the thread had any.
func (m *Model) toggleThreadActions() bool {
	id := firstNonEmptyS(m.ov.proposal, m.ov.open)
	if id == "" {
		return false
	}
	var in []int
	open := true
	for i, e := range m.orch.chat {
		if e.mission == id && e.collapsible() {
			in = append(in, i)
			open = open && e.expanded
		}
	}
	for _, i := range in {
		m.orch.chat[i].expanded = !open
	}
	return len(in) > 0
}

// ovList is the picked project's missions in list order.
func (m Model) ovList() []*mission.Mission { return m.missions.ForProject(m.proj.cur) }

// ovIndex is the selected mission's row in list: the one with ov.sel, or
// its neighbour by ov.idx when it went.
func (m Model) ovIndex(list []*mission.Mission) int {
	for i, ms := range list {
		if ms.ID == m.ov.sel {
			return i
		}
	}
	return clamp(m.ov.idx, 0, max(0, len(list)-1))
}

// missionsPerScreen is how many three-line rows fit in a list of height h,
// leaving room for the "more" lines.
func missionsPerScreen(h int) int { return max(1, (h-3)/4) }

// listWindow is the first and last+1 mission rows in view: the selected
// row is always whole, and the window moves only when it must.
func listWindow(top, sel, n, h int) (int, int) {
	k := missionsPerScreen(h)
	if n <= k {
		return 0, n
	}
	start := clamp(top, sel-k+1, sel)
	start = clamp(start, 0, n-k)
	return start, start + k
}

// moveMission moves the selection by delta rows and keeps it in view.
func (m *Model) moveMission(delta int) {
	list := m.ovList()
	if len(list) == 0 {
		return
	}
	i := clamp(m.ovIndex(list)+delta, 0, len(list)-1)
	m.ov.sel, m.ov.idx = list[i].ID, i
	m.ov.top, _ = listWindow(m.ov.top, i, len(list), m.listHeight())
}

// missionRows lists the project's missions, three lines each, the selected
// one in a band with the bar, scrolled to keep it in view.
func (m Model) missionRows(w, h int) []string {
	s := m.st
	list := m.ovList()
	if len(list) == 0 {
		out := []string{""}
		for _, l := range strings.Split(ansi.Wrap("No missions here yet. Tell the orchestrator the goal on the command line (or /mission <goal>): it proposes the done criteria and who does what. Nothing starts until you press ^Y.", ovBody(w), ""), "\n") {
			out = append(out, m.ovLine(s.dim.Render(l), w))
		}
		return out
	}
	sel := m.ovIndex(list)
	start, end := listWindow(m.ov.top, sel, len(list), h)
	body := ovBody(w)
	out := []string{""}
	if start > 0 {
		out[0] = m.ovLine(s.dim.Render(fmt.Sprintf("↑ %d more", start)), w)
	}
	for i := start; i < end; i++ {
		ms := list[i]
		selected := i == sel
		fg, dim, muted, bright, ok, attn, errS, marker := m.rowStyles(selected)
		var st string
		switch m.missionState(ms) {
		case mission.Running:
			st = ok.Render("● running · " + humanAge(time.Since(ms.Updated)))
		case mission.Waiting:
			st = attn.Render("◐ needs you · " + humanAge(time.Since(ms.Updated)))
		case mission.Paused:
			st = dim.Render("‖ paused")
		case mission.Done:
			st = ok.Render("✓ done · " + humanAge(time.Since(ms.Updated)))
		case mission.Failed:
			st = errS.Render("✕ failed")
		case mission.Proposed:
			st = attn.Render("proposal")
		}
		name := fg.Render(ms.Name)
		if selected {
			name = bright.Bold(true).Render(ms.Name)
		}
		// The state is never cut: the name gives way.
		head := fg.Render(ms.Shape.Glyph()+" ") + ansi.Truncate(name, max(4, body-ansi.StringWidth(st)-4), "…")
		l1 := justifyIn(head, st, body, fg)
		l2 := fg.Render("  ") + dim.Render(ansi.Truncate("goal: "+ms.Mission, body-2, "…"))
		l3 := fg.Render("  ") + muted.Render(ansi.Truncate(m.missionLine(ms), body-2, "…"))
		for _, l := range []string{l1, l2, l3} {
			if selected {
				out = append(out, m.ovSelLine(l, w, fg, marker))
			} else {
				out = append(out, m.ovLine(l, w))
			}
		}
		out = append(out, "")
	}
	if end < len(list) {
		out = append(out, m.ovLine(s.dim.Render(fmt.Sprintf("↓ %d more · PgDn", len(list)-end)), w))
	}
	if len(out) > h {
		out = out[:h]
	}
	return out
}

// missionLine is a row's third line: the criteria met, who owns the
// sub-goals, and which sub-goal needs the human; a mission from before
// criteria says what its run is doing.
func (m Model) missionLine(ms *mission.Mission) string {
	if len(ms.Criteria) == 0 && len(ms.Goals) == 0 {
		return m.missionNow(ms)
	}
	var parts []string
	if met, _, total := ms.CriteriaCounts(); total > 0 {
		parts = append(parts, fmt.Sprintf("done %d/%d", met, total))
	}
	var owners []string
	for _, g := range ms.Goals {
		o := firstNonEmptyS(g.Name, g.Role)
		if o != "" && !containsS(owners, o) {
			owners = append(owners, o)
		}
	}
	if len(owners) > 2 {
		owners = append(owners[:2], fmt.Sprintf("+%d", len(owners)-2))
	}
	if len(owners) > 0 {
		parts = append(parts, strings.Join(owners, ", "))
	}
	for _, g := range ms.Goals {
		if m.goalNeedsYou(g) {
			parts = append(parts, g.ID+" needs you")
		}
	}
	if ms.Shape == mission.Schedule && !ms.NextRun.IsZero() && ms.State == mission.Running {
		parts = append(parts, "next "+untilShort(ms.NextRun))
	}
	return strings.Join(parts, " · ")
}

// goalNeedsYou is a sub-goal's derived state: its owner, on this machine,
// is waiting on the human. Never stored.
func (m Model) goalNeedsYou(g mission.SubGoal) bool {
	if g.Owner == "" || g.Host != "" {
		return false
	}
	a, ok := m.agentFor(g.Owner)
	return ok && a.Status == fleet.NeedsYou
}

// missionBrief is a short line of shape, agents and done-when.
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

// missionNow is what is happening in the current run, the last run's
// summary, or the next scheduled time.
func (m Model) missionNow(ms *mission.Mission) string {
	if r := ms.CurrentRun(); r != nil {
		return fmt.Sprintf("run %d · %s", r.N, m.runProgress(ms, r))
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

// runProgress says how a run's agents are doing.
func (m Model) runProgress(ms *mission.Mission, r *mission.Run) string {
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
		return fmt.Sprintf("%s needs you · %d of %d done", strings.Join(needs, ", "), len(done), len(cards))
	case len(cards) == 0:
		return "dispatching"
	case len(working) == 0:
		return fmt.Sprintf("%d of %d done", len(done), len(cards))
	default:
		return fmt.Sprintf("%d of %d done · %s working", len(done), len(cards), strings.Join(working, ", "))
	}
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
