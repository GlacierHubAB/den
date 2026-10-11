package ui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
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

func newInput(st styles) textarea.Model {
	ta := textarea.New()
	ta.Placeholder = "Ask the orchestrator, or @Name to message one agent · /help for commands"
	ta.ShowLineNumbers = false
	// The textarea sizes itself from its own word wrap, so the rows it
	// shows are the rows it has; no limit on the text, only on the rows.
	ta.CharLimit = 0
	ta.DynamicHeight = true
	ta.MinHeight = 1
	ta.MaxHeight = minInputCap
	ta.MaxContentHeight = maxInputContent
	ta.EndOfBufferCharacter = ' '
	ta.SetVirtualCursor(true)
	// The first line carries the prompt; continuation lines align under it.
	ta.SetPromptFunc(2, func(info textarea.PromptInfo) string {
		if info.LineNumber == 0 {
			return "› "
		}
		return "  "
	})
	// Enter sends, so a newline needs a modifier.
	ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("shift+enter", "alt+enter", "ctrl+j"))
	applyInputStyles(&ta, st)
	ta.SetWidth(80)
	ta.SetHeight(1)
	return ta
}

func applyInputStyles(ta *textarea.Model, st styles) {
	c := lipgloss.Color
	s := textarea.DefaultDarkStyles()
	plain := lipgloss.NewStyle()
	s.Focused.Base, s.Blurred.Base = plain, plain
	s.Focused.CursorLine, s.Blurred.CursorLine = plain, plain
	s.Focused.EndOfBuffer, s.Blurred.EndOfBuffer = plain, plain
	s.Focused.Prompt = lipgloss.NewStyle().Foreground(c(st.pal.Bright))
	s.Focused.Text = lipgloss.NewStyle().Foreground(c(st.pal.Fg))
	s.Focused.Placeholder = lipgloss.NewStyle().Foreground(c(st.pal.Muted))
	s.Blurred.Prompt = lipgloss.NewStyle().Foreground(c(st.pal.Muted))
	s.Blurred.Text = lipgloss.NewStyle().Foreground(c(st.pal.FgDim))
	s.Blurred.Placeholder = lipgloss.NewStyle().Foreground(c(st.pal.Muted))
	ta.SetStyles(s)
}

// minInputCap is the fewest rows the command line may grow to, on a
// short terminal; maxInputContent is far more than anyone types, so the
// textarea never refuses a newline or trims a paste.
const (
	minInputCap     = 3
	maxInputContent = 9000
)

// inputMaxRows is how tall the command line grows before it scrolls: a
// third of the screen.
func (m Model) inputMaxRows() int {
	h := m.height
	if h <= 0 {
		h = 40
	}
	return max(minInputCap, h/3)
}

// fitInput sizes the command line to its content: one row for a short
// line, more as it wraps (by display width, at word boundaries) or gains
// newlines, up to inputMaxRows, after which the textarea scrolls and keeps
// the cursor in view. Growing scrolls back up, so the first row stays.
func (m *Model) fitInput() {
	w := m.width
	if w <= 0 {
		w = 120
	}
	m.input.MaxHeight = m.inputMaxRows()
	m.input.SetWidth(max(10, w-2)) // recalculates the height
}

// clear empties the command line after a send and lets go of the images.
func (m *Model) clear() tea.Cmd {
	m.input.Reset()
	m.complIdx = 0
	gone := m.attachments
	m.attachments = nil
	m.fitInput()
	if m.kitty {
		return forgetThumbs(gone)
	}
	return nil
}

// submit sends the command line's text.
func (m Model) submit() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return m, nil
	}
	if strings.HasPrefix(text, "/") {
		cleared := m.clear()
		mm, cmd := m.command(text)
		return mm, tea.Batch(cleared, cmd)
	}
	// Chips become "[Image #1: /path]" so the receiver can open the file.
	text = m.expandChips(text)
	mt := resolveMention(text, m.agents)
	switch {
	case mt.target == nil && len(mt.ambiguous) > 0:
		return m.say(fmt.Sprintf("Which one: %s?", strings.Join(mt.ambiguous, ", ")), true)
	case mt.target == nil && (mt.missing != "" || strings.HasPrefix(text, "@")):
		return m.say(fmt.Sprintf("No agent called %q", mt.missing), true)
	case mt.target == nil && m.tab == tabOverview:
		cleared := m.clear()
		return m, tea.Batch(cleared, m.missionText(mt.body))
	case mt.target == nil:
		cleared := m.clear()
		return m, tea.Batch(cleared, m.askOrchestrator(mt.body))
	case mt.body == "":
		return m.say("Add the message after the name.", true)
	}
	target := *mt.target
	if target.Status == fleet.Gone {
		return m.say(target.Name+" is gone.", true)
	}
	cleared := m.clear()
	if m.atTerminal(target.Session.SessionID) {
		it := m.store.Sent(target.Session.SessionID, mt.body, "waiting: you are at that terminal")
		m.queue = append(m.queue, outgoing{agent: target, text: mt.body, item: it, since: time.Now()})
		m.refresh()
		return m, tea.Batch(cleared, holdTick())
	}
	it := m.store.Sent(target.Session.SessionID, mt.body, "sending…")
	m.refresh()
	return m, tea.Batch(cleared, deliver(target, mt.body, it))
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
