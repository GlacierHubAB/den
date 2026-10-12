package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/remote"
	"github.com/GlacierHubAB/den/internal/sanitize"
	"github.com/GlacierHubAB/den/internal/spawn"
)

// Starting agents on remote hosts (docs/REMOTE.md). The human types
// /spawn @host <folder> … <brief>; Den asks the host for a dry run and
// shows what would start (the host, the route, the folder as the host
// resolved it, the model, the brief), and a second Enter starts it: that
// preview is the confirmation, with no inbox step. `den remote spawn` asks
// over den.sock instead, and since any process here can run it, Den puts
// it to the human as one inbox question with the full brief: ^Y starts
// it, there is no always, and ten minutes without an answer declines it.
// Once the agent runs, Enter or /open on it opens a terminal attached to
// its tmux session on the host.

// spawnAnswerWait is how long a spawn question waits before it declines.
var spawnAnswerWait = 10 * time.Minute

// openAttach opens the window; tests swap it.
var openAttach = func(ctx context.Context, title string, argv []string) error {
	home, _ := os.UserHomeDir()
	_, err := spawn.OpenWindow(ctx, home, title, argv, nil)
	return err
}

// remoteSpawnState is the spawn the human is looking at, and the questions
// in the inbox.
type remoteSpawnState struct {
	ready *spawnPlan            // checked, waiting for Enter or Esc
	asks  map[string]*spawnPlan // inbox spawn questions, by item id
	busy  bool                  // a check or start is under way
	// leftovers are the sessions each leftover question would end.
	leftovers map[string][]remote.Spawned
}

// spawnPlan is one spawn: what was asked and what the host's check said.
type spawnPlan struct {
	host    string
	req     remote.SpawnRequest
	brief   string // as typed, before Den's introduction
	chk     remote.SpawnResult
	askedBy string
	cli     *bridge.Request // the command that asked, for a question
}

type spawnCheckedMsg struct {
	plan *spawnPlan
	res  remote.SpawnResult
	err  error
}

type spawnStartedMsg struct {
	plan   *spawnPlan
	itemID string
	res    remote.SpawnResult
	err    error
}

type spawnExpiredMsg struct{ itemID string }

type attachOpenedMsg struct {
	name string
	err  error
}

// spawnBrief is the agent's first prompt: the brief, introduced so the
// agent knows it came from its human through Den on another machine.
func spawnBrief(brief, worktree string) string {
	p := "Brief from your human, sent through Den on another machine, where they follow your work: permission prompts and questions reach them there.\n\n" + strings.TrimSpace(brief)
	if worktree != "" {
		p += "\n\n" + spawn.WorktreeNote(worktree)
	}
	return p
}

// spawnUsage is /spawn's help.
const spawnUsage = "/spawn @host <folder> [model=haiku|sonnet|opus] [name=<peer>] [worktree=<name>] <brief>"

// spawnCommand is /spawn: it parses the line and asks the host for a dry run.
func (m Model) spawnCommand(text string) (tea.Model, tea.Cmd) {
	if m.remote.feed == nil {
		return m.say("/spawn starts agents on remote hosts, and none is set up for spawn (see docs/REMOTE.md).", true)
	}
	if m.rspawn.busy {
		return m.say("A spawn is being checked or started; wait for it.", true)
	}
	plan, err := parseSpawn(text)
	if err != nil {
		return m.say(err.Error(), true)
	}
	h, ok := m.hostStatus(plan.host)
	switch {
	case !ok:
		return m.say("No host called "+plan.host+". Hosts that allow spawn: "+m.spawnHosts()+".", true)
	case h.Spawn == "" || h.Spawn == remote.SpawnOff || len(h.SpawnDirs) == 0:
		return m.say(plan.host+" does not allow spawn (set spawn and spawn_dirs for it in remotes.toml). Hosts that do: "+m.spawnHosts()+".", true)
	}
	m.rspawn.busy, m.rspawn.ready = true, nil
	m.tab, m.focus, m.remote.listing = tabFleet, paneAgents, false
	m.input.SetValue("")
	m.fitInput()
	feed := m.remote.feed
	cmd := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		res, err := feed.Spawn(ctx, plan.host, plan.req)
		return spawnCheckedMsg{plan: plan, res: res, err: err}
	}
	m.flash, m.flashErr = "Checking with "+plan.host+"…", false
	return m, cmd
}

// spawnHosts lists the hosts whose entries allow spawn.
func (m Model) spawnHosts() string {
	var out []string
	for _, h := range m.remote.hosts {
		if h.Spawn != "" && h.Spawn != remote.SpawnOff && len(h.SpawnDirs) > 0 {
			out = append(out, "@"+h.Label)
		}
	}
	if len(out) == 0 {
		return "none"
	}
	return strings.Join(out, ", ")
}

// parseSpawn reads /spawn @host <folder> [key=value]… <brief>. The brief is
// the rest of the line as typed, newlines and all.
func parseSpawn(text string) (*spawnPlan, error) {
	rest := strings.TrimSpace(text)
	rest = strings.TrimSpace(rest[len(strings.Fields(rest)[0]):]) // the command word
	next := func() string {
		rest = strings.TrimLeft(rest, " \t")
		i := strings.IndexAny(rest, " \t\n")
		if i < 0 {
			i = len(rest)
		}
		tok := rest[:i]
		rest = rest[i:]
		return tok
	}
	host := next()
	if !strings.HasPrefix(host, "@") || len(host) < 2 {
		return nil, fmt.Errorf("name the host: %s", spawnUsage)
	}
	p := &spawnPlan{host: strings.ToLower(host[1:])}
	p.req.Dir = next()
	if p.req.Dir == "" {
		return nil, fmt.Errorf("name the folder: %s", spawnUsage)
	}
	for {
		save := rest
		tok := next()
		k, v, ok := strings.Cut(tok, "=")
		if !ok {
			rest = save
			break
		}
		switch k {
		case "model":
			p.req.Model = v
		case "name":
			p.req.Peer = v
		case "worktree":
			p.req.Worktree = v
		default:
			rest = save
		}
		if rest == save {
			break
		}
	}
	p.brief = strings.TrimSpace(rest)
	if p.brief == "" {
		return nil, fmt.Errorf("the brief is missing: %s", spawnUsage)
	}
	p.req.Brief = spawnBrief(p.brief, p.req.Worktree)
	p.req.Check = true
	return p, nil
}

// spawnChecked takes the host's dry run: the preview for /spawn, or the
// inbox question for the CLI.
func (m *Model) spawnChecked(msg spawnCheckedMsg) tea.Cmd {
	m.rspawn.busy = false
	p := msg.plan
	if msg.err != nil {
		why := "Nothing started on " + p.host + ": " + sanitize.Line(msg.err.Error())
		if p.cli != nil {
			p.cli.Reply(bridge.Decision{Behavior: "deny", Message: why})
			return nil
		}
		m.flash, m.flashErr = why, true
		return tea.Tick(8*time.Second, func(time.Time) tea.Msg { return flashClearMsg{} })
	}
	p.chk = msg.res
	p.req.SessionID, p.req.Peer, p.req.Check = msg.res.SessionID, msg.res.Peer, false
	if p.cli == nil {
		m.rspawn.ready = p
		m.flash, m.flashErr = "Enter starts "+p.chk.Critter+" on "+p.host+", Esc cancels.", false
		return nil
	}
	if p.cli.HungUp() {
		return nil
	}
	it := m.store.AddSpawn(p.host, "Start "+p.chk.Critter+" on "+p.host+"?", spawnBody(p), p.cli)
	if m.rspawn.asks == nil {
		m.rspawn.asks = map[string]*spawnPlan{}
	}
	m.rspawn.asks[it.ID] = p
	m.flash, m.flashErr = "Start "+p.chk.Critter+" on "+p.host+"? ^Y starts it, ^N declines.", false
	m.refresh()
	id := it.ID
	return tea.Tick(spawnAnswerWait, func(time.Time) tea.Msg { return spawnExpiredMsg{itemID: id} })
}

// spawnBody is what the human confirms: everything that decides what runs.
func spawnBody(p *spawnPlan) []string {
	route := "tmux on " + p.host + "; survives disconnects and closed windows"
	model := p.req.Model
	if model == "" {
		model = "the host's default"
	}
	out := []string{
		"host      " + p.host,
		"route     " + route,
		"folder    " + p.chk.Dir,
		"peer      " + p.chk.Peer + "  (tmux session " + p.chk.TmuxName + ")",
		"model     " + model,
	}
	if p.req.Worktree != "" {
		out = append(out, "worktree  "+p.req.Worktree+" (branch "+spawn.WorktreeBranch(p.req.Worktree)+")")
	}
	out = append(out, "skills    none (role skills are not shipped to hosts yet)")
	if p.chk.Warning != "" {
		out = append(out, "note      "+p.chk.Warning)
	}
	if p.askedBy != "" {
		out = append(out, "asked by  "+sanitize.Label(p.askedBy, 120))
	}
	out = append(out, "", "brief:")
	return append(out, strings.Split(sanitize.Text(p.brief), "\n")...)
}

// spawnPreviewLines replace the details pane while a /spawn waits for Enter.
func (m Model) spawnPreviewLines(w int) []string {
	s := m.st
	p := m.rspawn.ready
	out := []string{"", "  " + s.bright.Render("Start "+p.chk.Critter+" on "+p.host), ""}
	for _, l := range spawnBody(p) {
		out = append(out, "  "+s.fg.Render(l))
	}
	return append(out, "", "  "+s.key.Render("Enter")+s.keyLabel.Render(" start it  ")+s.key.Render("Esc")+s.keyLabel.Render(" cancel"))
}

// startSpawn runs a checked spawn.
func (m *Model) startSpawn(p *spawnPlan, itemID string) tea.Cmd {
	m.rspawn.busy = true
	feed := m.remote.feed
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		res, err := feed.Spawn(ctx, p.host, p.req)
		return spawnStartedMsg{plan: p, itemID: itemID, res: res, err: err}
	}
}

// confirmSpawn is Enter on a /spawn preview.
func (m Model) confirmSpawn() (tea.Model, tea.Cmd) {
	p := m.rspawn.ready
	m.rspawn.ready = nil
	m.flash, m.flashErr = "Starting "+p.chk.Critter+" on "+p.host+"…", false
	return m, m.startSpawn(p, "")
}

// spawnStarted says what came of a start, settles its question, and opens
// the attach window.
func (m *Model) spawnStarted(msg spawnStartedMsg) tea.Cmd {
	m.rspawn.busy = false
	p := msg.plan
	outcome := fmt.Sprintf("started %s on %s in tmux session %s", msg.res.Critter, p.host, msg.res.TmuxName)
	if msg.err != nil {
		outcome = "not started on " + p.host + ": " + sanitize.Line(msg.err.Error())
	}
	if msg.res.Warning != "" && msg.err == nil {
		outcome += " (" + msg.res.Warning + ")"
	}
	for _, it := range m.store.Items() {
		if it.ID == msg.itemID {
			m.store.Settle(it, msg.err == nil, outcome)
		}
	}
	m.flash, m.flashErr = strings.ToUpper(outcome[:1])+outcome[1:], msg.err != nil
	m.refresh()
	cmds := []tea.Cmd{tea.Tick(6*time.Second, func(time.Time) tea.Msg { return flashClearMsg{} })}
	if msg.err == nil || strings.Contains(msg.err.Error(), "window") {
		cmds = append(cmds, m.openRemoteAgent(p.host, msg.res))
	}
	return tea.Batch(cmds...)
}

// openRemoteAgent opens the window of an agent just started on host, unless
// remote.open_window is off. Every spawn path calls it after the host's
// result, so a remote agent's window lands where the human is looking.
func (m *Model) openRemoteAgent(host string, res remote.SpawnResult) tea.Cmd {
	if res.TmuxName == "" || !m.cfg.Remote.OpenWindow || m.remote.feed == nil {
		return nil
	}
	return m.attachWindow(host, res.Peer, res.TmuxName)
}

// attachWindow opens a terminal attached to an agent's tmux session on host.
func (m Model) attachWindow(host, peer, name string) tea.Cmd {
	argv, err := m.remote.feed.AttachCommand(host, name)
	if err != nil {
		return func() tea.Msg { return attachOpenedMsg{name: name, err: err} }
	}
	title := sanitize.Line("den · " + peer + "@" + host)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return attachOpenedMsg{name: name, err: openAttach(ctx, title, argv)}
	}
}

func (m *Model) attachOpened(msg attachOpenedMsg) tea.Cmd {
	if msg.err == nil {
		return nil
	}
	m.flash, m.flashErr = "Could not open a window for "+msg.name+": "+sanitize.Line(msg.err.Error())+"; it keeps running in tmux on its host", true
	return tea.Tick(8*time.Second, func(time.Time) tea.Msg { return flashClearMsg{} })
}

// decideSpawn is ^Y or ^N on a spawn question. There is no always: each
// remote spawn is asked for on its own.
func (m Model) decideSpawn(it *inbox.Item, verb string) (tea.Model, tea.Cmd) {
	if verb == "always" {
		return m.say("There is no always for starting agents on a host: ^Y starts this one, ^N declines.", true)
	}
	p, ok := m.rspawn.asks[it.ID]
	if !ok {
		m.store.Settle(it, false, "the spawn was lost; nothing started")
		return m.say("That spawn is stale; nothing started.", true)
	}
	delete(m.rspawn.asks, it.ID)
	if verb != "allow" {
		m.store.Settle(it, false, "declined in Den; nothing started")
		m.refresh()
		return m.say("Declined the spawn on "+p.host+".", false)
	}
	it.Resolved = "starting…"
	m.flash, m.flashErr = "Starting "+p.chk.Critter+" on "+p.host+"…", false
	return m, m.startSpawn(p, it.ID)
}

// expireSpawn declines a spawn question nobody answered.
func (m *Model) expireSpawn(msg spawnExpiredMsg) {
	if _, ok := m.rspawn.asks[msg.itemID]; !ok {
		return
	}
	delete(m.rspawn.asks, msg.itemID)
	for _, it := range m.store.Items() {
		if it.ID == msg.itemID && it.State == inbox.Waiting {
			m.store.Settle(it, false, "declined (no answer)")
		}
	}
	m.refresh()
}

// handleSpawnRequest is `den remote spawn`: checked here and on the host
// before it reaches the inbox, so a refused folder never does.
func (m *Model) handleSpawnRequest(r *bridge.Request) tea.Cmd {
	deny := func(msg string) { r.Reply(bridge.Decision{Behavior: "deny", Message: msg}) }
	if r.Host != "" {
		deny("a spawn can only be asked for from this machine")
		return nil
	}
	var in bridge.SpawnInput
	_ = json.Unmarshal(r.Payload, &in)
	if m.remote.feed == nil {
		deny("this Den has no remote hosts")
		return nil
	}
	if _, ok := m.hostStatus(in.Host); !ok {
		deny(fmt.Sprintf("no remote host called %q in this Den", sanitize.Label(in.Host, 40)))
		return nil
	}
	p := &spawnPlan{host: in.Host, brief: strings.TrimSpace(in.Brief), askedBy: in.AskedBy, cli: r}
	p.req = remote.SpawnRequest{Dir: in.Dir, Peer: in.Name, Model: in.Model, Worktree: in.Worktree, Brief: spawnBrief(in.Brief, in.Worktree), Check: true}
	if p.brief == "" {
		deny("the brief is empty")
		return nil
	}
	feed := m.remote.feed
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		res, err := feed.Spawn(ctx, p.host, p.req)
		return spawnCheckedMsg{plan: p, res: res, err: err}
	}
}

// attachOrHint is Enter or /open on a remote agent: a terminal attached
// to its tmux session when Den started it there, else where to find it.
func (m Model) attachOrHint(a remoteAgentRef) (tea.Model, tea.Cmd) {
	if a.tmux == "" {
		return m.say(a.hint, false)
	}
	m.flash, m.flashErr = "Opening "+a.name+" in tmux on "+a.host+"…", false
	return m, m.attachWindow(a.host, a.peer, a.tmux)
}

// remoteAgentRef is what opening a remote agent needs.
type remoteAgentRef struct {
	host, name, peer, tmux, hint string
}

// leftoverCommand is /remote cleanup <host>: one question to end every
// leftover tmux session Den started there.
func (m Model) leftoverCommand(host string) (tea.Model, tea.Cmd) {
	host = strings.TrimPrefix(host, "@")
	if _, ok := m.hostStatus(host); !ok {
		return m.say("No host called "+host+". /remote lists them.", true)
	}
	left := m.remote.feed.Leftovers(host)
	if len(left) == 0 {
		return m.say("No leftover tmux sessions of Den's on "+host+".", false)
	}
	body := []string{"These tmux sessions on " + host + " were started by Den and have no Claude Code running in them:"}
	for _, l := range left {
		body = append(body, "  "+l.TmuxName)
	}
	body = append(body, "", "^Y ends them (tmux kill-session); nothing else on the host is touched.")
	it := m.store.AddLeftover(host, fmt.Sprintf("End %d leftover tmux session(s) on %s?", len(left), host), body)
	if m.rspawn.leftovers == nil {
		m.rspawn.leftovers = map[string][]remote.Spawned{}
	}
	m.rspawn.leftovers[it.ID] = left
	m.tab = tabInbox
	m.refresh()
	return m.say("Asked in the inbox: ^Y ends them, ^N keeps them.", false)
}

// decideLeftover is ^Y or ^N on a leftover question.
func (m Model) decideLeftover(it *inbox.Item, verb string) (tea.Model, tea.Cmd) {
	left, ok := m.rspawn.leftovers[it.ID]
	delete(m.rspawn.leftovers, it.ID)
	host := strings.TrimPrefix(it.SessionID, inbox.HostPrefix)
	if !ok || verb != "allow" {
		m.store.Settle(it, false, "kept")
		m.refresh()
		return m.say("Kept the leftover sessions on "+host+".", false)
	}
	it.Resolved = "ending them…"
	feed := m.remote.feed
	id := it.ID
	return m, func() tea.Msg {
		var failed []string
		for _, l := range left {
			if err := feed.Cleanup(host, l.SessionID, l.TmuxName); err != nil {
				failed = append(failed, l.TmuxName+": "+err.Error())
			}
		}
		return leftoverDoneMsg{itemID: id, host: host, n: len(left), failed: failed}
	}
}

type leftoverDoneMsg struct {
	itemID string
	host   string
	n      int
	failed []string
}

func (m *Model) leftoverDone(msg leftoverDoneMsg) {
	outcome := fmt.Sprintf("ended %d leftover session(s) on %s", msg.n-len(msg.failed), msg.host)
	if len(msg.failed) > 0 {
		outcome += "; not ended: " + strings.Join(msg.failed, "; ")
	}
	for _, it := range m.store.Items() {
		if it.ID == msg.itemID {
			m.store.Settle(it, len(msg.failed) == 0, outcome)
		}
	}
	m.flash, m.flashErr = sanitize.Line(strings.ToUpper(outcome[:1])+outcome[1:]), len(msg.failed) > 0
	m.refresh()
}
