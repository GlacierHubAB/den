package ui

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/remote"
)

// spawnModel is remoteModel with devbox allowing spawn, and the window
// opener recorded instead of run.
func spawnModel(t *testing.T) (*Model, *fakeFeed, *[]string) {
	t.Helper()
	m, feed, _ := remoteModel(t)
	feed.hosts[0].Policy, feed.hosts[0].Applied = remote.Approve, remote.Approve
	feed.hosts[0].Spawn, feed.hosts[0].SpawnDirs = remote.SpawnTmux, []string{"~/src"}
	feed.hosts[0].SpawnCap = &remote.SpawnCap{Tmux: "3.4", Claude: "2.1.289"}
	feed.spawnRes = remote.SpawnResult{SessionID: "11111111-2222-4333-8444-555555555555", Peer: "api", Critter: "Snug Panda", Route: "tmux",
		TmuxName: "den-api-11111111", Dir: "/home/user/src/api"}
	m.syncRemote()
	var opened []string
	old := openAttach
	openAttach = func(_ context.Context, title string, argv []string) error {
		opened = append(opened, title+" | "+strings.Join(argv, " "))
		return nil
	}
	t.Cleanup(func() { openAttach = old })
	return m, feed, &opened
}

// run is one command's message, ignoring the timers it batches.
func runOne[T any](t *testing.T, c tea.Cmd) T {
	t.Helper()
	var zero T
	if c == nil {
		t.Fatalf("no command")
	}
	switch v := c().(type) {
	case T:
		return v
	case tea.BatchMsg:
		for _, x := range v {
			if x == nil {
				continue
			}
			done := make(chan tea.Msg, 1)
			go func() { done <- x() }()
			select {
			case msg := <-done:
				if got, ok := msg.(T); ok {
					return got
				}
			case <-time.After(time.Second): // a timer
			}
		}
	}
	t.Fatalf("no %T from the command", zero)
	return zero
}

func TestParseSpawn(t *testing.T) {
	p, err := parseSpawn("/spawn @DevBox ~/src/api model=sonnet name=Api worktree=fix-1 Fix the login;\n then $(test) `x`")
	if err != nil {
		t.Fatal(err)
	}
	if p.host != "devbox" || p.req.Dir != "~/src/api" || p.req.Model != "sonnet" || p.req.Peer != "Api" || p.req.Worktree != "fix-1" || !p.req.Check {
		t.Errorf("plan = %+v", p)
	}
	if p.brief != "Fix the login;\n then $(test) `x`" {
		t.Errorf("brief = %q", p.brief)
	}
	if !strings.HasPrefix(p.req.Brief, "Brief from your human") || !strings.Contains(p.req.Brief, p.brief) || !strings.Contains(p.req.Brief, "worktree-fix-1") {
		t.Errorf("prompt = %q", p.req.Brief)
	}
	for _, bad := range []string{"/spawn", "/spawn devbox ~/src brief", "/spawn @devbox", "/spawn @devbox ~/src", "/spawn @devbox ~/src model=opus"} {
		if _, err := parseSpawn(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestSpawnCommandPreviewsThenEnterStartsAndOpensTheWindow(t *testing.T) {
	m, feed, opened := spawnModel(t)
	mm, cmd := m.spawnCommand("/spawn @devbox ~/src/api model=haiku Fix the login")
	*m = mm.(Model)
	m.spawnChecked(runOne[spawnCheckedMsg](t, cmd))
	if len(feed.spawns) != 1 || !feed.spawns[0].Check || feed.spawns[0].Dir != "~/src/api" || feed.spawns[0].Orchestrator {
		t.Fatalf("check = %+v", feed.spawns)
	}
	out := frame(m)
	for _, want := range []string{"Start on devbox", "Start Snug Panda on devbox", "/home/user/src/api", "tmux on devbox", "haiku", "Fix the login", "Enter start it"} {
		if !strings.Contains(out, want) {
			t.Errorf("preview lacks %q:\n%s", want, out)
		}
	}
	mm, cmd = m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	*m = mm.(Model)
	m.spawnStarted(runOne[spawnStartedMsg](t, cmd))
	if len(feed.spawns) != 2 {
		t.Fatalf("spawns = %+v", feed.spawns)
	}
	start := feed.spawns[1]
	if start.Check || start.SessionID != feed.spawnRes.SessionID || start.Peer != "api" || start.Model != "haiku" {
		t.Errorf("start = %+v", start)
	}
	if !strings.Contains(m.flash, "Started Snug Panda on devbox") {
		t.Errorf("flash = %q", m.flash)
	}
	// The window: started by the batch spawnStarted returned.
	cmdOpen := m.openRemoteAgent("devbox", feed.spawnRes)
	runOne[attachOpenedMsg](t, cmdOpen)
	if len(*opened) != 1 || (*opened)[0] != "den · api@devbox | ssh -t -- devbox den remote attach den-api-11111111" {
		t.Errorf("opened = %q", *opened)
	}
	if len(m.store.Items()) != 0 {
		t.Errorf("the human's own spawn put something in the inbox: %+v", m.store.Items()[0])
	}
}

func TestSpawnEscCancelsAndRefusals(t *testing.T) {
	m, feed, _ := spawnModel(t)
	mm, cmd := m.spawnCommand("/spawn @devbox ~/src/api Fix it")
	*m = mm.(Model)
	m.spawnChecked(runOne[spawnCheckedMsg](t, cmd))
	mm, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	*m = mm.(Model)
	if m.rspawn.ready != nil || len(feed.spawns) != 1 {
		t.Errorf("Esc did not cancel: %+v", feed.spawns)
	}
	mm, _ = m.spawnCommand("/spawn @gpu ~/src Fix it")
	if f := mm.(Model).flash; !strings.Contains(f, "does not allow spawn") || !strings.Contains(f, "@devbox") {
		t.Errorf("gpu: %q", f)
	}
	feed.checkErr = errors.New("~/elsewhere is not under spawn_dirs for devbox (~/src); nothing was started")
	mm, cmd = m.spawnCommand("/spawn @devbox ~/elsewhere Fix it")
	*m = mm.(Model)
	m.spawnChecked(runOne[spawnCheckedMsg](t, cmd))
	if m.rspawn.ready != nil || !strings.Contains(m.flash, "not under spawn_dirs") {
		t.Errorf("refused check: %q", m.flash)
	}
}

func spawnRequest(host string) *bridge.Request {
	payload, _ := json.Marshal(bridge.SpawnInput{Host: host, Dir: "~/src/api", Brief: "SECRET-BRIEF do the thing\nall of it", AskedBy: "den remote spawn, pid 42"})
	return bridge.TestRequest(bridge.Envelope{Event: bridge.SpawnEvent, Payload: payload})
}

func spawnItem(m *Model) *inbox.Item {
	for _, it := range m.store.Items() {
		if it.Kind == inbox.Spawn {
			return it
		}
	}
	return nil
}

func TestCLISpawnIsOneQuestionWithTheFullBriefAndNoAlways(t *testing.T) {
	m, feed, _ := spawnModel(t)
	req := spawnRequest("devbox")
	m.spawnChecked(runOne[spawnCheckedMsg](t, m.handleRequest(req)))
	it := spawnItem(m)
	if it == nil || !it.Answerable() || it.Title != "Start Snug Panda on devbox?" {
		t.Fatalf("item = %+v", it)
	}
	body := strings.Join(it.Body, "\n")
	for _, want := range []string{"host      devbox", "tmux on devbox", "/home/user/src/api", "the host's default", "SECRET-BRIEF do the thing\nall of it", "asked by  den remote spawn, pid 42", "skills    none"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	if _, done := req.Decided(); done || len(feed.spawns) != 1 {
		t.Fatal("started or answered before the human did")
	}
	mm, _ := m.answer("always")
	*m = mm.(Model)
	if !it.Answerable() || len(feed.spawns) != 1 || !strings.Contains(m.flash, "no always") {
		t.Fatalf("always: %q %+v", m.flash, it)
	}
	m.tab = tabInbox
	mm, cmd := m.answer("allow")
	*m = mm.(Model)
	m.spawnStarted(runOne[spawnStartedMsg](t, cmd))
	d, done := req.Decided()
	if !done || d.Behavior != "allow" || !strings.Contains(d.Message, "started Snug Panda on devbox") {
		t.Errorf("decision = %+v %v", d, done)
	}
	if len(feed.spawns) != 2 || feed.spawns[1].Check {
		t.Errorf("spawns = %+v", feed.spawns)
	}
}

func TestCLISpawnDeclinedAndUnanswered(t *testing.T) {
	m, feed, _ := spawnModel(t)
	req := spawnRequest("devbox")
	m.spawnChecked(runOne[spawnCheckedMsg](t, m.handleRequest(req)))
	m.tab = tabInbox
	mm, _ := m.answer("deny")
	*m = mm.(Model)
	if d, _ := req.Decided(); d.Behavior != "deny" || len(feed.spawns) != 1 {
		t.Errorf("declined: %+v %+v", d, feed.spawns)
	}
	req2 := spawnRequest("devbox")
	m.spawnChecked(runOne[spawnCheckedMsg](t, m.handleRequest(req2)))
	var id string
	for _, it := range m.store.Items() {
		if it.Kind == inbox.Spawn && it.Answerable() {
			id = it.ID
		}
	}
	m.expireSpawn(spawnExpiredMsg{itemID: id})
	if d, _ := req2.Decided(); d.Behavior != "deny" || !strings.Contains(d.Message, "declined (no answer)") || len(feed.spawns) != 2 {
		t.Errorf("unanswered: %+v", d)
	}
	if spawnAnswerWait != 10*time.Minute {
		t.Errorf("wait = %s", spawnAnswerWait)
	}
	// A refused check never reaches the inbox.
	feed.checkErr = errors.New("the folder is not under the host's spawn_dirs, by its real path")
	req3 := spawnRequest("devbox")
	n := len(m.store.Items())
	m.spawnChecked(runOne[spawnCheckedMsg](t, m.handleRequest(req3)))
	if d, _ := req3.Decided(); d.Behavior != "deny" || len(m.store.Items()) != n {
		t.Errorf("refused: %+v", d)
	}
	// From a host: never.
	r := spawnRequest("devbox")
	r.Host = "devbox"
	m.handleRequest(r)
	if d, _ := r.Decided(); d.Behavior != "deny" {
		t.Errorf("a host asked: %+v", d)
	}
}

func TestEnterOnASpawnedRemoteAgentAttaches(t *testing.T) {
	m, feed, opened := spawnModel(t)
	feed.agents[0].Tmux, feed.agents[0].Session.Name = "den-mossy-11111111", "mossy"
	m.syncRemote()
	m.refresh()
	for i, a := range m.agents {
		if a.Host == "devbox" {
			m.cursor = i
		}
	}
	_, cmd := m.openSelected()
	runOne[attachOpenedMsg](t, cmd)
	if len(*opened) != 1 || !strings.HasSuffix((*opened)[0], "den remote attach den-mossy-11111111") {
		t.Errorf("opened = %q", *opened)
	}
}

func TestLeftoverCleanupAsksFirst(t *testing.T) {
	m, feed, _ := spawnModel(t)
	feed.leftovers = []remote.Spawned{{SessionID: "11111111-2222-4333-8444-555555555555", TmuxName: "den-api-11111111"}}
	if out := strings.Join(m.remoteLines(100), "\n"); !strings.Contains(out, "/remote cleanup devbox") {
		t.Errorf("listing:\n%s", out)
	}
	mm, _ := m.remoteCommand([]string{"cleanup", "devbox"})
	*m = mm.(Model)
	var it *inbox.Item
	for _, x := range m.store.Items() {
		if x.Kind == inbox.Leftover {
			it = x
		}
	}
	if it == nil || !strings.Contains(strings.Join(it.Body, "\n"), "den-api-11111111") || len(feed.cleaned) != 0 {
		t.Fatalf("item = %+v cleaned %v", it, feed.cleaned)
	}
	m.tab = tabInbox
	mm, cmd := m.answer("allow")
	*m = mm.(Model)
	m.leftoverDone(runOne[leftoverDoneMsg](t, cmd))
	if len(feed.cleaned) != 1 || feed.cleaned[0] != "devbox/den-api-11111111" {
		t.Errorf("cleaned = %v", feed.cleaned)
	}
}
