// Package ui is Den's bubbletea application.
package ui

import (
	"path/filepath"
	"sort"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/fsnotify/fsnotify"

	"github.com/GlacierHubAB/den/internal/badge"
	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/hooks"
	"github.com/GlacierHubAB/den/internal/hypr"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/theme"
)

type pane int

const (
	paneAgents pane = iota
	paneInbox
	paneDetail
)

type tab int

const (
	tabFleet tab = iota
	tabInbox
)

type tickMsg time.Time
type holdTickMsg time.Time
type changedMsg struct{}
type requestMsg struct{ req *bridge.Request }
type serverClosedMsg struct{}
type snapshotMsg struct {
	agents []fleet.Agent
	err    error
}
type focusedMsg struct {
	note string
	err  error
}
type flashClearMsg struct{}

// Model is the root bubbletea model.
type Model struct {
	st       styles
	base     []fleet.Agent // as discovered, before inbox state is applied
	agents   []fleet.Agent // base with "needs you" applied and re-sorted
	cursor   int
	focus    pane
	tab      tab
	width    int
	height   int
	err      error
	flash    string
	flashErr bool
	help     bool
	watch    <-chan struct{}
	lastScan time.Time

	srv        *bridge.Server
	srvErr     error
	store      *inbox.Store
	inboxCur   int
	hooksReady bool

	input    textinput.Model
	complIdx int
	queue    []outgoing
}

// New returns the root model and starts listening for hook events.
func New() Model {
	m := Model{
		st:    newStyles(theme.Load()),
		watch: watchDirs(discover.Dir(), badge.StoreDir(), theme.ColorsFile()),
		store: inbox.New(),
	}
	m.srv, m.srvErr = bridge.Listen(bridge.SocketPath())
	m.hooksReady = hooks.Installed("")
	m.input = newInput(m.st)
	return m
}

// Close releases the socket. Call it after the program exits.
func (m Model) Close() {
	if m.srv != nil {
		m.srv.Close()
	}
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{snapshot, tick(), waitChange(m.watch)}
	if m.srv != nil {
		cmds = append(cmds, waitRequest(m.srv))
	}
	return tea.Batch(cmds...)
}

func tick() tea.Cmd {
	return tea.Tick(5*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func holdTick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return holdTickMsg(t) })
}

func snapshot() tea.Msg {
	agents, err := fleet.Snapshot()
	return snapshotMsg{agents: agents, err: err}
}

func waitRequest(srv *bridge.Server) tea.Cmd {
	return func() tea.Msg {
		r, ok := <-srv.Requests()
		if !ok {
			return serverClosedMsg{}
		}
		return requestMsg{req: r}
	}
}

func waitChange(ch <-chan struct{}) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		<-ch
		return changedMsg{}
	}
}

// watchDirs emits one signal per burst of filesystem changes in the given
// paths (directories are watched as a whole). Returns nil when watching is
// unavailable; the periodic tick still refreshes then.
func watchDirs(paths ...string) <-chan struct{} {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil
	}
	added := 0
	for _, p := range paths {
		dir := p
		if filepath.Ext(p) != "" {
			dir = filepath.Dir(p)
		}
		if w.Add(dir) == nil {
			added++
		}
	}
	if added == 0 {
		w.Close()
		return nil
	}
	out := make(chan struct{}, 1)
	go func() {
		var timer *time.Timer
		for {
			select {
			case _, ok := <-w.Events:
				if !ok {
					return
				}
				if timer != nil {
					timer.Stop()
				}
				timer = time.AfterFunc(250*time.Millisecond, func() {
					select {
					case out <- struct{}{}:
					default:
					}
				})
			case <-w.Errors:
			}
		}
	}()
	return out
}

func (m Model) selected() (fleet.Agent, bool) {
	if m.cursor < 0 || m.cursor >= len(m.agents) {
		return fleet.Agent{}, false
	}
	return m.agents[m.cursor], true
}

func (m Model) agentFor(sessionID string) (fleet.Agent, bool) {
	for _, a := range m.agents {
		if a.Session.SessionID == sessionID {
			return a, true
		}
	}
	return fleet.Agent{}, false
}

// refresh recomputes the displayed agents from base plus inbox state, and
// keeps the cursor on the same agent.
func (m *Model) refresh() {
	var keep string
	if a, ok := m.selected(); ok {
		keep = a.Session.SessionID
	}
	m.agents = append([]fleet.Agent(nil), m.base...)
	for i := range m.agents {
		a := &m.agents[i]
		if a.Status != fleet.Gone && m.store.SessionNeedsYou(a.Session.SessionID) {
			a.Status = fleet.NeedsYou
		}
	}
	sort.SliceStable(m.agents, func(i, j int) bool {
		if m.agents[i].Status != m.agents[j].Status {
			return m.agents[i].Status < m.agents[j].Status
		}
		return m.agents[i].Session.StartedAt < m.agents[j].Session.StartedAt
	})
	m.cursor = 0
	for i, a := range m.agents {
		if a.Session.SessionID == keep {
			m.cursor = i
		}
	}
	if n := len(m.store.Items()); m.inboxCur >= n {
		m.inboxCur = max(0, n-1)
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tickMsg:
		m.store.Prune(2*time.Minute, 30)
		m.refresh()
		return m, tea.Batch(snapshot, tick())
	case changedMsg:
		m.st = newStyles(theme.Load())
		applyInputStyles(&m.input, m.st)
		return m, tea.Batch(snapshot, waitChange(m.watch))
	case snapshotMsg:
		m.err = msg.err
		if msg.err == nil {
			m.base = msg.agents
			m.lastScan = time.Now()
			m.refresh()
		}
	case requestMsg:
		cmd := m.handleRequest(msg.req)
		return m, tea.Batch(cmd, waitRequest(m.srv))
	case holdTickMsg:
		held := m.releaseIfAtTerminal()
		cmds, queued := m.flushQueue()
		if held || queued {
			cmds = append(cmds, holdTick())
		}
		return m, tea.Batch(cmds...)
	case sentMsg:
		m.applySent(msg)
		m.refresh()
		return m, tea.Tick(4*time.Second, func(time.Time) tea.Msg { return flashClearMsg{} })
	case serverClosedMsg:
		m.srv = nil
	case focusedMsg:
		if msg.err != nil {
			m.flash, m.flashErr = msg.err.Error(), true
		} else {
			m.flash, m.flashErr = "Focused "+msg.note, false
		}
		return m, tea.Tick(3*time.Second, func(time.Time) tea.Msg { return flashClearMsg{} })
	case flashClearMsg:
		m.flash = ""
	case tea.KeyPressMsg:
		if m.input.Focused() {
			return m.handleComposeKey(msg)
		}
		return m.handleKey(msg)
	}
	if m.input.Focused() {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

// handleRequest folds a hook event into the inbox and decides whether a
// permission request is held for Den or released to the terminal.
func (m *Model) handleRequest(r *bridge.Request) tea.Cmd {
	it := m.store.Apply(r)
	var cmds []tea.Cmd
	switch r.Event {
	case "SessionStart", "SessionEnd":
		cmds = append(cmds, snapshot)
	}
	if it != nil && it.Answerable() {
		if m.atTerminal(it.SessionID) {
			m.store.Release(it, "you were at that terminal")
		} else {
			cmds = append(cmds, holdTick())
		}
	}
	if r.WantsReply() && it == nil {
		// Nothing holds it (auto-allowed or rejected above); make sure the
		// hook is never left hanging.
		r.Reply(bridge.Decision{})
	}
	m.refresh()
	return tea.Batch(cmds...)
}

// atTerminal reports whether the session's terminal window is the focused
// window right now, in which case the terminal dialog is the better UI.
func (m Model) atTerminal(sessionID string) bool {
	a, ok := m.agentFor(sessionID)
	if !ok || a.Window == nil || !hypr.Available() {
		return false
	}
	return hypr.ActiveAddress() == a.Window.Client.Address
}

// releaseIfAtTerminal releases held permissions whose terminal became the
// active window. It returns true while anything is still held.
func (m *Model) releaseIfAtTerminal() bool {
	held := false
	for _, it := range m.store.Items() {
		if !it.Answerable() {
			continue
		}
		if m.atTerminal(it.SessionID) {
			m.store.Release(it, "you went to that terminal")
			continue
		}
		held = true
	}
	m.refresh()
	return held
}

// topAnswerable is the item y/n/a act on from the agents pane.
func (m Model) topAnswerable() *inbox.Item {
	for _, it := range m.store.Items() {
		if it.Answerable() {
			return it
		}
	}
	return nil
}

func (m Model) inboxSelected() *inbox.Item {
	items := m.store.Items()
	if m.inboxCur < 0 || m.inboxCur >= len(items) {
		return nil
	}
	return items[m.inboxCur]
}

func (m Model) inboxActive() bool {
	return m.tab == tabInbox || m.focus == paneInbox
}

func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.help {
		m.help = false
		return m, nil
	}
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "?":
		m.help = true
	case "1":
		m.tab = tabFleet
	case "2":
		m.tab = tabInbox
	case "j", "down":
		if m.inboxActive() {
			if m.inboxCur < len(m.store.Items())-1 {
				m.inboxCur++
			}
		} else if m.cursor < len(m.agents)-1 {
			m.cursor++
		}
	case "k", "up":
		if m.inboxActive() {
			if m.inboxCur > 0 {
				m.inboxCur--
			}
		} else if m.cursor > 0 {
			m.cursor--
		}
	case "g", "home":
		if m.inboxActive() {
			m.inboxCur = 0
		} else {
			m.cursor = 0
		}
	case "G", "end":
		if m.inboxActive() {
			m.inboxCur = max(0, len(m.store.Items())-1)
		} else {
			m.cursor = max(0, len(m.agents)-1)
		}
	case "tab":
		m.focus = (m.focus + 1) % 3
	case "shift+tab":
		m.focus = (m.focus + 2) % 3
	case "r":
		return m, snapshot
	case "@", "i", ":":
		return m, m.startCompose("@")
	case "m":
		if a, ok := m.selected(); ok {
			return m, m.startCompose("@" + a.Name + " ")
		}
		return m, m.startCompose("@")
	case "y", "n", "a":
		it := m.topAnswerable()
		if m.inboxActive() {
			it = m.inboxSelected()
		}
		if it != nil && it.Answerable() {
			verb := map[string]string{"y": "allow", "n": "deny", "a": "always"}[msg.String()]
			m.store.Answer(it, verb)
			m.flash, m.flashErr = verbPast(verb)+" for "+m.nameFor(it.SessionID), false
			m.refresh()
			return m, tea.Tick(3*time.Second, func(time.Time) tea.Msg { return flashClearMsg{} })
		}
	case "x":
		if it := m.inboxSelected(); it != nil && m.inboxActive() {
			m.store.Dismiss(it)
			m.refresh()
		}
	case "enter", "o":
		var target fleet.Agent
		var ok bool
		if m.inboxActive() {
			if it := m.inboxSelected(); it != nil {
				target, ok = m.agentFor(it.SessionID)
			}
		} else {
			target, ok = m.selected()
		}
		if ok {
			return m, func() tea.Msg {
				note, err := fleet.Focus(target)
				return focusedMsg{note: note, err: err}
			}
		}
	}
	return m, nil
}

func (m Model) nameFor(sessionID string) string {
	if a, ok := m.agentFor(sessionID); ok {
		return a.Name
	}
	if len(sessionID) > 8 {
		return sessionID[:8]
	}
	return sessionID
}

func verbPast(v string) string {
	switch v {
	case "allow":
		return "Allowed"
	case "deny":
		return "Denied"
	case "always":
		return "Allowed, and again this session,"
	}
	return v
}
