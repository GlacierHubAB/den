package ui

import (
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
	err     string      // why the API is not running, "" when it is or is off
	askItem *inbox.Item // the open /api on question
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

// startAPI binds den-api.sock. A failure is kept for /api and the header.
func (m *Model) startAPI() tea.Cmd {
	if m.api.srv != nil {
		m.api.srv.Resume()
		return nil
	}
	state := stateDir()
	lane := filepath.Join(state, "drey-lane")
	if err := os.MkdirAll(lane, 0o700); err != nil {
		m.api.err = err.Error()
		return nil
	}
	os.Chmod(lane, 0o700)
	cfg := m.cfg
	srv, err := api.Listen(api.Options{
		Version: Version,
		ChatLog: filepath.Join(state, "drey-chat.jsonl"),
		Lane: api.LaneConfig{
			Dir:      lane,
			Model:    func() string { return cfg.Orchestrator.Model },
			BudgetUS: func() float64 { return cfg.API.AskBudgetUSD },
		},
	})
	if err != nil {
		m.api.err = err.Error()
		return nil
	}
	m.api.srv, m.api.err = srv, ""
	m.publishAPI()
	return waitAPINote(srv)
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
func (m *Model) publishAPI() {
	if m.api.srv == nil {
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
	for _, a := range m.agents {
		snap.Agents = append(snap.Agents, a)
		snap.Names[a.Session.SessionID] = a.Name
		if a.Host == "" && a.Session.PID > 0 {
			snap.AgentPIDs = append(snap.AgentPIDs, a.Session.PID)
		}
	}
	for _, a := range m.base {
		if a.Session.PID > 0 {
			snap.AgentPIDs = append(snap.AgentPIDs, a.Session.PID) // the orchestrator and the lane too
		}
	}
	if m.orch.proc != nil {
		snap.AgentPIDs = append(snap.AgentPIDs, m.orch.proc.PID)
	}
	if p := m.lanePID(); p > 0 {
		snap.AgentPIDs = append(snap.AgentPIDs, p)
	}
	for _, h := range m.remote.hosts {
		if string(h.Applied) == "full" {
			snap.FullHosts[h.Label] = true
		}
	}
	if m.missions != nil {
		snap.Missions = m.missions.All()
	}
	m.api.srv.Publish(api.Project(snap))
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

// apiCommand is /api, /api on, /api off, /api disconnect, /api resume.
func (m Model) apiCommand(args []string) (tea.Model, tea.Cmd) {
	sub := ""
	if len(args) > 0 {
		sub = strings.ToLower(args[0])
	}
	switch sub {
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
		it := m.store.AddAPIOn("Turn on den-api.sock for apps such as Drey?", body)
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
		return m.say("Apps may connect again.", false)
	}
	return m.say("/api [on|off|disconnect|resume]", true)
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
		line := "\n" + sanitize.Label(c.Label, 40) + " · pid " + strconv.Itoa(c.PID) + " · " + sanitize.Label(c.Exe, 120) + " · since " + c.Since.Format("15:04")
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
	b.WriteString("\nlane sessions and asks: " + filepath.Join(stateDir(), "drey-chat.jsonl"))
	return b.String()
}

// apiChip is the header's API indicator, "" when the API is off. The names
// are the base names of the exes Den read, never what an app says it is.
func (m Model) apiChip() string {
	srv := m.api.srv
	if srv == nil {
		if m.cfg.API.Enabled && m.api.err != "" {
			return m.st.err.Render("API: " + m.api.err)
		}
		return ""
	}
	if srv.Paused() {
		return ""
	}
	if !srv.Healthy() {
		return m.st.err.Render("⇄ socket replaced")
	}
	clients := srv.Clients()
	names := map[string]bool{}
	asking := false
	for _, c := range clients {
		if c.Agent {
			continue
		}
		names[sanitize.Label(c.Label, 20)] = true
		asking = asking || c.Asking
	}
	if len(names) == 0 {
		return "API on"
	}
	var list []string
	for n := range names {
		list = append(list, n)
	}
	sort.Strings(list)
	chip := "⇄ " + strings.Join(list, ", ") + " connected"
	if asking {
		chip += " · asking"
	}
	return m.st.attn.Render(chip)
}

// isLane reports whether a discovered session is the API's read-only lane.
func (m Model) isLane(a fleet.Agent) bool {
	p := m.lanePID()
	return p > 0 && a.Session.PID == p
}
