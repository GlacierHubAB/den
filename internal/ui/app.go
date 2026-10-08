// Package ui is Den's bubbletea application.
package ui

import (
	"path/filepath"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/fsnotify/fsnotify"

	"github.com/GlacierHubAB/den/internal/badge"
	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/critter"
	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/hooks"
	"github.com/GlacierHubAB/den/internal/hypr"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/theme"
)

// pane is which half of the Fleet view has the arrow keys.
type pane int

const (
	paneAgents pane = iota
	paneDetail
)

type tab int

const (
	tabFleet tab = iota
	tabInbox
	tabOrch
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
	colors     map[string]string // session id → body colour, unique while hues last

	input    textinput.Model
	complIdx int
	queue    []outgoing
	orch     orchState
	anim     int

	focus        pane // Fleet view: agents or detail
	detailScroll int  // lines scrolled in the detail pane
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
	m.input.Focus()
	return m
}

// Close releases the socket and stops the orchestrator. Call it after the
// program exits.
func (m Model) Close() {
	if m.srv != nil {
		m.srv.Close()
	}
	m.closeOrchestrator()
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{snapshot, tick(), waitChange(m.watch), animTick()}
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

// assignColors gives every agent a body colour: its own seed picks the first
// choice, and the oldest sessions win collisions so colours stay stable as
// agents come and go.
func (m *Model) assignColors() {
	hues := m.st.pal.Bodies
	if len(hues) == 0 {
		return
	}
	order := make([]fleet.Agent, len(m.agents))
	copy(order, m.agents)
	sort.SliceStable(order, func(i, j int) bool { return order[i].Session.StartedAt < order[j].Session.StartedAt })
	taken := map[int]bool{}
	m.colors = map[string]string{}
	for _, a := range order {
		idx := int(critter.Hash(a.Session.SessionID) % uint32(len(hues)))
		if len(taken) < len(hues) {
			for taken[idx] {
				idx = (idx + 1) % len(hues)
			}
		}
		taken[idx] = true
		m.colors[a.Session.SessionID] = hues[idx]
	}
}

// agentColor is the colour that stands for this agent everywhere in the UI.
func (m Model) agentColor(a fleet.Agent) string {
	if c, ok := m.colors[a.Session.SessionID]; ok {
		return c
	}
	hues := m.st.pal.Bodies
	if len(hues) == 0 {
		return m.st.pal.Bright
	}
	return hues[int(critter.Hash(a.Session.SessionID)%uint32(len(hues)))]
}

// refresh recomputes the displayed agents from base plus inbox state, and
// keeps the cursor on the same agent.
func (m *Model) refresh() {
	var keep string
	if a, ok := m.selected(); ok {
		keep = a.Session.SessionID
	}
	m.agents = m.agents[:0]
	for _, a := range m.base {
		if m.isOrchestrator(a) {
			continue
		}
		m.agents = append(m.agents, a)
	}
	for i := range m.agents {
		a := &m.agents[i]
		if a.Status != fleet.Gone && m.store.SessionNeedsYou(a.Session.SessionID) {
			a.Status = fleet.NeedsYou
		}
	}
	m.assignColors()
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
	case animTickMsg:
		m.anim++
		return m, animTick()
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
	case orchStartedMsg:
		return m, m.handleOrchStarted(msg)
	case orchEventMsg:
		cmd := m.handleOrchEvent(msg.ev)
		m.refresh()
		return m, cmd
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
		return m.handleKey(msg)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// handleRequest folds a hook event into the inbox and decides whether a
// permission request is held for Den or released to the terminal.
func (m *Model) handleRequest(r *bridge.Request) tea.Cmd {
	if m.orch.proc != nil && r.SessionID != "" && r.SessionID == m.orch.proc.SessionID {
		// The orchestrator's turns show on their own tab, not in the inbox.
		if r.WantsReply() {
			r.Reply(bridge.Decision{Behavior: "deny", Message: "den: the orchestrator may only list and message sessions"})
		}
		return nil
	}
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

func (m Model) inboxActive() bool { return m.tab == tabInbox }

// handleKey routes every key. The command line is always open, so letters
// and most punctuation go to it; navigation uses arrows and Tab, answers use
// Ctrl keys or slash commands, and Enter sends or, when the line is empty,
// focuses the selected agent's terminal.
func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	m.help = false
	compl := completions(m.input.Value(), m.agents)
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.input.SetValue("")
		m.complIdx = 0
		return m, nil
	case "tab":
		if len(compl) > 0 {
			m.input.SetValue("@" + compl[m.complIdx%len(compl)].Name + " ")
			m.input.CursorEnd()
			m.complIdx = 0
			return m, nil
		}
		m.tab = (m.tab + 1) % 3
		return m, nil
	case "shift+tab":
		m.tab = (m.tab + 2) % 3
		return m, nil
	case "left":
		if m.tab == tabFleet {
			m.focus = paneAgents
		}
		return m, nil
	case "right":
		if m.tab == tabFleet {
			m.focus = paneDetail
		}
		return m, nil
	case "down":
		if len(compl) > 0 {
			m.complIdx = (m.complIdx + 1) % len(compl)
			return m, nil
		}
		m.move(1)
		return m, nil
	case "up":
		if len(compl) > 0 {
			m.complIdx = (m.complIdx + len(compl) - 1) % len(compl)
			return m, nil
		}
		m.move(-1)
		return m, nil
	case "pgdown":
		m.move(5)
		return m, nil
	case "pgup":
		m.move(-5)
		return m, nil
	case "ctrl+y":
		return m.answer("allow")
	case "ctrl+n":
		return m.answer("deny")
	case "enter":
		if strings.TrimSpace(m.input.Value()) == "" {
			return m.openSelected()
		}
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

// move shifts the selection of whatever the active view lists.
func (m *Model) move(delta int) {
	switch m.tab {
	case tabFleet:
		if m.focus == paneDetail {
			m.detailScroll = max(0, m.detailScroll+delta)
			return
		}
		if next := clamp(m.cursor+delta, 0, max(0, len(m.agents)-1)); next != m.cursor {
			m.cursor = next
			m.detailScroll = 0
		}
	case tabInbox:
		m.inboxCur = clamp(m.inboxCur+delta, 0, max(0, len(m.store.Items())-1))
	case tabOrch:
		m.orch.scroll = max(0, m.orch.scroll-delta)
	}
}

// openSelected focuses the terminal of the selected agent or inbox item.
func (m Model) openSelected() (tea.Model, tea.Cmd) {
	var target fleet.Agent
	var ok bool
	switch m.tab {
	case tabInbox:
		if it := m.inboxSelected(); it != nil {
			target, ok = m.agentFor(it.SessionID)
		}
	case tabFleet:
		target, ok = m.selected()
	}
	if !ok {
		return m, nil
	}
	return m, func() tea.Msg {
		note, err := fleet.Focus(target)
		return focusedMsg{note: note, err: err}
	}
}

// answer applies allow, deny or always to the selected inbox item, or to the
// top waiting one from any other view.
func (m Model) answer(verb string) (tea.Model, tea.Cmd) {
	it := m.topAnswerable()
	if m.tab == tabInbox {
		it = m.inboxSelected()
	}
	if it == nil || !it.Answerable() {
		return m.say("Nothing is waiting for an answer.", true)
	}
	m.store.Answer(it, verb)
	m.refresh()
	return m.say(verbPast(verb)+" for "+m.nameFor(it.SessionID), false)
}

// command runs a slash command typed on the command line.
func (m Model) command(text string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(text)
	switch strings.ToLower(fields[0]) {
	case "/allow", "/y", "/yes":
		return m.answer("allow")
	case "/deny", "/n", "/no":
		return m.answer("deny")
	case "/always", "/a":
		return m.answer("always")
	case "/open", "/o", "/focus":
		return m.openSelected()
	case "/dismiss", "/x":
		if it := m.inboxSelected(); it != nil && m.tab == tabInbox {
			m.store.Dismiss(it)
			m.refresh()
			return m, nil
		}
		return m.say("Select an inbox item first.", true)
	case "/rescan", "/r":
		return m, snapshot
	case "/fleet":
		m.tab = tabFleet
	case "/inbox":
		m.tab = tabInbox
	case "/orchestrator", "/orch":
		m.tab = tabOrch
	case "/help", "/?":
		m.help = true
	case "/quit", "/q", "/exit":
		return m, tea.Quit
	default:
		return m.say("Unknown command "+fields[0]+". Try /help.", true)
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
