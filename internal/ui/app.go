// Package ui is Den's bubbletea application.
package ui

import (
	"fmt"
	"maps"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/fsnotify/fsnotify"

	"github.com/GlacierHubAB/den/internal/badge"
	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/closer"
	"github.com/GlacierHubAB/den/internal/colors"
	"github.com/GlacierHubAB/den/internal/config"
	"github.com/GlacierHubAB/den/internal/cost"
	"github.com/GlacierHubAB/den/internal/critter"
	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/hooks"
	"github.com/GlacierHubAB/den/internal/hypr"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/mission"
	"github.com/GlacierHubAB/den/internal/orch"
	"github.com/GlacierHubAB/den/internal/remote"
	"github.com/GlacierHubAB/den/internal/review"
	"github.com/GlacierHubAB/den/internal/sanitize"
	"github.com/GlacierHubAB/den/internal/skills"
	"github.com/GlacierHubAB/den/internal/theme"
	"github.com/GlacierHubAB/den/internal/tree"
	"github.com/GlacierHubAB/den/internal/usage"
)

// pane is which half of the Fleet view has the arrow keys.
type pane int

const (
	paneAgents pane = iota
	paneDetail
)

type tab int

const (
	tabOverview tab = iota
	tabFleet
	tabInbox
	tabOrch
	tabReview
	tabCount // number of tabs, for cycling
)

// cycleTab moves through the views in the tab row. The Orchestrator view
// is not in the row: Ctrl+O opens and closes it, and sending plain text
// opens it too.
func (m *Model) cycleTab(delta int) {
	next := m.tab
	for {
		next = (next + tab(delta) + tabCount) % tabCount
		if next != tabOrch {
			break
		}
	}
	m.tab = next
}

// toggleOrchestrator opens the Orchestrator view, or returns to the view
// it was opened from.
func (m *Model) toggleOrchestrator() {
	if m.tab == tabOrch {
		m.tab = m.orch.prevTab
		return
	}
	m.orch.prevTab = m.tab
	m.tab = tabOrch
}

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

// flashClearMsg ends the flash it was set for, if that flash still shows:
// an older flash's timer must not take a newer one away.
type flashClearMsg struct{ text string }

// flashTick clears text from the top bar after d, unless another flash
// took its place.
func flashTick(d time.Duration, text string) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return flashClearMsg{text: text} })
}

// Model is the root bubbletea model.
type Model struct {
	// peers is the bookkeeping of messages other sessions send the
	// orchestrator (inbound.go); missionUpdates are the updates waiting on
	// the human's ^Y, by inbox item (mission_update.go).
	peers          peerState
	missionUpdates map[string]mission.Update
	// use is the usage guard (usage.go).
	use usageGuard
	// ments are the agents names in rendered text are coloured for
	// (mention_color.go), rebuilt with the fleet.
	ments *mentionSet
	// quietMentions leaves mentions uncoloured: text a host or an agent
	// report wrote, where a name in someone's colour would vouch for it.
	quietMentions bool
	// hl is the code colouring this frame may still do, set by View;
	// plainCode draws fenced code uncoloured (remote agents' text).
	hl        *hlBudget
	plainCode bool
	st        styles
	base      []fleet.Agent // as discovered, before inbox state is applied
	agents    []fleet.Agent // base with "needs you" applied and re-sorted
	cursor    int
	tab       tab
	width     int
	height    int
	err       error
	flash     string
	flashErr  bool
	help      bool
	costs     costState // cost figures kept by the tick
	watch     <-chan struct{}
	lastScan  time.Time

	srv        *bridge.Server
	closing    map[string]closer.Plan     // close questions in the inbox, by item id
	offers     map[string]skills.Approval // skills offers in the inbox, by item id
	installs   map[string]remote.Plan     // remote install questions in the inbox, by item id
	sends      map[string]askedSend       // den send questions in the inbox, by item id
	wt         map[string]wtOffer         // merge and cleanup questions in the inbox, by item id
	sel        selState                   // Den's own text selection on the Orchestrator and Overview views
	guard      *closer.Guard              // agents a close must leave alone or only propose when named
	guardPath  string
	srvErr     error
	store      *inbox.Store
	inboxCur   int
	inboxSel   string          // id of the selected inbox item: the selection follows the item, never its place in the list
	inboxLost  bool            // the selected item left the list: nothing is selected until the human picks one
	inboxAt    time.Time       // when the human last chose an item: requests after it are new to them
	shown      shownTarget     // what ^Y and ^N act on now, and since when
	bodyOff    int             // rows the selected inbox item is scrolled, when it is taller than the pane
	bodyRead   map[string]bool // items whose whole body was scrolled through (inbox_body.go)
	hooksReady bool
	cfg        config.Config
	chatLog    string // the orchestrator log on disk
	// Images attached to the command line, and whether the terminal can
	// draw them inline (kitty graphics, confirmed by a probe reply).
	attachments []attachment
	nextImage   int
	kitty       bool
	colors      map[string]string // session id → body colour, unique while hues last
	colorStamp  colors.Stamp      // the record on disk as Den last wrote or read it

	input    textarea.Model
	complIdx int
	queue    []outgoing
	orch     orchState
	anim     int

	focus        pane      // Fleet view: agents or detail
	detailScroll int       // lines scrolled in the detail pane
	tree         treeState // who directs whom, and the map

	proj           projectState   // the projects and the pick
	ov             overviewState  // the Overview tab
	missions       *mission.Store // standing goals, by project
	turn           missionTurn    // which mission the orchestrator's turn serves
	missionDirSent bool           // the orchestrator has had the mission vocabulary

	rev reviewState // the Review view

	restore restoreState // the roster of agents seen live, and the questions to reopen them

	att attention // desktop notifications, reminders and stalls

	remote remoteState // agents on remote hosts (remote.go); zero without remotes.toml

	guide guidanceState // orchestrator guidance and its approval (guidance.go)

	api apiState     // den-api.sock (api.go); zero while the API is off
	ctl controlState // a paired app's control (control.go)
}

// New returns the root model and starts listening for hook events.
func New(cfg config.Config) Model {
	m := Model{
		cfg:   cfg,
		orch:  orchState{cardCur: -1},
		st:    newStyles(theme.Load()),
		watch: watchDirs(discover.Dir(), badge.StoreDir(), theme.ColorsFile()),
		store: inbox.New(),
		tree:  newTreeState(),
		att:   newAttention(),
	}
	m.tree.path = tree.Path()
	m.tree.edges = tree.Load(m.tree.path)
	m.missions = mission.Load(mission.Path())
	m.guard, m.guardPath = closer.LoadGuard(closer.GuardPath()), closer.GuardPath()
	m.loadProjectPick()
	fleet.Recent = cfg.Detail.Recent
	fleet.Prices = cost.NewTable(config.Prices())
	m.store.HistoryLimit(cfg.Inbox.History)
	m.store.ToolFailureLimit(cfg.Stuck.ToolFailures)
	m.store.AnswerQuestions(cfg.Hooks.AnswerQuestions)
	m.store.Restore(inbox.LoadHistory(inbox.HistoryPath(), cfg.Inbox.History))
	m.chatLog = ChatLogPath()
	m.use.path = usage.Path()
	m.use.st = usage.Load(m.use.path)
	m.orch.chat, m.orch.resume = loadChatLog(m.chatLog, maxChatLog)
	m.orch.reconcile = len(m.liveCards()) > 0
	m.backfillEdges()
	m.srv, m.srvErr = bridge.Listen(bridge.SocketPath())
	m.hooksReady = hooks.Installed("")
	m.rev.commentsPath = review.CommentsPath()
	m.rev.comments = review.LoadComments(m.rev.commentsPath)
	m.restore = newRestoreState()
	m.input = newInput(m.st)
	m.input.Focus()
	m.fitInput()
	m.openRemote(cfg)
	m.startGuidance(cfg.Orchestrator.Guidance)
	if cfg.API.Enabled {
		// Only the Den that holds den.sock serves the API too.
		if m.srv != nil {
			m.startAPI()
		} else {
			m.api.err = "another Den is running"
		}
	}
	return m
}

// Close releases the socket and stops the orchestrator. Call it after the
// program exits.
func (m Model) Close() {
	if m.srv != nil {
		m.srv.Close()
	}
	if m.remote.feed != nil {
		m.remote.feed.Close()
	}
	m.closeOrchestrator()
	m.closeAPI()
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{snapshot, tick(), waitChange(m.watch), animTick(), kittyProbe()}
	if m.srv != nil {
		cmds = append(cmds, waitRequest(m.srv))
	}
	if m.api.srv != nil {
		cmds = append(cmds, waitAPINote(m.api.srv), waitAPIAct(m.api.srv))
	}
	if m.remote.feed != nil {
		m.remote.pump.start(m.remote.feed)
		m.remote.feed.Start()
		cmds = append(cmds, waitRemote(m.remote.pump))
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

// assignColors gives every live agent a body colour through the shared
// record, so the badge in each terminal shows the same one. The record is
// read once; Den's copy is what it shows, so it is written when the fleet
// changes and whenever the file on disk is no longer the one Den wrote (a
// test run or a second Den replaced it), else every badge would fall back
// to its own pick while Den showed another colour.
func (m *Model) assignColors() {
	hues := m.st.pal.Bodies
	if len(hues) == 0 {
		return
	}
	path := colors.Path()
	if m.colors == nil {
		m.colors = colors.Load(path)
		m.colorStamp = colors.StampOf(path)
	}
	var live []colors.Agent
	for _, a := range m.agents {
		if a.Status == fleet.Gone {
			continue
		}
		live = append(live, colors.Agent{SessionID: a.Session.SessionID, StartedAt: a.Session.StartedAt})
	}
	next, changed := colors.Assign(m.colors, live, hues)
	if changed {
		m.colors = next
	}
	if changed || colors.StampOf(path) != m.colorStamp {
		if changed || !maps.Equal(colors.Load(path), next) {
			_ = colors.Save(path, next) // best effort: the UI works without the file
		}
		m.colorStamp = colors.StampOf(path)
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
	m.agents = append(m.agents, m.remote.agents...)
	m.ments = buildMentions(m.base, m.isOrchestrator)
	m.findTwins()
	for i := range m.agents {
		a := &m.agents[i]
		if sid, host := splitKey(a.Session.SessionID); a.Status != fleet.Gone && !a.Stale && m.store.NeedsYouOn(host, sid) {
			a.Status = fleet.NeedsYou
		}
	}
	m.assignColors()
	if m.orch.reconcile && len(m.base) > 0 {
		m.orch.reconcile = false
		m.reconcileCards()
	}
	m.settleCards()
	m.releaseAssignments(time.Now())
	sort.SliceStable(m.agents, func(i, j int) bool {
		if oi, oj := m.remoteOrder(m.agents[i]), m.remoteOrder(m.agents[j]); oi != oj {
			return oi < oj // a host's last known agents after everyone live, under its row
		}
		if m.agents[i].Status != m.agents[j].Status {
			return m.agents[i].Status < m.agents[j].Status
		}
		return m.agents[i].Session.StartedAt < m.agents[j].Session.StartedAt
	})
	m.buildTree()
	m.buildProjects()
	m.groupByProject()
	m.cursor = 0
	for i, a := range m.agents {
		if a.Session.SessionID == keep {
			m.cursor = i
		}
	}
	m.publishAPI()
	m.followSelection()
	if t := m.answerTarget(); t != nil && t.ID != m.shown.id {
		m.shown = shownTarget{id: t.ID, since: answerNow()}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.tab == tabInbox {
		m.store.MarkSeen() // whatever was on screen before this message counts as seen
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.fitInput()
		return m, nil
	case uv.KittyGraphicsEvent:
		if kittyReply(msg) {
			m.kitty = true
		}
		return m, nil
	case tea.PasteMsg:
		// Image paths (dragged files, copied paths) become chips; anything
		// else is text for the command line.
		if paths := imagePaths(msg.Content); len(paths) > 0 {
			var cmds []tea.Cmd
			for _, p := range paths {
				cmds = append(cmds, m.attach(p))
			}
			return m, tea.Batch(cmds...)
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.fitInput()
		return m, cmd
	case clipMsg:
		switch {
		case msg.err != nil:
			m.flash, m.flashErr = "clipboard: "+msg.err.Error(), true
			return m, flashTick(3*time.Second, m.flash)
		case msg.path != "":
			return m, m.attach(msg.path)
		case msg.text != "":
			m.input.InsertString(msg.text)
			m.fitInput()
		}
		return m, nil
	case animTickMsg:
		m.anim++
		return m, animTick()
	case tickMsg:
		m.store.Prune(2*time.Minute, 30)
		m.checkAPISocket()
		m.refresh()
		m.restartForGuidance()
		cmds := []tea.Cmd{snapshot, tick(), m.watchOrchestrator(), m.checkUsage(time.Now()), m.settleMissions(), m.checkStuck(time.Now()), m.hostNotesDue(time.Now()), m.pollGuidance(), m.controlTick(time.Now())}
		if m.tab == tabReview && time.Since(m.rev.loadedAt) > reviewReload {
			cmds = append(cmds, m.loadReview())
		}
		return m, tea.Batch(cmds...)
	case reviewLoadedMsg:
		m.applyReview(msg)
		return m, nil
	case reviewRemoteMsg:
		return m, m.applyRemoteReview(msg)
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
			m.afterScan()
		}
	case remoteMsg:
		return m, m.handleRemote(msg)
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
	case appCancelMsg:
		if y := m.ctl.turn; y != nil && y.Turn == msg.turn {
			if !m.ctl.sent {
				m.ctl.turn = nil
			} else if !m.ctl.ended {
				m.ctl.ended = true // the app was told "cancelled"
				if m.orch.proc != nil {
					m.orch.proc.Interrupt()
				}
			}
		}
		return m, nil
	case orchEventMsg:
		if msg.from != nil && msg.from != m.orch.proc {
			return m, nil // a process that was restarted away; its tail is noise
		}
		cmd := m.handleOrchEvent(msg.ev)
		m.restartForGuidance()
		m.refresh()
		return m, cmd
	case guidanceReadMsg:
		cmd := m.applyGuidance(msg)
		m.refresh()
		return m, cmd
	case apiActMsg:
		if msg.from != m.api.srv {
			return m, nil
		}
		cmd := m.applyAPIAct(msg.act)
		return m, tea.Batch(cmd, waitAPIAct(msg.from))
	case apiNoteMsg:
		if msg.from != m.api.srv {
			return m, nil
		}
		m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: sanitize.Text(msg.text), at: time.Now()}))
		return m, waitAPINote(msg.from)
	case cliSentMsg:
		m.applyCLISent(msg)
		m.refresh()
		return m, nil
	case sentMsg:
		m.applySent(msg)
		m.refresh()
		return m, flashTick(4*time.Second, m.flash)
	case closeDoneMsg:
		return m, m.finishClose(msg)
	case skillsDoneMsg:
		return m, m.finishSkills(msg)
	case installPlanMsg:
		m.offerInstall(msg)
		return m, nil
	case installDoneMsg:
		return m, m.finishInstall(msg)
	case askedSentMsg:
		return m, m.finishAskedSend(msg)
	case restoreDoneMsg:
		return m, m.finishRestore(msg)
	case serverClosedMsg:
		m.srv = nil
	case focusedMsg:
		if msg.err != nil {
			m.flash, m.flashErr = msg.err.Error(), true
		} else {
			m.flash, m.flashErr = "Focused "+msg.note, false
		}
		return m, flashTick(3*time.Second, m.flash)
	case flashClearMsg:
		if msg.text == m.flash {
			m.flash = ""
		}
	case tea.KeyPressMsg:
		m.api.lastKey = time.Now()
		return m.handleKey(msg)
	case tea.MouseClickMsg, tea.MouseMotionMsg, tea.MouseReleaseMsg, tea.MouseWheelMsg:
		if m.tab == tabReview {
			m.reviewMouse(msg)
			return m, nil
		}
		return m, m.copyMouse(msg)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.fitInput()
	return m, cmd
}

// handleRequest folds a hook event into the inbox and decides whether a
// permission request is held for Den or released to the terminal.
func (m *Model) handleRequest(r *bridge.Request) tea.Cmd {
	if r.Host != "" {
		// Never local: a remote host's request takes remoteHeld's path, so
		// it cannot reach the close, skills or install questions, the
		// orchestrator's rules, or the timers, cards and [on] commands
		// keyed by a bare session id. Anything but a permission request
		// is refused outright.
		if r.Event != "PermissionRequest" {
			r.Reply(bridge.Decision{Behavior: "deny", Message: "den: a remote host may only relay permission requests"})
			return nil
		}
		return m.remoteHeld(r.Host, r)
	}
	m.noteMode(r)
	if r.Event == bridge.CloseEvent {
		m.handleCloseRequest(r)
		return nil
	}
	if r.Event == bridge.SkillsEvent {
		m.handleSkillsRequest(r)
		return nil
	}
	if r.Event == bridge.InstallEvent {
		return m.handleInstallRequest(r)
	}
	if r.Event == bridge.SendEvent {
		return m.handleSendRequest(r)
	}
	if r.Event == bridge.InboundEvent || r.Event == bridge.MissionEvent || r.Event == bridge.OrchSendEvent {
		// Only the orchestrator's own hook and its `den mcp` (its child
		// processes) may ask these; any other process on the machine could
		// otherwise speak as an agent or move a mission.
		if m.orch.proc == nil || !m.orch.proc.Spawned(r.PeerPID) {
			r.Reply(bridge.Decision{Behavior: "deny", Message: "den: only the orchestrator's own hook and tools may ask this"})
			return nil
		}
		switch r.Event {
		case bridge.InboundEvent:
			return m.handleInbound(r)
		case bridge.OrchSendEvent:
			return m.handleOrchSend(r)
		}
		return m.handleMissionUpdate(r)
	}
	if r.Event == bridge.UsageEvent {
		if why := m.usageStopped(); why != "" {
			r.Reply(bridge.Decision{Behavior: "deny", Message: m.use.st.StopWhy})
		} else {
			r.Reply(bridge.Decision{Behavior: "allow"})
		}
		return nil
	}
	if m.orch.proc != nil && r.SessionID != "" && r.SessionID == m.orch.proc.SessionID {
		// The orchestrator's turns show on their own tab, not in the inbox.
		if r.WantsReply() {
			r.Reply(bridge.Decision{Behavior: "deny", Message: "den: the orchestrator may only list and message sessions"})
		}
		return nil
	}
	if r.Event == "UserPromptSubmit" {
		m.learnPeer(r.SessionID, r.Input.Prompt)
	}
	if r.Event == "PostToolUse" && r.Input.ToolName == orch.SpawnAgentTool {
		in, _ := orch.ParseSpawn(r.Input.ToolInput)
		m.learnSpawn(orch.ResultText(r.Input.ToolResponse), r.SessionID, "", in.Brief)
	}
	it := m.store.Apply(r)
	m.trackHook(r, it)
	if it != nil {
		if a, ok := m.agentFor(itemKey(it)); ok {
			it.Role, it.Task = a.Labels.Role, a.Labels.Task
		}
		if it.Kind == inbox.Done {
			_ = inbox.AppendHistory(inbox.HistoryPath(), it) // best effort
		}
	}
	cmds := []tea.Cmd{m.attend(r, it), m.onHook(r, it)}
	switch r.Event {
	case "SessionStart", "SessionEnd":
		cmds = append(cmds, snapshot)
	}
	if it != nil && it.Answerable() {
		if m.atTerminal(itemKey(it)) {
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
	if hostOf(sessionID) != "" {
		return false // a remote agent's terminal is never on this desktop
	}
	a, ok := m.agentFor(sessionID)
	if !ok || a.Window == nil || !hypr.Available() {
		return false
	}
	return hypr.ActiveAddress() == a.Window.Client.Address
}

// releaseIfAtTerminal releases held permissions whose terminal became the
// active window, and lets go of held questions whose hook stopped waiting.
// It returns true while anything is still held.
func (m *Model) releaseIfAtTerminal() bool {
	m.store.LetGo(m.permissionWait())
	held := false
	for _, it := range m.store.Items() {
		if !it.Answerable() {
			continue
		}
		if m.atTerminal(itemKey(it)) {
			m.store.Release(it, "you went to that terminal")
			continue
		}
		held = true
	}
	m.refresh()
	return held
}

// permissionWait is how long den hook holds a permission request.
func (m Model) permissionWait() time.Duration {
	return time.Duration(m.cfg.Hooks.PermissionWait) * time.Second
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

// inboxSelected is the selected inbox item, found by its id: when the list
// changes under the human (items arrive, resolve and reorder), ^Y and ^N
// still act on the item they selected, or on nothing.
func (m Model) inboxSelected() *inbox.Item {
	items := m.store.Items()
	if m.inboxSel != "" {
		for _, it := range items {
			if it.ID == m.inboxSel {
				return it
			}
		}
		return nil
	}
	if m.inboxCur < 0 || m.inboxCur >= len(items) {
		return nil
	}
	return items[m.inboxCur]
}

// selectInbox is the human selecting the item at i: it is in view and
// chosen, so ^Y acts on it at once.
func (m *Model) selectInbox(i int) {
	old := m.inboxSel
	m.inboxCur, m.inboxSel, m.inboxLost, m.inboxAt = i, "", false, time.Now()
	if items := m.store.Items(); i >= 0 && i < len(items) {
		if items[i].ID != old {
			m.bodyOff = 0
		}
		m.inboxSel = items[i].ID
		m.shown = shownTarget{id: m.inboxSel}
	}
}

// questionWaiting reports whether a question waits for its answer in Den.
func (m Model) questionWaiting() bool {
	for _, it := range m.store.Items() {
		if it.AsksInDen() {
			return true
		}
	}
	return false
}

// inboxWaiting counts the requests waiting on the human that ^Y and ^N do
// not act on: each must be selected before it can be answered.
func (m Model) inboxWaiting() int {
	bound, n := m.inboxSelected(), 0
	for _, it := range m.store.Items() {
		if it.Answerable() && it != bound {
			n++
		}
	}
	return n
}

// unboundNote is what the Inbox shows for a waiting request ^Y and ^N do
// not act on, and what a refused answer adds when one is waiting.
func (m Model) unboundNote() string {
	switch n := m.inboxWaiting(); n {
	case 0:
		return ""
	case 1:
		return "a request is waiting: select it to answer (↑ ↓)"
	default:
		return fmt.Sprint(n) + " requests are waiting: select one to answer (↑ ↓)"
	}
}

// selectItem selects it in the inbox.
func (m *Model) selectItem(it *inbox.Item) {
	for i, o := range m.store.Items() {
		if o == it {
			m.selectInbox(i)
		}
	}
}

// followSelection keeps the cursor on the selected item as the list
// changes. When that item leaves the list nothing is selected: the cursor
// never lands on another request by itself. With nothing selected yet, the
// highlighted row becomes the selection.
func (m *Model) followSelection() {
	items := m.store.Items()
	if m.inboxSel != "" {
		for i, it := range items {
			if it.ID == m.inboxSel {
				m.inboxCur = i
				return
			}
		}
		m.inboxSel, m.inboxCur, m.inboxLost = "", -1, true
		return
	}
	if m.inboxCur >= len(items) {
		m.inboxCur = max(0, len(items)-1)
	}
	if m.inboxCur >= 0 && m.inboxCur < len(items) {
		m.inboxSel = items[m.inboxCur].ID
	}
}

// refusedNote is a refused answer: what happened, then what is waiting.
func refusedNote(what, waiting string) string {
	if waiting == "" {
		return what
	}
	return what + " " + strings.ToUpper(waiting[:1]) + waiting[1:] + "."
}

// shownTarget is the item ^Y and ^N would act on, and when it became that
// item without the human choosing it (zero when they chose it).
type shownTarget struct {
	id    string
	since time.Time
}

// unsettled reports whether it only just became what ^Y, ^N and Enter act
// on without the human choosing it, or got there before any render showed
// it: that first key is held, and the next one acts.
func (m *Model) unsettled(it *inbox.Item) bool {
	if it.ID != m.shown.id {
		m.shown = shownTarget{id: it.ID, since: answerNow()}
	}
	if !m.shown.since.IsZero() && answerNow().Sub(m.shown.since) < answerSettle {
		m.shown.since = time.Time{}
		return true
	}
	return false
}

// answerSettle is how long an item must have been what ^Y acts on before
// ^Y acts on it, when it got there by itself (it arrived, or the one before
// it was answered elsewhere): a keypress meant for the item that was on
// screen never answers the one that just took its place. Tests set it to 0.
var answerSettle = 700 * time.Millisecond

// answerNow is the clock for answerSettle; tests pin it.
var answerNow = time.Now

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
		if m.sel.active {
			m.sel.active = false
			return m, nil
		}
		if m.remote.listing && strings.TrimSpace(m.input.Value()) == "" {
			m.remote.listing = false
			return m, nil
		}
		if m.tab == tabOverview && strings.TrimSpace(m.input.Value()) == "" {
			switch {
			case m.ov.listing:
				m.ov.listing = false
			case m.ov.proposal != "":
				m.ov.proposal = "" // the proposal stays in the list
			case m.ov.open != "":
				m.ov.open, m.ov.scroll = "", 0
			}
			return m, nil
		}
		if m.tab == tabReview {
			switch {
			case m.rev.editing != "":
				m.rev.editing = "" // drop the edit, keep the comment
			case m.rev.ranged && strings.TrimSpace(m.input.Value()) == "":
				m.rev.ranged = false
				return m, nil
			}
		}
		return m, m.clear()
	case "shift+down", "shift+up":
		if m.tab == tabReview && m.rev.focus == revDiff {
			d := 1
			if msg.String() == "shift+up" {
				d = -1
			}
			m.moveReviewSel(d, true)
			return m, nil
		}
	case "shift+enter", "alt+enter", "ctrl+j":
		m.input.InsertString("\n")
		m.fitInput()
		return m, nil
	case "ctrl+v":
		return m, readClipboard()
	case "tab":
		if len(compl) > 0 {
			m.input.SetValue(mentionText(compl[m.complIdx%len(compl)]) + " ")
			m.input.CursorEnd()
			m.complIdx = 0
			return m, nil
		}
		m.cycleTab(1)
		cmd := tea.Batch(m.dropEdit(), m.enteredTab())
		return m, cmd
	case "shift+tab":
		m.cycleTab(-1)
		cmd := tea.Batch(m.dropEdit(), m.enteredTab())
		return m, cmd
	case "ctrl+e":
		// On an open mission with an empty line: show or hide the full
		// text of its actions. Enter stays with the mission itself.
		if m.tab == tabOverview && strings.TrimSpace(m.input.Value()) == "" && m.toggleThreadActions() {
			return m, nil
		}
	case "ctrl+o":
		m.toggleOrchestrator()
		cmd := tea.Batch(m.dropEdit(), m.enteredTab())
		return m, cmd
	case "ctrl+x":
		return m.closeSelected(false)
	case "ctrl+t":
		m.tab = tabFleet
		m.focus = paneAgents
		m.tree.mapView = !m.tree.mapView
		return m, nil
	case "left":
		switch {
		case m.tab == tabOverview:
			m.switchProject(-1)
		case m.tab == tabFleet && m.orch.backToCard:
			m.orch.backToCard = false
			m.tab = tabOrch
		case m.tab == tabFleet && m.tree.mapView:
			m.mapMove(true)
		case m.tab == tabFleet && m.focus == paneAgents && m.toggleFold(true):
		case m.tab == tabFleet:
			m.focus = paneAgents
		case m.tab == tabReview:
			m.rev.focus = max(revWork, m.rev.focus-1)
		}
		return m, nil
	case "right":
		switch {
		case m.tab == tabOverview:
			m.switchProject(1)
		case m.tab == tabFleet && m.tree.mapView:
			m.mapMove(false)
		case m.tab == tabFleet && m.focus == paneAgents && m.toggleFold(false):
		case m.tab == tabFleet:
			m.focus = paneDetail
		case m.tab == tabReview:
			m.rev.focus = min(revDiff, m.rev.focus+1)
		case m.tab == tabOrch:
			if d := m.focusedCard(); d != nil {
				return m.openCardAgent(d)
			}
		}
		return m, nil
	case "down":
		if len(compl) > 0 {
			m.complIdx = (m.complIdx + 1) % len(compl)
			return m, nil
		}
		if m.input.LineCount() > 1 && m.input.Line() < m.input.LineCount()-1 {
			break // move within a multi-line prompt
		}
		m.move(1)
		return m, nil
	case "up":
		if len(compl) > 0 {
			m.complIdx = (m.complIdx + len(compl) - 1) % len(compl)
			return m, nil
		}
		if m.input.LineCount() > 1 && m.input.Line() > 0 {
			break
		}
		m.move(-1)
		return m, nil
	case "pgdown":
		if m.tab == tabOrch {
			m.orch.scroll = max(0, m.orch.scroll-5)
			return m, nil
		}
		if m.tab == tabReview {
			m.moveReview(max(5, m.height/2))
			return m, nil
		}
		m.move(5)
		return m, nil
	case "pgup":
		if m.tab == tabOrch {
			m.orch.scroll += 5
			return m, nil
		}
		if m.tab == tabReview {
			m.moveReview(-max(5, m.height/2))
			return m, nil
		}
		m.move(-5)
		return m, nil
	case "ctrl+y":
		if m.tab == tabOverview && m.ov.proposal != "" {
			if cmd := m.startMission(); cmd != nil {
				return m, cmd
			}
			return m.say("Nothing to start yet: the orchestrator has not proposed.", true)
		}
		return m.answer("allow")
	case "ctrl+n":
		if m.tab == tabOverview && m.ov.proposal != "" {
			m.dropMission()
			return m, nil
		}
		return m.answer("deny")
	case "enter":
		// An open edit always goes back to its comment, even if focus moved
		// off the diff meanwhile, so it can never be sent as a message.
		if m.tab == tabReview && (m.rev.focus == revDiff || m.rev.editing != "") && m.curFile() != nil {
			text := strings.TrimSpace(m.input.Value())
			switch {
			case text == "":
				if c := m.commentAtCursor(); c != nil {
					m.rev.editing = c.ID
					m.input.SetValue(c.Body)
					m.input.CursorEnd()
					m.fitInput()
					return m, nil
				}
			case !strings.HasPrefix(text, "/") && !strings.HasPrefix(text, "@"):
				if err := m.addComment(text); err != nil {
					return m.say("Comment not saved: "+err.Error(), true)
				}
				cmd := m.clear()
				return m, cmd
			}
		}
		if text := strings.TrimSpace(m.input.Value()); m.tab == tabInbox && text != "" && !strings.HasPrefix(text, "/") && !strings.HasPrefix(text, "@") {
			// Text typed while a question is shown answers the selected
			// question or nothing: it never goes on to the orchestrator.
			if it := m.inboxSelected(); it != nil && it.AsksInDen() {
				if m.unsettled(it) {
					return m.say("A new question just came up: "+m.nameFor(itemKey(it))+": "+it.Title+". Press Enter again to answer it.", true)
				}
				return m.answerQuestion(it, text)
			}
			if m.questionWaiting() {
				return m.say("Nothing was sent: the item you selected is not a waiting question. Select the question to answer it (↑ ↓), or start with @Name to message an agent.", true)
			}
		}
		if strings.TrimSpace(m.input.Value()) == "" {
			if m.tab == tabOverview {
				m.toggleMission()
				return m, nil
			}
			if m.tab == tabOrch && m.toggleAction() {
				return m, nil
			}
			if d := m.focusedCard(); d != nil && m.tab == tabOrch {
				d.Expanded = !d.Expanded
				if d.Expanded && !d.turnLoaded {
					m.loadTurn(d)
				}
				return m, nil
			}
			return m.openSelected()
		}
		if len(compl) > 0 && resolveMention(m.input.Value(), m.agents).target == nil {
			m.input.SetValue(mentionText(compl[m.complIdx%len(compl)]) + " ")
			m.input.CursorEnd()
			return m, nil
		}
		return m.submit()
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.fitInput()
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
		if next := m.stepVisible(delta); next != m.cursor {
			m.cursor = next
			m.detailScroll = 0
			m.orch.backToCard = false
		}
	case tabInbox:
		if m.scrollBody(delta) {
			return
		}
		m.selectInbox(clamp(m.inboxCur+delta, 0, max(0, len(m.store.Items())-1)))
	case tabOverview:
		if m.ov.open != "" || m.ov.proposal != "" {
			m.ov.scroll = max(0, m.ov.scroll-delta)
		} else {
			m.ov.cur = clamp(m.ov.cur+delta, 0, max(0, len(m.missions.ForProject(m.proj.cur))-1))
		}
	case tabOrch:
		m.moveCard(delta)
	case tabReview:
		m.moveReview(delta)
	}
}

// enteredTab runs what a view needs on arrival: Review reloads when its
// data is stale.
func (m *Model) enteredTab() tea.Cmd {
	if m.tab == tabReview && time.Since(m.rev.loadedAt) > 2*time.Second {
		return m.loadReview()
	}
	return nil
}

// openCardAgent jumps from a card to its agent in the Fleet view; ← comes
// back to the card.
func (m Model) openCardAgent(d *dispatch) (tea.Model, tea.Cmd) {
	for i, a := range m.agents {
		if a.Session.SessionID == d.SessionID {
			m.cursor, m.tab, m.focus, m.detailScroll = i, tabFleet, paneDetail, 0
			m.orch.backToCard = true
			return m, nil
		}
	}
	return m.say(d.Agent+" is not in the fleet any more.", true)
}

// openSelected focuses the terminal of the selected agent or inbox item.
func (m Model) openSelected() (tea.Model, tea.Cmd) {
	var target fleet.Agent
	var ok bool
	switch m.tab {
	case tabInbox:
		if it := m.inboxSelected(); it != nil {
			m.store.MarkRead(it)
			target, ok = m.agentFor(itemKey(it))
		}
	case tabFleet:
		target, ok = m.selected()
	case tabOrch:
		if d := m.focusedCard(); d != nil {
			target, ok = m.agentFor(d.SessionID)
		}
	case tabReview:
		if w := m.curWork(); w != nil {
			target, ok = m.agentFor(w.SessionID)
		}
	}
	if !ok {
		return m, nil
	}
	if target.Host != "" {
		// Den never types into or raises a remote terminal.
		return m.say(m.remoteHint(target), false)
	}
	return m, func() tea.Msg {
		note, err := fleet.Focus(target)
		return focusedMsg{note: note, err: err}
	}
}

// answerTarget is the item ^Y and ^N act on: the selected inbox item, the
// focused card's (or what it directs), or the top waiting one from any
// other view.
func (m Model) answerTarget() *inbox.Item {
	if m.tab == tabInbox {
		return m.inboxSelected()
	}
	it := m.topAnswerable()
	if d := m.focusedCard(); d != nil && m.tab == tabOrch {
		// The card's own session first, then whatever it directs, so the
		// "needs you · via <child>" state can be answered from here.
		it = nil
		for _, sid := range append([]string{d.SessionID}, m.descendants(d.SessionID)...) {
			for _, o := range m.store.Items() {
				if itemKey(o) == sid && o.Answerable() {
					it = o
					break
				}
			}
			if it != nil {
				break
			}
		}
	}
	return it
}

// answer applies allow, deny or always to the item answerTarget names, only
// while it is still open and has not just taken another's place.
func (m Model) answer(verb string) (tea.Model, tea.Cmd) {
	m.store.LetGo(m.permissionWait()) // a hook gone since the last tick takes no answer
	it := m.answerTarget()
	if m.tab == tabInbox {
		switch {
		case it == nil && m.inboxLost:
			return m.say(refusedNote("The item you selected is gone; nothing was answered.", m.unboundNote()), true)
		case it != nil && it.State == inbox.Resolved && (it.Kind <= inbox.Question || it.DenAsks()):
			return m.say(refusedNote("The request you selected was already answered; nothing was sent.", m.unboundNote()), true)
		case it != nil && hostOf(itemKey(it)) == "" && !it.Answerable() && (it.Kind <= inbox.Question || it.DenAsks()) && m.inboxWaiting() > 0:
			return m.say(refusedNote("The request you selected can only be answered in its terminal (/open); nothing was sent.", m.unboundNote()), true)
		case (it == nil || !it.Answerable() && hostOf(itemKey(it)) == "") && m.inboxWaiting() > 0:
			return m.say(refusedNote("The item you selected is not a request; nothing was answered.", m.unboundNote()), true)
		}
	}
	if it != nil && hostOf(itemKey(it)) != "" && !it.Answerable() {
		host := hostOf(itemKey(it))
		if it.State == inbox.Resolved {
			return m.say("That request on "+host+" is no longer waiting.", true)
		}
		msg := m.nameFor(itemKey(it)) + " runs on " + host + ": answer it in the terminal there (ssh -t " + m.hostTarget(host) + ")"
		if h, ok := m.hostStatus(host); ok && h.State == remote.AttachedElsewhere {
			msg += "; " + remote.ElsewhereNote(host)
		} else if ok && !answersHere(h) {
			msg += "; " + remote.ObserveNote(host)
		}
		msg += "."
		return m.say(msg, true)
	}
	if it == nil || !it.Answerable() {
		return m.say("Nothing is waiting for an answer.", true)
	}
	if m.tab != tabInbox && verb != "deny" && !m.detailShowsWhole(it) {
		return m.showInInbox(it)
	}
	if why := m.readGate(it); why != "" && verb != "deny" {
		return m.say(why, true)
	}
	if m.unsettled(it) {
		return m.say("A new request just came up: "+m.nameFor(itemKey(it))+": "+it.Title+". Press again to answer it.", true)
	}
	if it.Kind == inbox.Question {
		if verb == "deny" {
			m.store.Release(it, "you chose the terminal")
			m.refresh()
			return m.say(m.nameFor(itemKey(it))+"'s question is back in its terminal", false)
		}
		return m.say(questionHint(it), true)
	}
	if it.Kind == inbox.Plan && verb == "always" {
		return m.say(inbox.PlansOneAtATime, true)
	}
	if it.Kind == inbox.Close {
		return m.decideClose(it, verb)
	}
	if it.Kind == inbox.Skills {
		return m.decideSkills(it, verb)
	}
	if it.Kind == inbox.Budget {
		return m.decideBudget(it, verb)
	}
	if it.Kind == inbox.MissionMove {
		return m.decideMove(it, verb)
	}
	if it.Kind == inbox.Restore {
		return m.decideRestore(it, verb)
	}
	if it.Kind == inbox.Merge {
		return m.decideMerge(it, verb)
	}
	if it.Kind == inbox.Cleanup {
		return m.decideCleanup(it, verb)
	}
	if it.Kind == inbox.Message {
		return m.decideSend(it, verb)
	}
	if it.Kind == inbox.Install {
		return m.decideInstall(it, verb)
	}
	if it.Kind == inbox.APIOn {
		return m.decideAPIOn(it, verb)
	}
	if it.Kind == inbox.Grant {
		return m.decideGrant(it, verb)
	}
	if it.Kind == inbox.Pair {
		return m.decidePair(it, verb)
	}
	if it.Kind == inbox.ControlAsk {
		return m.decideControlTool(it, verb)
	}
	if it.Kind == inbox.Guidance {
		return m.decideGuidance(it, verb)
	}
	if it.Kind == inbox.MissionUpdate {
		return m.decideMissionUpdate(it, verb)
	}
	if it.Kind == inbox.UsageResume {
		return m.decideUsageResume(it, verb)
	}
	if verb == "always" {
		if why := inbox.AlwaysRefusal(it); why != "" {
			return m.say(why, true)
		}
		scope := inbox.AlwaysScope(it)
		if !m.store.Answer(it, verb) {
			m.refresh()
			return m.say(m.nameFor(itemKey(it))+": "+it.Resolved, true)
		}
		m.refresh()
		return m.say("Allowed for "+m.nameFor(itemKey(it))+", and from now on this session: "+scope, false)
	}
	if !m.store.Answer(it, verb) {
		m.refresh()
		return m.say(m.nameFor(itemKey(it))+": "+it.Resolved, true)
	}
	m.refresh()
	return m.say(verbPast(verb)+" for "+m.nameFor(itemKey(it)), false)
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
	case "/answer":
		it := m.inboxSelected()
		if m.tab != tabInbox || it == nil || !it.AsksInDen() {
			it = nil
			for _, o := range m.store.Items() {
				if o.AsksInDen() {
					it = o
					break
				}
			}
		}
		if it == nil {
			return m.say("No question is waiting for an answer in Den.", true)
		}
		text := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), fields[0]))
		if text == "" {
			return m.say(questionHint(it), true)
		}
		return m.answerQuestion(it, text)
	case "/api":
		return m.apiCommand(fields[1:])
	case "/remote", "/remotes", "/hosts":
		return m.remoteCommand(fields[1:])
	case "/open", "/o", "/focus":
		return m.openSelected()
	case "/copy":
		if cmd := m.copyLast(); cmd != nil {
			return m, cmd
		}
		return m.say("No command block in view to copy.", true)
	case "/close", "/kill":
		return m.closeCommand(fields[1:])
	case "/reopen":
		return m.reopenCommand(fields[1:])
	case "/protect", "/unprotect", "/flag", "/unflag":
		return m.guardCommand(strings.ToLower(fields[0]), fields[1:])
	case "/dismiss", "/x":
		if it := m.inboxSelected(); it != nil && m.tab == tabInbox {
			// The row that takes its place is highlighted but not chosen:
			// ^Y waits until it has been on screen a moment.
			cur := m.inboxCur
			m.store.Dismiss(it)
			m.inboxCur, m.inboxSel = cur, ""
			m.refresh()
			return m, nil
		}
		return m.say("Select an inbox item first.", true)
	case "/unread", "/u":
		if it := m.inboxSelected(); it != nil && m.tab == tabInbox {
			if m.store.KeepUnread(it) {
				return m.say("Kept unread: it counts as new until you open or dismiss it.", false)
			}
			return m.say("Marked read.", false)
		}
		return m.say("Select an inbox item first.", true)
	case "/rescan", "/r":
		if m.tab == tabReview {
			// Separate statement: the load marks m as loading, and a return
			// list does not promise that happens before m is copied.
			cmd := m.loadReview()
			return m, tea.Batch(snapshot, cmd)
		}
		return m, snapshot
	case "/map":
		m.tab = tabFleet
		m.focus = paneAgents
		m.tree.mapView = !m.tree.mapView
		return m, nil
	case "/fleet":
		m.tab = tabFleet
		if len(fields) > 1 {
			switch strings.ToLower(fields[1]) {
			case "project", "projects":
				m.proj.byProject = true
			case "flat":
				m.proj.byProject = false
			default:
				return m.say("/fleet project groups the Fleet by project; /fleet flat goes back.", true)
			}
			m.saveProjectPick()
			m.refresh()
		}
	case "/inbox":
		m.tab = tabInbox
	case "/delete", "/del":
		if m.tab != tabReview {
			return m.say("/delete removes a review comment; open Review first.", true)
		}
		if !m.deleteComment() {
			return m.say("No comment on this line.", true)
		}
		return m, nil
	case "/send":
		if m.tab != tabReview {
			return m.say("/send delivers review comments; open Review first.", true)
		}
		return m.sendReview(fields[1:])
	case "/merge":
		return m.mergeCommand(fields[1:])
	case "/cleanup":
		return m.cleanupCommand(fields[1:])
	case "/review":
		m.tab = tabReview
		cmd := m.enteredTab()
		return m, cmd
	case "/overview", "/missions":
		m.tab = tabOverview
		return m, nil
	case "/project":
		if len(fields) < 2 {
			return m.say("Which project? /project <name>, /project add <dir>, /project drop", true)
		}
		switch fields[1] {
		case "add", "pin":
			if len(fields) < 3 {
				return m.say("Which folder? /project add <dir>", true)
			}
			p, err := m.pinProject(strings.Join(fields[2:], " "))
			if err != nil {
				return m.say(err.Error(), true)
			}
			m.tab = tabOverview
			return m.say("Listed "+p.Name+" as a project.", false)
		case "drop", "unpin":
			p, _ := m.curProject()
			if !m.unpinProject() {
				return m.say("That project is not pinned; it is listed because someone works there.", true)
			}
			return m.say("Dropped "+p.Name+" from the pinned projects.", false)
		}
		if !m.pickProject(strings.Join(fields[1:], " ")) {
			return m.say("No project called "+strings.Join(fields[1:], " "), true)
		}
		m.tab = tabOverview
		return m, nil
	case "/mission":
		if len(fields) < 2 {
			return m.say("Describe the mission: /mission <goal>", true)
		}
		// "/mission move <project>" names a project Den lists; any other
		// goal that starts with "move" is a goal.
		if len(fields) > 2 && strings.EqualFold(fields[1], "move") && m.listsProject(strings.Join(fields[2:], " ")) {
			return m.askMove(strings.Join(fields[2:], " "))
		}
		m.tab = tabOverview
		return m, m.proposeMission(strings.Join(fields[1:], " "))
	case "/template", "/templates":
		return m.templateCommand(fields[1:], text)
	case "/pause", "/resume", "/stop":
		if note, isErr, ok := m.missionCommand(strings.ToLower(fields[0])); ok {
			return m.say(note, isErr)
		}
	case "/usage":
		return m.usageCommand(fields[1:])
	case "/budget":
		note, isErr := m.budgetCommand(fields[1:])
		return m.say(note, isErr)
	case "/orchestrator", "/orch":
		m.tab = tabOrch
		if len(fields) > 1 && fields[1] == "new" {
			return m, m.newOrchestratorSession()
		}
		if len(fields) > 1 && fields[1] == "restart" {
			return m, m.restartOrchestrator()
		}
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
	if host, ok := strings.CutPrefix(sessionID, inbox.HostPrefix); ok {
		return "@" + host
	}
	if sessionID == inbox.GuidanceSession || sessionID == inbox.UsageSession || sessionID == inbox.APISession {
		return "Den"
	}
	if id, ok := strings.CutPrefix(sessionID, missionSessionPrefix); ok {
		if ms := m.missions.Get(id); ms != nil {
			return ms.Name
		}
		return "mission"
	}
	if a, ok := m.agentFor(sessionID); ok {
		return a.Name
	}
	if name := m.rosterName(sessionID); name != "" {
		return name
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
