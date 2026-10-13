package ui

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/sanitize"
)

// The orchestrator's actions in the chat are drawn as one line each: who
// it wrote to and the gist, a mission proposal as its name, shape, seats
// and goal. Enter on a focused one (or a click on it) shows the full text
// in place, through the markdown renderer, and hides it again. What is
// logged and what the cards hold is unchanged.

// summaryMax caps a summary before it is fitted to the line.
const summaryMax = 200

// actionSummary is the one line a tool call is shown as: the call's own
// summary, else the first line of its text. It is sanitised for a single
// line and capped.
func actionSummary(summary, text string) string {
	s := sanitize.Line(summary)
	if strings.TrimSpace(s) == "" {
		s = sanitize.Line(firstLine(text))
	}
	s = strings.Join(strings.Fields(s), " ")
	return ansi.Truncate(s, summaryMax, "…")
}

// hasSummary says whether a call carried a summary of its own.
func hasSummary(summary string) bool {
	return strings.TrimSpace(sanitize.Line(summary)) != ""
}

// firstLine is the first non-blank line of s.
func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			return t
		}
	}
	return ""
}

// messageCall is a tool input that sends someone a message.
type messageCall struct {
	To      string `json:"to"`
	Message string `json:"message"`
	Summary string `json:"summary"`
}

// parseMessageCall reads a call to a tool other than Den's own that has a
// recipient and a message.
func parseMessageCall(tool string, raw json.RawMessage) (messageCall, bool) {
	if strings.HasPrefix(tool, "mcp__den__") {
		return messageCall{}, false
	}
	var in messageCall
	if json.Unmarshal(raw, &in) != nil || strings.TrimSpace(in.To) == "" || strings.TrimSpace(in.Message) == "" {
		return messageCall{}, false
	}
	return in, true
}

// collapsible says whether the entry has more to show than its line.
func (e chatEntry) collapsible() bool {
	if e.card != nil {
		return false
	}
	if e.full != "" {
		return true
	}
	return e.role == "orchestrator" && missionBlock.MatchString(e.text)
}

// noteLines is a tool note: wrapped as before, or cut to one line when it
// is the summary of something longer.
func (m Model) noteLines(e chatEntry, w int) []string {
	if e.full == "" {
		return m.plainLines(e.text, w, m.st.muted)
	}
	return []string{m.st.muted.Render(ansi.Truncate(sanitize.Line(e.text), w, "…"))}
}

// expandedLines is the full text under an expanded line, indented a step.
func (m Model) expandedLines(full string, w int) []string {
	var out []string
	for _, l := range m.markdownLines(full, max(10, w-2)) {
		out = append(out, m.st.muted.Render("│ ")+l)
	}
	return out
}

// missionBlock finds a fenced JSON block about a mission, closed or (while
// the reply streams) not yet.
var missionBlock = regexp.MustCompile("(?s)```(?:json)?[ \\t]*\\n?\\s*(\\{\\s*\"mission(?:_update)?\".*?)(?:```|$)")

// proposalView is what the summary shows of a proposal or an update. The
// fields are read loosely: a team member may carry a name.
type proposalView struct {
	Mission *struct {
		Name     string   `json:"name"`
		Mission  string   `json:"mission"`
		DoneWhen string   `json:"done_when"`
		Shape    string   `json:"shape"`
		Cadence  string   `json:"cadence"`
		Agents   []string `json:"agents"`
		Team     []struct {
			Name string `json:"name"`
			Role string `json:"role"`
		} `json:"team"`
	} `json:"mission"`
	Update *struct {
		ID      string `json:"id"`
		State   string `json:"state"`
		Summary string `json:"summary"`
	} `json:"mission_update"`
}

// mdMemo is one chat entry's rendered text and what it was rendered from.
type mdMemo struct {
	text  string
	width int
	look  string // palette, code colour, plain mode and expanded state
	lines []string
}

// memoTextLines is orchTextLines, kept on the entry until its text, the
// width or the look changes. Entries made without a memo use the shared
// cache alone.
func (m Model) memoTextLines(e chatEntry, w int) []string {
	if e.memo == nil {
		return m.orchTextLines(e, w)
	}
	look := fmt.Sprintf("%v|%v|%v|%v", m.st.pal, m.st.code.GetForeground(), m.plainCode, e.expanded)
	if mm := e.memo; mm.lines != nil && mm.width == w && mm.look == look && mm.text == e.text {
		return append([]string(nil), mm.lines...)
	}
	if m.hl == nil {
		m.hl = newHLBudget()
	}
	short, left := m.hl.short, m.hl.left
	lines := m.orchTextLines(e, w)
	// As for the shared cache: a text that found the frame's budget
	// spent is drawn again next frame, when it can be coloured.
	if m.hl.short == short || m.hl.left < left {
		*e.memo = mdMemo{text: e.text, width: w, look: look, lines: append([]string(nil), lines...)}
	}
	return lines
}

// orchTextLines is an orchestrator reply: its words as markdown, and each
// mission block as a short summary, or in full when the entry is expanded.
func (m Model) orchTextLines(e chatEntry, w int) []string {
	if e.expanded {
		return m.markdownLines(e.text, w)
	}
	var out []string
	rest := e.text
	for {
		loc := missionBlock.FindStringSubmatchIndex(rest)
		if loc == nil {
			break
		}
		if before := strings.TrimSpace(rest[:loc[0]]); before != "" {
			out = append(out, m.markdownLines(before, w)...)
			out = append(out, "")
		}
		whole := strings.HasSuffix(rest[:loc[1]], "```")
		out = append(out, m.missionSummary(rest[loc[2]:loc[3]], whole, w)...)
		rest = rest[loc[1]:]
	}
	if after := strings.TrimSpace(rest); after != "" {
		if len(out) > 0 {
			out = append(out, "")
		}
		out = append(out, m.markdownLines(after, w)...)
	}
	return out
}

// missionSummary draws a mission block in a few lines. A block that is not
// whole yet (whole false) says it is being drafted.
func (m Model) missionSummary(raw string, whole bool, w int) []string {
	s := m.st
	line := func(label, value string) string {
		value = sanitize.Line(strings.Join(strings.Fields(value), " "))
		return ansi.Truncate(s.dim.Render(label)+s.fg.Render(value), w, "…")
	}
	var v proposalView
	if json.Unmarshal([]byte(strings.TrimSpace(raw)), &v) != nil || v.Mission == nil && v.Update == nil {
		msg := "◇ drafting a mission proposal…"
		if whole {
			msg = "◇ a mission block Den could not read · Enter shows it"
		}
		return []string{s.muted.Render(ansi.Truncate(msg, w, "…"))}
	}
	if u := v.Update; u != nil {
		head := "◇ mission update"
		if st := sanitize.Line(u.State); st != "" {
			head += " · " + st
		}
		out := []string{s.bright.Render(ansi.Truncate(head, w, "…"))}
		if u.Summary != "" {
			out = append(out, line("  ", u.Summary))
		}
		return out
	}
	p := v.Mission
	name := firstNonEmptyS(p.Name, p.Mission)
	head := "◇ mission proposal: " + strings.Join(strings.Fields(sanitize.Line(name)), " ")
	if p.Shape != "" {
		head += " · " + sanitize.Line(p.Shape)
	}
	if p.Cadence != "" {
		head += " · " + sanitize.Line(p.Cadence)
	}
	out := []string{s.bright.Bold(true).Render(ansi.Truncate(head, w, "…"))}
	var seats []string
	for _, t := range p.Team {
		seat := strings.TrimSpace(t.Role)
		if n := strings.TrimSpace(t.Name); n != "" {
			seat += " (" + n + ")"
		}
		if seat != "" {
			seats = append(seats, seat)
		}
	}
	if len(seats) == 0 {
		seats = p.Agents
	}
	if len(seats) > 0 {
		out = append(out, line("  seats: ", strings.Join(seats, ", ")))
	}
	if p.DoneWhen != "" {
		out = append(out, line("  done when: ", p.DoneWhen))
	}
	return out
}

// toggleAction expands or collapses the focused action, if one is.
func (m *Model) toggleAction() bool {
	i := m.orch.cardCur
	if i < 0 || i >= len(m.orch.chat) || !m.orch.chat[i].collapsible() {
		return false
	}
	m.orch.chat[i].expanded = !m.orch.chat[i].expanded
	return true
}

// clickAction toggles the action drawn on screen row y of the
// Orchestrator pane, and focuses it. It reports whether there was one.
func (m *Model) clickAction(y int) bool {
	pane, ok := m.copyPane()
	if !ok || m.tab != tabOrch || len(m.orch.chat) == 0 {
		return false
	}
	_, paneH := m.paneArea()
	strip, _, owners := m.orchWindow(pane.x1-pane.x0+1, paneH-2)
	r := y - pane.y0 - len(strip)
	if r < 0 || r >= len(owners) || owners[r] < 0 || !m.orch.chat[owners[r]].collapsible() {
		return false
	}
	m.orch.cardCur = owners[r]
	return m.toggleAction()
}
