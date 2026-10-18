package ui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/desktop"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/procs"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// Desktop missions: an agent driving the human's desktop through hypruse.
// The agent's only MCP server is Den's proxy (`den desktop-mcp`), which
// asks here before every tool call and forwards nothing without an allow;
// its PreToolUse hook (`den hook desktop-gate`) asks here too, as a second
// layer. Den decides with desktop.Decide on the desktop as it is, and asks
// the human with a card for what the policy says to ask about.

// deskMission is one live mission as Den holds it.
type deskMission struct {
	desktop.Mission
	SessionID  string // the agent's session
	AgentPID   int    // its Claude Code process
	AgentStart string // and that process's start, in clock ticks
	ProxyPID   int    // the proxy, once it started
	ProxyStart string
	ChildPID   int // hypruse, once the proxy started it
	ChildStart string
	Dir        string // the mission's private folder: journal, crops
	// Guard says why the agent's guard no longer holds (its settings file
	// or den changed), "" while it holds. nil: no guard to check.
	Guard func() string
	Ended string // why the mission ended, "" while it is live
	// AskedBy is who dispatched it (its peer name, as it said), AgentName
	// the agent's peer name once it started.
	AskedBy, AgentName string
	// The executables of the agent, the proxy and hypruse, as Den read
	// them when each started: a stop checks them again.
	AgentExe, ProxyExe, ChildExe string
	// Claude is the Claude Code binary Den launched the agent with: its
	// process must run that very file (device and inode).
	Claude       string
	report       string    // the agent's last words, from its Stop hook
	endedAt      time.Time // when it ended: its private files go deskKeepFor later
	launchedAt   time.Time // when its terminal opened: its server must call in by deskStartWait
	kept, purged bool      // the human kept its files; they were deleted
	tokens       map[string]deskToken
	turn         string // the open your-turn card's item id
	// A task mission: view is the agent view's address, placed says it is
	// recorded (no call is answered before), and the limits from the
	// human's config with the calls forwarded so far.
	view        string
	viewPID     int  // the agent view's process: the agent's own terminal
	headless    bool // started with no window of its own, by design
	viewChecked bool // the agent was found running under it
	placed      bool
	// overlayPaused: the console's overlay is down and input waits, since
	// overlayPausedAt (deskOverlayCheck).
	overlayPaused   bool
	overlayPausedAt time.Time
	// overlayExpect: the task started while the console watched, and its
	// first overlay.linked has not come; input waits for it until
	// overlayFirstBy.
	overlayExpect  bool
	overlayFirstBy time.Time
	// A headless agent's steps in the chat (desktop_headless.go): shown
	// in the current minute, since when, and those left out.
	stepsShown, stepsHidden int
	stepsSince              time.Time
	limitActions            int
	actions                 int
	deadline                time.Time
	checking, pinWarned     bool
	checkedAt               time.Time
}

// deskToken lets the proxy forward the rest of an allowed typed text.
type deskToken struct {
	window string
	left   int
	until  time.Time
}

// deskPending is a card's held call.
type deskPending struct {
	mission string
	tool    string
	verdict desktop.Verdict
	pieces  int // a typed text's pieces, for its token
}

// deskState is Den's desktop missions.
type deskState struct {
	missions map[string]*deskMission
	pending  map[string]deskPending // card item id -> its call
	offers   map[string]deskOffer   // mission card item id -> its request
	chip     string                 // the header chip while a mission is live
	// installing is the install question's item id while its approved
	// install runs: the question stays open until the install's own result
	// settles it, which is what answers `den install-hypruse`.
	installing string
	// declinedAt is when the human last declined a desktop start card, or
	// one expired unanswered: new requests wait deskCooldown after it.
	declinedAt time.Time
	// title is Den's terminal title when Den sets one: a task's handshake
	// token while Den finds its own window, then restingTitle.
	title string
}

// Seams for tests.
var (
	deskSnapshot = desktop.ReadSnapshot
	deskVerify   = func() error { return desktop.Verify(desktop.DefaultPaths()) }
	deskExe      = func() string {
		exe, _ := os.Executable()
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			return real
		}
		return exe
	}
	deskChildOf = procs.ChildOf
	deskStart   = procs.StartTicks
	deskParent  = procs.Parent
	deskExeOf   = procs.Exe
	// deskCardWait is how long a desktop card waits before it denies.
	deskCardWait = 5 * time.Minute
	// deskConsole is the console's proved processes, pid to start, from
	// den-api.sock; deskSetDesktop tells the console the running mission.
	deskConsole = func(m *Model) map[int]uint64 {
		if m.api.srv == nil {
			return nil
		}
		return m.api.srv.ConsoleProcs()
	}
	deskSetDesktop = func(m *Model, mission string) {
		if m.api.srv != nil {
			m.api.srv.SetDesktop(mission)
		}
	}
	// deskOverlay says whether the console has linked its overlay to a
	// mission (backed), and whether the link holds now (api.Server.OverlayLink).
	deskOverlay = func(m *Model, mission string) (backed, linked bool) {
		if m.api.srv == nil {
			return false, false
		}
		return m.api.srv.OverlayLink(mission)
	}
	// deskWatching says whether the paired console has a proved,
	// subscribed connection (api.Server.ConsoleWatching).
	deskWatching = func(m *Model) bool {
		return m.api.srv != nil && m.api.srv.ConsoleWatching()
	}
	// deskSetAsking tells the console whether a desktop start card waits.
	deskSetAsking = func(m *Model, asking bool) {
		if m.api.srv != nil {
			m.api.srv.SetDesktopAsking(asking)
		}
	}
)

// deskSnapMsg is the desktop as read for one call.
type deskSnapMsg struct {
	req  *bridge.Request
	in   bridge.DesktopInput
	snap desktop.Snapshot
	err  error
}

// deskCardTimeoutMsg ends a desktop card nobody answered.
type deskCardTimeoutMsg struct{ itemID string }

func (m *Model) deskMission(id string) *deskMission {
	if m.desk.missions == nil {
		return nil
	}
	return m.desk.missions[id]
}

// addDeskMission registers a live mission (the dispatch card does, and
// tests).
func (m *Model) addDeskMission(dm *deskMission) {
	if m.desk.missions == nil {
		m.desk.missions = map[string]*deskMission{}
	}
	if dm.tokens == nil {
		dm.tokens = map[string]deskToken{}
	}
	// A task that starts while the console watches expects its overlay
	// (deskOverlayCheck).
	if dm.Task && deskWatching(m) {
		dm.overlayExpect = true
	}
	m.desk.missions[dm.ID] = dm
	m.publishDesktop()
}

// liveDeskMission is the mission that has not ended, nil for none.
func (m *Model) liveDeskMission() *deskMission {
	for _, dm := range m.desk.missions {
		if dm.Ended == "" {
			return dm
		}
	}
	return nil
}

// publishDesktop tells den-api.sock's console whether a desktop mission
// runs, for its overlay (api.Server.SetDesktop).
func (m *Model) publishDesktop() {
	id := ""
	if dm := m.liveDeskMission(); dm != nil {
		id = dm.ID
	}
	deskSetDesktop(m, id)
}

// consoleProcs are the console's proved processes that are still the ones
// that connected (pid and start), for the gate's pointer-outline
// exemption (desktop.Snapshot.ExemptConsole).
func (m *Model) consoleProcs() map[int]bool {
	var out map[int]bool
	for pid, start := range deskConsole(m) {
		if start == 0 || deskStart(pid) != strconv.FormatUint(start, 10) {
			continue
		}
		if out == nil {
			out = map[int]bool{}
		}
		out[pid] = true
	}
	return out
}

// deskGuard says why Den will not answer for a mission now, "" when it
// will.
func (dm *deskMission) refusal() string {
	if dm.Ended != "" {
		return "the desktop mission has ended (" + dm.Ended + ")"
	}
	if dm.Guard != nil {
		if why := dm.Guard(); why != "" {
			return "the agent is no longer guarded: " + why
		}
	}
	return ""
}

// handleDesktop answers the proxy: its start, its hypruse child, and each
// tool call.
func (m *Model) handleDesktop(r *bridge.Request) tea.Cmd {
	deny := func(why string) tea.Cmd {
		r.Reply(bridge.Decision{Behavior: "deny", Message: "Den: " + why})
		return nil
	}
	if r.Host != "" {
		return deny("desktop missions are this machine's only")
	}
	var in bridge.DesktopInput
	if json.Unmarshal(r.Payload, &in) != nil {
		return deny("unreadable request")
	}
	dm := m.deskMission(in.Mission)
	if dm == nil {
		return deny("no such desktop mission")
	}
	if why := dm.refusal(); why != "" {
		return deny(why)
	}
	switch in.Kind {
	case "start":
		// Only the agent's own MCP server: Den's binary, started by that
		// agent's Claude Code, once.
		if dm.AgentPID <= 1 && !adoptAgent(dm, r.PeerPID) {
			return deny("the mission agent has not started")
		}
		if !deskChildOf(r.PeerPID, dm.AgentPID, dm.AgentStart, deskExe()) {
			return deny("only the mission agent's own desktop server may start it")
		}
		if dm.ProxyPID != 0 && deskStart(dm.ProxyPID) == dm.ProxyStart {
			return deny("this mission's desktop server is already running")
		}
		if err := deskVerify(); err != nil {
			return deny(sanitize.Line(err.Error()))
		}
		dm.ProxyPID, dm.ProxyStart, dm.ProxyExe = r.PeerPID, deskStart(r.PeerPID), deskExe()
		dm.ChildPID, dm.ChildStart, dm.ChildExe = 0, "", ""
		paths := desktop.DefaultPaths()
		data, _ := json.Marshal(desktop.StartInfo{Argv: paths.ServerArgv(), Env: desktop.HypruseEnv(dm.Workspace, filepath.Join(dm.Dir, "journal.ndjson")), Log: filepath.Join(dm.Dir, "desktop.log"), Task: dm.Task})
		r.Reply(bridge.Decision{Behavior: "allow", Data: data})
		return nil
	case "child":
		if !m.fromProxy(dm, r) {
			return deny("only this mission's desktop server may say that")
		}
		pp, _, ok := deskParent(in.ChildPID)
		if !ok || pp != dm.ProxyPID || in.ChildStart == "" || deskStart(in.ChildPID) != in.ChildStart {
			return deny("that is not the desktop server's child")
		}
		dm.ChildPID, dm.ChildStart, dm.ChildExe = in.ChildPID, in.ChildStart, deskExeOf(in.ChildPID)
		dm.save()
		r.Reply(bridge.Decision{Behavior: "allow"})
		return nil
	case "call":
		if !m.fromProxy(dm, r) || dm.ChildPID == 0 {
			return deny("only this mission's running desktop server may ask")
		}
		return func() tea.Msg {
			snap, err := deskSnapshot(context.Background())
			return deskSnapMsg{req: r, in: in, snap: snap, err: err}
		}
	}
	return deny("unknown request")
}

// deskLooks are the desktop tools that only look (or ask the human): an
// overlay pause lets them through. Every other tool is input.
var deskLooks = map[string]bool{
	"desktop": true, "binds": true, "screenshot": true, "zoom": true, "ui": true, "marks": true,
	"request_approval": true, "your_turn": true,
}

// deskOverlayWait is how long a task's input stays paused for the
// console's overlay before Den ends the task.
const deskOverlayWait = 5 * time.Minute

// deskOverlayFirst is how long a task that started while the console
// watched waits for its first overlay.linked before it runs unbacked.
var deskOverlayFirst = 15 * time.Second

// deskOverlayMissing is Den's note when that wait ran out.
const deskOverlayMissing = "The dock did not show its overlay for this task; Den's chip and /desktop stop are the signs."

// deskOverlayCheck pauses or resumes a task's input on the console's
// overlay, and says whether input is paused now. Once the console has
// linked its overlay to the task (overlay.linked), input goes only while
// both hold, each Den's own observation: that link stands on a proved,
// subscribed connection of the paired console (which had its powers when
// it linked), and Den sees the console's overlay on every monitor, a layer
// surface of that console's process (glow, desktop.Snapshot.ConsoleGlow). Otherwise input pauses, with no grace, and a
// pause longer than deskOverlayWait ends the task, as Den's own window
// going does. A task that started while the console watched waits for its
// first link, at most deskOverlayFirst, so that killing the dock early
// cannot leave it unbacked unseen: with no link by then it runs unbacked,
// and Den's chat says so. A task that started with no console watching is
// never paused. Den's chat hears each pause and resume once.
func (m *Model) deskOverlayCheck(dm *deskMission, glow bool) (paused bool, end tea.Cmd) {
	if !dm.Task || dm.Ended != "" {
		return false, nil
	}
	backed, linked := deskOverlay(m, dm.ID)
	if !backed {
		if !dm.overlayExpect {
			return false, nil
		}
		if dm.overlayFirstBy.IsZero() {
			dm.overlayFirstBy = time.Now().Add(deskOverlayFirst)
		}
		if time.Now().Before(dm.overlayFirstBy) {
			return true, nil
		}
		dm.overlayExpect = false
		m.deskOverlayNote(deskOverlayMissing)
		return false, nil
	}
	dm.overlayExpect = false
	if linked && glow {
		if dm.overlayPaused {
			dm.overlayPaused = false
			m.deskOverlayNote("Desktop agent resumed: the dock's overlay is back.")
		}
		return false, nil
	}
	if !dm.overlayPaused {
		dm.overlayPaused, dm.overlayPausedAt = true, time.Now()
		m.deskOverlayNote("Desktop agent paused: the dock's overlay is down. Its input waits until the overlay is back, at most 5 minutes; /desktop stop ends it now.")
	}
	if time.Since(dm.overlayPausedAt) >= deskOverlayWait {
		return true, m.finishDeskMission(dm, "the dock's overlay was down for 5 minutes")
	}
	return true, nil
}

// deskOverlayPaused is what an input call hears while paused.
const deskOverlayPaused = "paused: the dock's overlay is down; Den resumes when it is back"

// deskOverlayNote puts a fixed overlay line in Den's chat.
func (m *Model) deskOverlayNote(text string) {
	m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: text, at: time.Now()}))
	m.flash, m.flashErr = text, true
}

// fromProxy reports whether a request comes from the mission's proxy, the
// process that started it: pid and start.
func (m *Model) fromProxy(dm *deskMission, r *bridge.Request) bool {
	return dm.ProxyPID > 1 && r.PeerPID == dm.ProxyPID && deskStart(r.PeerPID) == dm.ProxyStart
}

// decideDesktopCall runs the gate on the desktop as read, and answers or
// asks the human.
func (m *Model) decideDesktopCall(msg deskSnapMsg) tea.Cmd {
	r, in := msg.req, msg.in
	deny := func(why string) tea.Cmd {
		r.Reply(bridge.Decision{Behavior: "deny", Message: "Den: " + why})
		return nil
	}
	dm := m.deskMission(in.Mission)
	if dm == nil {
		return deny("no such desktop mission")
	}
	if why := dm.refusal(); why != "" {
		return deny(why)
	}
	if msg.err != nil {
		return deny("cannot read the desktop (" + sanitize.Line(msg.err.Error()) + "), so nothing runs")
	}
	if r.HungUp() {
		return nil
	}
	if dm.Task && !dm.placed {
		return deny("Den is still placing the agent view; call again in a moment")
	}
	// The agent view must be the agent's own terminal. What proves the
	// window is its marker class, set when it was created, on exactly one
	// window. On top of that the agent must run under the window's process:
	// Den starts that Ghostty as a process of its own
	// (--gtk-single-instance=false), so it is the agent's ancestor, but
	// under a shared terminal process this check would be weak, and
	// HeldWindow's "no other window of that process" could not hold.
	// Known once the agent is, which every call needs.
	if dm.Task && !dm.viewChecked {
		if !deskAncestorOf(dm.viewPID, dm.AgentPID) {
			why := "the agent view is not the agent's own terminal"
			deny(why + "; the task has ended")
			return m.finishDeskMission(dm, why)
		}
		dm.viewChecked = true
	}
	if why := dm.taskGone(msg.snap); why != "" {
		deny(why + "; the task has ended")
		return m.finishDeskMission(dm, why)
	}
	if paused, end := m.deskOverlayCheck(dm, msg.snap.ConsoleGlow(m.consoleProcs())); end != nil {
		deny(deskOverlayPaused)
		return end
	} else if paused && !deskLooks[in.Tool] {
		return deny(deskOverlayPaused)
	}
	// The console's pointer outline lies over the whole monitor and takes
	// no input: it never blocks a click, and nothing else of the console's
	// is let through.
	msg.snap.ExemptConsole(m.consoleProcs())
	mission := dm.Mission
	if dm.Task {
		// The console's windows (Drey's dock) are off limits to a task like
		// Den's own, read fresh on every call: the dock may reconnect.
		if console := m.consoleProcs(); len(console) > 0 {
			mission.Protected = maps.Clone(dm.Protected)
			for _, w := range msg.snap.Windows {
				if w.PID > 1 && console[w.PID] {
					mission.Protected[w.Address] = true
				}
			}
			mission.Console = console
		}
	}
	v := desktop.Decide(in.Tool, in.Args, mission, msg.snap)
	// spend counts a call Den forwards to hypruse against a task's limits.
	spend := func() (bool, tea.Cmd) {
		why, end := m.taskSpend(dm)
		if why != "" {
			deny(why)
			return false, end
		}
		return true, nil
	}
	if in.Token != "" {
		// The next piece of a text the gate (or the human) already allowed:
		// the same window, still in scope; no second card.
		tok, ok := dm.tokens[in.Token]
		switch {
		case !ok || tok.left <= 0 || time.Now().After(tok.until):
			return deny("that typing is no longer allowed")
		case v.Kind == desktop.Deny:
			delete(dm.tokens, in.Token)
			return deny(v.Reason)
		case v.Target == nil || v.Target.Address != tok.window:
			delete(dm.tokens, in.Token)
			return deny("the target window changed")
		}
		if ok, end := spend(); !ok {
			return end
		}
		tok.left--
		dm.tokens[in.Token] = tok
		m.deskAllow(r, dm, in.Tool, v, 0)
		return nil
	}
	switch v.Kind {
	case desktop.Deny:
		return deny(v.Reason)
	case desktop.Allow:
		if ok, end := spend(); !ok {
			return end
		}
		if in.Tool == "marks" && v.Target != nil {
			dm.MarksWindow = v.Target.Address
		}
		m.deskAllow(r, dm, in.Tool, v, deskPieces(in.Tool, v))
		if in.Tool == "desktop" {
			sc := desktop.ScopeOf(dm.Mission, msg.snap)
			data, _ := json.Marshal(desktop.Answer{Args: mustJSON(v.Args), Scope: &sc})
			r.Reply(bridge.Decision{Behavior: "allow", Data: data})
		}
		return nil
	}
	return m.askDesktop(r, dm, in, v)
}

// deskPieces is how many pieces past the first a call forwards: a typed
// text's, else none.
func deskPieces(tool string, v desktop.Verdict) int {
	if tool != "keyboard" || v.Args["action"] != "type" {
		return 0
	}
	text, _ := v.Args["text"].(string)
	return len(desktop.Chunks(text)) - 1
}

// deskAllow answers allow with the input to forward, and a token for the
// rest of a typed text. A desktop call is answered by the caller (it adds
// the scope).
func (m *Model) deskAllow(r *bridge.Request, dm *deskMission, tool string, v desktop.Verdict, pieces int) {
	if tool == "desktop" {
		return
	}
	a := desktop.Answer{Args: mustJSON(v.Args), Bounds: v.Bounds}
	if pieces > 0 && v.Target != nil {
		a.Token = newToken()
		dm.tokens[a.Token] = deskToken{window: v.Target.Address, left: pieces, until: time.Now().Add(2 * time.Minute)}
	}
	data, _ := json.Marshal(a)
	r.Reply(bridge.Decision{Behavior: "allow", Data: data})
}

// askDesktop holds the call on a card.
func (m *Model) askDesktop(r *bridge.Request, dm *deskMission, in bridge.DesktopInput, v desktop.Verdict) tea.Cmd {
	agent := m.nameFor(dm.SessionID)
	if m.desk.pending == nil {
		m.desk.pending = map[string]deskPending{}
	}
	var it *inbox.Item
	switch in.Tool {
	case "your_turn":
		step, _ := v.Args["step"].(string)
		body := []string{
			"The agent hands this step to you:",
			"  the agent says: " + quoteAgent(step, 300),
			"Every desktop tool is paused until you answer.",
			"Do the step yourself (type any password yourself), then ^Y: done, the agent goes on.",
			"^N ends the mission.",
		}
		it = m.store.AddYourTurn(dm.SessionID, "Your turn: "+sanitize.Label(step, 80), body, r)
		dm.Paused, dm.turn = true, it.ID
	default:
		title := agent + " asks to " + deskAction(in.Tool, v.Args)
		var body []string
		if in.Tool == "request_approval" {
			intent, _ := v.Args["intent"].(string)
			title = agent + " asks before its next step"
			body = append(body, "the agent says: "+quoteAgent(intent, 600))
		} else {
			body = append(body, "why Den asks: "+sanitize.Line(v.Reason))
			if v.Target != nil {
				body = append(body, fmt.Sprintf("window: %s · %s · workspace %d", sanitize.Label(v.Target.Class, 60), sanitize.Label(v.Target.Title, 120), v.Target.Workspace))
			}
			body = append(body, "input: "+deskInput(in.Tool, v.Args))
		}
		body = append(body, "limits: a coordinate click in an app not marked outward is not asked about; hypruse focuses then types about 50 ms apart; a zoom can show a notification drawn over the window.")
		it = m.store.AddDesktopAct(dm.SessionID, in.Tool, title, body, r)
	}
	m.desk.pending[it.ID] = deskPending{mission: dm.ID, tool: in.Tool, verdict: v, pieces: deskPieces(in.Tool, v)}
	m.flash, m.flashErr = it.Title+": ^Y allows, ^N denies.", false
	m.refresh()
	id := it.ID
	cmds := []tea.Cmd{tea.Tick(deskCardWait, func(time.Time) tea.Msg { return deskCardTimeoutMsg{itemID: id} })}
	if v.Target != nil && in.Tool != "your_turn" {
		cmds = append(cmds, m.deskCrop(id, dm.Dir, *v.Target))
	}
	return tea.Batch(cmds...)
}

// deskCropMsg is a screenshot crop taken for a card.
type deskCropMsg struct {
	itemID, path string
	err          error
}

// deskGrab takes a crop of a window into path; tests replace it.
var deskGrab = func(ctx context.Context, w desktop.Window, path string) error {
	return grabWindow(ctx, w, path)
}

// deskCrop takes the card's screenshot crop of the target window, private
// to the mission's folder.
func (m *Model) deskCrop(itemID, dir string, w desktop.Window) tea.Cmd {
	if dir == "" {
		return nil
	}
	path := filepath.Join(dir, "crop-"+itemID+".png")
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return deskCropMsg{itemID: itemID, path: path, err: deskGrab(ctx, w, path)}
	}
}

func (m *Model) finishDeskCrop(msg deskCropMsg) {
	for _, it := range m.store.Items() {
		if it.ID == msg.itemID && it.State == inbox.Waiting {
			if msg.err != nil {
				it.Body = append(it.Body, "screenshot: none ("+sanitize.Line(msg.err.Error())+")")
			} else {
				it.Body = append(it.Body, "screenshot: "+msg.path)
			}
		}
	}
	m.refresh()
}

// deskTTYOnly says why a desktop card cannot take this answer, "" when it
// can: only the human at Den's own window answers desktop cards, once
// each. The dock never gets the verbs (dockMayAnswer); this holds even if
// that changes.
func deskTTYOnly(it *inbox.Item, verb string) string {
	if it.AnsweredBy == "console" {
		return "Desktop control is answered only in Den's own window."
	}
	if verb == "always" {
		return "Desktop cards are answered once: ^Y or ^N."
	}
	return ""
}

// decideDesktopCard is ^Y or ^N on a desktop action or your-turn card.
func (m Model) decideDesktopCard(it *inbox.Item, verb string) (tea.Model, tea.Cmd) {
	if why := deskTTYOnly(it, verb); why != "" {
		return m.say(why, true)
	}
	p, ok := m.desk.pending[it.ID]
	delete(m.desk.pending, it.ID)
	dm := m.deskMission(p.mission)
	if !ok || dm == nil {
		m.store.SettleWith(it, bridge.Decision{Behavior: "deny", Message: "Den: that question is stale"}, "stale; denied")
		return m.say("That desktop question is stale; denied.", true)
	}
	if it.Kind == inbox.YourTurn {
		dm.Paused, dm.turn = false, ""
		if verb != "allow" {
			m.store.SettleWith(it, bridge.Decision{Behavior: "deny", Message: "Den: the human ended the mission at this step. Stop now."}, "you ended the mission")
			end := m.finishDeskMission(dm, "you ended it at your step")
			model, say := m.say("Ended the desktop mission.", false)
			return model, tea.Batch(say, end)
		}
		step, _ := p.verdict.Args["step"].(string)
		m.store.SettleWith(it, bridge.Decision{Behavior: "allow", Message: "The human finished this step: " + sanitize.Line(step) + ". Go on with the plan."}, "you did it; the agent goes on")
		m.refresh()
		return m.say("The agent goes on.", false)
	}
	if verb != "allow" {
		m.store.SettleWith(it, bridge.Decision{Behavior: "deny", Message: "Den: the human said no to this step. Do not try it another way; ask with request_approval if you think it is needed."}, "you said no")
		m.refresh()
		return m.say("Denied.", false)
	}
	if why := dm.refusal(); why != "" {
		m.store.SettleWith(it, bridge.Decision{Behavior: "deny", Message: "Den: " + why}, why)
		m.refresh()
		return m.say("Not allowed: "+why, true)
	}
	if p.tool == "request_approval" {
		m.store.SettleWith(it, bridge.Decision{Behavior: "allow", Message: "Approved by the human: go ahead with that step, and only that step."}, "approved")
		m.refresh()
		return m.say("Approved.", false)
	}
	a := desktop.Answer{Args: mustJSON(p.verdict.Args), Bounds: p.verdict.Bounds}
	if p.pieces > 0 && p.verdict.Target != nil {
		a.Token = newToken()
		dm.tokens[a.Token] = deskToken{window: p.verdict.Target.Address, left: p.pieces, until: time.Now().Add(2 * time.Minute)}
	}
	data, _ := json.Marshal(a)
	m.store.SettleWith(it, bridge.Decision{Behavior: "allow", Data: data}, "allowed once")
	m.refresh()
	return m.say("Allowed once.", false)
}

// timeoutDesktopCard denies a card nobody answered in time.
func (m *Model) timeoutDesktopCard(msg deskCardTimeoutMsg) {
	for _, it := range m.store.Items() {
		if it.ID != msg.itemID || it.State != inbox.Waiting {
			continue
		}
		p := m.desk.pending[it.ID]
		delete(m.desk.pending, it.ID)
		if dm := m.deskMission(p.mission); dm != nil && it.Kind == inbox.YourTurn {
			// The human's step was not done: the mission stays paused until
			// they end it or ask again.
			dm.turn = ""
		}
		m.store.SettleWith(it, bridge.Decision{Behavior: "deny", Message: "Den: no answer from the human in time, so it was not allowed"}, "no answer in time; denied")
	}
	m.refresh()
}

// handleDesktopGate answers the desktop agent's PreToolUse hook: allow
// (the hook then makes no decision) for a live mission's allowed tools,
// else deny. Asked by Claude Code's own child, the hook.
func (m *Model) handleDesktopGate(r *bridge.Request) tea.Cmd {
	deny := func(why string) tea.Cmd {
		r.Reply(bridge.Decision{Behavior: "deny", Message: why})
		return nil
	}
	if r.Host != "" {
		return deny("desktop missions are this machine's only")
	}
	var in bridge.HookInput
	if json.Unmarshal(r.Payload, &in) != nil || in.AgentID != "" {
		return deny("not a desktop agent's own call")
	}
	for _, dm := range m.desk.missions {
		if dm.SessionID == "" || dm.SessionID != in.SessionID {
			continue
		}
		if !deskChildOf(r.PeerPID, dm.AgentPID, dm.AgentStart, deskExe()) {
			return deny("the hook is not the mission agent's own")
		}
		if why := dm.refusal(); why != "" {
			return deny(why)
		}
		for _, t := range desktop.AllowedToolNames(dm.Task) {
			if in.ToolName == t {
				r.Reply(bridge.Decision{Behavior: "allow"})
				return nil
			}
		}
		return deny(sanitize.Label(in.ToolName, 80) + " is not a desktop agent's tool")
	}
	return deny("not a desktop mission's agent")
}

// endDeskMission marks a mission over: every later call is denied, and
// its open cards are settled.
func (m *Model) endDeskMission(dm *deskMission, why string) {
	if dm.Ended != "" {
		return
	}
	dm.Ended = why
	dm.endedAt = time.Now()
	deskNoFocus("") // never outlives a mission
	// Nor does the overlay: its pointer goes, and the console hears idle.
	if m.liveDeskMission() == nil {
		os.Remove(desktop.PointerPath())
	}
	m.publishDesktop()
	m.forgetGuard(dm.SessionID)
	for id, p := range m.desk.pending {
		if p.mission != dm.ID {
			continue
		}
		for _, it := range m.store.Items() {
			if it.ID == id && it.State == inbox.Waiting {
				m.store.SettleWith(it, bridge.Decision{Behavior: "deny", Message: "Den: the mission ended"}, "the mission ended")
			}
		}
		delete(m.desk.pending, id)
	}
}

// deskAction is a card's short wording of a call.
func deskAction(tool string, args map[string]any) string {
	act, _ := args["action"].(string)
	switch tool {
	case "pointer":
		return act + " the mouse"
	case "keyboard":
		if act == "type" {
			return "type text"
		}
		return "press keys"
	case "click_ui":
		return "click a control"
	}
	return "use " + tool
}

// deskInput is the call's input for a card: typed text shown with its line
// breaks, everything sanitised and cut.
func deskInput(tool string, args map[string]any) string {
	if tool == "keyboard" {
		if text, ok := args["text"].(string); ok {
			return "type " + quoteAgent(desktop.Describe(text, 400), 420)
		}
		keys, _ := args["keys"].(string)
		return "press " + sanitize.Label(keys, 60)
	}
	if tool == "click_ui" {
		if name, ok := args["name"].(string); ok && name != "" {
			return "click " + quoteAgent(name, 120)
		}
		return fmt.Sprintf("click mark %v", args["mark"])
	}
	b, _ := json.Marshal(args)
	return sanitize.Label(string(b), 300)
}

// quoteAgent is text the agent wrote, for a card: sanitised, cut, quoted,
// so it reads as the agent's words and never as Den's.
func quoteAgent(s string, n int) string {
	return "“" + sanitize.Label(strings.TrimSpace(s), n) + "”"
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func newToken() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// grabWindow saves a PNG of the window's rectangle on screen, private to
// the user.
func grabWindow(ctx context.Context, w desktop.Window, path string) error {
	geom := fmt.Sprintf("%d,%d %dx%d", w.At[0], w.At[1], w.Size[0], w.Size[1])
	if out, err := exec.CommandContext(ctx, "grim", "-g", geom, path).CombinedOutput(); err != nil {
		return fmt.Errorf("grim: %v %s", err, strings.TrimSpace(string(out)))
	}
	return os.Chmod(path, 0o600)
}
