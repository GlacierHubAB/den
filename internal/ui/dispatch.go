package ui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/transcript"
)

// cardState is where a dispatch is in its life.
type cardState int

const (
	cardQueued   cardState = iota // sent, the agent has not started on it
	cardWorking                   // the agent's turn is running
	cardNeedsYou                  // a permission, plan or question is waiting
	cardDone                      // the turn finished
	cardFailed                    // delivery failed or the session went away
)

func (c cardState) String() string {
	return [...]string{"queued", "working", "needs you", "done", "failed"}[c]
}

// dispatch is one brief the orchestrator handed to an agent, shown in the
// chat as a card the agent owns. Everything in it comes from the agent's
// own hooks and transcript, never from the orchestrator's retelling.
type dispatch struct {
	ID        string // the orchestrator's tool use id
	SessionID string
	Agent     string // critter name
	Brief     string
	SentAt    time.Time
	StartedAt time.Time
	EndedAt   time.Time
	State     cardState
	Reason    string // why it failed, or the delivery note while queued
	Calls     int
	Ticker    string // the latest tool call, or what is waiting on you
	Result    string // the agent's last message, verbatim
	// Summary is the orchestrator's own gist of the brief, sanitised, ""
	// when it gave none; the card shows it in place of the brief.
	Summary   string
	Files     int
	MissionID string // the mission this dispatch is a run of, "" for a plain one
	// Thread is the conversation the card was made in: the mission the
	// human was talking about, "" for the plain chat. A dispatch about a
	// finished mission has a thread but no run.
	Thread string
	// Stopped is when the agent's turn ended while people under it were
	// still at work; the card finishes once they are done.
	Stopped  time.Time
	Expanded bool

	// rawBrief is the brief as the orchestrator sent it, for the log;
	// Brief is that text sanitised, for everything Den draws.
	rawBrief string

	turn       []transcript.Entry
	turnLoaded bool
}

func (d *dispatch) live() bool { return d.State < cardDone }

// elapsed is how long the agent has been on it, or took.
func (d *dispatch) elapsed(now time.Time) time.Duration {
	from := d.StartedAt
	if from.IsZero() {
		from = d.SentAt
	}
	to := now
	if !d.EndedAt.IsZero() {
		to = d.EndedAt
	}
	return to.Sub(from)
}

// cards lists the dispatches in chat order.
func (m Model) cards() []*dispatch {
	var out []*dispatch
	for _, e := range m.orch.chat {
		if e.card != nil {
			out = append(out, e.card)
		}
	}
	return out
}

// liveCards lists the dispatches still in flight.
func (m Model) liveCards() []*dispatch {
	var out []*dispatch
	for _, d := range m.cards() {
		if d.live() {
			out = append(out, d)
		}
	}
	return out
}

// focusedCard is the card the arrows have landed on, if any.
func (m Model) focusedCard() *dispatch {
	if m.orch.cardCur < 0 || m.orch.cardCur >= len(m.orch.chat) {
		return nil
	}
	return m.orch.chat[m.orch.cardCur].card
}

// focusIndexes are the chat positions the arrows step through: the cards
// and the actions that expand.
func (m Model) focusIndexes() []int {
	var out []int
	for i, e := range m.orch.chat {
		if e.card != nil || e.collapsible() {
			out = append(out, i)
		}
	}
	return out
}

// moveCard steps the focus through the cards; past the last one the focus
// clears and the chat follows its tail again.
func (m *Model) moveCard(delta int) {
	idx := m.focusIndexes()
	if len(idx) == 0 {
		m.orch.scroll = max(0, m.orch.scroll-delta)
		return
	}
	pos := -1
	for i, ci := range idx {
		if ci == m.orch.cardCur {
			pos = i
		}
	}
	switch {
	case pos < 0 && delta < 0:
		m.orch.cardCur = idx[len(idx)-1]
	case pos < 0:
		return
	case pos+delta < 0:
		m.orch.cardCur = idx[0]
	case pos+delta >= len(idx):
		m.orch.cardCur = -1
	default:
		m.orch.cardCur = idx[pos+delta]
	}
}

// trackHook folds an agent's hook event into the cards that wait on it. The
// oldest live card for the session takes the event, so two briefs to one
// agent settle in the order they were sent.
func (m *Model) trackHook(r *bridge.Request, it *inbox.Item) {
	var card *dispatch
	for _, d := range m.cards() {
		if d.SessionID == r.SessionID && d.live() {
			card = d
			break
		}
	}
	if card == nil {
		return
	}
	now := time.Now()
	start := func() {
		if card.State == cardQueued {
			card.State = cardWorking
			card.StartedAt = now
			card.Reason = ""
		}
	}
	switch r.Event {
	case "UserPromptSubmit":
		start()
		card.Stopped = time.Time{}
	case "PostToolUse":
		start()
		card.Stopped = time.Time{}
		if card.State == cardNeedsYou {
			card.State = cardWorking
		}
		card.Calls++
		card.Ticker = tickerLine(r.Input.ToolName, r.Input.ToolInput)
	case "PermissionRequest", "PreToolUse", "Notification":
		if it != nil && it.Kind <= inbox.Question {
			start()
			card.State = cardNeedsYou
			card.Ticker = it.Title
		}
	case "Stop":
		if card.StartedAt.IsZero() {
			return // a turn that began before the brief arrived
		}
		card.Result = strings.TrimSpace(r.Input.LastAssistant)
		if n := m.subtreeBusy(card.SessionID); n > 0 {
			// A lead between turns: its people report to it, and it
			// reports up once they are done.
			card.State = cardWorking
			card.Stopped = now
			card.Ticker = waitingOn(n)
			break
		}
		m.finishCard(card, now)
	case "SessionEnd":
		card.State = cardFailed
		card.EndedAt = now
		card.Reason = "gone · session ended"
	}
	m.logCard(card)
}

// finishCard ends a card on the agent's last turn.
func (m *Model) finishCard(card *dispatch, now time.Time) {
	card.State = cardDone
	card.EndedAt = now
	card.Stopped = time.Time{}
	card.Ticker = ""
	m.loadTurn(card)
}

func waitingOn(n int) string {
	if n == 1 {
		return "waiting on 1 agent"
	}
	return fmt.Sprintf("waiting on %d agents", n)
}

// settleCards moves "needs you" cards back to working once nothing is open
// for their session, finishes a lead's card once its people are done, and
// marks cards whose agent vanished.
func (m *Model) settleCards() {
	for _, d := range m.liveCards() {
		if d.State == cardNeedsYou && !m.store.SessionNeedsYou(d.SessionID) {
			d.State = cardWorking
			d.Ticker = ""
			m.logCard(d)
		}
		if d.State == cardWorking && !d.Stopped.IsZero() {
			if n := m.subtreeBusy(d.SessionID); n == 0 {
				if a, ok := m.agentFor(d.SessionID); !ok || a.Status != fleet.Working {
					m.finishCard(d, time.Now())
					m.logCard(d)
				}
			} else if t := waitingOn(n); t != d.Ticker {
				d.Ticker = t
			}
		}
		if a, ok := m.agentFor(d.SessionID); ok && a.Status == fleet.Gone && d.State != cardFailed {
			d.State, d.Reason, d.EndedAt = cardFailed, "gone · session ended", time.Now()
			m.logCard(d)
		}
	}
}

// loadTurn reads the agent's transcript for the card's time window.
func (m *Model) loadTurn(d *dispatch) {
	a, ok := m.agentFor(d.SessionID)
	if !ok || a.Path == "" {
		return
	}
	from := d.StartedAt
	if from.IsZero() {
		from = d.SentAt
	}
	to := d.EndedAt
	if !to.IsZero() {
		to = to.Add(2 * time.Second) // the Stop hook fires just before the last record lands
	}
	d.turn = transcript.Turn(a.Path, from.Add(-time.Second), to, 200)
	d.turnLoaded = true
	files := map[string]bool{}
	for _, e := range d.turn {
		for _, c := range e.Changes {
			files[c.Path] = true
		}
	}
	d.Files = len(files)
}

// tickerLine is the one-line form of a tool call for the card's ticker.
func tickerLine(tool string, input []byte) string {
	desc, code := transcript.ToolBrief(input)
	brief := desc
	if brief == "" {
		brief = firstLineOf(code)
	}
	if brief == "" {
		return tool
	}
	return tool + "  " + brief
}

// spinner is the frame for this tick.
func (m Model) spinner() string {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	return frames[m.anim%len(frames)]
}

// cardLines renders a dispatch card: a bar in the agent's colour, the small
// avatar, name, role chip and state, then the brief, the live line, the
// keys, and when expanded the agent's turn and its last message.
func (m Model) cardLines(d *dispatch, w int, focused bool, now time.Time) []string {
	s := m.st
	color := m.colorFor(d.SessionID)
	agentStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(color))
	bar := s.muted.Render("▏")
	if focused {
		bar = agentStyle.Render("▌")
	}
	const avatarW = 8
	textW := max(20, w-2-avatarW-2)
	avatar := m.smallCritter(d.SessionID, false)
	role := ""
	if a, ok := m.agentFor(d.SessionID); ok && a.Labels.Role != "" {
		role = " " + s.chip(color).Render(strings.ToUpper(a.Labels.Role))
	}
	var state string
	switch d.State {
	case cardQueued:
		state = s.muted.Render("· " + firstNonEmptyS(d.Reason, "sending"))
	case cardWorking:
		state = agentStyle.Render("● working · " + humanAge(d.elapsed(now)))
	case cardNeedsYou:
		state = s.attn.Render("◐ needs you · " + humanAge(d.elapsed(now)))
	case cardDone:
		state = s.ok.Render("✓ done · " + humanAge(d.elapsed(now)))
	case cardFailed:
		state = s.err.Render("✕ " + d.Reason)
	}
	if d.State == cardWorking {
		if roll, via := m.rollupOf(d.SessionID); roll.needsYou > 0 {
			state = s.attn.Render("◐ needs you · via " + via + " · " + humanAge(d.elapsed(now)))
		}
	}
	head := agentStyle.Bold(true).Render(d.Agent) + role
	headW := textW - ansi.StringWidth(state) - 2
	line1 := justify(ansi.Truncate(head, max(8, headW), "…"), state, textW)
	line2 := s.fg.Render(ansi.Truncate("▸ "+actionSummary(d.Summary, d.Brief), textW, "…"))
	if hasSummary(d.Summary) {
		line2 = s.fg.Render("▸ ") + s.muted.Render("(summary) ") + s.fg.Render(ansi.Truncate(actionSummary(d.Summary, ""), max(4, textW-12), "…"))
	}
	var left, right string
	switch d.State {
	case cardQueued:
		left = s.dim.Render("waiting for the agent to pick it up")
	case cardWorking:
		left = s.dim.Render(m.spinner() + " " + firstNonEmptyS(d.Ticker, "starting"))
		right = s.dim.Render(fmt.Sprintf("%d calls", d.Calls))
		if a, ok := m.agentFor(d.SessionID); ok && a.HasUsage {
			right += s.dim.Render(fmt.Sprintf(" · ctx %d%%", a.Usage.Percent()))
		}
	case cardNeedsYou:
		left = s.attn.Render(d.Ticker)
		right = s.key.Render("^Y") + s.keyLabel.Render(" allow  ") + s.key.Render("^N") + s.keyLabel.Render(" deny  ") + s.key.Render("/inbox")
	case cardDone:
		left = s.dim.Render(inbox.Summarize(d.Result))
		right = s.dim.Render(fmt.Sprintf("%d calls · %d files", d.Calls, d.Files))
	case cardFailed:
		left = s.err.Render(d.Reason)
	}
	leftW := textW - ansi.StringWidth(right) - 2
	line3 := justify(ansi.Truncate(left, max(8, leftW), "…"), right, textW)
	foot := s.key.Render("→") + s.keyLabel.Render(" open agent  ")
	if d.Expanded {
		foot += s.key.Render("Enter") + s.keyLabel.Render(" collapse")
	} else {
		foot += s.key.Render("Enter") + s.keyLabel.Render(" expand")
	}
	rows := []string{line1, line2, line3}
	if d.State == cardNeedsYou {
		if it := m.openItem(d.SessionID); it != nil {
			a, _ := m.agentFor(d.SessionID)
			a.Session.SessionID = d.SessionID
			if it.Intent != "" {
				for _, l := range strings.Split(ansi.Wrap(it.Intent, textW, ""), "\n") {
					rows = append(rows, s.fg.Render(l))
				}
			}
			code := m.inAgentColor(a).st.code
			for _, l := range it.Body {
				rows = append(rows, m.wrapCode(l, textW, code)...)
			}
		}
	}
	rows = append(rows, m.childLines(d.SessionID, textW, true)...)
	rows = append(rows, foot)
	var out []string
	for i, r := range rows {
		av := strings.Repeat(" ", avatarW)
		if i < len(avatar) {
			av = avatar[i]
		}
		out = append(out, bar+" "+av+"  "+r)
	}
	if d.Expanded {
		indent := bar + strings.Repeat(" ", avatarW+3)
		out = append(out, indent)
		a, _ := m.agentFor(d.SessionID)
		a.Session.SessionID = d.SessionID
		// The brief in full, as the orchestrator wrote it.
		out = append(out, indent+s.dim.Render("brief"))
		for _, l := range m.markdownLines(d.Brief, textW) {
			out = append(out, indent+l)
		}
		out = append(out, indent)
		if !d.turnLoaded {
			out = append(out, indent+s.dim.Render("transcript not loaded yet"))
		} else if len(d.turn) == 0 {
			out = append(out, indent+s.dim.Render("nothing recorded for this turn yet"))
		}
		for _, l := range m.activityLines(d.turn, a, textW, false) {
			out = append(out, indent+l)
		}
		if d.Result != "" {
			out = append(out, indent)
			for _, l := range m.inAgentColor(a).markdownLines(d.Result, textW) {
				out = append(out, indent+l)
			}
		}
	}
	return out
}

// stripLines is the in-flight strip: one row per live card, shown when two
// or more are live so a long chat never hides a running agent.
func (m Model) stripLines(w int, now time.Time) []string {
	live := m.liveCards()
	if len(live) < 2 {
		return nil
	}
	s := m.st
	focused := m.focusedCard()
	var out []string
	for i, d := range live {
		if i == 4 {
			out = append(out, "  "+s.muted.Render(fmt.Sprintf("+%d more", len(live)-4)))
			break
		}
		label := strings.Repeat(" ", 10)
		if i == 0 {
			label = s.muted.Render("in flight ")
		}
		color := m.colorFor(d.SessionID)
		name := lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(ansi.Truncate(d.Agent, 16, "…"))
		var mark, detail string
		switch d.State {
		case cardQueued:
			mark, detail = s.muted.Render("·"), s.dim.Render("sending")
		case cardWorking:
			mark, detail = lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render("●"), s.dim.Render(m.spinner()+" "+firstNonEmptyS(d.Ticker, "starting"))
		case cardNeedsYou:
			mark, detail = s.attn.Render("◐"), s.attn.Render("needs you · "+d.Ticker)
		}
		age := s.dim.Render(humanAge(d.elapsed(now)))
		head := label + mark + " " + name + strings.Repeat(" ", max(1, 18-ansi.StringWidth(d.Agent)))
		room := w - ansi.StringWidth(head) - ansi.StringWidth(age) - 2
		row := head + ansi.Truncate(detail, max(8, room), "…")
		row = justify(row, age, w)
		if focused == d {
			row = fill(row, w, s.sel)
		}
		out = append(out, row)
	}
	out = append(out, s.border.Render(strings.Repeat("─", max(0, w))))
	return out
}

func firstNonEmptyS(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}
