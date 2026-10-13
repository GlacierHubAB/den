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
	"github.com/GlacierHubAB/den/internal/remote"
	"github.com/GlacierHubAB/den/internal/sanitize"
	"github.com/GlacierHubAB/den/internal/tree"
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
	// mission is the thread the entry belongs to: the mission the human's
	// latest message was about, "" for the plain chat.
	mission string
	// full is the whole text behind a tool call drawn as one line (a
	// message or a brief), shown when the entry is expanded.
	full     string
	expanded bool
}

type orchEventMsg struct {
	ev   orch.Event
	from *orch.Orchestrator // the process it came from; a replaced one is ignored
}
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
	// thread is the mission the latest message was about; what the
	// orchestrator says next joins it. said reports that this turn's answer
	// already came as a text block.
	thread string
	said   bool
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
	// lastEvent is when the process last said anything; unanswered holds
	// the messages sent since its last result, so a restart can resend
	// them; restarts counts automatic restarts for the current silence.
	lastEvent  time.Time
	unanswered []string
	restarts   int
	prevTab    tab // where Ctrl+O came from
}

// Silence thresholds: the status line says so after stallWarn, and Den
// restarts the process once after stallRestart, resending what it owes.
const (
	stallWarn    = 90 * time.Second
	stallRestart = 5 * time.Minute
)

func startOrchestrator(cfg config.Config, resume, guidance string) tea.Cmd {
	return func() tea.Msg {
		name := cfg.Orchestrator.Name
		if name == "" {
			name = OrchestratorName
		}
		o, err := orch.Start(context.Background(), orch.Options{Name: name, Model: cfg.Orchestrator.Model, Spawn: cfg.Orchestrator.Spawn, Resume: resume, Guidance: guidance})
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
	m.logEntry(m.addChat(chatEntry{role: "note", text: "new orchestrator session · the chat above is history", at: time.Now()}))
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
		return orchEventMsg{ev: ev, from: o}
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
			Host: a.Host,
		}
		if last, stale := m.lastKnown(a); stale {
			r.Status = "host offline, last known " + last.String()
		}
		if a.Host != "" {
			// A remote agent's title, files and recent activity cross only
			// for the human's own views under full: they never go into the
			// orchestrator's prompt, which Claude Code writes to disk, or
			// steer a local agent.
			r.Task, r.Title, r.Files, r.Recent = a.Labels.Task, "", nil, nil
		}
		for _, it := range m.store.Items() {
			if itemKey(it) == a.Session.SessionID && it.State != inbox.Resolved && it.Kind <= inbox.Question {
				line := it.Kind.String() + ": " + it.Title
				if len(it.Body) > 0 {
					line += " " + it.Body[0]
				}
				if a.Host != "" {
					// A host's request body is its own text, up to 16 KB of
					// it: never in the prompt, which is written to disk.
					line = "a " + it.Kind.String() + " request"
					if it.Kind == inbox.Question {
						line = "a question"
					}
					if remote.ToolName(it.Tool) {
						line += " (" + it.Tool + ")"
					}
				}
				r.Waiting = append(r.Waiting, line)
			}
		}
		out = append(out, r)
	}
	return out
}

// askOrchestrator sends a message typed outside the Overview: the plain
// chat, shown on the Orchestrator tab.
func (m *Model) askOrchestrator(text string) tea.Cmd {
	m.orch.cardCur = -1
	if m.tab != tabOrch {
		m.orch.prevTab = m.tab
	}
	m.tab = tabOrch
	return m.tell("", text, text)
}

// tell is the one way a message reaches the orchestrator. The chat shows
// the human's words in the mission's thread ("" for the plain chat) and
// the orchestrator receives the prompt, queued until the process is ready.
func (m *Model) tell(mission, shown, prompt string) tea.Cmd {
	m.orch.thread = mission
	m.logEntry(m.addChat(chatEntry{role: "you", text: shown, at: time.Now()}))
	m.orch.scroll, m.ov.scroll = 0, 0
	if m.orch.proc == nil {
		m.orch.pending = append(m.orch.pending, prompt)
		if m.orch.starting {
			return nil
		}
		m.orch.starting = true
		m.orch.status = "starting…"
		if m.orch.resume != "" {
			m.orch.status = "resuming…"
		}
		return startOrchestrator(m.cfg, m.orch.resume, m.guide.active)
	}
	return m.sendToOrchestrator(prompt)
}

// addChat appends an entry to the thread of the latest message and returns
// it, for logging.
func (m *Model) addChat(e chatEntry) chatEntry {
	e.mission = m.orch.thread
	if e.card != nil {
		e.card.Thread = e.mission
	}
	m.orch.chat = append(m.orch.chat, e)
	return e
}

// streaming is the index of the entry whose text is still arriving, -1 for
// none. The human may have typed since it began, so it need not be last.
func (o orchState) streaming() int {
	for i := len(o.chat) - 1; i >= 0; i-- {
		if o.chat[i].streaming {
			return i
		}
	}
	return -1
}

func (m *Model) sendToOrchestrator(text string) tea.Cmd {
	m.orch.thinking = true
	m.orch.status = "thinking…"
	m.orch.lastEvent = time.Now()
	m.orch.unanswered = append(m.orch.unanswered, text)
	if m.orch.replay != "" {
		text = m.orch.replay + "\n\nNow the human says:\n" + text
		m.orch.replay = ""
	}
	content := orch.ComposeWith(m.roster(), m.templateInfos(), text)
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
		m.addChat(chatEntry{role: "note", text: "Could not start the orchestrator: " + msg.err.Error(), at: time.Now()})
		m.orch.pending = nil
		return nil
	}
	m.orch.proc = msg.o
	m.orch.started = false
	m.orch.said = false
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
	m.orch.lastEvent = now
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
		if i := m.orch.streaming(); i >= 0 {
			m.orch.chat[i].text += ev.Text
		} else {
			m.addChat(chatEntry{role: "orchestrator", text: ev.Text, streaming: true, at: now})
		}
	case orch.Text:
		m.orch.said = true
		text := m.missionReply(strings.TrimSpace(ev.Text))
		if i := m.orch.streaming(); i >= 0 {
			m.orch.chat[i].text = text
			m.orch.chat[i].streaming = false
			m.logEntry(m.orch.chat[i])
		} else {
			m.logEntry(m.addChat(chatEntry{role: "orchestrator", text: text, at: now}))
		}
	case orch.ToolUse:
		if ev.Tool == "SendMessage" {
			in, ok := orch.ParseSendMessage(ev.Input)
			if ok {
				name, sid := m.agentForPeer(in.To)
				if sid == "" {
					m.addChat(chatEntry{role: "fan", to: name, text: actionSummary(in.Summary, in.Message), full: in.Message, status: "sending", toolUseID: ev.ToolUseID, at: now})
					break
				}
				d := &dispatch{ID: ev.ToolUseID, SessionID: sid, Agent: name, Brief: strings.TrimSpace(in.Message), Summary: actionSummary(in.Summary, ""), SentAt: now}
				m.attachCard(d)
				m.addChat(chatEntry{role: "card", card: d, toolUseID: ev.ToolUseID, at: now})
				m.logCard(d)
				m.learnEdge(tree.Edge{Parent: tree.Orchestrator, Child: sid, Brief: firstLineOf(in.Message), At: now, Source: "dispatch"})
			}
		} else {
			note := chatEntry{role: "note", text: orch.Describe(ev.Tool, ev.Input), toolUseID: ev.ToolUseID, at: now}
			if in, ok := orch.ParseSpawn(ev.Input); ok && ev.Tool == orch.SpawnAgentTool {
				m.noteSpawn(ev.ToolUseID, in.Brief, m.turn.id, now)
				note.full = in.Brief
			} else if mm, ok := parseMessageCall(ev.Tool, ev.Input); ok {
				// Another tool that messages someone: one line, like a
				// SendMessage to a peer outside the fleet.
				name, sid := m.agentForPeer(mm.To)
				note = chatEntry{role: "fan", to: name, sid: sid, text: actionSummary(mm.Summary, mm.Message), full: mm.Message, status: "sending", toolUseID: ev.ToolUseID, at: now}
			}
			m.addChat(note)
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
			case e.role == "note":
				// A spawn's answer says what started and where; it earns a
				// line. Other tool notes stay as they are.
				switch {
				case ev.IsError:
					e.status = "failed: " + firstLineOf(ev.Text)
				case strings.HasPrefix(e.text, "▶"):
					e.status = firstLineOf(ev.Text)
					m.learnSpawn(ev.Text, tree.Orchestrator, ev.ToolUseID, "")
				}
			case failed:
				e.status = "failed: " + firstLineOf(ev.Text)
			default:
				e.status = "delivered"
			}
		}
	case orch.Result:
		m.orch.thinking = false
		m.turn = missionTurn{}
		m.orch.unanswered = nil
		m.orch.restarts = 0
		m.orch.cost = ev.CostUSD
		m.orch.status = fmt.Sprintf("ready · $%.2f", ev.CostUSD)
		// The result text repeats the last assistant text; only add it when
		// no text block came this turn.
		if !m.orch.said && strings.TrimSpace(ev.Text) != "" {
			m.logEntry(m.addChat(chatEntry{role: "orchestrator", text: m.missionReply(strings.TrimSpace(ev.Text)), at: now}))
		}
		m.orch.said = false
	case orch.Error:
		m.orch.thinking = false
		m.turn.pending = false
		m.orch.status = "error"
		m.addChat(chatEntry{role: "note", text: ev.Text, at: now})
	case orch.Exited:
		m.orch.thinking = false
		m.orch.proc = nil
		if !m.orch.started && m.orch.resume != "" {
			// The old session could not be resumed (deleted, or Claude Code
			// changed): start fresh and carry the restored chat along.
			m.orch.resume = ""
			m.orch.replay = m.replayText()
			m.logSession("")
			m.addChat(chatEntry{role: "note", text: "the previous orchestrator session could not be resumed · starting a fresh one with the conversation so far", at: now})
			m.orch.starting = true
			m.orch.status = "starting…"
			return startOrchestrator(m.cfg, "", m.guide.active)
		}
		m.orch.status = "stopped"
		m.addChat(chatEntry{role: "note", text: ev.Text + " · it restarts with your next message", at: now})
		return nil
	}
	m.orch.scroll, m.ov.scroll = 0, 0
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
		pad := chatPad(w)
		lines = append(lines, "", pad+s.dim.Render("Type without an @ to talk to the orchestrator."), "")
		for _, l := range strings.Split(ansi.Wrap("It sees every agent's name, state, task and what is waiting on you, and it can message agents for you. It cannot approve anything; you do that in the inbox. The first message starts it, which takes a few seconds.", w-2*len(pad), ""), "\n") {
			lines = append(lines, pad+s.muted.Render(l))
		}
		return strings.Join(lines, "\n")
	}
	strip, lines, _ := m.orchWindow(w, h)
	return strings.Join(append(strip, lines...), "\n")
}

// orchWindow is what the Orchestrator pane shows: the strip, the chat lines
// in view, and for each of those the chat entry it draws (-1 for none).
func (m Model) orchWindow(w, h int) (strip, lines []string, owners []int) {
	strip = m.stripLines(w-2, time.Now())
	lines, owners, focusStart, focusEnd := m.chatLinesOwned(w, func(chatEntry) bool { return true })
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
		lines, owners = lines[start:start+body], owners[start:start+body]
	}
	return strip, lines, owners
}

// chatPad is the chat's inner margin, either side of the text: two
// columns, one when the pane is narrow.
func chatPad(w int) string {
	if w < 40 {
		return " "
	}
	return "  "
}

// chatLines renders the conversation entries keep picks, streaming text
// with its cursor, and the thinking line while the orchestrator works on
// their thread. The Orchestrator tab shows every entry, a mission pane its
// own thread. focusStart and focusEnd bound the focused card, -1 for none.
func (m Model) chatLines(w int, keep func(chatEntry) bool) (lines []string, focusStart, focusEnd int) {
	lines, _, focusStart, focusEnd = m.chatLinesOwned(w, keep)
	return lines, focusStart, focusEnd
}

// chatLinesOwned is chatLines with, for each line, the chat entry it
// draws (-1 for the thinking line).
func (m Model) chatLinesOwned(w int, keep func(chatEntry) bool) (lines []string, owners []int, focusStart, focusEnd int) {
	s := m.st
	// The speaker's name sits on its own line above the text, so the text
	// has the pane's width less a margin.
	pad := chatPad(w)
	bodyW := max(10, w-2*len(pad))
	// The orchestrator's code highlights carry its own colour, the way an
	// agent's do in the detail pane.
	orchColor := s.pal.Bright
	if m.orch.proc != nil && m.orch.proc.SessionID != "" {
		orchColor = m.colorFor(m.orch.proc.SessionID)
	}
	mo := m
	mo.st.code = lipgloss.NewStyle().Foreground(lipgloss.Color(orchColor))
	now := time.Now()
	focusStart, focusEnd = -1, -1
	for i, e := range m.orch.chat {
		if !keep(e) {
			continue
		}
		var from string
		var body []string
		if e.card != nil {
			focused := i == m.orch.cardCur
			cl := m.cardLines(e.card, bodyW, focused, now)
			if focused {
				focusStart, focusEnd = len(lines), len(lines)+len(cl)
			}
			for _, l := range cl {
				lines, owners = append(lines, pad+l), append(owners, i)
			}
			lines, owners = append(lines, ""), append(owners, -1)
			continue
		}
		switch e.role {
		case "you":
			from = s.dim.Render("you")
			body = m.plainLines(e.text, bodyW, s.fg)
		case "orchestrator":
			from = lipgloss.NewStyle().Foreground(lipgloss.Color(orchColor)).Bold(true).Render("orchestrator")
			body = mo.orchTextLines(e, bodyW)
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
			// Only a fleet agent's exact name is coloured; anything else
			// is the model's word and is shown plain.
			to := ansi.Truncate(sanitize.Line(e.to), max(8, bodyW/3), "…")
			name := s.fg.Render(to)
			if e.sid != "" {
				name = lipgloss.NewStyle().Foreground(lipgloss.Color(m.colorFor(e.sid))).Bold(true).Render(to)
			}
			status := ansi.Truncate(sanitize.Line(e.status), max(8, bodyW/3), "…")
			room := max(4, bodyW-ansi.StringWidth(to)-ansi.StringWidth(status)-7)
			body = []string{s.dim.Render("→ ") + name + s.dim.Render(": ") + s.fg.Render(ansi.Truncate(sanitize.Line(e.text), room, "…")) + s.dim.Render(" · ") + st.Render(status)}
			if e.expanded {
				body = append(body, mo.expandedLines(e.full, bodyW)...)
			}
		default:
			if e.status != "" {
				st := s.muted
				if strings.HasPrefix(e.status, "failed") {
					st = s.err
				}
				body = append(m.noteLines(e, bodyW), m.plainLines(e.status, bodyW, st)...)
				if e.expanded {
					body = append(body, mo.expandedLines(e.full, bodyW)...)
				}
				break
			}
			body = m.noteLines(e, bodyW)
			if e.expanded {
				body = append(body, mo.expandedLines(e.full, bodyW)...)
			}
			// The close tool waits for the human: its question is shown
			// right here, answerable with the same keys as in the Inbox.
			if strings.HasPrefix(e.text, "▶ asking you to close") {
				if it := m.pendingOrchClose(); it != nil {
					body = append(body, s.attn.Render("◐ "+it.Title+" It waits for you."))
					for _, l := range it.Body {
						body = append(body, m.plainLines(l, bodyW-2, s.fg)...)
					}
					body = append(body, s.key.Render("^Y")+s.keyLabel.Render(" close them")+"  "+s.key.Render("^N")+s.keyLabel.Render(" keep them")+s.dim.Render("  · also in the Inbox"))
				}
			}
			// So does a skills offer, from propose_skills or a spawn with a role.
			if strings.HasPrefix(e.text, "▶ offering you skills") || strings.HasPrefix(e.text, "▶ starting an agent") {
				if it := m.pendingOrchSkills(); it != nil {
					body = append(body, s.attn.Render("◐ "+it.Title+" It waits for you."))
					for _, l := range it.Body {
						body = append(body, m.plainLines(l, bodyW-2, s.fg)...)
					}
					body = append(body, s.key.Render("^Y")+s.keyLabel.Render(" install them")+"  "+s.key.Render("^N")+s.keyLabel.Render(" decline")+s.dim.Render("  · also in the Inbox"))
				}
			}
		}
		lp := pad
		if i == m.orch.cardCur && e.collapsible() {
			// The focused action takes the bar a focused card has.
			lp = s.bright.Render("▌") + pad[1:]
			focusStart, focusEnd = len(lines), len(lines)+len(body)+1
		}
		if from != "" {
			lines, owners = append(lines, pad+from), append(owners, i)
		}
		for _, l := range body {
			lines, owners = append(lines, lp+l), append(owners, i)
		}
		lines, owners = append(lines, ""), append(owners, -1)
	}
	if (m.orch.thinking || m.orch.starting) && keep(chatEntry{role: "orchestrator", mission: m.orch.thread}) {
		think := "thinking…"
		if m.orch.starting {
			think = m.orch.status
		} else if m.pendingOrchClose() != nil {
			think = "waiting for your ^Y or ^N on the close question"
		} else if m.pendingOrchSkills() != nil {
			think = "waiting for your ^Y or ^N on the skills offer"
		} else if q := time.Since(m.orch.lastEvent); q > 10*time.Second {
			think = "thinking… " + humanAge(q)
		}
		lines, owners = append(lines, pad+s.dim.Render(think)), append(owners, -1)
	}
	return lines, owners, focusStart, focusEnd
}

// closeOrchestrator stops the process if running.
func (m Model) closeOrchestrator() {
	if m.orch.proc != nil {
		m.orch.proc.Close()
	}
}

// watchOrchestrator runs every tick: a turn that has gone quiet is named
// in the status line, and after stallRestart the process is restarted
// once and owed messages are resent. A stuck API connection looks exactly
// like deep thought from outside, so the thresholds are generous.
func (m *Model) watchOrchestrator() tea.Cmd {
	if !m.orch.thinking || m.orch.proc == nil {
		return nil
	}
	quiet := time.Since(m.orch.lastEvent)
	switch {
	case quiet > stallRestart && m.orch.restarts == 0:
		m.orch.restarts++
		m.logEntry(m.addChat(chatEntry{role: "note", text: fmt.Sprintf("no reply for %s · restarting the orchestrator and resending", humanAge(quiet)), at: time.Now()}))
		return m.restartOrchestrator()
	case quiet > stallWarn:
		m.orch.status = fmt.Sprintf("thinking… no reply for %s · /orch restart", humanAge(quiet))
	}
	return nil
}

// restartOrchestrator kills the process, resumes the same session and
// resends the messages it never answered.
func (m *Model) restartOrchestrator() tea.Cmd {
	owed := append([]string(nil), m.orch.unanswered...)
	if m.orch.proc != nil {
		proc := m.orch.proc
		m.orch.proc = nil
		go proc.Close() // may take a few seconds to kill a stuck process
	}
	m.orch.thinking = false
	m.orch.unanswered = nil
	m.orch.pending = append(m.orch.pending, owed...)
	m.orch.started = false
	m.orch.starting = true
	m.orch.status = "restarting…"
	m.guide.restart = false // the new process starts with the guidance in use
	return startOrchestrator(m.cfg, m.orch.resume, m.guide.active)
}
