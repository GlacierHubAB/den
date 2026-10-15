package ui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/config"
	"github.com/GlacierHubAB/den/internal/desktop"
	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/hypr"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/procs"
	"github.com/GlacierHubAB/den/internal/sanitize"
	"github.com/GlacierHubAB/den/internal/spawn"
)

// A desktop mission starts from one card (DesktopMission): the asker names
// a plan and apps from the human's [desktop.apps] config; Den shows the
// plan, the apps and what the agent will and will not have. On ^Y Den
// picks an empty workspace, launches the apps itself onto it, writes the
// agent's settings and starts the agent like the orchestrator, with only
// the desktop tools.

// deskOffer is a mission card's request, until it is answered.
type deskOffer struct {
	in   bridge.DesktopMissionInput
	apps []desktop.App
}

// deskLaunched is one app window Den opened for a mission.
type deskLaunched struct {
	App     desktop.App
	Address string
	PID     int  // the window's process
	Joined  bool // it is a process that was running before: the app's own, signed-in instance
}

// deskPrepared is a mission whose workspace and apps are ready.
type deskPrepared struct {
	itemID    string
	offer     deskOffer
	id        string
	workspace int
	dir       string
	windows   []deskLaunched
	err       error
}

// deskSpawned is the agent's start.
type deskSpawned struct {
	itemID  string
	mission string
	res     spawn.Result
	err     error
}

// Seams for tests.
var (
	deskApps = func() ([]desktop.App, []string) { return desktop.ParseApps(config.DesktopApps()) }
	// deskLaunch starts an app for a mission and returns its new window.
	deskLaunch = launchApp
	deskMove   = func(addr string, ws int) error { return hypr.MoveToWorkspace(addr, strconv.Itoa(ws)) }
	deskClose  = hypr.CloseWindow
	deskSpawn  = spawn.Run
	// deskSession finds a session's Claude Code process by session id.
	deskSession = func(id string) (int, bool) {
		sessions, _ := discover.Scan()
		for _, s := range sessions {
			if s.SessionID == id && s.Alive {
				return s.PID, true
			}
		}
		return 0, false
	}
	deskStateDir = func() string { return filepath.Join(stateDir(), "desktop") }
	deskSocket   = bridge.SocketPath
)

// handleDesktopMissionRequest checks a desktop_mission call and puts it on
// a card. Nothing starts before ^Y.
func (m *Model) handleDesktopMissionRequest(r *bridge.Request) tea.Cmd {
	deny := func(why string) tea.Cmd {
		r.Reply(bridge.Decision{Behavior: "deny", Message: why})
		return nil
	}
	if r.Host != "" {
		return deny("desktop missions are this machine's only")
	}
	var in bridge.DesktopMissionInput
	if json.Unmarshal(r.Payload, &in) != nil || strings.TrimSpace(in.Plan) == "" || len(in.Apps) == 0 {
		return deny("a desktop mission needs a plan and apps")
	}
	if in.Model == "" {
		in.Model = "sonnet"
	}
	if !spawn.ValidModel(in.Model) {
		return deny("unknown model " + sanitize.Label(in.Model, 20))
	}
	if _, err := spawn.ExpandDir(in.Dir); err != nil {
		return deny(err.Error())
	}
	if err := deskVerify(); err != nil {
		return deny("desktop control is not ready: " + sanitize.Line(err.Error()))
	}
	all, problems := deskApps()
	var names []string
	for _, a := range all {
		names = append(names, a.Name)
	}
	var apps []desktop.App
	for _, want := range in.Apps {
		found := false
		for _, a := range all {
			if a.Name == want {
				apps, found = append(apps, a), true
			}
		}
		if !found {
			why := fmt.Sprintf("no app %q in Den's [desktop.apps] config", sanitize.Label(want, 40))
			if len(names) > 0 {
				why += "; it has: " + strings.Join(names, ", ")
			}
			if len(problems) > 0 {
				why += " (" + sanitize.Line(strings.Join(problems, "; ")) + ")"
			}
			return deny(why)
		}
	}
	for _, it := range m.store.Items() {
		if it.Kind == inbox.DesktopMission && it.State == inbox.Waiting {
			return deny("a desktop mission is already waiting for the human")
		}
	}
	for _, dm := range m.desk.missions {
		if dm.Ended == "" {
			return deny("a desktop mission is already running; one at a time")
		}
	}
	body := []string{"Asked by " + sanitize.Label(in.AskedBy, 60) + " (as it says). The plan, in its words:"}
	for _, l := range strings.Split(sanitize.Text(strings.TrimSpace(in.Plan)), "\n") {
		body = append(body, "  "+l)
	}
	body = append(body, "Den picks an empty workspace (9 down to 5), opens these apps there itself and starts the agent on it:")
	for _, a := range apps {
		line := fmt.Sprintf("  %s: %s, window class %s", a.Name, sanitize.Label(strings.Join(a.Command, " "), 120), sanitize.Label(a.Class, 60))
		if len(a.Profile) > 0 {
			line += ", on a profile of its own"
		} else {
			line += ", on your normal profile"
		}
		switch a.Outward {
		case desktop.OutwardAsk:
			line += "; every click and keystroke there asks you"
		case desktop.OutwardWords:
			line += "; Send-like clicks and Enter there ask you"
		}
		if a.IfRunning == "warn" {
			line += "; if it is already running, it joins that instance and its sign-ins"
		} else {
			line += "; refused if it joins an instance already running"
		}
		body = append(body, line)
	}
	if len(in.HumanSteps) > 0 {
		body = append(body, "Your turn (the mission pauses for each, and the agent never sees what you type):")
		for _, s := range in.HumanSteps {
			body = append(body, "  "+sanitize.Label(s, 160))
		}
	}
	body = append(body,
		"The agent never types credentials. It has only the desktop tools: no shell, files, web, messages, app launching, keybinds or clipboard.",
		"Model: "+in.Model+". Stop it any time: /desktop stop, or den desktop stop.",
	)
	it := m.store.AddDesktopMission("Start a desktop mission for "+sanitize.Label(in.AskedBy, 40)+"?", body, r)
	if m.desk.offers == nil {
		m.desk.offers = map[string]deskOffer{}
	}
	m.desk.offers[it.ID] = deskOffer{in: in, apps: apps}
	m.flash, m.flashErr = it.Title+" ^Y starts it, ^N declines.", false
	m.refresh()
	return nil
}

// decideDesktopMission is ^Y or ^N on a mission card.
func (m Model) decideDesktopMission(it *inbox.Item, verb string) (tea.Model, tea.Cmd) {
	if why := deskTTYOnly(it, verb); why != "" {
		return m.say(why, true)
	}
	offer, ok := m.desk.offers[it.ID]
	delete(m.desk.offers, it.ID)
	if !ok {
		m.store.Settle(it, false, "the mission was lost; nothing started")
		return m.say("That mission is stale; nothing started.", true)
	}
	if verb != "allow" {
		m.store.Settle(it, false, "declined in Den; nothing started")
		m.refresh()
		return m.say("Declined the desktop mission.", false)
	}
	it.Resolved = "starting…"
	id := it.ID
	cmd := func() tea.Msg { return prepareMission(id, offer) }
	model, say := m.say("Preparing the desktop mission…", false)
	return model, tea.Batch(say, cmd)
}

// prepareMission picks the workspace and launches the apps. It runs off
// the update loop.
func prepareMission(itemID string, offer deskOffer) deskPrepared {
	p := deskPrepared{itemID: itemID, offer: offer, id: newMissionID()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	snap, err := deskSnapshot(ctx)
	if err != nil {
		p.err = err
		return p
	}
	if p.workspace = freeWorkspace(snap); p.workspace == 0 {
		p.err = errors.New("no empty workspace from 9 down to 5; Den never moves your windows away to make one")
		return p
	}
	p.dir = filepath.Join(deskStateDir(), p.id)
	if err := os.MkdirAll(p.dir, 0o700); err != nil {
		p.err = err
		return p
	}
	os.Chmod(filepath.Dir(p.dir), 0o700)
	for _, a := range offer.apps {
		l, err := deskLaunch(ctx, a, p.dir, snap)
		if err != nil {
			p.err = fmt.Errorf("%s: %v", a.Name, err)
			return p
		}
		p.windows = append(p.windows, l)
		if l.Joined && a.IfRunning != "warn" {
			p.err = fmt.Errorf("%s opened its window in an instance that was already running (with its sign-ins); refused, as its config says", a.Name)
			return p
		}
		if err := deskMove(l.Address, p.workspace); err != nil {
			p.err = fmt.Errorf("%s: move to workspace %d: %v", a.Name, p.workspace, err)
			return p
		}
	}
	return p
}

// freeWorkspace is the first workspace from 9 down to 5 with no window.
func freeWorkspace(s desktop.Snapshot) int {
	for ws := 9; ws >= 5; ws-- {
		used := false
		for _, w := range s.Windows {
			if w.Workspace == ws {
				used = true
			}
		}
		if !used {
			return ws
		}
	}
	return 0
}

// finishPrepare registers the mission and starts the agent, or undoes the
// launch.
func (m *Model) finishPrepare(p deskPrepared) tea.Cmd {
	var it *inbox.Item
	for _, x := range m.store.Items() {
		if x.ID == p.itemID {
			it = x
		}
	}
	fail := func(why string) tea.Cmd {
		for _, l := range p.windows {
			if !l.Joined {
				deskClose(l.Address)
			}
		}
		if it != nil {
			m.store.Settle(it, false, "approved, but it did not start: "+why)
		}
		m.flash, m.flashErr = "The desktop mission did not start: "+why, true
		m.refresh()
		return nil
	}
	if p.err != nil {
		return fail(sanitize.Line(p.err.Error()))
	}
	den := deskExe()
	sock, err := filepath.Abs(deskSocket())
	if err != nil {
		return fail(err.Error())
	}
	guard, err := desktop.WriteSettings(filepath.Join(p.dir, "settings.json"), den, sock)
	if err != nil {
		return fail("settings: " + err.Error())
	}
	dm := &deskMission{
		Mission:   desktop.Mission{ID: p.id, Workspace: p.workspace, Apps: p.offer.apps, Windows: map[string]bool{}, PIDs: map[int]bool{}},
		SessionID: newSessionID(),
		Dir:       p.dir,
		Guard:     guard.Check,
		AskedBy:   p.offer.in.AskedBy,
	}
	for _, l := range p.windows {
		dm.Windows[l.Address] = true
		if !l.Joined {
			// A process Den launched: its later windows (a dialog) are the
			// mission's too. A joined instance's are not.
			dm.PIDs[l.PID] = true
		}
	}
	if err := recordDeskSession(dm.SessionID); err != nil {
		// Without the record, a restore could later resume it with
		// ordinary tools: no agent starts.
		return fail("cannot record the desktop session: " + err.Error())
	}
	m.addDeskMission(dm)
	dm.save() // its session id is on disk before the agent starts
	prompt := desktop.Brief(p.offer.in.Plan, p.offer.apps, p.workspace, p.offer.in.HumanSteps)
	launch := desktop.AgentLaunch{SessionID: dm.SessionID, Mission: dm.ID, Den: den, Socket: sock, Settings: guard.Settings, Model: p.offer.in.Model, Prompt: prompt}
	if _, err := desktop.AgentArgv(launch); err != nil {
		m.endDeskMission(dm, err.Error())
		return fail(err.Error())
	}
	req := spawn.Request{Dir: p.offer.in.Dir, Agent: true, Brief: prompt, Name: "desktop", Model: p.offer.in.Model,
		Custom: func(name string) []string {
			launch.Name = name
			argv, _ := desktop.AgentArgv(launch)
			return desktop.TerminalArgv(argv, den)
		},
		NoSessionWait: true,
	}
	id, missionID := p.itemID, dm.ID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		res, err := deskSpawn(ctx, req)
		return deskSpawned{itemID: id, mission: missionID, res: res, err: err}
	}
}

// finishSpawn records the agent's process and answers the asker.
func (m *Model) finishSpawn(s deskSpawned) tea.Cmd {
	dm := m.deskMission(s.mission)
	var it *inbox.Item
	for _, x := range m.store.Items() {
		if x.ID == s.itemID {
			it = x
		}
	}
	if dm == nil {
		return nil
	}
	if s.err != nil {
		m.endDeskMission(dm, "the agent did not start")
		m.restoreDeskMission(dm)
		if it != nil {
			m.store.Settle(it, false, "approved, but the agent did not start: "+sanitize.Line(s.err.Error()))
		}
		m.refresh()
		return nil
	}
	dm.AgentName = s.res.Name
	dm.launchedAt = time.Now()
	if pid, ok := deskSession(dm.SessionID); ok && dm.AgentPID == 0 {
		dm.AgentPID, dm.AgentStart, dm.AgentExe = pid, deskStart(pid), deskExeOf(pid)
	}
	dm.save()
	msg := fmt.Sprintf("Started desktop agent %q on workspace %d with %d app window(s). It reports to Den when done; Den passes the report to you.", s.res.Name, dm.Workspace, len(dm.Windows))
	if it != nil {
		m.store.Settle(it, true, msg)
	}
	m.flash, m.flashErr = msg, false
	m.refresh()
	return m.startDeskWatch(dm)
}

// adoptAgent learns a mission agent's process from its proxy's first
// call, when the session had not registered by the time Den looked: the
// proxy's parent must be the session Den started, by its session id.
func adoptAgent(dm *deskMission, proxyPID int) bool {
	parent, _, ok := deskParent(proxyPID)
	if !ok || parent <= 1 {
		return false
	}
	pid, ok := deskSession(dm.SessionID)
	if !ok {
		// A print-mode agent may write no session file: then its parent
		// must be Claude Code started with this mission's session id.
		pid, ok = parent, claudeWithSession(parent, dm.SessionID)
	}
	if !ok || pid != parent {
		return false
	}
	dm.AgentPID, dm.AgentStart, dm.AgentExe = pid, deskStart(pid), deskExeOf(pid)
	return dm.AgentStart != ""
}

// launchApp starts an app (argv, no shell, a session of its own) and
// waits for its new window. Joined says the window belongs to a process
// that was running before: the app handed the request to its running
// instance.
func launchApp(ctx context.Context, a desktop.App, dir string, before desktop.Snapshot) (deskLaunched, error) {
	l := deskLaunched{App: a}
	seen := map[string]bool{}
	for _, w := range before.Windows {
		seen[w.Address] = true
	}
	argv := a.Argv(dir)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return l, err
	}
	started := cmd.Process.Pid
	go cmd.Wait()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		snap, err := deskSnapshot(ctx)
		if err == nil {
			for _, w := range snap.Windows {
				if seen[w.Address] || w.Class != a.Class {
					continue
				}
				l.Address, l.PID = w.Address, w.PID
				l.Joined = true
				for _, p := range procs.Ancestors(w.PID) {
					if p.PID == started {
						l.Joined = false
					}
				}
				return l, nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return l, errors.New("no window of class " + a.Class + " appeared in 15 s")
}

// restoreDeskMission closes the windows Den launched for a mission (never
// a joined instance's) and leaves everything else as it is.
func (m *Model) restoreDeskMission(dm *deskMission) {
	for addr := range dm.Windows {
		deskClose(addr)
	}
}

func newMissionID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

// newSessionID is a random (version 4) UUID for claude --session-id.
func newSessionID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// deskCmdline is a process's argv; tests replace it.
var deskCmdline = func(pid int) []string {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
}

// claudeWithSession reports whether pid is Claude Code run with
// --session-id id.
func claudeWithSession(pid int, id string) bool {
	// The executable /proc reports, which a process cannot set, must be
	// Claude Code's; the command line alone could say anything.
	argv := deskCmdline(pid)
	if len(argv) == 0 || filepath.Base(deskExeOf(pid)) != "claude" {
		return false
	}
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "--session-id" && argv[i+1] == id && id != "" {
			return true
		}
	}
	return false
}
