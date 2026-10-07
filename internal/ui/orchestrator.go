package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/orch"
)

// OrchestratorName is the session name Den gives its orchestrator.
const OrchestratorName = "den-orchestrator"

// chatEntry is one row of the Orchestrator tab.
type chatEntry struct {
	role string // "you", "orchestrator", "fan", "note"
	text string
	// fan-out fields
	to        string
	status    string
	toolUseID string
	at        time.Time
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
}

func startOrchestrator() tea.Cmd {
	return func() tea.Msg {
		o, err := orch.Start(context.Background(), orch.Options{Name: OrchestratorName})
		return orchStartedMsg{o: o, err: err}
	}
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
	m.orch.chat = append(m.orch.chat, chatEntry{role: "you", text: text, at: time.Now()})
	m.orch.scroll = 0
	m.tab = tabOrch
	if m.orch.proc == nil {
		m.orch.pending = append(m.orch.pending, text)
		if m.orch.starting {
			return nil
		}
		m.orch.starting = true
		m.orch.status = "starting…"
		return startOrchestrator()
	}
	return m.sendToOrchestrator(text)
}

func (m *Model) sendToOrchestrator(text string) tea.Cmd {
	m.orch.thinking = true
	m.orch.status = "thinking…"
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
		m.orch.status = "ready · " + prettyModel(ev.Model)
	case orch.Text:
		m.orch.chat = append(m.orch.chat, chatEntry{role: "orchestrator", text: strings.TrimSpace(ev.Text), at: now})
	case orch.ToolUse:
		if ev.Tool == "SendMessage" {
			in, ok := orch.ParseSendMessage(ev.Input)
			if ok {
				m.orch.chat = append(m.orch.chat, chatEntry{role: "fan", to: m.critterForPeer(in.To), text: firstLineOf(in.Message), status: "sending", toolUseID: ev.ToolUseID, at: now})
			}
		} else {
			m.orch.chat = append(m.orch.chat, chatEntry{role: "note", text: "· " + ev.Tool, toolUseID: ev.ToolUseID, at: now})
		}
	case orch.ToolResult:
		for i := range m.orch.chat {
			e := &m.orch.chat[i]
			if e.toolUseID == ev.ToolUseID && e.role == "fan" {
				switch {
				case ev.IsError, strings.HasPrefix(ev.Text, "Not sent"):
					e.status = "failed: " + firstLineOf(ev.Text)
				default:
					e.status = "delivered"
				}
			}
		}
	case orch.Result:
		m.orch.thinking = false
		m.orch.cost = ev.CostUSD
		m.orch.status = fmt.Sprintf("ready · $%.2f", ev.CostUSD)
		// The result text repeats the last assistant text; only add it when
		// no text block was streamed this turn.
		if n := len(m.orch.chat); n > 0 && m.orch.chat[n-1].role == "you" && strings.TrimSpace(ev.Text) != "" {
			m.orch.chat = append(m.orch.chat, chatEntry{role: "orchestrator", text: strings.TrimSpace(ev.Text), at: now})
		}
	case orch.Error:
		m.orch.thinking = false
		m.orch.status = "error"
		m.orch.chat = append(m.orch.chat, chatEntry{role: "note", text: ev.Text, at: now})
	case orch.Exited:
		m.orch.thinking = false
		m.orch.proc = nil
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
	p := strings.TrimPrefix(peer, "@")
	if i := strings.Index(p, " ["); i > 0 {
		p = p[:i]
	}
	for _, a := range m.agents {
		if a.Session.Name == p {
			return a.Name
		}
	}
	return peer
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
	for _, e := range m.orch.chat {
		var from string
		var body []string
		var style = s.fg
		switch e.role {
		case "you":
			from = s.dim.Render("you ›")
			style = s.fg
			body = strings.Split(ansi.Wrap(e.text, bodyW, ""), "\n")
		case "orchestrator":
			from = s.bright.Render("orchestrator ›")
			style = s.fg
			body = strings.Split(ansi.Wrap(e.text, bodyW, ""), "\n")
		case "fan":
			from = ""
			st := s.ok
			if strings.HasPrefix(e.status, "failed") {
				st = s.err
			} else if e.status == "sending" {
				st = s.attn
			}
			body = []string{s.dim.Render("→ ") + s.fg.Render(e.to) + s.dim.Render(" · "+ansi.Truncate(e.text, max(10, bodyW-len(e.to)-len(e.status)-8), "…")+" · ") + st.Render(e.status)}
		default:
			from = ""
			style = s.muted
			body = strings.Split(ansi.Wrap(e.text, bodyW, ""), "\n")
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
			lines = append(lines, prefix+style.Render(l))
		}
		lines = append(lines, "")
	}
	if m.orch.thinking {
		lines = append(lines, strings.Repeat(" ", label+1)+s.dim.Render("thinking…"))
	}
	// Keep the tail visible, honouring scroll-back.
	if len(lines) > h {
		end := len(lines) - m.orch.scroll
		if end < h {
			end = h
		}
		if end > len(lines) {
			end = len(lines)
		}
		lines = lines[end-h : end]
	}
	return strings.Join(lines, "\n")
}

// closeOrchestrator stops the process if running.
func (m Model) closeOrchestrator() {
	if m.orch.proc != nil {
		m.orch.proc.Close()
	}
}
