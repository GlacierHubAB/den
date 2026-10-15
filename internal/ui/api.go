package ui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/api"
	"github.com/GlacierHubAB/den/internal/config"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/orch"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// Den's local API, den-api.sock (internal/api, docs/DREY-API.md), as the
// TUI runs it. It is off unless the human turns it on: /api on shows what
// it lets apps do and takes ^Y once that was read, then writes
// api.enabled = true. /api off and /api disconnect drop every app at once
// and keep the socket's path, so nothing else can take it; /api resume
// lets apps in again.

type apiState struct {
	srv     *api.Server
	err     string                 // why the API is not running, "" when it is or is off
	askItem *inbox.Item            // the open /api on question
	grants  map[*inbox.Item]string // open grant cards → grant id
	lastKey time.Time              // the human's last key in Den: no focus from outside right after it
	warned  bool                   // the second socket replacement was noted
}

// focusQuiet is how long after the human's last key in Den an app may not
// move the focus.
const focusQuiet = 1500 * time.Millisecond

// focusAgent brings an agent's terminal to the front; tests replace it.
var focusAgent = fleet.Focus

// laneCommand is the claude the read-only lane runs, nil for claude on
// PATH; tests set a fake.
var laneCommand []string

type apiActMsg struct {
	act  api.Act
	from *api.Server
}

func waitAPIAct(srv *api.Server) tea.Cmd {
	return func() tea.Msg {
		act, ok := <-srv.Acts()
		if !ok {
			return nil
		}
		return apiActMsg{act: act, from: srv}
	}
}

// applyAPIAct puts an app's grant card in the inbox, or settles the cards
// of grants whose app is gone.
func (m *Model) applyAPIAct(act api.Act) tea.Cmd {
	if act.CardAnswer != nil {
		return m.answerFromDock(act.CardAnswer)
	}
	if act.Command != nil {
		return m.runFromDock(act.Command)
	}
	if isConsoleAct(act) {
		return m.applyConsoleAct(act)
	}
	if act.Pair != nil || act.CodeGone || act.PairDone != "" || act.Paired || act.Say != nil {
		return m.applyControlAct(act)
	}
	if m.api.grants == nil {
		m.api.grants = map[*inbox.Item]string{}
	}
	if f := act.Focus; f != nil {
		if time.Since(m.api.lastKey) < focusQuiet {
			f.Reply <- "the human is typing in Den"
			return nil
		}
		var target *fleet.Agent
		for i := range m.agents {
			if a := &m.agents[i]; a.Session.SessionID == f.AgentID && a.Host == "" && a.Status != fleet.Gone {
				target = a
			}
		}
		if target == nil {
			f.Reply <- "no such agent here"
			return nil
		}
		a := *target
		// Only the agent's own terminal moves: Den's tab, selection and
		// command line stay as the human left them.
		return func() tea.Msg {
			if _, err := focusAgent(a); err != nil {
				f.Reply <- sanitize.Label(err.Error(), 120)
			} else {
				f.Reply <- ""
			}
			return nil
		}
	}
	if g := act.Grant; g != nil {
		title := "App grant · " + g.Name + " v" + strconv.Itoa(g.Version)
		body := []string{
			"requested by " + appName(g.Exe) + " (pid " + strconv.Itoa(g.PID) + ", program " + sanitize.Label(api.ExeShown(g.Exe), 120) + ") through den-api.sock",
			"capabilities: " + strings.Join(g.Capabilities, ", "),
			"──── the app's request, as it sent it ────",
		}
		body = append(body, strings.Split(g.Body, "\n")...)
		body = append(body, "──── end of the app's request · sha256 "+strings.TrimPrefix(g.Hash, "sha256:")[:16]+"… ────")
		it := m.store.AddGrant(title, body)
		m.api.grants[it] = g.ID
		m.refresh()
		if m.cfg.Notify.NeedsYou {
			return m.notifyCmd(inbox.APISession, "has an app asking for a grant")
		}
		return nil
	}
	gone := map[string]bool{}
	for _, id := range act.Expired {
		gone[id] = true
	}
	for it, id := range m.api.grants {
		if gone[id] {
			if it.State != inbox.Resolved {
				m.store.Settle(it, false, "expired: the app is gone")
			}
			delete(m.api.grants, it)
		}
	}
	m.refresh()
	return nil
}

// decideGrant is the human's ^Y or ^N on a grant card, after the whole
// request was shown (answer's readGate). It is answered once: no /always.
func (m Model) decideGrant(it *inbox.Item, verb string) (tea.Model, tea.Cmd) {
	id, ok := m.api.grants[it]
	if !ok || m.api.srv == nil {
		m.store.Settle(it, false, "stale; nothing granted")
		m.refresh()
		return m.say("That grant request is stale; nothing was granted.", true)
	}
	if verb == "always" {
		return m.say("A grant is answered once: ^Y grants it, ^N denies it.", true)
	}
	delete(m.api.grants, it)
	answer := "denied"
	if verb == "allow" {
		answer = "granted"
	}
	m.api.srv.GrantAnswered(id, answer)
	m.store.Settle(it, verb == "allow", answer)
	m.refresh()
	return m.say("Grant "+answer+".", false)
}

type apiNoteMsg struct {
	text string
	from *api.Server
}

// stateDir is $XDG_STATE_HOME/den, else ~/.local/state/den.
func stateDir() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "den")
}

// apiStateRenames are den-api.sock's files in Den's state under the names
// earlier test builds gave them, and their names now.
var apiStateRenames = [][2]string{
	{"drey-chat.jsonl", "api-chat.jsonl"},
	{"drey-chat.jsonl.1", "api-chat.jsonl.1"},
	{"drey-spend.json", "api-spend.json"},
	{"drey-lane", "api-lane"},
}

// migrateAPIState moves each file or folder under its old name to its new
// one, once: only a regular file or a folder (never a link), and only when
// nothing has the new name yet.
func migrateAPIState(state string) {
	for _, r := range apiStateRenames {
		old, now := filepath.Join(state, r[0]), filepath.Join(state, r[1])
		fi, err := os.Lstat(old)
		if err != nil || !(fi.Mode().IsRegular() || fi.IsDir()) {
			continue
		}
		if _, err := os.Lstat(now); !errors.Is(err, os.ErrNotExist) {
			continue
		}
		os.Rename(old, now)
	}
}

// startAPI binds den-api.sock. A failure is kept for /api and the header.
func (m *Model) startAPI() tea.Cmd {
	if m.api.srv != nil {
		m.api.srv.Resume()
		return nil
	}
	state := stateDir()
	migrateAPIState(state)
	lane := filepath.Join(state, "api-lane")
	if err := os.MkdirAll(lane, 0o700); err != nil {
		m.api.err = err.Error()
		return nil
	}
	os.Chmod(lane, 0o700)
	cfg := m.cfg
	key, kerr := loadConsoleKey(state)
	m.con.keyErr = ""
	if kerr != nil {
		key, m.con.keyErr = nil, sanitize.Label(kerr.Error(), 160)
	}
	srv, err := api.Listen(api.Options{
		Version:      Version,
		ConsoleKey:   key,
		ConsoleStore: filepath.Join(state, "consoles.json"),
		ChatLog:      filepath.Join(state, "api-chat.jsonl"),
		Lane: api.LaneConfig{
			Command:   laneCommand,
			Dir:       lane,
			SpendPath: filepath.Join(state, "api-spend.json"),
			Model:     func() string { return cfg.Orchestrator.Model },
			BudgetUS:  func() float64 { return cfg.API.AskBudgetUSD },
		},
	})
	if err != nil {
		m.api.err = err.Error()
		return nil
	}
	m.api.srv, m.api.err = srv, ""
	m.publishAPI()
	return tea.Batch(waitAPINote(srv), waitAPIAct(srv))
}

func waitAPINote(srv *api.Server) tea.Cmd {
	return func() tea.Msg {
		text, ok := <-srv.Notes()
		if !ok {
			return nil
		}
		return apiNoteMsg{text: text, from: srv}
	}
}

// closeAPI stops the server when Den exits.
func (m Model) closeAPI() {
	if m.api.srv != nil {
		m.api.srv.Close()
	}
}

// lanePID is the read-only lane's pid, 0 when it is not running.
func (m Model) lanePID() int {
	if m.api.srv == nil {
		return 0
	}
	return m.api.srv.LanePID()
}

// publishAPI hands the API the fleet as it is now. It runs on the UI's own
// goroutine and never blocks.
// publishedHook, set by tests only, sees each snapshot as it is published.
var publishedHook func(api.Snapshot)

func (m *Model) publishAPI() {
	if m.api.srv == nil && publishedHook == nil {
		return
	}
	snap := api.Snapshot{
		Taken:     time.Now().UTC(),
		Names:     map[string]string{},
		Items:     m.store.Items(),
		FullHosts: map[string]bool{},
		Roster:    m.laneRoster(),
		Guidance:  m.guide.active,
	}
	snap.Colors = map[string]string{}
	for _, a := range m.agents {
		snap.Agents = append(snap.Agents, a)
		snap.Names[a.Session.SessionID] = a.Name
		if a.Host == "" {
			snap.Colors[a.Session.SessionID] = m.colorFor(a.Session.SessionID)
		}
	}
	snap.AgentProcs = m.agentProcs()
	snap.Cards = m.dockCards()
	snap.Settled = m.dockSettled()
	snap.Commands = dockCommands()
	for _, h := range m.remote.hosts {
		if string(h.Applied) == "full" {
			snap.FullHosts[h.Label] = true
		}
	}
	if m.missions != nil {
		snap.Missions = m.missions.All()
	}
	if publishedHook != nil {
		publishedHook(snap)
	}
	if m.api.srv != nil {
		m.api.srv.Publish(api.Project(snap))
	}
}

// laneRoster is the roster the lane reads: the orchestrator's, without
// agents' recent words and tool calls or earlier tasks, and with what waits
// on the human named by kind and title only.
func (m Model) laneRoster() []orch.RosterAgent {
	var out []orch.RosterAgent
	for _, r := range m.rosterFor(true) {
		if !r.IsSelf {
			out = append(out, r)
		}
	}
	return out
}

// apiCommand is /api, /api on, /api off, /api disconnect, /api resume,
// /api pair and /api unpair.
func (m Model) apiCommand(args []string) (tea.Model, tea.Cmd) {
	sub := ""
	if len(args) > 0 {
		sub = strings.ToLower(args[0])
	}
	switch sub {
	case "console":
		return m.consoleCommand()
	case "pair", "unpair":
		return m.controlCommand(sub)
	case "disallow", "control":
		return m.standingCommand(sub, args[1:])
	case "", "status", "list":
		m.logEntry(m.addChat(chatEntry{role: "note", text: m.apiReport(), at: time.Now()}))
		return m.say("den-api.sock: see the note in the orchestrator chat (^O).", false)
	case "on":
		if m.cfg.API.Enabled && m.api.srv != nil && !m.api.srv.Paused() {
			return m.say("The API is on already.", false)
		}
		if m.api.askItem != nil && m.api.askItem.State == inbox.Waiting {
			m.tab = tabInbox
			m.refresh()
			m.selectItem(m.api.askItem)
			return m.say("Read the question in the Inbox, then ^Y to turn the API on.", false)
		}
		body := []string{
			"Lets apps on this machine, running as your user, read Den's fleet and inbox digests and ask a read-only orchestrator, which costs money: up to api.ask_budget_usd (" + strconv.FormatFloat(m.cfg.API.AskBudgetUSD, 'f', -1, 64) + " dollars) a day.",
			"Agents' processes are refused. The orchestrator the apps reach has no tools: it cannot message, start, close or approve anything. /api off or /api disconnect drops every app at once.",
		}
		it := m.store.AddAPIOn("Turn on den-api.sock for apps on this machine?", body)
		m.api.askItem = it
		m.tab = tabInbox
		m.refresh()
		m.selectItem(it)
		return m.say("Read the question in the Inbox, then ^Y to turn the API on.", false)
	case "off":
		if err := config.SetKey("api", "enabled", "false"); err != nil {
			return m.say("Could not write the config: "+err.Error(), true)
		}
		m.cfg.API.Enabled = false
		if m.api.srv != nil {
			m.api.srv.Pause()
		}
		m.logEntry(m.addChat(chatEntry{role: "note", text: "den-api.sock is off: every app was dropped, and new ones are refused (api.enabled = false)", at: time.Now()}))
		return m.say("API off.", false)
	case "disconnect":
		if m.api.srv == nil {
			return m.say("The API is not running.", true)
		}
		m.api.srv.Pause()
		m.logEntry(m.addChat(chatEntry{role: "note", text: "den-api.sock: every app was dropped; new ones are refused until /api resume", at: time.Now()}))
		return m.say("Apps disconnected.", false)
	case "resume":
		if m.api.srv == nil || !m.cfg.API.Enabled {
			return m.say("The API is off: /api on turns it on.", true)
		}
		m.api.srv.Resume()
		m.api.warned = false
		return m.say("Apps may connect again; the read-only lane may try to start again.", false)
	}
	return m.say("/api [on|off|disconnect|resume|pair|unpair|disallow|control]", true)
}

// decideAPIOn is the human's ^Y or ^N on /api on, after the whole question
// was shown (answer's readGate).
func (m Model) decideAPIOn(it *inbox.Item, verb string) (tea.Model, tea.Cmd) {
	if it != m.api.askItem {
		m.store.Settle(it, false, "stale; nothing changed")
		m.refresh()
		return m.say("That question is stale; the API was not turned on.", true)
	}
	m.api.askItem = nil
	if verb != "allow" {
		m.store.Settle(it, false, "declined; the API stays off")
		m.refresh()
		return m.say("The API stays off.", false)
	}
	if err := config.SetKey("api", "enabled", "true"); err != nil {
		m.store.Settle(it, false, "could not write the config")
		m.refresh()
		return m.say("Could not write the config: "+err.Error(), true)
	}
	m.cfg.API.Enabled = true
	cmd := m.startAPI()
	if m.api.srv == nil {
		m.store.Settle(it, false, "the socket could not be opened")
		m.refresh()
		return m, tea.Batch(cmd, m.flashCmd("den-api.sock could not be opened: "+m.api.err, true))
	}
	m.store.Settle(it, true, "API on")
	m.logEntry(m.addChat(chatEntry{role: "note", text: "den-api.sock is on at " + m.api.srv.Path() + " (api.enabled = true). /api lists apps; /api off turns it off.", at: time.Now()}))
	m.refresh()
	mm, flash := m.say("API on.", false)
	return mm, tea.Batch(cmd, flash)
}

func (m Model) flashCmd(text string, isErr bool) tea.Cmd {
	_, cmd := m.say(text, isErr)
	return cmd
}

// apiReport is /api's note: state, socket, apps and the lane.
func (m Model) apiReport() string {
	var b strings.Builder
	switch {
	case m.api.srv == nil && !m.cfg.API.Enabled:
		b.WriteString("den-api.sock is off (api.enabled = false). /api on turns it on.")
		return b.String()
	case m.api.srv == nil:
		b.WriteString("den-api.sock is not running: " + m.api.err)
		return b.String()
	case m.api.srv.Paused():
		b.WriteString("den-api.sock: apps refused (off or disconnected); the path stays Den's: " + m.api.srv.Path())
	default:
		b.WriteString("den-api.sock is on: " + m.api.srv.Path())
	}
	if !m.api.srv.Healthy() {
		b.WriteString("\nthe socket at that path was replaced or removed")
	}
	clients := m.api.srv.Clients()
	if len(clients) == 0 {
		b.WriteString("\nno apps connected")
	}
	for _, c := range clients {
		line := "\n" + sanitize.Label(c.Label, 40) + " · pid " + strconv.Itoa(c.PID) + " · " + sanitize.Label(api.ExeShownShort(c.Exe), 120) + " · since " + c.Since.Format("15:04")
		if len(c.Streams) > 0 {
			line += " · " + strings.Join(c.Streams, ", ")
		}
		if c.Asking {
			line += " · asking"
		}
		if c.Agent {
			line += " · an agent's process: refused"
		}
		b.WriteString(line)
	}
	for _, r := range m.api.srv.Refusals() {
		b.WriteString(fmt.Sprintf("\nrefused: %s · %s · %d since %s, last %s", sanitize.Label(api.ExeShownShort(r.Exe), 120), r.Reason, r.Count, r.First.Format("15:04"), r.Last.Format("15:04:05")))
	}
	for _, p := range m.api.srv.Pairings() {
		state := "waiting for your ^Y"
		switch {
		case p.Confirmed && p.Off:
			state = "control off (/api control on)"
		case p.Confirmed:
			state = "control on"
		}
		used := "never"
		if !p.LastUsed.IsZero() {
			used = p.LastUsed.Format("15:04:05")
		}
		b.WriteString("\npaired: " + sanitize.Label(api.ExeShownShort(p.Exe), 120) + " · pid " + strconv.Itoa(p.PID) + " · since " + p.Since.Format("15:04") + " · last used " + used + " · " + state + " · /api unpair revokes")
	}
	b.WriteString(m.standingReport())
	if m.ctl.noControl != "" {
		b.WriteString("\ncontrol: " + m.ctl.noControl)
	}
	ls := m.api.srv.LaneState()
	b.WriteString("\nread-only lane: " + laneWords(ls))
	b.WriteString(fmt.Sprintf(" · spent today %.2f of %.2f dollars", ls.SpentUSD, m.cfg.API.AskBudgetUSD))
	b.WriteString("\nlane sessions and asks: " + filepath.Join(stateDir(), "api-chat.jsonl"))
	return b.String()
}

// apiChip is the header's API indicator, "" when the API is off: its
// parts (apiChipParts) joined.
func (m Model) apiChip() string { return strings.Join(m.apiChipParts(), " · ") }

// apiChipParts is the header's API indicator in parts, the first the one
// that matters; nil when the API is off. The names are the base names of
// the exes Den read, never what an app says it is; one Den cannot identify
// is only counted here, in a short form, and named in full in /api and on
// its cards.
func (m Model) apiChipParts() []string {
	srv := m.api.srv
	if srv == nil {
		if m.cfg.API.Enabled && m.api.err != "" {
			return []string{m.st.err.Render("API: " + m.api.err)}
		}
		return nil
	}
	if srv.Paused() {
		return nil
	}
	if !srv.Healthy() {
		return []string{m.st.err.Render("⇄ socket replaced")}
	}
	var paired []string
	for _, p := range srv.Pairings() {
		if !p.Confirmed {
			continue
		}
		if api.Unidentified(p.Exe) {
			paired = append(paired, "an unidentified app, pid "+strconv.Itoa(p.PID))
		} else {
			paired = append(paired, appName(p.Exe)+" pid "+strconv.Itoa(p.PID))
		}
	}
	if len(paired) > 0 {
		return []string{m.st.attn.Render("paired: " + strings.Join(paired, ", "))}
	}
	consoleExe := ""
	if c, ok := srv.CurrentConsole(); ok {
		consoleExe = c.Exe
	}
	chips, asking := connectedChips(srv.Clients(), consoleExe)
	lane := laneChip(srv.LaneState())
	if len(chips) == 0 {
		if lane != "" {
			return []string{"API on", lane}
		}
		return []string{"API on"}
	}
	var parts []string
	for _, c := range chips {
		parts = append(parts, m.st.attn.Render(c))
	}
	if asking {
		parts = append(parts, m.st.attn.Render("asking"))
	}
	if lane != "" {
		parts = append(parts, lane)
	}
	return parts
}

// connectedChip is the header's words for the apps connected: the names
// Den read, and how many it cannot identify, in a short form.
func connectedChip(names []string, unidentified int) string {
	switch {
	case len(names) == 0:
		return "⇄ " + appCount(unidentified) + " connected · unidentified"
	case unidentified == 0:
		return "⇄ " + strings.Join(names, ", ") + " connected"
	}
	return "⇄ " + strings.Join(names, ", ") + ", " + strconv.Itoa(unidentified) + " unidentified connected"
}

// connectedChips are the header's words for what is connected, and
// whether any of it is asking. A connection proved as the console is
// "console", which Den knows from the pairing, never from what the
// program says, with the program as Den read it (consoleExe), "program
// unverified" when it could not read one. Every other app keeps its own
// words (connectedChip).
func connectedChips(clients []api.Client, consoleExe string) ([]string, bool) {
	names := map[string]bool{}
	unidentified := map[int]bool{}
	asking, console := false, false
	for _, c := range clients {
		if c.Agent {
			continue
		}
		asking = asking || c.Asking
		switch {
		case c.Console:
			console = true
		case api.Unidentified(c.Exe):
			unidentified[c.PID] = true
		default:
			names[sanitize.Label(c.Label, 20)] = true
		}
	}
	var out []string
	if console {
		what := "program unverified"
		if !api.Unidentified(consoleExe) {
			what = appName(consoleExe)
		}
		out = append(out, "⇄ "+consoleWord+" connected · "+what)
	}
	if len(names) > 0 || len(unidentified) > 0 {
		out = append(out, connectedChip(sortedKeys(names), len(unidentified)))
	}
	return out, asking
}

// appCount is "1 app" or "N apps".
func appCount(n int) string {
	if n == 1 {
		return "1 app"
	}
	return strconv.Itoa(n) + " apps"
}

// sortedKeys is a set's members in order.
func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// laneWords is the lane's state for /api: off, starting, ready or failed
// with why, then what is running and waiting.
func laneWords(ls api.LaneState) string {
	out := sanitize.Label(ls.State, 160)
	if ls.Running {
		out += " · 1 question running"
	}
	if ls.Queued > 0 {
		out += " · " + strconv.Itoa(ls.Queued) + " queued"
	}
	return out
}

// laneChip is the lane's part of the header chip: only what is worth a
// glance (starting, failed, a queue).
func laneChip(ls api.LaneState) string {
	var parts []string
	switch {
	case ls.State == "starting":
		parts = append(parts, "lane starting")
	case strings.HasPrefix(ls.State, "failed"):
		parts = append(parts, sanitize.Label("lane "+ls.State, 60))
	}
	if ls.Queued > 0 {
		parts = append(parts, strconv.Itoa(ls.Queued)+" queued")
	}
	return strings.Join(parts, " · ")
}

// checkAPISocket runs on the tick: a socket replaced or removed at the
// path is taken back once and noted; a second time only the chip says so,
// until /api resume.
func (m *Model) checkAPISocket() {
	srv := m.api.srv
	if srv == nil || srv.Healthy() {
		return
	}
	if err := srv.Rebind(); err != nil {
		if !m.api.warned {
			m.api.warned = true
			m.logEntry(m.addChat(chatEntry{role: "note", text: "den-api.sock was replaced or removed again (" + sanitize.Label(err.Error(), 120) + "); /api resume lets Den take it back", at: time.Now()}))
		}
		return
	}
	m.logEntry(m.addChat(chatEntry{role: "note", text: "den-api.sock was replaced or removed; Den took the path back", at: time.Now()}))
}

// isLane reports whether a discovered session is the API's read-only lane.
func (m Model) isLane(a fleet.Agent) bool {
	p := m.lanePID()
	return p > 0 && a.Session.PID == p
}

// agentProcs are the processes whose descendants den-api.sock refuses:
// every live Claude session on this machine (the orchestrator's and the
// lane's included), each by pid and start time, and Den's own orchestrator
// and lane processes. A session on another machine, or one that has ended,
// is never one: its pid may be any process here.
func (m Model) agentProcs() []api.AgentProc {
	seen := map[int]bool{}
	var out []api.AgentProc
	add := func(a fleet.Agent) {
		pid := a.Session.PID
		if a.Host != "" || !a.Session.Alive || a.Status == fleet.Gone || pid <= 1 || seen[pid] {
			return
		}
		name := a.Name
		if a.Session.Name != "" {
			name += " [" + a.Session.Name + "]"
		}
		if p, ok := sessionProc(pid, a.Session.StartedAt, "agent "+name); ok {
			seen[pid] = true
			out = append(out, p)
		}
	}
	for _, a := range m.base {
		add(a)
	}
	for _, a := range m.agents {
		add(a)
	}
	if m.orch.proc != nil && !seen[m.orch.proc.PID] {
		seen[m.orch.proc.PID] = true
		out = append(out, api.AgentProc{PID: m.orch.proc.PID, Name: "the orchestrator"})
	}
	if p := m.lanePID(); p > 0 && !seen[p] {
		out = append(out, api.AgentProc{PID: p, Name: "Den's read-only lane"})
	}
	return out
}

// sessionProc is api.SessionProc; tests replace it.
var sessionProc = api.SessionProc
