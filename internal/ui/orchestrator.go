package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/config"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/orch"
)

// OrchestratorName is the session name Den gives its orchestrator.
const OrchestratorName = "den-orchestrator"

// chatEntry is one row of the Orchestrator tab.
type chatEntry struct {
	role      string // "you", "orchestrator", "fan", "note"
	text      string
	streaming bool // text is still arriving
	// fan-out fields
	to        string // the agent's critter name
	sid       string // the agent's session id, for its colour
	status    string
	toolUseID string
	at        time.Time
	// card is set for role "card": a dispatch to a fleet agent.
	card *dispatch
}

type orchEventMsg struct{ ev orch.Event }
type orchStartedMsg struct {
	o   *orch.Orchestrator
	err error
}

// orchState is everything the Orchestrator tab needs.
type orchState struct {
	proc     *orch.Orchestrator
	starting bool
	thinking bool
	chat     []chatEntry
	scroll   int // lines from the bottom
	cost     float64
	pending  []string // messages typed before the process was ready
	status   string
	// cardCur is the chat index of the focused dispatch card, -1 for none;
	// backToCard remembers that → left the Orchestrator view from a card.
	cardCur    int
	backToCard bool
	// resume is the session id to continue; started reports that the
	// current process reached its init event; reconcile asks the first
	// fleet snapshot to settle restored cards; replay carries the restored
	// conversation into a fresh session when a resume fails.
	resume    string
	started   bool
	reconcile bool
	replay    string
}

func startOrchestrator(cfg config.Config, resume string) tea.Cmd {
	return func() tea.Msg {
		name := cfg.Orchestrator.Name
		if name == "" {
			name = OrchestratorName
		}
		o, err := orch.Start(context.Background(), orch.Options{Name: name, Model: cfg.Orchestrator.Model, Resume: resume})
		return orchStartedMsg{o: o, err: err}
	}
}

// newOrchestratorSession forgets the resumable session: the next message
// starts a fresh one. The chat on screen and on disk stays.
func (m *Model) newOrchestratorSession() tea.Cmd {
	if m.orch.proc != nil {
		m.orch.proc.Close()
		m.orch.proc = nil
	}
	m.orch.resume = ""
	m.orch.started = false
	m.orch.status = ""
	m.logSession("")
	note := chatEntry{role: "note", text: "new orchestrator session · the chat above is history", at: time.Now()}
	m.orch.chat = append(m.orch.chat, note)
	m.logEntry(note)
	return nil
}

// replayText is the restored conversation, for a fresh session that could
// not resume the old one: the last exchanges, oldest first.
func (m Model) replayText() string {
	var lines []string
	for _, e := range m.orch.chat {
		switch e.role {
		case "you":
			lines = append(lines, "human: "+e.text)
		case "orchestrator":
			lines = append(lines, "you: "+e.text)
		case "card":
			lines = append(lines, fmt.Sprintf("you dispatched to %s: %s (%s)", e.card.Agent, firstLineOf(e.card.Brief), e.card.State))
		}
	}
	if len(lines) > 20 {
		lines = lines[len(lines)-20:]
	}
	if len(lines) == 0 {
		return ""
	}
	return "Previous conversation, restored by Den after a restart (the session could not be resumed):\n" + strings.Join(lines, "\n")
}

func waitOrch(o *orch.Orchestrator) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-o.Events()
		if !ok {
			return nil
		}
		return orchEventMsg{ev: ev}
	}
}

// isOrchestrator reports whether a discovered session is Den's own orchestrator.
func (m Model) isOrchestrator(a fleet.Agent) bool {
	if a.Session.Name == OrchestratorName {
		return true
	}
	return m.orch.proc != nil && (a.Session.PID == m.orch.proc.PID || (m.orch.proc.SessionID != "" && a.Session.SessionID == m.orch.proc.SessionID))
}

// roster describes the fleet for the orchestrator.
func (m Model) roster() []orch.RosterAgent {
	var out []orch.RosterAgent
	for _, a := range m.agents {
		if a.Status == fleet.Gone {
			continue
		}
		r := orch.RosterAgent{
			Critter: a.Name, Peer: a.Session.Name, Role: a.Labels.Role,
			Status: a.Status.String(), Task: a.Task(), Where: a.Where(),
			Title: a.Title, Files: fleet.TouchedFolders(a), Recent: fleet.RecentActivity(a),
			Earlier: a.Labels.History,
			IsSelf:  m.isOrchestrator(a), Terminal: a.Window != nil,
		}
		for _, it := range m.store.Items() {
			if it.SessionID == a.Session.SessionID && it.State != inbox.Resolved && it.Kind <= inbox.Question {
				line := it.Kind.String() + ": " + it.Title
				if len(it.Body) > 0 {
					line += " " + it.Body[0]
				}
				r.Waiting = append(r.Waiting, line)
			}
		}
		out = append(out, r)
	}
	return out
}

// askOrchestrator queues or sends a human message to the orchestrator.
func (m *Model) askOrchestrator(text string) tea.Cmd {
	e := chatEntry{role: "you", text: text, at: time.Now()}
	m.orch.chat = append(m.orch.chat, e)
	m.logEntry(e)
	m.orch.scroll = 0
	m.orch.cardCur = -1
	m.tab = tabOrch
	if m.orch.proc == nil {
		m.orch.pending = append(m.orch.pending, text)
		if m.orch.starting {
			return nil
		}
		m.orch.starting = true
		m.orch.status = "starting…"
		if m.orch.resume != "" {
			m.orch.status = "resuming…"
		}
		return startOrchestrator(m.cfg, m.orch.resume)
	}
	return m.sendToOrchestrator(text)
}

func (m *Model) sendToOrchestrator(text string) tea.Cmd {
	m.orch.thinking = true
	m.orch.status = "thinking…"
	if m.orch.replay != "" {
		text = m.orch.replay + "\n\nNow the human says:\n" + text
		m.orch.replay = ""
	}
	content := orch.Compose(m.roster(), text)
	proc := m.orch.proc
	return func() tea.Msg {
		if err := proc.Send(content); err != nil {
			return orchEventMsg{ev: orch.Event{Kind: orch.Error, Text: "send failed: " + err.Error()}}
		}
		return nil
	}
}

func (m *Model) handleOrchStarted(msg orchStartedMsg) tea.Cmd {
	m.orch.starting = false
	if msg.err != nil {
		m.orch.status = "failed to start"
		m.orch.chat = append(m.orch.chat, chatEntry{role: "note", text: "Could not start the orchestrator: " + msg.err.Error(), at: time.Now()})
		m.orch.pending = nil
		return nil
	}
	m.orch.proc = msg.o
	m.orch.started = false
	m.orch.status = "ready"
	cmds := []tea.Cmd{waitOrch(msg.o)}
	for _, p := range m.orch.pending {
		cmds = append(cmds, m.sendToOrchestrator(p))
	}
	m.orch.pending = nil
	return tea.Batch(cmds...)
}

func (m *Model) handleOrchEvent(ev orch.Event) tea.Cmd {
	now := time.Now()
	switch ev.Kind {
	case orch.Started:
		m.orch.started = true
		m.orch.status = "ready · " + prettyModel(ev.Model)
		if ev.SessionID != "" && ev.SessionID != m.orch.resume {
			m.orch.resume = ev.SessionID
			m.logSession(ev.SessionID)
		}
	case orch.Delta:
		// Streamed text grows the entry in place; the full block replaces it.
		m.orch.thinking = false
		if n := len(m.orch.chat); n > 0 && m.orch.chat[n-1].streaming {
			m.orch.chat[n-1].text += ev.Text
		} else {
			m.orch.chat = append(m.orch.chat, chatEntry{role: "orchestrator", text: ev.Text, streaming: true, at: now})
		}
	case orch.Text:
		if n := len(m.orch.chat); n > 0 && m.orch.chat[n-1].streaming {
			m.orch.chat[n-1].text = strings.TrimSpace(ev.Text)
			m.orch.chat[n-1].streaming = false
			m.logEntry(m.orch.chat[n-1])
		} else {
			e := chatEntry{role: "orchestrator", text: strings.TrimSpace(ev.Text), at: now}
			m.orch.chat = append(m.orch.chat, e)
			m.logEntry(e)
		}
	case orch.ToolUse:
		if ev.Tool == "SendMessage" {
			in, ok := orch.ParseSendMessage(ev.Input)
			if ok {
				name, sid := m.agentForPeer(in.To)
				if sid == "" {
					m.orch.chat = append(m.orch.chat, chatEntry{role: "fan", to: name, text: firstLineOf(in.Message), status: "sending", toolUseID: ev.ToolUseID, at: now})
					break
				}
				d := &dispatch{ID: ev.ToolUseID, SessionID: sid, Agent: name, Brief: strings.TrimSpace(in.Message), SentAt: now}
				m.orch.chat = append(m.orch.chat, chatEntry{role: "card", card: d, toolUseID: ev.ToolUseID, at: now})
				m.logCard(d)
			}
		} else {
			m.orch.chat = append(m.orch.chat, chatEntry{role: "note", text: "· " + ev.Tool, toolUseID: ev.ToolUseID, at: now})
		}
	case orch.ToolResult:
		for i := range m.orch.chat {
			e := &m.orch.chat[i]
			if e.toolUseID != ev.ToolUseID {
				continue
			}
			failed := ev.IsError || strings.HasPrefix(ev.Text, "Not sent")
			switch {
			case e.card != nil && failed:
				e.card.State, e.card.Reason, e.card.EndedAt = cardFailed, "not sent: "+firstLineOf(ev.Text), now
				m.logCard(e.card)
			case e.card != nil:
				e.card.Reason = "delivered"
				m.logCard(e.card)
			case failed:
				e.status = "failed: " + firstLineOf(ev.Text)
			default:
				e.status = "delivered"
			}
		}
	case orch.Result:
		m.orch.thinking = false
		m.orch.cost = ev.CostUSD
		m.orch.status = fmt.Sprintf("ready · $%.2f", ev.CostUSD)
		// The result text repeats the last assistant text; only add it when
		// no text block was streamed this turn.
		if n := len(m.orch.chat); n > 0 && m.orch.chat[n-1].role == "you" && strings.TrimSpace(ev.Text) != "" {
			e := chatEntry{role: "orchestrator", text: strings.TrimSpace(ev.Text), at: now}
			m.orch.chat = append(m.orch.chat, e)
			m.logEntry(e)
		}
	case orch.Error:
		m.orch.thinking = false
		m.orch.status = "error"
		m.orch.chat = append(m.orch.chat, chatEntry{role: "note", text: ev.Text, at: now})
	case orch.Exited:
		m.orch.thinking = false
		m.orch.proc = nil
		if !m.orch.started && m.orch.resume != "" {
			// The old session could not be resumed (deleted, or Claude Code
			// changed): start fresh and carry the restored chat along.
			m.orch.resume = ""
			m.orch.replay = m.replayText()
			m.logSession("")
			m.orch.chat = append(m.orch.chat, chatEntry{role: "note", text: "the previous orchestrator session could not be resumed · starting a fresh one with the conversation so far", at: now})
			m.orch.starting = true
			m.orch.status = "starting…"
			return startOrchestrator(m.cfg, "")
		}
		m.orch.status = "stopped"
		m.orch.chat = append(m.orch.chat, chatEntry{role: "note", text: ev.Text + " · it restarts with your next message", at: now})
		return nil
	}
	m.orch.scroll = 0
	if m.orch.proc != nil {
		return waitOrch(m.orch.proc)
	}
	return nil
}

func (m Model) critterForPeer(peer string) string {
	name, _ := m.agentForPeer(peer)
	return name
}

// agentForPeer resolves a peer name to the agent's critter name and session
// id; an unknown peer keeps its name and has no id.
func (m Model) agentForPeer(peer string) (name, sid string) {
	p := strings.TrimPrefix(peer, "@")
	if i := strings.Index(p, " ["); i > 0 {
		p = p[:i]
	}
	for _, a := range m.agents {
		if a.Session.Name == p {
			return a.Name, a.Session.SessionID
		}
	}
	return peer, ""
}

func firstLineOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	return s
}

// orchPane renders the Orchestrator tab's conversation.
func (m Model) orchPane(w, h int) string {
	s := m.st
	var lines []string
	if len(m.orch.chat) == 0 {
		lines = append(lines, "", " "+s.dim.Render("Type without an @ to talk to the orchestrator."), "")
		for _, l := range strings.Split(ansi.Wrap("It sees every agent's name, state, task and what is waiting on you, and it can message agents for you. It cannot approve anything; you do that in the inbox. The first message starts it, which takes a few seconds.", w-2, ""), "\n") {
			lines = append(lines, " "+s.muted.Render(l))
		}
		return strings.Join(lines, "\n")
	}
	label := 16
	bodyW := w - label - 3
	if bodyW < 20 {
		label, bodyW = 0, w-2
	}
	// The orchestrator's code highlights carry its own colour, the way an
	// agent's do in the detail pane.
	orchColor := s.pal.Bright
	if m.orch.proc != nil && m.orch.proc.SessionID != "" {
		orchColor = m.colorFor(m.orch.proc.SessionID)
	}
	mo := m
	mo.st.code = lipgloss.NewStyle().Foreground(lipgloss.Color(orchColor))
	now := time.Now()
	strip := m.stripLines(w-2, now)
	focusStart, focusEnd := -1, -1
	for i, e := range m.orch.chat {
		var from string
		var body []string
		if e.card != nil {
			focused := i == m.orch.cardCur
			cl := m.cardLines(e.card, w-2, focused, now)
			if focused {
				focusStart, focusEnd = len(lines), len(lines)+len(cl)
			}
			for _, l := range cl {
				lines = append(lines, " "+l)
			}
			lines = append(lines, "")
			continue
		}
		switch e.role {
		case "you":
			from = s.dim.Render("you ›")
			body = m.plainLines(e.text, bodyW, s.fg)
		case "orchestrator":
			from = lipgloss.NewStyle().Foreground(lipgloss.Color(orchColor)).Render("orchestrator ›")
			body = mo.markdownLines(e.text, bodyW)
			if e.streaming {
				if len(body) == 0 {
					body = []string{""}
				}
				body[len(body)-1] += s.bright.Render("▍")
			}
		case "fan":
			st := s.ok
			if strings.HasPrefix(e.status, "failed") {
				st = s.err
			} else if e.status == "sending" {
				st = s.attn
			}
			name := s.fg.Render(e.to)
			if e.sid != "" {
				name = lipgloss.NewStyle().Foreground(lipgloss.Color(m.colorFor(e.sid))).Bold(true).Render(e.to)
			}
			body = []string{s.dim.Render("→ ") + name + s.dim.Render(" · "+ansi.Truncate(e.text, max(10, bodyW-ansi.StringWidth(e.to)-len(e.status)-8), "…")+" · ") + st.Render(e.status)}
		default:
			body = m.plainLines(e.text, bodyW, s.muted)
		}
		for i, l := range body {
			prefix := strings.Repeat(" ", label+1)
			if i == 0 && label > 0 {
				pad := label - ansi.StringWidth(from)
				if pad < 0 {
					pad = 0
				}
				prefix = strings.Repeat(" ", pad) + from + " "
			}
			lines = append(lines, prefix+l)
		}
		lines = append(lines, "")
	}
	if m.orch.thinking {
		lines = append(lines, strings.Repeat(" ", label+1)+s.dim.Render("thinking…"))
	}
	// The strip stays put; the chat scrolls under it. With a card focused
	// the window is anchored to that card, otherwise it follows the tail,
	// honouring scroll-back.
	body := h - len(strip)
	if body < 3 {
		body = 3
	}
	if len(lines) > body {
		start := len(lines) - body - m.orch.scroll
		if focusStart >= 0 {
			start = focusStart
			if focusEnd-focusStart < body {
				start = min(focusStart, len(lines)-body)
			}
		}
		start = clamp(start, 0, len(lines)-body)
		lines = lines[start : start+body]
	}
	return strings.Join(append(strip, lines...), "\n")
}

// closeOrchestrator stops the process if running.
func (m Model) closeOrchestrator() {
	if m.orch.proc != nil {
		m.orch.proc.Close()
	}
}
