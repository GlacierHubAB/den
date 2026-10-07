package ui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/peer"
)

// outgoing is a message waiting for the typing guard to let it through.
type outgoing struct {
	agent fleet.Agent
	text  string
	item  *inbox.Item
	since time.Time
}

type sentMsg struct {
	item *inbox.Item
	name string
	err  error
}

// guardMax is how long a message waits for you to leave that terminal
// before Den delivers it anyway.
const guardMax = 10 * time.Minute

func newInput(st styles) textinput.Model {
	ti := textinput.New()
	ti.Prompt = "› "
	ti.Placeholder = "@Name to message one agent · plain text for the orchestrator (M4)"
	ti.CharLimit = 4000
	ti.SetVirtualCursor(true)
	applyInputStyles(&ti, st)
	return ti
}

func applyInputStyles(ti *textinput.Model, st styles) {
	c := lipgloss.Color
	s := textinput.DefaultDarkStyles()
	s.Focused.Prompt = lipgloss.NewStyle().Foreground(c(st.pal.Bright))
	s.Focused.Text = lipgloss.NewStyle().Foreground(c(st.pal.Fg))
	s.Focused.Placeholder = lipgloss.NewStyle().Foreground(c(st.pal.Muted))
	s.Blurred.Prompt = lipgloss.NewStyle().Foreground(c(st.pal.Muted))
	s.Blurred.Text = lipgloss.NewStyle().Foreground(c(st.pal.FgDim))
	s.Blurred.Placeholder = lipgloss.NewStyle().Foreground(c(st.pal.Muted))
	ti.SetStyles(s)
}

// startCompose focuses the command line with an optional prefill.
func (m *Model) startCompose(prefill string) tea.Cmd {
	m.input.SetValue(prefill)
	m.input.CursorEnd()
	m.complIdx = 0
	return m.input.Focus()
}

func (m *Model) stopCompose() {
	m.input.Blur()
	m.input.SetValue("")
	m.complIdx = 0
}

// handleComposeKey routes keys while the command line is focused.
func (m Model) handleComposeKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	compl := completions(m.input.Value(), m.agents)
	switch msg.String() {
	case "esc":
		m.stopCompose()
		return m, nil
	case "ctrl+c":
		return m, tea.Quit
	case "tab":
		if len(compl) > 0 {
			m.input.SetValue("@" + compl[m.complIdx%len(compl)].Name + " ")
			m.input.CursorEnd()
			m.complIdx = 0
		}
		return m, nil
	case "down", "ctrl+n":
		if len(compl) > 0 {
			m.complIdx = (m.complIdx + 1) % len(compl)
		}
		return m, nil
	case "up", "ctrl+p":
		if len(compl) > 0 {
			m.complIdx = (m.complIdx + len(compl) - 1) % len(compl)
		}
		return m, nil
	case "enter":
		if len(compl) > 0 && resolveMention(m.input.Value(), m.agents).target == nil {
			m.input.SetValue("@" + compl[m.complIdx%len(compl)].Name + " ")
			m.input.CursorEnd()
			return m, nil
		}
		return m.submit()
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.complIdx = 0
	return m, cmd
}

// submit sends the command line's text.
func (m Model) submit() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		m.stopCompose()
		return m, nil
	}
	mt := resolveMention(text, m.agents)
	switch {
	case mt.target == nil && len(mt.ambiguous) > 0:
		return m.say(fmt.Sprintf("Which one: %s?", strings.Join(mt.ambiguous, ", ")), true)
	case mt.target == nil && (mt.missing != "" || strings.HasPrefix(text, "@")):
		return m.say(fmt.Sprintf("No agent called %q", mt.missing), true)
	case mt.target == nil:
		return m.say("The orchestrator arrives in milestone 4. Address one agent with @Name for now.", true)
	case mt.body == "":
		return m.say("Add the message after the name.", true)
	}
	target := *mt.target
	if target.Status == fleet.Gone {
		return m.say(target.Name+" is gone.", true)
	}
	m.stopCompose()
	if m.atTerminal(target.Session.SessionID) {
		it := m.store.Sent(target.Session.SessionID, mt.body, "waiting: you are at that terminal")
		m.queue = append(m.queue, outgoing{agent: target, text: mt.body, item: it, since: time.Now()})
		m.refresh()
		return m, holdTick()
	}
	it := m.store.Sent(target.Session.SessionID, mt.body, "sending…")
	m.refresh()
	return m, deliver(target, mt.body, it)
}

// say shows a short message on the top bar without leaving compose mode.
func (m Model) say(text string, isErr bool) (tea.Model, tea.Cmd) {
	m.flash, m.flashErr = text, isErr
	return m, tea.Tick(4*time.Second, func(time.Time) tea.Msg { return flashClearMsg{} })
}

func deliver(a fleet.Agent, text string, it *inbox.Item) tea.Cmd {
	return func() tea.Msg {
		err := peer.Send(a.Session.Socket, text)
		return sentMsg{item: it, name: a.Name, err: err}
	}
}

// flushQueue delivers queued messages whose terminal is no longer focused,
// or that have waited long enough. It returns the deliveries to run and
// whether anything is still queued.
func (m *Model) flushQueue() ([]tea.Cmd, bool) {
	var cmds []tea.Cmd
	var keep []outgoing
	for _, o := range m.queue {
		if m.atTerminal(o.agent.Session.SessionID) && time.Since(o.since) < guardMax {
			keep = append(keep, o)
			continue
		}
		m.store.SetStatus(o.item, "sending…", false)
		cmds = append(cmds, deliver(o.agent, o.text, o.item))
	}
	m.queue = keep
	return cmds, len(keep) > 0
}

func (m *Model) applySent(msg sentMsg) {
	if msg.err != nil {
		m.store.SetStatus(msg.item, "failed: "+msg.err.Error(), true)
		m.flash, m.flashErr = "Not delivered to "+msg.name+": "+msg.err.Error(), true
		return
	}
	m.store.SetStatus(msg.item, "delivered", true)
	m.flash, m.flashErr = "Delivered to "+msg.name, false
}
