package ui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/badge"
	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/config"
	"github.com/GlacierHubAB/den/internal/cost"
	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/events"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/gitinfo"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/notify"
	"github.com/GlacierHubAB/den/internal/orch"
	"github.com/GlacierHubAB/den/internal/remote"
	"github.com/GlacierHubAB/den/internal/theme"
	"github.com/GlacierHubAB/den/internal/transcript"
)

// fakeFeed stands in for remote.Manager: no ssh, the hosts and agents as
// the test sets them.
type fakeFeed struct {
	hosts     []remote.HostStatus
	agents    []fleet.Agent
	ch        chan remote.Update
	reconnect []string
	// What Send and CloseAgent got, and what they answer.
	sent      []string // "host/session: text"
	closed    []string // "host/session force"
	actErr    error    // Actionable's answer
	sendErr   error
	closeErr  error
	closeKill bool
}

func (f *fakeFeed) Start()                        {}
func (f *fakeFeed) Updates() <-chan remote.Update { return f.ch }
func (f *fakeFeed) Hosts() []remote.HostStatus    { return f.hosts }
func (f *fakeFeed) Agents() []fleet.Agent         { return f.agents }
func (f *fakeFeed) Reconnect(label string)        { f.reconnect = append(f.reconnect, label) }
func (f *fakeFeed) Close()                        {}
func (f *fakeFeed) Actionable(string) error       { return f.actErr }
func (f *fakeFeed) Send(host, sid, text string) error {
	f.sent = append(f.sent, host+"/"+sid+": "+text)
	return f.sendErr
}
func (f *fakeFeed) CloseAgent(host, sid string, force bool) (bool, error) {
	f.closed = append(f.closed, fmt.Sprintf("%s/%s force=%v", host, sid, force))
	return f.closeKill, f.closeErr
}
func (f *fakeFeed) setState(label string, s remote.State) {
	for i := range f.hosts {
		if f.hosts[i].Label == label {
			f.hosts[i].State = s
		}
	}
	for i := range f.agents {
		if f.agents[i].Host == label {
			f.agents[i].HostState, f.agents[i].Stale = string(s), s != remote.Live
		}
	}
}

// dupSession is one session id on this machine, on devbox and on gpu, so
// any keying by session id alone would mix them.
const dupSession = "11111111-2222-4333-8444-555555555555"

func remoteAgent(host, sid, name string, st fleet.Status, usd float64) fleet.Agent {
	return fleet.Agent{
		Session: discover.Session{SessionID: sid, Name: "user-" + host, Version: "2.1.300", StartedAt: time.Now().Add(-time.Hour).UnixMilli(), StatusAt: time.Now().Add(-5 * time.Minute).UnixMilli(), Alive: true},
		Name:    name, Labels: badge.Labels{Role: "coding agent", Task: "port the parser"}, Status: st, Model: "claude-fable-5-1",
		Git: gitinfo.Info{Repo: "den", Branch: "main"}, HasGit: true,
		Cost: cost.Summary{Tokens: transcript.Tokens{Output: 1000}, USD: usd},
		Host: host, HostState: "live",
	}
}

// remoteModel is a fleet with one local agent and the same session id on
// devbox (live) and gpu (offline since 14:02), plus a second gpu agent.
// Notifications are recorded.
func remoteModel(t *testing.T) (*Model, *fakeFeed, *[]string) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	since := time.Date(2026, 10, 12, 14, 2, 0, 0, time.Local)
	feed := &fakeFeed{
		ch: make(chan remote.Update),
		hosts: []remote.HostStatus{
			{Label: "devbox", Target: "devbox", Policy: remote.Observe, Applied: remote.Observe, State: remote.Live, LastSeen: time.Now(), Agents: 1, Offset: 10 * time.Minute},
			{Label: "gpu", Target: "user@gpu", Policy: remote.Observe, State: remote.Offline, Since: since, Reason: "the session ended", Agents: 2},
		},
		agents: []fleet.Agent{
			remoteAgent("devbox", dupSession, "Mossy Lemur", fleet.Working, 0.40),
			remoteAgent("gpu", dupSession, "Mossy Lemur", fleet.Working, 0.25),
			remoteAgent("gpu", "22222222-2222-4333-8444-555555555555", "Snug Capybara", fleet.Idle, 0.10),
		},
	}
	close(feed.ch)
	feed.setState("gpu", remote.Offline)
	local := sample()[1]
	local.Session.SessionID, local.Name, local.Status = dupSession, "Mossy Lemur", fleet.Idle
	local.Cost = cost.Summary{Tokens: transcript.Tokens{Output: 3000}, USD: 1.20}
	m := Model{st: newStyles(theme.Deepharness), store: inbox.New(), base: []fleet.Agent{local}, width: 140, height: 50, tab: tabFleet, cfg: config.Defaults(), hooksReady: true}
	m.input = newInput(m.st)
	m.colors = map[string]string{}
	m.att = newAttention()
	var sent []string
	m.att.send = func(n notify.Note) bool { sent = append(sent, n.Summary); return true }
	m.att.looking = func(string) bool { return false }
	m.remote.feed = feed
	m.syncRemote()
	m.refresh()
	return &m, feed, &sent
}

func frame(m *Model) string { return ansi.Strip(m.View().Content) }

func agentByKey(m *Model, key string) (fleet.Agent, bool) { return m.agentFor(key) }

func TestRemoteAgentsJoinTheFleetKeyedByHost(t *testing.T) {
	m, _, _ := remoteModel(t)
	keys := map[string]bool{}
	for _, a := range m.agents {
		keys[a.Session.SessionID] = true
	}
	for _, k := range []string{dupSession, dupSession + "@devbox", dupSession + "@gpu"} {
		if !keys[k] {
			t.Errorf("missing %s among %v", k, keys)
		}
	}
	if len(m.agents) != 4 {
		t.Fatalf("agents = %d, want 4", len(m.agents))
	}
	out := frame(m)
	if !strings.Contains(out, "Mossy Lemur @devbox") {
		t.Errorf("no host chip:\n%s", out)
	}
	// The local agent has no chip.
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "Mossy Lemur") && !strings.Contains(l, "@") && strings.Contains(l, "@devbox") {
			t.Errorf("chip on a local row: %q", l)
		}
	}
}

func TestOfflineHostIsOneRowAndNeverCounted(t *testing.T) {
	m, _, _ := remoteModel(t)
	out := frame(m)
	if !strings.Contains(out, "gpu · offline since 14:02 · 2 agents (last known)") {
		t.Errorf("no host row:\n%s", out)
	}
	if !strings.Contains(out, "last known: working") {
		t.Errorf("stale agent not shown as last known:\n%s", out)
	}
	needs, working, idle, unknown, _ := m.counts()
	// Live: devbox working, local idle. The gpu agents count nowhere.
	if needs != 0 || working != 1 || idle != 1 || unknown != 0 {
		t.Errorf("counts = needs %d working %d idle %d unknown %d", needs, working, idle, unknown)
	}
	if !strings.Contains(out, "2 agents · 1 working · 1 idle · 2 last known on hosts offline") {
		t.Errorf("header counts stale agents:\n%s", out)
	}
	// The feed reports stale agents with their last status, "working":
	// nothing in the views may see that as working or needing you.
	for _, a := range m.agents {
		if a.Stale && (a.Status == fleet.Working || a.Status == fleet.NeedsYou) {
			t.Errorf("stale %s carries status %v", a.Session.SessionID, a.Status)
		}
	}
	if n := m.subtreeBusy(dupSession + "@gpu"); n != 0 {
		t.Errorf("subtreeBusy = %d", n)
	}
	// No stall for an agent whose host is offline, however long it is quiet.
	m.cfg.Stuck.Stalled = 1
	runAll(m.checkStuck(time.Now().Add(time.Hour)))
	for _, it := range m.store.Items() {
		if hostOf(itemKey(it)) == "gpu" {
			t.Errorf("stale agent got %v %q", it.Kind, it.Title)
		}
	}
}

func TestNeedsAttentionHostShowsTheHint(t *testing.T) {
	m, feed, _ := remoteModel(t)
	feed.hosts[1].State, feed.hosts[1].Reason, feed.hosts[1].Hint = remote.NeedsAttention, "host key verification failed", remote.AttentionHint("user@gpu")
	m.handleRemote(remoteMsg{u: remote.Update{Kind: remote.HostChanged, Host: "gpu"}, ok: true})
	out := frame(m)
	if !strings.Contains(out, "gpu · needs you since 14:02") || !strings.Contains(out, "host key verification failed") {
		t.Errorf("no attention row:\n%s", out)
	}
	if !strings.Contains(strings.Join(strings.Fields(out), " "), "ssh") {
		t.Errorf("no hint:\n%s", out)
	}
}

func TestRemotePermissionIsShownNotAnswerable(t *testing.T) {
	m, feed, sent := remoteModel(t)
	ev := events.New(events.NeedsYou, dupSession)
	ev.Request, ev.Tool, ev.Host = "permission", "Bash", "devbox"
	runAll(m.remoteEvent(ev))
	m.refresh()

	var it *inbox.Item
	for _, o := range m.store.Items() {
		if itemKey(o) == dupSession+"@devbox" {
			it = o
		}
	}
	if it == nil {
		t.Fatalf("no inbox item: %+v", m.store.Items())
	}
	if it.Answerable() || it.Request() != nil {
		t.Errorf("a slice-1 remote item must not be answerable nor carry a bridge request")
	}
	if !strings.Contains(it.Title, "on devbox") {
		t.Errorf("title = %q", it.Title)
	}
	if len(*sent) != 1 || (*sent)[0] != "Mossy Lemur on devbox needs your permission (Bash)" {
		t.Errorf("notifications = %q", *sent)
	}
	// Only the devbox agent needs you: not the local one, not gpu's.
	for _, a := range m.agents {
		want := a.Session.SessionID == dupSession+"@devbox"
		if (a.Status == fleet.NeedsYou) != want {
			t.Errorf("%s status %v", a.Session.SessionID, a.Status)
		}
	}
	if m.topAnswerable() != nil {
		t.Errorf("^Y from the fleet would pick the remote item")
	}
	m.tab = tabInbox
	m.inboxCur = 0
	mm, _ := m.answer("allow")
	got := mm.(Model)
	if !strings.Contains(got.flash, "answer it in the terminal there") || it.State == inbox.Resolved {
		t.Errorf("^Y on a remote item: flash %q, state %v", got.flash, it.State)
	}
	out := frame(&got)
	if !strings.Contains(out, "@devbox") || !strings.Contains(out, "answer it in the terminal on devbox") {
		t.Errorf("inbox view:\n%s", out)
	}
	if strings.Contains(out, "/always this session") {
		t.Errorf("remote item offers ^Y:\n%s", out)
	}

	// The link drops: nothing from devbox may still look waiting.
	feed.setState("devbox", remote.Offline)
	m.handleRemote(remoteMsg{u: remote.Update{Kind: remote.HostChanged, Host: "devbox"}, ok: true})
	if it.State != inbox.Resolved || it.Resolved != "released to the terminal on devbox (link lost)" {
		t.Errorf("after the drop: state %v, %q", it.State, it.Resolved)
	}
	if m.store.Waiting() != 0 {
		t.Errorf("still waiting: %d", m.store.Waiting())
	}
}

func TestRemoteEventsResolveAndDoneOnTheirHostOnly(t *testing.T) {
	m, _, _ := remoteModel(t)
	// A local wait for the same session id stays when devbox's turn ends.
	m.store.Apply(req("Notification", dupSession, map[string]any{"notification_type": "idle_prompt", "message": "waiting"}))
	ev := events.New(events.NeedsYou, dupSession)
	ev.Request, ev.Host = "question", "devbox"
	runAll(m.remoteEvent(ev))
	done := events.New(events.Done, dupSession)
	done.Host = "devbox"
	runAll(m.remoteEvent(done))
	for _, it := range m.store.Items() {
		switch {
		case itemKey(it) == dupSession && it.State == inbox.Resolved:
			t.Errorf("the local item was resolved by a remote event: %q", it.Resolved)
		case itemKey(it) == dupSession+"@devbox" && it.Kind == inbox.Question && it.State != inbox.Resolved:
			t.Errorf("devbox's question still open after its turn ended")
		}
	}
	// An event from a host that is not live is history, not a wait.
	off := events.New(events.NeedsYou, dupSession)
	off.Request, off.Host = "permission", "gpu"
	runAll(m.remoteEvent(off))
	for _, it := range m.store.Items() {
		if hostOf(itemKey(it)) == "gpu" {
			t.Errorf("offline host made an item: %q", it.Title)
		}
	}
}

func TestEnterOnRemoteAgentNamesSSH(t *testing.T) {
	m, _, _ := remoteModel(t)
	for i, a := range m.agents {
		if a.Session.SessionID == dupSession+"@gpu" {
			m.cursor = i
		}
	}
	mm, cmd := m.openSelected()
	got := mm.(Model)
	if !strings.Contains(got.flash, "run `ssh -t user@gpu`") {
		t.Errorf("flash = %q", got.flash)
	}
	if cmd == nil {
		t.Fatal("no flash clear")
	}
	if _, ok := cmd().(focusedMsg); ok {
		t.Errorf("tried to focus a remote window")
	}
	// Closing an agent on a host that is not live is refused, with why.
	cm, _ := got.closeSelected(false)
	if !strings.Contains(cm.(Model).flash, "gpu is offline") {
		t.Errorf("close flash = %q", cm.(Model).flash)
	}
}

func TestRemoteDetailsPane(t *testing.T) {
	m, _, _ := remoteModel(t)
	for i, a := range m.agents {
		if a.Session.SessionID == dupSession+"@devbox" {
			m.cursor = i
		}
	}
	out := strings.Join(strings.Fields(frame(m)), " ")
	for _, want := range []string{"host devbox · ssh devbox", "policy observe", "link live", "clock the host's clock is 10m0s ahead", "ssh -t devbox"} {
		if !strings.Contains(out, want) {
			t.Errorf("details lack %q:\n%s", want, frame(m))
		}
	}
	if strings.Contains(out, "pid 0") || strings.Contains(out, "cwd") {
		t.Errorf("details show local-only fields:\n%s", frame(m))
	}
}

func TestRemoteCostJoinsFleetWithHostRollup(t *testing.T) {
	m, _, _ := remoteModel(t)
	total := m.fleetCost()
	if d := total.USD - (1.20 + 0.40 + 0.25 + 0.10); d > 1e-9 || d < -1e-9 {
		t.Errorf("fleet total = %v", total.USD)
	}
	by := m.hostCosts()
	if by["devbox"].USD != 0.40 || by[""].USD != 1.20 {
		t.Errorf("by host = %+v", by)
	}
	note := m.hostCostNote()
	if !strings.Contains(note, "here ") || !strings.Contains(note, "devbox ") || !strings.Contains(note, "gpu ") {
		t.Errorf("note = %q", note)
	}
	m.tab = tabOverview
	if out := frame(m); !strings.Contains(out, "devbox") {
		t.Errorf("overview strip lacks the host roll-up:\n%s", out)
	}
}

func TestRemoteProjectsAreHostScoped(t *testing.T) {
	m, _, _ := remoteModel(t)
	found := false
	for _, p := range m.proj.list {
		if p.Root == "den @devbox" {
			found = true
			if len(p.Agents) != 1 || p.Agents[0] != dupSession+"@devbox" {
				t.Errorf("agents = %v", p.Agents)
			}
		}
	}
	if !found {
		t.Errorf("no den @devbox project: %+v", m.proj.list)
	}
	m.proj.cur = "den @devbox"
	if cmd := m.proposeMission("ship it"); cmd == nil || !strings.Contains(m.flash, "remote hosts come later") {
		t.Errorf("mission on a remote project: flash %q", m.flash)
	}
}

func TestRosterTagsRemoteAgents(t *testing.T) {
	m, _, _ := remoteModel(t)
	text := orch.Compose(m.roster(), "hi")
	if !strings.Contains(text, "Mossy Lemur @devbox → on host devbox, SendMessage cannot reach it") {
		t.Errorf("roster:\n%s", text)
	}
	if !strings.Contains(text, "host offline, last known working") {
		t.Errorf("stale agent not marked:\n%s", text)
	}
	if !strings.Contains(text, "SendMessage is local to this machine") {
		t.Errorf("no warning:\n%s", text)
	}
}

func TestRemoteCommand(t *testing.T) {
	m, feed, _ := remoteModel(t)
	mm, _ := m.command("/remote")
	got := mm.(Model)
	out := strings.Join(strings.Fields(frame(&got)), " ")
	for _, want := range []string{"Remote hosts", "devbox ssh devbox live", "gpu ssh user@gpu offline", "reason the session ended", "policy observe"} {
		if !strings.Contains(out, want) {
			t.Errorf("listing lacks %q:\n%s", want, frame(&got))
		}
	}
	mm, _ = got.command("/remote connect gpu")
	if len(feed.reconnect) != 1 || feed.reconnect[0] != "gpu" {
		t.Errorf("reconnect = %v", feed.reconnect)
	}
	if _, _ = mm.(Model).command("/remote connect nowhere"); len(feed.reconnect) != 1 {
		t.Errorf("an unknown host was reconnected")
	}
	// Esc goes back to the details.
	back, _ := got.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if back.(Model).remote.listing {
		t.Errorf("Esc kept the listing")
	}
}

func TestNoRemotesNoFeed(t *testing.T) {
	t.Setenv("DEN_CONFIG", t.TempDir()+"/config.toml")
	var m Model
	m.openRemote(config.Defaults())
	if m.remote.feed != nil || m.remoteOn() {
		t.Errorf("a feed without remotes.toml")
	}
}

func TestPumpDeliversEveryUpdateInOrder(t *testing.T) {
	feed := &fakeFeed{ch: make(chan remote.Update)}
	p := newPump()
	p.start(feed)
	go func() {
		for i := range 50 {
			feed.ch <- remote.Update{Kind: remote.EventArrived, Host: fmt.Sprint(i)}
		}
		close(feed.ch)
	}()
	for i := range 50 {
		msg := waitRemote(p)().(remoteMsg)
		if !msg.ok || msg.u.Host != fmt.Sprint(i) {
			t.Fatalf("update %d = %+v", i, msg)
		}
	}
	if msg := waitRemote(p)().(remoteMsg); msg.ok {
		t.Errorf("no end after close: %+v", msg)
	}
}

func TestOneAgentIsSingular(t *testing.T) {
	m, feed, _ := remoteModel(t)
	m.base = nil
	feed.agents = feed.agents[:2] // devbox's, and one on gpu
	feed.hosts[1].Agents = 1
	m.handleRemote(remoteMsg{u: remote.Update{Kind: remote.HostChanged, Host: "gpu"}, ok: true})
	out := frame(m)
	if !strings.Contains(out, "1 agent ·") || !strings.Contains(out, "gpu · offline since 14:02 · 1 agent (last known)") || strings.Contains(out, "1 agents") {
		t.Errorf("plural:\n%s", out)
	}
}

func TestConnectFlashFollowsTheHost(t *testing.T) {
	m, feed, _ := remoteModel(t)
	mm, _ := m.command("/remote connect gpu")
	got := mm.(Model)
	if got.flash != "Connecting to gpu…" {
		t.Fatalf("flash = %q", got.flash)
	}
	feed.setState("gpu", remote.Connecting)
	got.handleRemote(remoteMsg{u: remote.Update{Kind: remote.HostChanged, Host: "gpu"}, ok: true})
	if got.flash != "Connecting to gpu…" {
		t.Errorf("connecting replaced the flash: %q", got.flash)
	}
	feed.setState("gpu", remote.Live)
	got.handleRemote(remoteMsg{u: remote.Update{Kind: remote.HostChanged, Host: "gpu"}, ok: true})
	if got.flash != "gpu is live" || got.flashErr {
		t.Errorf("flash after live = %q (err %v)", got.flash, got.flashErr)
	}
	if strings.Contains(frame(&got), "Connecting to gpu") {
		t.Errorf("still connecting on screen")
	}
}

func TestHostNowReadsAsASentence(t *testing.T) {
	for s, want := range map[remote.State]string{
		remote.Live:              "b is live",
		remote.Offline:           "b is offline",
		remote.NeedsAttention:    "b needs your attention",
		remote.VersionMismatch:   "b has a den that does not match this one",
		remote.AttachedElsewhere: "another Den controls b, this one only observes it",
	} {
		if got := hostNow("b", s); got != want {
			t.Errorf("%s: %q, want %q", s, got, want)
		}
	}
}

// heldRequest is a request the Manager would hand over for devbox: a
// PermissionRequest whose decisions are recorded instead of sent.
func heldRequest(host, event, tool string, input any, sent *[]bridge.Decision) *bridge.Request {
	raw, _ := json.Marshal(input)
	payload, _ := json.Marshal(map[string]any{"session_id": dupSession, "hook_event_name": event, "tool_name": tool, "tool_input": json.RawMessage(raw)})
	env := bridge.Envelope{Event: event, SessionID: dupSession, Payload: payload}
	return bridge.NewRemoteRequest(host, "0123456789abcdef0123456789abcdef", env, func(d bridge.Decision) { *sent = append(*sent, d) })
}

func approveDevbox(feed *fakeFeed) {
	feed.hosts[0].Policy, feed.hosts[0].Applied = remote.Approve, remote.Approve
}

func TestRemoteHeldPermissionIsAnsweredHere(t *testing.T) {
	m, feed, notes := remoteModel(t)
	approveDevbox(feed)
	m.syncRemote()
	var sent []bridge.Decision
	r := heldRequest("devbox", "PermissionRequest", "Bash", map[string]string{"command": "go test ./..."}, &sent)
	runAll(m.handleRemote(remoteMsg{u: remote.Update{Kind: remote.HeldArrived, Host: "devbox", Request: r}, ok: true}))
	it := m.topAnswerable()
	if it == nil || it.Host != "devbox" || !strings.Contains(it.Title, "on devbox") || it.Body[0] != "$ go test ./..." {
		t.Fatalf("item = %+v", it)
	}
	if len(*notes) != 1 || !strings.Contains((*notes)[0], "on devbox needs your permission") {
		t.Errorf("notifications = %q", *notes)
	}
	m.tab = tabInbox
	m.inboxCur = 0
	out := frame(m)
	if !strings.Contains(out, "^Y allow") || !strings.Contains(out, "$ go test ./...") {
		t.Errorf("inbox view:\n%s", out)
	}
	mm, _ := m.answer("allow")
	if len(sent) != 1 || sent[0].Behavior != "allow" || it.State != inbox.Resolved {
		t.Errorf("sent %+v, item %v %q (flash %q)", sent, it.State, it.Resolved, mm.(Model).flash)
	}

	// The link drops with a request held: released, never answerable.
	m2 := mm.(Model)
	r = heldRequest("devbox", "PermissionRequest", "Bash", map[string]string{"command": "make"}, &sent)
	runAll(m2.handleRemote(remoteMsg{u: remote.Update{Kind: remote.HeldArrived, Host: "devbox", Request: r}, ok: true}))
	r.LetGo("released to the terminal on devbox (link lost)")
	runAll(m2.handleRemote(remoteMsg{u: remote.Update{Kind: remote.HeldReleased, Host: "devbox", Request: r}, ok: true}))
	if m2.topAnswerable() != nil {
		t.Error("a released request still answerable")
	}
	mm, _ = m2.answer("allow")
	if len(sent) != 1 {
		t.Errorf("a released request sent %+v", sent)
	}
	if !strings.Contains(frame(&m2), "link lost") {
		t.Errorf("no link lost trailer:\n%s", frame(&m2))
	}
}

// What is not a PermissionRequest from that very host never reaches the
// local handlers: no close, skills or install question appears, and the
// request goes back to its terminal.
func TestRemoteHeldCannotReachLocalHandlers(t *testing.T) {
	m, feed, _ := remoteModel(t)
	approveDevbox(feed)
	m.syncRemote()
	closeIn := map[string]any{"peers": []string{"all"}, "reason": "cleanup", "idle_for": "1m"}
	cases := []struct {
		host, event, why string
	}{
		{"devbox", bridge.CloseEvent, "close"},
		{"devbox", bridge.SkillsEvent, "skills"},
		{"devbox", bridge.InstallEvent, "install"},
		{"gpu", "PermissionRequest", "another host's label"},
	}
	for _, c := range cases {
		var sent []bridge.Decision
		r := heldRequest(c.host, c.event, "Bash", closeIn, &sent)
		runAll(m.handleRemote(remoteMsg{u: remote.Update{Kind: remote.HeldArrived, Host: "devbox", Request: r}, ok: true}))
		if len(sent) != 1 || sent[0].Behavior != "" {
			t.Errorf("%s: sent %+v", c.why, sent)
		}
	}
	for _, it := range m.store.Items() {
		if it.DenAsks() || it.Answerable() {
			t.Errorf("remote input made %v %q", it.Kind, it.Title)
		}
	}
	// Under observe here, a held request is not taken either.
	feed.hosts[0].Policy, feed.hosts[0].Applied = remote.Observe, remote.Observe
	m.syncRemote()
	var sent []bridge.Decision
	r := heldRequest("devbox", "PermissionRequest", "Bash", map[string]string{"command": "ls"}, &sent)
	runAll(m.handleRemote(remoteMsg{u: remote.Update{Kind: remote.HeldArrived, Host: "devbox", Request: r}, ok: true}))
	if len(sent) != 1 || m.topAnswerable() != nil {
		t.Errorf("observe host: sent %+v", sent)
	}
}

// The host's needs-you event and the held request are one wait: one item,
// one notification, whichever comes first.
func TestRemoteHeldAndItsEventAreOneItem(t *testing.T) {
	for _, eventFirst := range []bool{true, false} {
		m, feed, notes := remoteModel(t)
		approveDevbox(feed)
		m.syncRemote()
		ev := events.New(events.NeedsYou, dupSession)
		ev.Request, ev.Tool, ev.Host = "plan", "ExitPlanMode", "devbox"
		var sent []bridge.Decision
		r := heldRequest("devbox", "PermissionRequest", "ExitPlanMode", map[string]string{"plan": "1. do it"}, &sent)
		held := remoteMsg{u: remote.Update{Kind: remote.HeldArrived, Host: "devbox", Request: r}, ok: true}
		if eventFirst {
			runAll(m.remoteEvent(ev))
			runAll(m.handleRemote(held))
		} else {
			runAll(m.handleRemote(held))
			runAll(m.remoteEvent(ev))
		}
		open := 0
		for _, it := range m.store.Items() {
			if it.Host == "devbox" && it.State != inbox.Resolved {
				open++
			}
		}
		if open != 1 || len(*notes) != 1 || m.topAnswerable() == nil {
			t.Errorf("event first %v: %d open items, notes %q", eventFirst, open, *notes)
		}
	}
}

func TestAttachedElsewhereHostIsLiveReadOnly(t *testing.T) {
	m, feed, _ := remoteModel(t)
	feed.hosts[0].Policy = remote.Approve
	feed.hosts[0].Reason = remote.ElsewhereNote("devbox") + " (its serve is pid 4242 on the host, attached for 3m0s)"
	feed.setState("devbox", remote.AttachedElsewhere)
	for i := range feed.agents {
		if feed.agents[i].Host == "devbox" {
			feed.agents[i].Stale = false
		}
	}
	m.handleRemote(remoteMsg{u: remote.Update{Kind: remote.HostChanged, Host: "devbox"}, ok: true})
	h, _ := m.hostStatus("devbox")
	if line := m.hostLine(h); strings.Contains(line, "last known") || !strings.Contains(line, "another Den") {
		t.Errorf("host line = %q", line)
	}
	ev := events.New(events.NeedsYou, dupSession)
	ev.Request, ev.Tool, ev.Host = "permission", "Bash", "devbox"
	runAll(m.remoteEvent(ev))
	var it *inbox.Item
	for _, o := range m.store.Items() {
		if o.Host == "devbox" && o.State != inbox.Resolved {
			it = o
		}
	}
	if it == nil || it.Answerable() {
		t.Fatalf("attached elsewhere: item %+v", it)
	}
	m.tab, m.inboxCur = tabInbox, 0
	mm, _ := m.answer("allow")
	if f := mm.(Model).flash; !strings.Contains(f, "answer it in the terminal there") || !strings.Contains(f, "another Den controls devbox, this one only observes it") {
		t.Errorf("flash = %q", f)
	}
}

// A request that carries a host never takes the local path, even with
// the orchestrator's session id or a local agent's.
func TestHandleRequestSendsRemoteRequestsAway(t *testing.T) {
	m, feed, _ := remoteModel(t)
	approveDevbox(feed)
	m.syncRemote()
	var sent []bridge.Decision
	r := heldRequest("devbox", bridge.CloseEvent, "Bash", map[string]any{"peers": []string{"all"}}, &sent)
	runAll(m.handleRequest(r))
	r = heldRequest("devbox", "PermissionRequest", "Bash", map[string]string{"command": "ls"}, &sent)
	runAll(m.handleRequest(r))
	if _, ok := m.att.lastHook[dupSession]; ok {
		t.Error("a remote request moved the local agent's stall timer")
	}
	it := m.topAnswerable()
	if len(sent) != 1 || sent[0].Behavior != "deny" || it == nil || it.Host != "devbox" {
		t.Errorf("sent %+v, item %+v", sent, it)
	}
}
