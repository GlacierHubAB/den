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
	"github.com/GlacierHubAB/den/internal/remote"
)

// outgoing is a message waiting for the typing guard to let it through.
type outgoing struct {
	agent fleet.Agent
	text  string
	item  *inbox.Item
	since time.Time
	// review holds the ids of the review comments the message carries.
	review []string
}

type sentMsg struct {
	item   *inbox.Item
	name   string
	err    error
	review []string // review comments delivered, or not, with it
}

// sendPeer posts a message to a session's socket; tests replace it.
var sendPeer = peer.Send

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
	// The line after /god on is the phrase or nothing: it goes nowhere else.
	if m.godPhraseTyped(text) {
		cleared := m.clear()
		return m, tea.Batch(cleared, m.godSweep())
	}
	if strings.HasPrefix(text, "/") {
		cleared := m.clear()
		mm, cmd := m.command(text)
		next := mm.(Model)
		return next, tea.Batch(cleared, cmd, next.godSweep())
	}
	// Chips become "[Image #1: /path]" so the receiver can open the file.
	text = m.expandChips(text)
	mt := resolveMention(text, m.agents)
	switch {
	case mt.target == nil && len(mt.ambiguous) > 0:
		return m.say(fmt.Sprintf("Which one: %s?", strings.Join(mt.ambiguous, ", ")), true)
	case mt.target == nil && (mt.missing != "" || strings.HasPrefix(text, "@")):
		return m.say(fmt.Sprintf("No agent called %q", mt.missing), true)
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
	if target.Host != "" {
		// Checked up front, so a refusal costs no message in the inbox.
		if err := m.remoteCan(target.Host); err != nil {
			return m.say(err.Error(), true)
		}
		if len(mt.body) > remote.MaxSendText {
			return m.say(fmt.Sprintf("That message is over %d KB, the most Den sends to a remote agent.", remote.MaxSendText>>10), true)
		}
	}
	cleared := m.clear()
	if m.atTerminal(target.Session.SessionID) {
		it := m.sentItem(target, mt.body, "waiting: you are at that terminal")
		m.queue = append(m.queue, outgoing{agent: target, text: mt.body, item: it, since: time.Now()})
		m.refresh()
		return m, tea.Batch(cleared, holdTick())
	}
	it := m.sentItem(target, mt.body, "sending…")
	m.refresh()
	return m, tea.Batch(cleared, deliver(m.sender(), target, mt.body, it))
}

// sentItem records an outgoing message in the inbox, on the agent's host
// when it is a remote one.
func (m Model) sentItem(a fleet.Agent, text, status string) *inbox.Item {
	sid, host := splitKey(a.Session.SessionID)
	return m.store.SentOn(host, sid, text, status)
}

// sendFunc posts a message to an agent.
type sendFunc func(a fleet.Agent, text string) error

// sender is how this Model posts to an agent: into its session socket on
// this machine, through the host's live session for a remote one. The text
// goes as data both ways, never as an argument.
func (m Model) sender() sendFunc {
	feed := m.remote.feed
	return func(a fleet.Agent, text string) error {
		if a.Host == "" {
			return sendPeer(a.Session.Socket, text)
		}
		if feed == nil {
			return fmt.Errorf("%s is offline; nothing was sent", a.Host)
		}
		sid, host := splitKey(a.Session.SessionID)
		return feed.Send(host, sid, text)
	}
}

// say shows a short message on the top bar without leaving compose mode.
func (m Model) say(text string, isErr bool) (tea.Model, tea.Cmd) {
	m.flash, m.flashErr = text, isErr
	return m, flashTick(4*time.Second, m.flash)
}

func deliver(send sendFunc, a fleet.Agent, text string, it *inbox.Item) tea.Cmd {
	return func() tea.Msg {
		err := send(a, text)
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
		if o.review != nil {
			cmds = append(cmds, deliverReview(m.sender(), o.agent, o.text, o.item, o.review))
			continue
		}
		cmds = append(cmds, deliver(m.sender(), o.agent, o.text, o.item))
	}
	m.queue = keep
	return cmds, len(keep) > 0
}

func (m *Model) applySent(msg sentMsg) {
	m.applyReviewSent(msg)
	if msg.err != nil {
		m.store.SetStatus(msg.item, "failed: "+msg.err.Error(), true)
		m.flash, m.flashErr = "Not delivered to "+msg.name+": "+msg.err.Error(), true
		if msg.review != nil {
			m.flash += "; the comments stay drafts"
		}
		return
	}
	m.store.SetStatus(msg.item, "delivered", true)
	m.flash, m.flashErr = "Delivered to "+msg.name, false
	if msg.review != nil {
		m.flash = fmt.Sprintf("Delivered %s to %s", commentCount(len(msg.review)), msg.name)
	}
}
