package ui

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/closer"
	"github.com/GlacierHubAB/den/internal/events"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/remote"
)

// Send and close in the UI: @mentions and closes reach a remote agent through
// the Manager (here a fakeFeed), with the host in every question.

// firstMsg runs a command tree concurrently and returns the first message
// ok accepts (the ticks that clear the flash are left to sleep).
func firstMsg(t *testing.T, cmd tea.Cmd, ok func(tea.Msg) bool) tea.Msg {
	t.Helper()
	out := make(chan tea.Msg, 16)
	var run func(c tea.Cmd)
	run = func(c tea.Cmd) {
		if c == nil {
			return
		}
		go func() {
			msg := c()
			if b, isBatch := msg.(tea.BatchMsg); isBatch {
				for _, cc := range b {
					run(cc)
				}
				return
			}
			out <- msg
		}()
	}
	run(cmd)
	deadline := time.After(3 * time.Second)
	for {
		select {
		case msg := <-out:
			if ok(msg) {
				return msg
			}
		case <-deadline:
			t.Fatal("the command produced no message of the kind asked for")
		}
	}
}

func isSent(m tea.Msg) bool   { _, ok := m.(sentMsg); return ok }
func isClosed(m tea.Msg) bool { _, ok := m.(closeDoneMsg); return ok }

func typeIn(m Model, s string) Model {
	m.input.SetValue(s)
	return m
}

func TestMentionSendsToARemoteAgentThroughTheFeed(t *testing.T) {
	m, feed, _ := remoteModel(t)
	mm, cmd := typeIn(*m, "@Mossy Lemur@devbox rebase on main `id`").submit()
	got := mm.(Model)
	msg := firstMsg(t, cmd, isSent).(sentMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	if len(feed.sent) != 1 || feed.sent[0] != "devbox/"+dupSession+": rebase on main `id`" {
		t.Fatalf("feed.sent = %q (the bare session id and the text go to the host)", feed.sent)
	}
	// The notice sits under the remote agent, not the local one with that id.
	var it *inbox.Item
	for _, o := range got.store.Items() {
		if o.Kind == inbox.Sent {
			it = o
		}
	}
	if it == nil || it.Host != "devbox" || it.SessionID != dupSession {
		t.Fatalf("sent notice = %+v", it)
	}
	got.applySent(msg)
	if it.Resolved != "delivered" || !strings.Contains(got.flash, "Delivered to Mossy Lemur") {
		t.Errorf("resolved %q flash %q", it.Resolved, got.flash)
	}
}

func TestMentionOfANameOnSeveralHostsNeedsTheHost(t *testing.T) {
	m, feed, _ := remoteModel(t)
	// A local agent wins an unqualified mention; the hosts' are reached by host.
	mm, _ := typeIn(*m, "@Mossy Lemur hello").submit()
	_ = mm
	if mt := resolveMention("@Mossy Lemur hello", m.agents); mt.target == nil || mt.target.Host != "" {
		t.Errorf("unqualified with a local fit -> %+v", mt)
	}
	// With nothing local, the same name on two hosts is ambiguous.
	m.base = nil
	m.refresh()
	mm, cmd := typeIn(*m, "@Mossy Lemur hello").submit()
	got := mm.(Model)
	if cmd == nil || !strings.Contains(got.flash, "Which one") || !strings.Contains(got.flash, "Mossy Lemur@devbox") || !strings.Contains(got.flash, "Mossy Lemur@gpu") {
		t.Errorf("flash = %q", got.flash)
	}
	if len(feed.sent) != 0 {
		t.Errorf("sent anyway: %q", feed.sent)
	}
	if mt := resolveMention("@mossy@gpu hi", m.agents); mt.target == nil || mt.target.Host != "gpu" {
		t.Errorf("partial with host -> %+v", mt)
	}
	if mt := resolveMention("@Mossy Lemur@nowhere hi", m.agents); mt.target != nil || mt.missing != "Mossy Lemur@nowhere" {
		t.Errorf("unknown host -> %+v", mt)
	}
}

// A host cannot capture a mention meant for another agent: an unqualified
// mention goes to the local agent even when a remote name is longer.
func TestRemoteNameCannotCaptureALocalMention(t *testing.T) {
	m, _, _ := remoteModel(t)
	r := remoteAgent("devbox", "33333333-2222-4333-8444-555555555555", "Mossy Lemur fix", fleet.Idle, 0)
	r.Session.SessionID += "@devbox"
	m.agents = append(m.agents, r)
	mt := resolveMention("@Mossy Lemur fix the login", m.agents)
	if mt.target == nil || mt.target.Host != "" || mt.body != "fix the login" {
		t.Errorf("mention -> target %+v body %q", mt.target, mt.body)
	}
}

func TestSendRequestNameMustBeExactOrUniquePrefix(t *testing.T) {
	m, feed, _ := remoteModel(t)
	b := remoteAgent("devbox", "44444444-2222-4333-8444-555555555555", "Mossy Moth", fleet.Idle, 0)
	b.Session.SessionID += "@devbox"
	m.remote.agents = append(m.remote.agents, b)
	ask := func(who string) *bridge.Request {
		pl, _ := json.Marshal(bridge.SendInput{Who: who, Text: "hi"})
		r := bridge.TestRequest(bridge.Envelope{Event: bridge.SendEvent, Payload: pl})
		m.handleRequest(r)
		return r
	}
	r := ask("mossy@devbox") // fits Mossy Lemur and Mossy Moth
	if d, ok := r.Decided(); !ok || d.Behavior != "deny" || !strings.Contains(d.Message, "Mossy Lemur") || !strings.Contains(d.Message, "Mossy Moth") {
		t.Errorf("an ambiguous prefix: %+v %v", d, ok)
	}
	if len(feed.sent) != 0 {
		t.Fatalf("guessed: %q", feed.sent)
	}
	// An exact name wins over longer ones; a unique prefix works.
	if _, err := onHostAgent(m.remote.agents, "mossy lemur@devbox"); err != nil {
		t.Errorf("exact: %v", err)
	}
	if a, err := onHostAgent(m.remote.agents, "mossy m@devbox"); err != nil || a.Name != "Mossy Moth" {
		t.Errorf("unique prefix: %v %v", a.Name, err)
	} // No fit: the agents on that host are named, never guessed.
	r = ask("nobody@devbox")
	if d, _ := r.Decided(); d.Behavior != "deny" || !strings.Contains(d.Message, "on devbox: ") || !strings.Contains(d.Message, "Mossy Lemur") || !strings.Contains(d.Message, "Mossy Moth") {
		t.Errorf("an unknown name: %+v", d)
	}
	if _, err := onHostAgent(m.remote.agents, "nobody@nohost"); err == nil || !strings.Contains(err.Error(), "Den knows no agents on nohost") {
		t.Errorf("an unknown host: %v", err)
	}
	if len(feed.sent) != 0 {
		t.Fatalf("guessed: %q", feed.sent)
	}
}

func TestCompletionShowsTheHostAndInsertsIt(t *testing.T) {
	m, _, _ := remoteModel(t)
	m.input.SetValue("@snug")
	rows := strings.Join(m.completionRows(100), "\n")
	if !strings.Contains(ansi.Strip(rows), "Snug Capybara @gpu") {
		t.Errorf("rows = %q", ansi.Strip(rows))
	}
	if got := mentionText(m.agents[0]); !strings.HasPrefix(got, "@") {
		t.Errorf("mentionText = %q", got)
	}
	for _, a := range m.agents {
		if a.Host == "gpu" && a.Name == "Snug Capybara" && mentionText(a) != "@Snug Capybara@gpu" {
			t.Errorf("inserted %q", mentionText(a))
		}
	}
}

func TestMentionRefusedWhenTheHostCannotAct(t *testing.T) {
	m, feed, _ := remoteModel(t)
	feed.actErr = errors.New("devbox is set to observe, which takes no actions; nothing was done")
	mm, _ := typeIn(*m, "@Mossy Lemur@devbox hi").submit()
	got := mm.(Model)
	if !strings.Contains(got.flash, "observe") || !got.flashErr {
		t.Errorf("flash = %q", got.flash)
	}
	for _, it := range got.store.Items() {
		if it.Kind == inbox.Sent {
			t.Errorf("a refused message left a notice: %+v", it)
		}
	}
	if len(feed.sent) != 0 {
		t.Errorf("sent: %q", feed.sent)
	}
	// An offline host is refused with its own words.
	feed.actErr = nil
	mm, _ = typeIn(*m, "@Snug Capybara@gpu hi").submit()
	if g := mm.(Model); !strings.Contains(g.flash, "gpu is offline") {
		t.Errorf("flash = %q", g.flash)
	}
}

func TestMentionFailureShowsInTheNotice(t *testing.T) {
	m, feed, _ := remoteModel(t)
	feed.sendErr = errors.New("devbox did not answer within 10s; whether it acted is not known")
	mm, cmd := typeIn(*m, "@Mossy Lemur@devbox hi").submit()
	got := mm.(Model)
	msg := firstMsg(t, cmd, isSent).(sentMsg)
	got.applySent(msg)
	if !strings.HasPrefix(msg.item.Resolved, "failed: devbox did not answer") || !got.flashErr {
		t.Errorf("resolved %q flash %q", msg.item.Resolved, got.flash)
	}
}

// remoteIdle makes devbox's Mossy Lemur idle for an hour with an unclean checkout.
func remoteIdle(m *Model, feed *fakeFeed) {
	yes := true
	for i := range feed.agents {
		if feed.agents[i].Host == "devbox" {
			feed.agents[i].Status = fleet.Idle
			feed.agents[i].Dirty = &yes
			feed.agents[i].LastActive = time.Now().Add(-time.Hour)
		}
	}
	m.syncRemote()
	m.refresh()
}

func TestCloseQuestionNamesTheHostAndTheDirtyCheckout(t *testing.T) {
	m, feed, _ := remoteModel(t)
	remoteIdle(m, feed)
	mm, _ := m.closeCommand([]string{"Mossy", "Lemur@devbox"})
	got := mm.(Model)
	it := openClose(got)
	if it == nil {
		t.Fatalf("no close question; flash %q", got.flash)
	}
	text := it.Title + "\n" + strings.Join(it.Body, "\n")
	for _, want := range []string{"Close Mossy Lemur on devbox?", "on devbox", "SIGTERM there", "uncommitted changes in its checkout on devbox"} {
		if !strings.Contains(text, want) {
			t.Errorf("the question lacks %q:\n%s", want, text)
		}
	}
	if it.Host != "devbox" || it.SessionID != dupSession {
		t.Errorf("item host %q session %q", it.Host, it.SessionID)
	}
	if len(feed.closed) != 0 {
		t.Fatal("closed before ^Y")
	}
	// ^N keeps it.
	if mm, _ = got.decideClose(it, "deny"); len(feed.closed) != 0 || openClose(mm.(Model)) != nil {
		t.Errorf("^N: closed %v", feed.closed)
	}
}

func TestCloseCarriesOutOnlyAfterYAndSignalsThroughTheFeed(t *testing.T) {
	m, feed, _ := remoteModel(t)
	remoteIdle(m, feed)
	mm, _ := m.closeCommand([]string{"Mossy Lemur@devbox"})
	got := mm.(Model)
	it := openClose(got)
	mm, cmd := got.decideClose(it, "allow")
	got = mm.(Model)
	done := firstMsg(t, cmd, isClosed).(closeDoneMsg)
	if len(feed.closed) != 1 || feed.closed[0] != "devbox/"+dupSession+" force=false" {
		t.Fatalf("feed.closed = %q", feed.closed)
	}
	got.finishClose(done)
	if !strings.Contains(got.flash, "Closed Mossy Lemur") {
		t.Errorf("flash = %q", got.flash)
	}
	// The local agent with the same id was not touched.
	for _, a := range got.base {
		if a.Status == fleet.Gone {
			t.Errorf("a local agent was marked gone: %s", a.Name)
		}
	}
}

func TestCloseWorkingRemoteNeedsForceAndFailureIsReported(t *testing.T) {
	m, feed, _ := remoteModel(t)
	mm, _ := m.closeCommand([]string{"Mossy Lemur@devbox"}) // working
	if g := mm.(Model); openClose(g) != nil || !strings.Contains(g.flash, "working") {
		t.Errorf("a working remote agent without --force: flash %q", g.flash)
	}
	feed.closeErr = errors.New("devbox is offline; nothing was closed")
	mm, _ = m.closeCommand([]string{"--force", "Mossy Lemur@devbox"})
	got := mm.(Model)
	it := openClose(got)
	if it == nil || !strings.Contains(strings.Join(it.Body, "\n"), "Forced") {
		t.Fatalf("no forced question; flash %q", got.flash)
	}
	mm, cmd := got.decideClose(it, "allow")
	done := firstMsg(t, cmd, isClosed).(closeDoneMsg)
	if feed.closed[0] != "devbox/"+dupSession+" force=true" {
		t.Errorf("feed.closed = %q", feed.closed)
	}
	g := mm.(Model)
	g.finishClose(done)
	if !strings.Contains(g.flash, "devbox is offline; nothing was closed") {
		t.Errorf("flash = %q", g.flash)
	}
}

func TestCloseNameOnSeveralHostsNeedsTheHost(t *testing.T) {
	m, _, _ := remoteModel(t)
	mm, _ := m.closeCommand([]string{"Mossy Lemur"})
	if g := mm.(Model); openClose(g) != nil || !strings.Contains(g.flash, "add @host") {
		t.Errorf("flash = %q", g.flash)
	}
}

func TestCloseIdleListsRemoteAgentsAndRespectsProtect(t *testing.T) {
	m, feed, _ := remoteModel(t)
	remoteIdle(m, feed)
	mm, _ := m.closeCommand([]string{"idle"})
	got := mm.(Model)
	it := openClose(got)
	if it == nil {
		t.Fatalf("no question; flash %q", got.flash)
	}
	text := strings.Join(it.Body, "\n")
	if !strings.Contains(text, "on devbox") {
		t.Errorf("the idle remote agent is not listed:\n%s", text)
	}
	// gpu's agents are not live: they are unknown, never idle.
	if strings.Contains(text, "on gpu") {
		t.Errorf("an offline host's agent is listed:\n%s", text)
	}
	// Protected by name: left out of the bulk close.
	m1, feed1, _ := remoteModel(t)
	remoteIdle(m1, feed1)
	m1.ensureGuard()
	m1.guard.Protect("Mossy Lemur")
	mm, _ = m1.closeCommand([]string{"idle"})
	for _, o := range mm.(Model).store.Items() {
		if o.Kind == inbox.Close && strings.Contains(strings.Join(o.Body, "\n"), "SIGTERM there") {
			t.Errorf("a protected remote agent was proposed:\n%s", strings.Join(o.Body, "\n"))
		}
	}
	// A host that takes no actions: skipped with why.
	m2, feed2, _ := remoteModel(t)
	remoteIdle(m2, feed2)
	feed2.actErr = errors.New("devbox is set to observe; nothing was done")
	mm, _ = m2.closeCommand([]string{"idle"})
	g3 := mm.(Model)
	body := ""
	for _, o := range g3.store.Items() {
		if o.Kind == inbox.Close {
			body += strings.Join(o.Body, "\n")
		}
	}
	if strings.Contains(body, "SIGTERM there") || (!strings.Contains(body, "Skipped") && !strings.Contains(g3.flash, "observe")) {
		t.Errorf("an observe host's agent was proposed or silently dropped:\nbody %s\nflash %s", body, g3.flash)
	}
}

func TestOrchestratorCannotCloseRemoteAgents(t *testing.T) {
	m, feed, _ := remoteModel(t)
	remoteIdle(m, feed)
	for i := range m.remote.agents {
		m.remote.agents[i].Session.Name = "devbox-peer"
	}
	m.refresh()
	// By peer name: not found among the agents it may close.
	pl, _ := json.Marshal(bridge.CloseInput{Peers: []string{"user-devbox", "devbox-peer"}, Reason: "tidy up"})
	r := bridge.TestRequest(bridge.Envelope{Event: bridge.CloseEvent, SessionID: "orch", Payload: pl})
	m.handleCloseRequest(r)
	if d, ok := r.Decided(); !ok || d.Behavior != "deny" {
		t.Errorf("close by peer name: %+v %v", d, ok)
	}
	// By idleness: the remote agent is idle for an hour and is not picked.
	pl, _ = json.Marshal(bridge.CloseInput{IdleFor: "30m", Reason: "idle"})
	r = bridge.TestRequest(bridge.Envelope{Event: bridge.CloseEvent, SessionID: "orch", Payload: pl})
	m.handleCloseRequest(r)
	for _, it := range m.store.Items() {
		if it.Kind == inbox.Close && strings.Contains(strings.Join(it.Body, "\n"), "devbox") {
			t.Errorf("the orchestrator's idle close reached a remote agent:\n%s", strings.Join(it.Body, "\n"))
		}
	}
	// And the plan builder itself refuses them when asked by the orchestrator.
	a, _ := m.agentFor(dupSession + "@devbox")
	if it, _ := m.proposeClose([]fleet.Agent{a}, true, "orchestrator", "", nil, nil); it != nil {
		t.Error("proposeClose accepted a remote agent from the orchestrator")
	}
}

func TestSendRequestFromDenSockMessagesARemoteAgent(t *testing.T) {
	m, feed, _ := remoteModel(t)
	ask := func(who, text string) *bridge.Request {
		pl, _ := json.Marshal(bridge.SendInput{Who: who, Text: text, AskedBy: "den send, pid 1"})
		return bridge.TestRequest(bridge.Envelope{Event: bridge.SendEvent, Payload: pl})
	}
	r := ask("mossy@devbox", "run the tests\nthen push")
	if cmd := m.handleRequest(r); cmd != nil {
		t.Fatal("a den send went out without asking")
	}
	// One question in the inbox, with the host, the agent and the full text.
	var it *inbox.Item
	for _, o := range m.store.Items() {
		if o.Kind == inbox.Message {
			it = o
		}
	}
	if it == nil || !it.Answerable() || it.Host != "devbox" || it.SessionID != dupSession {
		t.Fatalf("no send question: %+v", it)
	}
	text := it.Title + "\n" + strings.Join(it.Body, "\n")
	for _, want := range []string{"Send a message to Mossy Lemur on devbox?", "on devbox · Mossy Lemur (user-devbox)", "asked by den send, pid 1", "run the tests\nthen push"} {
		if !strings.Contains(text, want) {
			t.Errorf("the question lacks %q:\n%s", want, text)
		}
	}
	m.tab = tabInbox
	m.selectItem(it)
	if out := frame(m); !strings.Contains(out, "^Y send it  ^N do not send") || strings.Contains(out, "/always this session") {
		t.Errorf("inbox view:\n%s", out)
	}
	if _, ok := r.Decided(); ok || len(feed.sent) != 0 {
		t.Fatalf("answered or sent before ^Y: sent %q", feed.sent)
	}
	// /always is not an answer here; ^Y sends it once.
	if mm, _ := m.answer("always"); len(feed.sent) != 0 || !it.Answerable() || !strings.Contains(mm.(Model).flash, "asked for once") {
		t.Errorf("/always: sent %q, flash %q", feed.sent, mm.(Model).flash)
	}
	mm, cmd := m.answer("allow")
	*m = mm.(Model)
	msg := firstMsg(t, cmd, func(x tea.Msg) bool { _, ok := x.(askedSentMsg); return ok }).(askedSentMsg)
	m.finishAskedSend(msg)
	d, ok := r.Decided()
	if !ok || d.Behavior != "allow" || !strings.Contains(d.Message, "delivered to Mossy Lemur on devbox") {
		t.Errorf("reply = %+v", d)
	}
	if len(feed.sent) != 1 || feed.sent[0] != "devbox/"+dupSession+": run the tests\nthen push" {
		t.Errorf("feed.sent = %q", feed.sent)
	}
	for _, c := range []struct{ who, text, want string }{
		{"mossy", "x", "name an agent on a host"},
		{"mossy@nowhere", "x", "no agent matches"},
		{"snug@gpu", "x", "gpu is offline"},
		{"mossy@devbox", "  ", "must be"},
	} {
		r := ask(c.who, c.text)
		m.handleRequest(r)
		if d, ok := r.Decided(); !ok || d.Behavior != "deny" || !strings.Contains(d.Message, c.want) {
			t.Errorf("%q: %+v %v, want a refusal with %q", c.who, d, ok, c.want)
		}
	}
	// A request that came from a host is refused outright.
	pl, _ := json.Marshal(bridge.SendInput{Who: "mossy@devbox", Text: "x"})
	var got bridge.Decision
	rr := bridge.NewRemoteRequest("devbox", "id", bridge.Envelope{Event: bridge.SendEvent, Payload: pl}, func(d bridge.Decision) { got = d })
	m.handleRequest(rr)
	if got.Behavior != "deny" || len(feed.sent) != 1 {
		t.Errorf("a remote host's SendRequest: %+v, sent %q", got, feed.sent)
	}
}

// A den send whose text sanitising would change is refused: the question
// would show other text than is sent.
func TestSendRequestRefusesTextDenCannotShow(t *testing.T) {
	m, feed, _ := remoteModel(t)
	for _, text := range []string{"run the tests\x1b]0;and something else\x07", "run the tests\U000E0041", "run the tests\r\nthen push"} {
		pl, _ := json.Marshal(bridge.SendInput{Who: "mossy@devbox", Text: text, AskedBy: "den send, pid 1"})
		r := bridge.TestRequest(bridge.Envelope{Event: bridge.SendEvent, Payload: pl})
		m.handleRequest(r)
		if d, ok := r.Decided(); !ok || d.Behavior != "deny" || !strings.Contains(d.Message, "Den would not show") {
			t.Errorf("%q: %+v %v", text, d, ok)
		}
	}
	for _, o := range m.store.Items() {
		if o.Kind == inbox.Message {
			t.Errorf("a question was asked: %q", o.Body)
		}
	}
	if len(feed.sent) != 0 {
		t.Errorf("sent %q", feed.sent)
	}
}

func TestRunCloseOrderMixesLocalAndRemote(t *testing.T) {
	feed := &fakeFeed{}
	ops := &fakeCloseOps{}
	was := closeOps
	closeOps = ops
	defer func() { closeOps = was }()
	plan := closer.Plan{Targets: []closer.Target{
		{SessionID: "r1@devbox", Name: "R", Host: "devbox"},
		{SessionID: "l1", Name: "L", PID: 4242},
	}}
	res := runClose(t.Context(), plan, closer.Self{}, feed)
	if len(res) != 2 || res[0].Target.Name != "R" || res[1].Target.Name != "L" || !res[0].Ended {
		t.Fatalf("results = %+v", res)
	}
	if len(feed.closed) != 1 || len(ops.signals) != 1 {
		t.Errorf("remote %v local %v", feed.closed, ops.signals)
	}
}

// The Review /send path is host-aware, ready for remote Review: a remote
// agent is a recipient while its host is live and allows actions.
func TestReviewRecipientIsHostAware(t *testing.T) {
	m, feed, _ := remoteModel(t)
	if _, ok := m.reviewRecipient(dupSession + "@devbox"); !ok {
		t.Error("a live remote agent is not a recipient")
	}
	feed.actErr = errors.New("devbox is set to observe; nothing was done")
	if _, ok := m.reviewRecipient(dupSession + "@devbox"); ok {
		t.Error("an observe host's agent is a recipient")
	}
	if _, ok := m.reviewRecipient(dupSession + "@gpu"); ok {
		t.Error("an offline host's agent is a recipient")
	}
	a, _ := m.agentFor(dupSession + "@devbox")
	feed.actErr = nil
	msg := deliverReview(m.sender(), a, "comment", inbox.New().SentOn("devbox", dupSession, "x", ""), []string{"c1"})()
	if s := msg.(sentMsg); s.err != nil || len(feed.sent) != 1 || !strings.HasPrefix(feed.sent[0], "devbox/"+dupSession+": ") {
		t.Errorf("sent %v err %v", feed.sent, s.err)
	}
}

func TestProtectNameAtHostHoldsForEveryClosePath(t *testing.T) {
	m, feed, _ := remoteModel(t)
	remoteIdle(m, feed)
	m.ensureGuard()
	m.guardPath = filepath.Join(t.TempDir(), "protected.json")
	g := func(mm tea.Model, _ tea.Cmd) Model { return mm.(Model) }
	remoteProposed := func(m Model) bool {
		for _, it := range closeItems(m) {
			if it.Answerable() && strings.Contains(strings.Join(it.Body, "\n"), "SIGTERM there") {
				return true
			}
		}
		return false
	}

	// By peer name and host: recorded as the critter name on that host.
	*m = g(m.guardCommand("/protect", []string{"user-devbox@devbox"}))
	if len(m.guard.Names) != 1 || m.guard.Names[0] != "Mossy Lemur@devbox" || !strings.Contains(m.flash, "Mossy Lemur@devbox is protected") {
		t.Fatalf("names %q flash %q", m.guard.Names, m.flash)
	}
	// /close Name@host: refused, saying how to allow it.
	got := g(m.closeCommand([]string{"Mossy", "Lemur@devbox"}))
	if remoteProposed(got) || !strings.Contains(got.flash, "Mossy Lemur: protected (Mossy Lemur@devbox) · /unprotect Mossy Lemur@devbox to allow it") {
		t.Errorf("named close of a protected remote agent: flash %q", got.flash)
	}
	// ^X on it.
	a, ok := m.agentFor(dupSession + "@devbox")
	if !ok {
		t.Fatal("no remote agent")
	}
	if got = g(m.askClose([]fleet.Agent{a}, true, nil)); remoteProposed(got) || !strings.Contains(got.flash, "protected (Mossy Lemur@devbox)") {
		t.Errorf("^X on a protected remote agent: flash %q", got.flash)
	}
	// /close idle: skipped and listed; the local Mossy Lemur is not protected.
	got = g(m.closeCommand([]string{"idle"}))
	it := openClose(got)
	if it == nil || remoteProposed(got) {
		t.Fatalf("idle close: item %v flash %q", it, got.flash)
	}
	if body := strings.Join(it.Body, "\n"); !strings.Contains(body, "Skipped Mossy Lemur: protected (Mossy Lemur@devbox)") {
		t.Errorf("idle close does not list the protected remote agent:\n%s", body)
	}
	// The orchestrator's idle_for never reaches it either.
	pl, _ := json.Marshal(bridge.CloseInput{IdleFor: "30m", Reason: "idle"})
	r := bridge.TestRequest(bridge.Envelope{Event: bridge.CloseEvent, SessionID: "orch", Payload: pl})
	m.handleCloseRequest(r)
	if remoteProposed(*m) {
		t.Error("the orchestrator's idle close proposed the protected remote agent")
	}
	for _, it := range closeItems(*m) {
		m.store.Settle(it, false, "kept")
	}
	m.refresh()

	// /unprotect takes the same form back; then the named close is offered.
	*m = g(m.guardCommand("/unprotect", []string{"Mossy Lemur@devbox"}))
	if len(m.guard.Names) != 0 {
		t.Fatalf("unprotect left %q", m.guard.Names)
	}
	if got = g(m.closeCommand([]string{"Mossy Lemur@devbox"})); !remoteProposed(got) {
		t.Errorf("unprotected remote agent not offered: flash %q", got.flash)
	}
}

func TestProtectBareNameCoversLocalAndRemote(t *testing.T) {
	m, feed, _ := remoteModel(t)
	remoteIdle(m, feed)
	m.ensureGuard()
	m.guardPath = filepath.Join(t.TempDir(), "protected.json")
	// "@local" is this machine's agent, recorded by its bare name, which
	// also covers an agent called so on any host.
	mm, _ := m.guardCommand("/protect", []string{"mossy lemur@local"})
	*m = mm.(Model)
	if len(m.guard.Names) != 1 || m.guard.Names[0] != "Mossy Lemur" {
		t.Fatalf("names %q", m.guard.Names)
	}
	for _, name := range []string{"Mossy Lemur@local", "Mossy Lemur@devbox"} {
		mm, _ = m.closeCommand([]string{name})
		if got := mm.(Model); openClose(got) != nil || !strings.Contains(got.flash, "protected (Mossy Lemur) · /unprotect Mossy Lemur to allow it") {
			t.Errorf("/close %s: flash %q", name, got.flash)
		}
	}
	// The orchestrator naming the local one by peer: refused.
	pl, _ := json.Marshal(bridge.CloseInput{Peers: []string{"user-aa"}, Reason: "tidy"})
	r := bridge.TestRequest(bridge.Envelope{Event: bridge.CloseEvent, SessionID: "orch", Payload: pl})
	m.handleCloseRequest(r)
	if d, ok := r.Decided(); !ok || d.Behavior != "deny" || !strings.Contains(d.Message, "/unprotect Mossy Lemur to allow it") {
		t.Errorf("close_agent of a protected agent: %+v %v", d, ok)
	}
	// Flags by name@host hold too.
	m.guard.Names = nil
	m.guard.SetFlag("Mossy Lemur@devbox", "keep")
	mm, _ = m.closeCommand([]string{"idle"})
	if it := openClose(mm.(Model)); it == nil || !strings.Contains(strings.Join(it.Body, "\n"), "Holds context, not selected: Mossy Lemur (keep)") || strings.Contains(strings.Join(it.Body, "\n"), "SIGTERM there") {
		t.Errorf("flagged remote agent in an idle close: %+v", it)
	}
}

func TestSendQuestionDeclinedGivenUpOrLinkLost(t *testing.T) {
	m, feed, _ := remoteModel(t)
	m.tab = tabInbox
	ask := func() (*bridge.Request, *inbox.Item) {
		pl, _ := json.Marshal(bridge.SendInput{Who: "mossy@devbox", Text: "rm -rf build", AskedBy: "den send, pid 2"})
		r := bridge.TestRequest(bridge.Envelope{Event: bridge.SendEvent, Payload: pl})
		m.handleRequest(r)
		for _, o := range m.store.Items() {
			if o.Kind == inbox.Message && o.Answerable() {
				m.selectItem(o)
				return r, o
			}
		}
		t.Fatal("no send question")
		return nil, nil
	}
	// ^N: nothing sent, the command told so.
	r, _ := ask()
	mm, _ := m.answer("deny")
	*m = mm.(Model)
	if d, ok := r.Decided(); !ok || d.Behavior != bridge.SendDeclined || d.Message != "declined in Den; nothing was sent" || len(feed.sent) != 0 {
		t.Errorf("^N: %+v, sent %q", d, feed.sent)
	}
	// The den send that asked gave up: ^Y sends nothing.
	r, it := ask()
	bridge.TestHangUp(r)
	mm, _ = m.answer("allow")
	*m = mm.(Model)
	if len(feed.sent) != 0 || it.State != inbox.Resolved || !strings.Contains(it.Resolved, "gave up") {
		t.Errorf("after hang-up: sent %q, item %v %q", feed.sent, it.State, it.Resolved)
	}
	// The agent's turns come and go while it waits: the question stays.
	// When its session ends, it is cancelled, and the command hears why,
	// never an empty answer.
	r, it = ask()
	event := func(kind string) {
		runAll(m.handleRemote(remoteMsg{u: remote.Update{Kind: remote.EventArrived, Host: "devbox", Event: events.Event{Kind: kind, Host: "devbox", Session: it.SessionID, Time: time.Now()}}, ok: true}))
	}
	for _, kind := range []string{events.TurnStart, events.Done, events.TurnStart, events.TurnFailed} {
		if event(kind); !it.Answerable() || decided(r) {
			t.Fatalf("after %s the question ended: %v %q", kind, it.State, it.Resolved)
		}
	}
	event(events.SessionEnd)
	if d, ok := r.Decided(); !ok || d.Behavior != bridge.SendCancelled || d.Message != "the request was cancelled: the session on devbox ended; nothing was sent" || it.Answerable() {
		t.Errorf("session ended: %+v %v, item %v %q", d, ok, it.State, it.Resolved)
	}
	// After a new turn, ^Y still sends it.
	r, it = ask()
	event(events.TurnStart)
	mm, cmd := m.answer("allow")
	*m = mm.(Model)
	m.finishAskedSend(firstMsg(t, cmd, func(x tea.Msg) bool { _, ok := x.(askedSentMsg); return ok }).(askedSentMsg))
	if d, ok := r.Decided(); len(feed.sent) != 1 || !ok || d.Behavior != "allow" {
		t.Errorf("^Y after a new turn: sent %q, answered %+v", feed.sent, d)
	}
	feed.sent = nil
	// The link drops while it waits: released and refused.
	r, it = ask()
	feed.setState("devbox", remote.Offline)
	m.handleRemote(remoteMsg{u: remote.Update{Kind: remote.HostChanged, Host: "devbox"}, ok: true})
	if d, ok := r.Decided(); !ok || d.Behavior != bridge.SendCancelled || d.Message != "the request was cancelled: the link to devbox was lost; nothing was sent" || it.Answerable() {
		t.Errorf("link lost: %+v, item %v", d, it.State)
	}
	if len(feed.sent) != 0 {
		t.Errorf("sent %q", feed.sent)
	}
}
