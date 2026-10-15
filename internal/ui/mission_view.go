package ui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/mission"
	"github.com/GlacierHubAB/den/internal/sanitize"
	"github.com/GlacierHubAB/den/internal/transcript"
)

// The open mission: a header and three sections, GOAL, DONE WHEN and WHO
// DOES WHAT. No conversation with the orchestrator shows here and nothing
// typed goes to the mission: the human tells the orchestrator in the chat.
// Enter on an owner (or on one of a lead's people) shows that agent's
// transcript in the mission's place, read-only and fenced: the agent's
// own words as data, never sent anywhere.

// whoRow is one row of WHO DOES WHAT: a sub-goal, or one of the people its
// owner leads (sid set, goal empty).
type whoRow struct {
	goal *mission.SubGoal
	sid  string // the agent whose transcript Enter shows, "" for none
	lead string // for a lead's person: the lead's session
}

// whoRows lists WHO DOES WHAT: each sub-goal, then the people its owner
// started, read-only, as Den knows the spawn tree.
func (m Model) whoRows(ms *mission.Mission) []whoRow {
	var out []whoRow
	for i := range ms.Goals {
		g := &ms.Goals[i]
		sid := ""
		if g.Owner != "" && g.Host == "" {
			sid = g.Owner
		}
		out = append(out, whoRow{goal: g, sid: sid})
		if sid == "" {
			continue
		}
		for _, c := range m.childrenOf(sid) {
			if c.Host == "" {
				out = append(out, whoRow{sid: c.Session.SessionID, lead: sid})
			}
		}
	}
	return out
}

// section is a section label in the mission views.
func (m Model) sectionLabel(s string) string { return m.st.section.Render(s) }

// goalState draws a sub-goal's state: "needs you" is derived from its
// owner and shows over doing; ☐ is kept for criteria.
func (m Model) goalState(g mission.SubGoal, w int) string {
	s := m.st
	if m.goalNeedsYou(g) {
		return s.attn.Render("◐ needs you")
	}
	switch g.State {
	case mission.GoalDoing:
		return s.fg.Render("● doing")
	case mission.GoalDone:
		return s.ok.Render("✓ done")
	case mission.GoalBlocked:
		line := "■ blocked"
		if g.Note != "" {
			line += " · " + g.Note
		}
		return s.attn.Render(ansi.Truncate(line, max(10, w), "…"))
	}
	return s.dim.Render("○ todo")
}

// ownerLabel is who owns a sub-goal, in the owner's colour, or the role to
// start, or "unassigned" in attention colour.
func (m Model) ownerLabel(g mission.SubGoal) string {
	s := m.st
	switch {
	case g.Owner != "":
		name := g.Name
		if a, ok := m.agentFor(g.Owner); ok && g.Host == "" {
			name = firstNonEmptyS(a.Name, name)
			return lipgloss.NewStyle().Foreground(lipgloss.Color(m.agentColor(a))).Render(name)
		}
		return s.fg.Render(name)
	case g.Role != "":
		return s.muted.Render(g.Role + " (to start)")
	}
	return s.attn.Render("unassigned")
}

// criterionLines draws DONE WHEN: ☐ open or claimed (with the claim's
// evidence, dim), ☑ met by the human's word.
func (m Model) criterionLines(ms *mission.Mission, w int) []string {
	s := m.st
	body := ovBody(w)
	var out []string
	if len(ms.Criteria) == 0 {
		return append(out, m.ovLine(s.attn.Render("No done criteria yet: ask the orchestrator to set them."), w))
	}
	for _, c := range ms.Criteria {
		box := "☐"
		if c.State == mission.CritMet {
			box = "☑"
		}
		line := s.fg.Render(box+" "+c.ID+" ") + s.fg.Render(c.Text)
		if c.State == mission.CritClaimed {
			line += s.fg.Render(" · claimed: ") + s.dim.Render(c.Evidence)
		}
		out = append(out, m.ovLine(ansi.Truncate(line, body, "…"), w))
	}
	return out
}

// goalCols lays a WHO DOES WHAT row out: id, sub-goal, owner (ownerW
// wide), state.
func (m Model) goalCols(id, text, owner, state string, w, ownerW int) string {
	body := ovBody(w)
	textW := goalTextWidth(w)
	t := ansi.Truncate(text, textW, "…")
	o := ansi.Truncate(owner, ownerW, "…")
	line := id + " " + t + strings.Repeat(" ", max(1, textW-ansi.StringWidth(t)+2)) + o
	if state == "" {
		return line
	}
	return line + strings.Repeat(" ", max(1, ownerW-ansi.StringWidth(o)+2)) + ansi.Truncate(state, max(8, body-textW-ownerW-8), "…")
}

// goalTextWidth is the sub-goal column's width in a pane of inner width w.
func goalTextWidth(w int) int { return clamp(ovBody(w)/3, 12, 40) }

// ownerWidth sizes the owner column to its longest entry, up to the room
// left after the state column (stateRoom wide, 0 for none).
func ownerWidth(labels []string, w, stateRoom int) int {
	n := 8
	for _, l := range labels {
		n = max(n, ansi.StringWidth(l))
	}
	return min(n, max(8, ovBody(w)-goalTextWidth(w)-stateRoom-6))
}

// missionView is the open mission, and the line of its selected WHO
// DOES WHAT row (-1 for none), for scrolling it into view.
func (m Model) missionView(w int) ([]string, int) {
	lines, sel, _ := m.missionLayout(w)
	return lines, sel
}

// missionLayout is missionView with the line where each section starts:
// marks[0] ends the fixed header (the blank row under the meta line is
// part of it), and each later mark is a section's label or first line.
func (m Model) missionLayout(w int) ([]string, int, []int) {
	s := m.st
	ms := m.missions.Get(m.ov.open)
	if ms == nil {
		return nil, -1, nil
	}
	var marks []int
	selLine := -1
	body := ovBody(w)
	now := time.Now()
	var st string
	switch m.missionState(ms) {
	case mission.Running:
		st = s.ok.Render("● running · " + humanAge(now.Sub(ms.Created)))
	case mission.Waiting:
		st = s.attn.Render("◐ needs you · " + humanAge(now.Sub(ms.Updated)))
	case mission.Done:
		st = s.ok.Render("✓ done")
	case mission.Failed:
		st = s.err.Render("✕ failed")
	case mission.Paused:
		st = s.dim.Render("‖ paused")
	default:
		st = s.dim.Render(string(ms.State))
	}
	out := []string{""}
	out = append(out, m.ovLine(justify(s.bright.Bold(true).Render(ms.Name), st, body), w))
	meta := []string{firstNonEmptyS(ms.ProjectName, "no project"), ms.Shape.Glyph() + " " + ms.Shape.Label()}
	if ms.Cadence != "" {
		meta = append(meta, ms.Cadence)
	}
	if r := ms.CurrentRun(); r != nil {
		meta = append(meta, fmt.Sprintf("run %d · %s → now", r.N, r.StartedAt.Format("15:04")))
	} else if n := len(ms.Runs); n > 0 {
		meta = append(meta, fmt.Sprintf("%d runs", ms.Runs[n-1].N))
	}
	out = append(out, m.ovLine(s.dim.Render(strings.Join(meta, " · ")), w))
	for _, l := range m.costLines(ms, w) {
		out = append(out, m.ovLine(strings.TrimLeft(l, " "), w))
	}
	out = append(out, "")
	marks = append(marks, len(out))
	out = append(out, m.ovLine(m.sectionLabel("GOAL"), w))
	for _, l := range strings.Split(ansi.Wrap(ms.Mission, body, ""), "\n") {
		out = append(out, m.ovLine(s.fg.Render(l), w))
	}
	met, claimed, total := ms.CriteriaCounts()
	count := fmt.Sprintf("%d of %d met", met, total)
	if claimed > 0 {
		count += fmt.Sprintf(" · %d claimed", claimed)
	}
	out = append(out, "")
	marks = append(marks, len(out))
	out = append(out, m.ovLine(justify(m.sectionLabel("DONE WHEN"), s.dim.Render(count), body), w))
	out = append(out, m.criterionLines(ms, w)...)
	rows := m.whoRows(ms)
	out = append(out, "")
	marks = append(marks, len(out))
	out = append(out, m.ovLine(justify(m.sectionLabel("WHO DOES WHAT"), s.dim.Render(fmt.Sprintf("%d sub-goals", len(ms.Goals))), body), w))
	if len(rows) == 0 {
		out = append(out, m.ovLine(s.attn.Render("No sub-goals yet: ask the orchestrator who does what."), w))
	}
	owners := make([]string, len(rows))
	for i, r := range rows {
		if r.goal != nil {
			owners[i] = m.ownerLabel(*r.goal)
		} else {
			a, _ := m.agentFor(r.sid)
			owners[i] = lipgloss.NewStyle().Foreground(lipgloss.Color(m.agentColor(a))).Render(a.Name)
		}
	}
	ownerW := ownerWidth(owners, w, 16)
	for i, r := range rows {
		var line string
		if r.goal != nil {
			line = m.goalCols(r.goal.ID, r.goal.Text, owners[i], m.goalState(*r.goal, body), w, ownerW)
		} else {
			a, _ := m.agentFor(r.sid)
			line = m.goalCols("  ", s.dim.Render("└ under the owner"), owners[i], s.dim.Render(strings.ToLower(a.Status.String())), w, ownerW)
		}
		if i == clamp(m.ov.who, 0, len(rows)-1) {
			selLine = len(out)
			fg, _, _, _, _, _, _, marker := m.rowStyles(true)
			out = append(out, m.ovSelLine(line, w, fg, marker))
		} else {
			out = append(out, m.ovLine(line, w))
		}
	}
	// The last runs and what happened to the mission, short.
	var past []string
	for i := len(ms.Runs) - 1; i >= 0 && i >= len(ms.Runs)-3; i-- {
		r := ms.Runs[i]
		when := r.StartedAt.Format("15:04")
		if !r.EndedAt.IsZero() {
			when += " → " + r.EndedAt.Format("15:04")
		}
		trail := r.Summary
		if r.EndedAt.IsZero() {
			trail = m.runProgress(ms, &r)
		}
		past = append(past, fmt.Sprintf("run %d · %s · %s", r.N, when, trail))
	}
	for i := len(ms.History) - 1; i >= 0 && i >= len(ms.History)-3; i-- {
		h := ms.History[i]
		past = append(past, h.At.Format("Jan 2 15:04")+" · "+h.What)
	}
	if len(past) > 0 {
		out = append(out, "")
		marks = append(marks, len(out))
		for _, p := range past {
			out = append(out, m.ovLine(s.dim.Render(p), w))
		}
	}
	out = append(out, "", m.ovLine(justify(s.key.Render("↑↓")+s.keyLabel.Render(" owner  ")+s.key.Render("Enter")+s.keyLabel.Render(" transcript  ")+s.dim.Render("/mission met c1 · /pause /resume /stop"), s.key.Render("Esc")+s.keyLabel.Render(" back"), body), w))
	out = append(out, m.ovLine(s.dim.Render("type to tell the orchestrator: it tracks every mission"), w))
	return out, selLine, marks
}

// proposalView is a proposal waiting for ^Y, in the open mission's shape.
func (m Model) proposalView(w int) []string {
	s := m.st
	ms := m.missions.Get(m.ov.proposal)
	if ms == nil {
		return nil
	}
	body := ovBody(w)
	out := []string{""}
	if ms.Mission == "" {
		out = append(out, m.ovLine(s.dim.Render("The orchestrator has not proposed yet."), w))
		out = append(out, "", m.ovLine(s.key.Render("^N")+s.keyLabel.Render(" drop")+"   "+s.key.Render("Esc")+s.keyLabel.Render(" keep it for later"), w))
		return out
	}
	out = append(out, m.ovLine(s.bright.Bold(true).Render(ms.Name)+"  "+s.chip(s.pal.Bright).Render("PROPOSAL"), w))
	meta := []string{firstNonEmptyS(ms.ProjectName, "no project"), ms.Shape.Glyph() + " " + ms.Shape.Label()}
	if ms.Cadence != "" {
		meta = append(meta, ms.Cadence)
	}
	if ms.Template != "" {
		meta = append(meta, "template "+ms.Template)
	}
	if ms.Reason != "" {
		meta = append(meta, ms.Reason)
	}
	out = append(out, m.ovLine(s.dim.Render(strings.Join(meta, " · ")), w))
	out = append(out, "", m.ovLine(m.sectionLabel("GOAL"), w))
	for _, l := range strings.Split(ansi.Wrap(ms.Mission, body, ""), "\n") {
		out = append(out, m.ovLine(s.fg.Render(l), w))
	}
	out = append(out, "", m.ovLine(m.sectionLabel("DONE WHEN"), w))
	out = append(out, m.criterionLines(ms, w)...)
	out = append(out, "", m.ovLine(m.sectionLabel("WHO DOES WHAT"), w))
	if len(ms.Goals) == 0 {
		out = append(out, m.ovLine(s.attn.Render("No owners yet: ask the orchestrator who does what."), w))
	}
	owners := m.proposalOwners(ms)
	ownerW := ownerWidth(owners, w, 0)
	for i, g := range ms.Goals {
		out = append(out, m.ovLine(m.goalCols(g.ID, g.Text, owners[i], "", w, ownerW), w))
	}
	if len(ms.Constraints) > 0 {
		var tags []string
		for _, c := range ms.Constraints {
			tags = append(tags, s.keycap.Render(c))
		}
		out = append(out, "", m.ovLine(m.sectionLabel("CONSTRAINTS"), w), m.ovLine(strings.Join(tags, " "), w))
	}
	if len(ms.Team) > 0 {
		out = append(out, "", m.ovLine(m.sectionLabel("TEAM"), w))
		for _, l := range m.teamLines(ms.Team) {
			out = append(out, m.ovLine(l, w))
		}
	}
	out = append(out, "")
	if len(ms.Criteria) == 0 || len(ms.Goals) == 0 {
		out = append(out, m.ovLine(s.key.Render("^N")+s.keyLabel.Render(" drop")+"   "+s.dim.Render("^Y starts it once the orchestrator sets the done criteria and the owners"), w))
	} else {
		out = append(out, m.ovLine(s.key.Render("^Y")+s.keyLabel.Render(" start")+"   "+s.key.Render("^N")+s.keyLabel.Render(" drop")+"   "+s.dim.Render("or tell the orchestrator what to change"), w))
	}
	return out
}

// stripProposals are the proposal rows the chat's missions strip shows,
// top to bottom: the ones the arrows can select.
func (m Model) stripProposals() []*mission.Mission {
	if m.missions == nil {
		return nil
	}
	var out []*mission.Mission
	for _, ms := range m.missions.All() {
		if ms.State == mission.Proposed && ms.Mission != "" && len(out) < missionStripRows {
			out = append(out, ms)
		}
	}
	return out
}

// focusedProposalIndex is the selected proposal row among props, -1 for
// none (or one no longer waiting).
func (m Model) focusedProposalIndex(props []*mission.Mission) int {
	for i, ms := range props {
		if ms.ID == m.orch.propFocus {
			return i
		}
	}
	return -1
}

// focusProposal selects a proposal row ("" for none) and notes when, so a
// change after it is caught.
func (m *Model) focusProposal(id string) {
	m.orch.propFocus, m.orch.propAt = id, time.Now()
	if id != "" {
		m.orch.cardCur = -1
	}
}

// selectedProposal is the proposal the human selected in the chat, by its
// id, while it still waits; nil otherwise, and ^Y and ^N then answer a
// request as ever.
func (m Model) selectedProposal() *mission.Mission {
	if m.orch.propFocus == "" || m.missions == nil {
		return nil
	}
	if ms := m.missions.Get(m.orch.propFocus); ms != nil && ms.State == mission.Proposed && ms.Mission != "" {
		return ms
	}
	return nil
}

// answerProposal is ^Y or ^N on the selected proposal row in the chat. It
// takes only the row the human selected, and refuses when the proposal
// changed or a card arrived after they selected it (the key may have been
// meant for the card), or within proposalSettle of a change.
func (m Model) answerProposal(start bool) (tea.Model, tea.Cmd) {
	ms := m.selectedProposal()
	if ms == nil {
		return m, nil
	}
	if ms.Updated.After(m.orch.propAt) {
		m.focusProposal(ms.ID) // the human has seen it change: the next ^Y counts after the settle
		return m.say("The proposal changed after you selected it: read it again, then ^Y.", true)
	}
	// Only a proposal the human can read whole, expanded in the strip: not
	// in a folded strip, nor in one taller than the pane.
	if w, h := m.orchPaneSize(); h < 16 || len(m.missionStrip(w, h)) > h {
		return m.say("Read the whole proposal first: /missions", true)
	}
	for _, e := range m.orch.chat {
		if e.card != nil && e.at.After(m.orch.propAt) {
			m.focusProposal(ms.ID)
			return m.say("A card came into the chat after you selected the proposal: nothing was started. Look, then ^Y again.", true)
		}
	}
	m.ov.proposal = ms.ID
	if !start {
		m.dropMission()
		m.focusProposal("")
		return m.say("Dropped "+ms.Name+".", false)
	}
	cmd := m.startMission()
	if ms.State == mission.Running {
		m.focusProposal("")
		return m, cmd
	}
	m.ov.proposal = ""
	return m, cmd
}

// expandedProposal is a selected proposal row in the chat's strip, open
// into the proposal card's body: GOAL, the criteria, the sub-goals with
// their owners, and the keys on the last line, all in the band.
func (m Model) expandedProposal(ms *mission.Mission, head string, w int) []string {
	s := m.st
	fg, dim, _, _, _, _, _, marker := m.rowStyles(true)
	body := ovBody(w)
	// The body starts under the proposal's name, after its shape glyph, so
	// it reads as that row's own and not as more rows.
	in := fg.Render("  ")
	var lines []string
	lines = append(lines, head)
	for i, l := range strings.Split(ansi.Wrap(ms.Mission, max(10, body-8), ""), "\n") {
		lead := "      "
		if i == 0 {
			lead = "GOAL  "
		}
		lines = append(lines, in+fg.Render(lead+l))
	}
	for _, c := range ms.Criteria {
		lines = append(lines, in+fg.Render("☐ "+c.ID+" "+c.Text))
	}
	owners := m.proposalOwners(ms)
	ownerW := ownerWidth(owners, w-2, 0)
	for i, g := range ms.Goals {
		lines = append(lines, in+fg.Render(m.goalCols(g.ID, g.Text, ansi.Strip(owners[i]), "", w-2, ownerW)))
	}
	lines = append(lines, in+justifyIn(dim.Render("or tell the orchestrator what to change"), s.key.Background(lipgloss.Color(s.pal.Sel)).Render("^Y")+fg.Render(" start · ")+s.key.Background(lipgloss.Color(s.pal.Sel)).Render("^N")+fg.Render(" drop"), body-2, fg))
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = m.ovSelLine(l, w, fg, marker)
	}
	return out
}

// orchPaneSize is the Orchestrator pane's inner size as laid out now.
func (m Model) orchPaneSize() (int, int) {
	_, ph := m.paneArea()
	return m.width - 2, ph - 2
}

// Rows of the chat's missions strip before it says "+N more".
const missionStripRows = 3

// missionStrip is the top of the chat: the proposals waiting for ^Y, then
// the live missions on every project, at most missionStripRows, folded to
// one line in a short pane, and nothing with none.
func (m Model) missionStrip(w, h int) []string {
	if m.missions == nil {
		return nil
	}
	s := m.st
	var proposals, live []*mission.Mission
	for _, ms := range m.missions.All() {
		switch {
		case ms.State == mission.Proposed && ms.Mission != "":
			proposals = append(proposals, ms)
		case ms.Live():
			live = append(live, ms)
		}
	}
	all := append(proposals, live...)
	if len(all) == 0 {
		return nil
	}
	body := ovBody(w)
	rule := m.ovLine(s.border.Render(strings.Repeat("─", body)), w)
	if h < 16 {
		needs := 0
		for _, ms := range live {
			if m.missionState(ms) == mission.Waiting {
				needs++
			}
		}
		parts := []string{fmt.Sprintf("%d running", len(live))}
		if needs > 0 {
			parts = append(parts, s.attn.Render(fmt.Sprintf("%d needs you", needs)))
		}
		if len(proposals) > 0 {
			parts = append(parts, s.attn.Render(fmt.Sprintf("%d proposal", len(proposals))))
		}
		return []string{m.ovLine(s.section.Render("MISSIONS")+"  "+strings.Join(parts, s.dim.Render(" · ")), w), rule, ""}
	}
	out := []string{m.ovLine(justify(s.section.Render("MISSIONS"), s.dim.Render("/missions"), body), w)}
	for i, ms := range all {
		if i == missionStripRows {
			out = append(out, m.ovLine(s.dim.Render(fmt.Sprintf("+%d more · /missions", len(all)-i)), w))
			break
		}
		left := ms.Shape.Glyph() + " " + ms.Name + "  " + s.dim.Render(firstNonEmptyS(ms.ProjectName, "no project"))
		var right string
		if ms.State == mission.Proposed {
			// ^Y and ^N take a proposal only once the human selected its
			// row with the arrows: never by default.
			left += "  " + s.attn.Render("proposal")
			right = s.dim.Render("↑ select · /missions to read it")
			if ms.ID == m.orch.propFocus {
				right = s.key.Render("^Y") + s.keyLabel.Render(" start · ") + s.key.Render("^N") + s.keyLabel.Render(" drop")
			}
		} else {
			if met, _, total := ms.CriteriaCounts(); total > 0 {
				left += "  " + s.dim.Render(fmt.Sprintf("%d/%d", met, total))
			}
			switch m.missionState(ms) {
			case mission.Waiting:
				right = s.attn.Render("◐ needs you")
				if w >= 100 {
					for _, g := range ms.Goals {
						if m.goalNeedsYou(g) {
							right = s.attn.Render(g.ID + " · ◐ needs you")
							break
						}
					}
				}
			case mission.Paused:
				right = s.dim.Render("‖ paused")
			default:
				right = s.ok.Render("● running")
			}
			if w >= 100 {
				var owners []string
				for _, g := range ms.Goals {
					if g.Name != "" && !containsS(owners, g.Name) {
						owners = append(owners, g.Name)
					}
				}
				if len(owners) > 1 {
					left += "  " + s.dim.Render(fmt.Sprintf("%s +%d", owners[0], len(owners)-1))
				} else if len(owners) == 1 {
					left += "  " + s.dim.Render(owners[0])
				}
			}
		}
		left = ansi.Truncate(left, max(8, body-ansi.StringWidth(right)-2), "…")
		if ms.ID == m.orch.propFocus {
			// The selected proposal opens in place into its body, so ^Y is
			// never pressed on a line whose criteria the human cannot see.
			out = append(out, m.expandedProposal(ms, left, w)...)
			continue
		}
		out = append(out, m.ovLine(justify(left, right, body), w))
	}
	return append(out, rule, "")
}

// proposalOwners are a proposal's owner labels: a running agent in its
// colour, or a role to start with its model and "(new)".
func (m Model) proposalOwners(ms *mission.Mission) []string {
	s := m.st
	out := make([]string, len(ms.Goals))
	for i, g := range ms.Goals {
		out[i] = m.ownerLabel(g)
		if g.Owner == "" && g.Role != "" {
			out[i] = s.muted.Render(g.Role + " (new)")
			for _, t := range ms.Team {
				if strings.EqualFold(t.Role, g.Role) && t.Model != "" {
					out[i] = s.muted.Render(g.Role + " · " + t.Model + " (new)")
				}
			}
		}
	}
	return out
}

// transcriptState is the transcript header's right side: the sub-goal
// the agent owns (or works under) and its state with its glyph, so the
// meaning survives without colour.
func (m Model) transcriptState(a fleet.Agent, ok bool) string {
	s := m.st
	which := ""
	if ms := m.missions.Get(m.ov.open); ms != nil {
		for _, g := range ms.Goals {
			if g.Owner == m.ov.transcript {
				which = g.ID
				break
			}
		}
		if which == "" {
			if p := m.parentOf(m.ov.transcript); p != "" {
				for _, g := range ms.Goals {
					if g.Owner == p {
						which = "under " + g.ID
						break
					}
				}
			}
		}
	}
	var st string
	switch {
	case !ok || a.Status == fleet.Gone:
		st = s.dim.Render("ended")
	case a.Status == fleet.NeedsYou:
		st = s.attn.Render("◐ needs you")
	case a.Status == fleet.Working:
		st = s.fg.Render("● working")
	case a.Status == fleet.Idle:
		st = s.dim.Render("○ idle")
	default:
		st = s.dim.Render(strings.ToLower(a.Status.String()))
	}
	if which == "" {
		return st
	}
	return s.dim.Render(which+" · ") + st
}

// Caps on a transcript view.
const (
	maxTranscriptLines = 400
	maxTranscriptBytes = 256 << 10
	transcriptEntries  = 200
	transcriptReload   = 2 * time.Second
)

// openTranscript shows the transcript of the selected WHO DOES WHAT row's
// agent, or says why it cannot: only sessions Den holds on the mission
// (an owner, or one its owner started), on this machine.
func (m *Model) openTranscript() (string, bool) {
	ms := m.missions.Get(m.ov.open)
	if ms == nil {
		return "", false
	}
	rows := m.whoRows(ms)
	if len(rows) == 0 {
		return "No sub-goals yet.", true
	}
	r := rows[clamp(m.ov.who, 0, len(rows)-1)]
	if r.goal != nil && r.goal.Owner != "" && r.goal.Host != "" {
		return sanitize.Label(r.goal.Name, 80) + " is on " + sanitize.Label(r.goal.Host, 80) + ": its transcript stays there.", true
	}
	if r.sid == "" {
		return r.goal.ID + " has no owner running yet.", true
	}
	m.ov.transcript, m.ov.scroll = r.sid, 0
	m.loadTranscript()
	return "", false
}

// loadTranscript reads the open transcript's last turns. The text stays in
// the view: it reaches no prompt, log, inbox item or den-api view.
func (m *Model) loadTranscript() {
	m.ov.trEntries, m.ov.trAt = nil, time.Now()
	a, ok := m.agentFor(m.ov.transcript)
	if !ok || a.Path == "" || a.Host != "" {
		return
	}
	m.ov.trEntries = transcript.Tail(a.Path, transcriptEntries)
}

// refreshTranscript reloads an open transcript every few seconds.
func (m *Model) refreshTranscript() {
	if m.tab == tabOverview && m.ov.transcript != "" && time.Since(m.ov.trAt) >= transcriptReload {
		m.loadTranscript()
	}
}

// promptSender names who sent a prompt in a transcript, from Den's own
// records, never from the text: the orchestrator's dispatch (a card's
// brief to this session), the lead that started it, or "prompt" when Den
// cannot tell.
func (m Model) promptSender(sid, text string) string {
	t := strings.TrimSpace(text)
	for _, d := range m.cards() {
		if d.SessionID != sid {
			continue
		}
		brief := []rune(strings.TrimSpace(sanitize.Text(d.rawBrief)))
		if len(brief) > 0 && strings.HasPrefix(t, string(brief[:min(len(brief), 40)])) {
			return "orchestrator"
		}
	}
	// A lead only when Den recorded its spawn of this session with that
	// brief: any peer can message the session, so a parent alone proves
	// nothing about who sent a prompt.
	if e := m.edgeFor(sid); e.Source == "spawn" && e.Parent != "" {
		brief := []rune(strings.TrimSpace(sanitize.Text(e.Brief)))
		if a, ok := m.agentFor(e.Parent); ok && !m.isOrchestrator(a) && len(brief) > 0 && strings.HasPrefix(t, string(brief[:min(len(brief), 40)])) {
			return a.Name
		}
	}
	return "prompt"
}

// transcriptView is the open transcript: Den's header and status outside a
// fence, and inside it only the transcript's own text, plain (no markdown,
// no links, no colours from the text), every line behind a dim rule.
func (m Model) transcriptView(w, h int) []string {
	s := m.st
	body := ovBody(w)
	a, ok := m.agentFor(m.ov.transcript)
	name := "this agent"
	if ok {
		name = a.Name
	}
	head := []string{"", m.ovLine(justify(s.bright.Render(name+"'s transcript")+s.dim.Render(" · read-only"), m.transcriptState(a, ok), body), w),
		m.ovLine(s.dim.Render("the agent's own words, shown as data; never sent to the orchestrator"), w), ""}
	rule := s.dim.Render("│ ")
	textW := max(10, body-2)
	var fenced []string
	add := func(style lipgloss.Style, prefix, text string) {
		indent := strings.Repeat(" ", ansi.StringWidth(prefix))
		for i, l := range strings.Split(ansi.Wrap(sanitize.Text(text), max(8, textW-len(indent)), ""), "\n") {
			p := indent
			if i == 0 {
				p = prefix
			}
			fenced = append(fenced, m.ovLine(rule+s.muted.Render(p)+style.Render(l), w))
		}
	}
	for _, e := range m.ov.trEntries {
		switch e.Kind {
		case "you":
			add(s.fg, m.promptSender(m.ov.transcript, e.Text)+" › ", e.Text)
		case "text":
			add(s.fg, "", e.Text)
		case "tool":
			add(s.dim, "⏺ "+sanitize.Line(e.Tool)+"  ", firstLineOf(e.Text))
			for _, o := range strings.Split(strings.TrimRight(sanitize.Text(e.Output), "\n"), "\n") {
				if strings.TrimSpace(o) != "" {
					add(s.dim, "    ", o)
				}
			}
		}
		fenced = append(fenced, m.ovLine(rule, w))
	}
	if len(m.ov.trEntries) == 0 {
		fenced = append(fenced, m.ovLine(rule+s.dim.Render("(nothing to show yet)"), w))
	}
	// Caps: the last lines only.
	if n := len(fenced) - maxTranscriptLines; n > 0 {
		fenced = fenced[n:]
	}
	size := 0
	for i := len(fenced) - 1; i >= 0; i-- {
		if size += len(fenced[i]); size > maxTranscriptBytes {
			fenced = fenced[i+1:]
			break
		}
	}
	// Den's own status, outside the fence, so an agent cannot write a line
	// that passes for it.
	var tail []string
	switch {
	case ok && a.Status == fleet.NeedsYou:
		tail = append(tail, m.ovLine(s.attn.Render("◐ Den: waiting for your answer in the Inbox"), w))
	case !ok || a.Status == fleet.Gone:
		tail = append(tail, m.ovLine(s.dim.Render("Den: the session has ended"), w))
	}
	tail = append(tail, "", m.ovLine(justify(s.key.Render("↑↓")+s.keyLabel.Render(" scroll"), s.key.Render("Esc")+s.keyLabel.Render(" back to the mission"), body), w))
	room := h - len(head) - len(tail)
	if room < 1 {
		room = 1
	}
	if len(fenced) > room {
		start := clamp(len(fenced)-room-m.ov.scroll, 0, len(fenced)-room)
		fenced = fenced[start : start+room]
	}
	out := append(head, fenced...)
	return append(out, tail...)
}
