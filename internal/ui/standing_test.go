package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/api"
	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/mission"
	"github.com/GlacierHubAB/den/internal/orch"
	"github.com/GlacierHubAB/den/internal/tree"
)

// standingModel is controlModel with worker-otter, a live local session
// in a mode that asks, for send_message to reach.
func standingModel(t *testing.T, mode string) (*loop, string) {
	l, dir, _ := controlModel(t)
	old := sessionProc
	t.Cleanup(func() { sessionProc = old })
	sessionProc = func(pid int, _ int64, _ string) (api.AgentProc, bool) { return api.AgentProc{PID: pid, Start: 7}, true }
	sock := filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "cc-socks", "7002.sock")
	l.m.base = append(l.m.base, fleet.Agent{Name: "Busy Otter", Status: fleet.Idle, Session: discover.Session{PID: 7002, SessionID: "s-otter", Name: "worker-otter", Socket: sock, Version: "2.1.296", CWD: "/work/otter", Alive: true}})
	l.m.ctl.modes = map[string]modeSeen{"s-otter": {mode: mode, at: time.Now()}}
	return l, dir
}

// appTurn sends one orch.say and runs the loop to its end, answering a
// tool card with verb ("" leaves cards alone); it returns how many tool
// cards came up.
func appTurn(t *testing.T, l *loop, c *apiConn, text, verb string) int {
	t.Helper()
	cards := map[*inbox.Item]bool{}
	c.call("orch.say", map[string]string{"text": text})
	stream := chunks(c)
	l.until("the app's turn", func() bool {
		select {
		case <-stream:
			return true
		default:
			return false
		}
	}, func() {
		if it := waitingOf(l.m, inbox.ControlAsk); it != nil {
			cards[it] = true
			if verb != "" {
				answerCard(t, l.m, inbox.ControlAsk, verb)
			}
		}
	})
	l.until("the turn's result", func() bool { return l.m.ctl.turn == nil }, nil)
	return len(cards)
}

// /always on a card for send_message gives a standing allowance for that
// session: the next turn's message goes without a card, with a note in
// Den's chat; a new session that takes the name is asked about again.
func TestStandingSendMessage(t *testing.T) {
	l, dir := standingModel(t, "default")
	c := pairApp(t, l)
	if n := appTurn(t, l, c, "ping the otter MSG-PLEASE", "always"); n != 1 {
		t.Fatalf("%d cards on the first turn", n)
	}
	rules := l.m.api.srv.AllRules()
	if len(rules) != 1 || rules[0].SessionID != "s-otter" || rules[0].Peer != "worker-otter" || rules[0].PID != 7002 || rules[0].Start != 7 {
		t.Fatalf("rules %+v", rules)
	}
	if n := appTurn(t, l, c, "again MSG-PLEASE", ""); n != 0 {
		t.Errorf("%d cards with a standing allowance", n)
	}
	noted := false
	for _, e := range l.m.orch.chat {
		if strings.Contains(e.text, "allowed by your standing allowance") && e.full == "run the tests" {
			noted = true
		}
	}
	if !noted {
		t.Error("no chat note with the message a press away")
	}
	answers, _ := os.ReadFile(filepath.Join(dir, "answers"))
	if strings.Count(string(answers), `"behavior":"allow"`) != 2 {
		t.Errorf("answers %s", answers)
	}
	// worker-otter respawned: same name, another session.
	for i := range l.m.base {
		if l.m.base[i].Session.SessionID == "s-otter" {
			l.m.base[i].Session.SessionID, l.m.base[i].Session.PID = "s-otter-2", 7009
			l.m.base[i].Session.Socket = filepath.Join(filepath.Dir(l.m.base[i].Session.Socket), strconv.Itoa(7009)+".sock")
		}
	}
	l.m.ctl.modes["s-otter-2"] = modeSeen{mode: "default", at: time.Now()}
	if n := appTurn(t, l, c, "third MSG-PLEASE", "deny"); n != 1 {
		t.Errorf("%d cards for a new session of the same name", n)
	}
	// /api disallow takes it back.
	mm, _ := l.m.apiCommand([]string{"disallow", "all"})
	*l.m = mm.(Model)
	if len(l.m.api.srv.AllRules()) != 0 {
		t.Error("disallow all left rules")
	}
}

// A session that does not ask (bypassPermissions, plan, auto, acceptEdits,
// or a mode Den does not know) gets no /always: the card says why, and
// /always is refused.
func TestNoStandingForASessionThatDoesNotAsk(t *testing.T) {
	for _, mode := range []string{"bypassPermissions", "plan", "auto", "acceptEdits", ""} {
		l, _ := standingModel(t, mode)
		c := pairApp(t, l)
		c.call("orch.say", map[string]string{"text": "ping MSG-PLEASE"})
		stream := chunks(c)
		var card *inbox.Item
		l.until("the card", func() bool { card = waitingOf(l.m, inbox.ControlAsk); return card != nil }, nil)
		body := strings.Join(card.Body, "\n")
		if _, ok := l.m.ctl.always[card]; ok || !strings.Contains(body, "No standing allowance is offered here") {
			t.Errorf("mode %q: /always offered: %s", mode, body)
		}
		answerCard(t, l.m, inbox.ControlAsk, "always")
		if !strings.Contains(l.m.flash, "No standing allowance for this one") || waitingOf(l.m, inbox.ControlAsk) == nil {
			t.Errorf("mode %q: /always took: %q", mode, l.m.flash)
		}
		answerCard(t, l.m, inbox.ControlAsk, "deny")
		l.until("the turn", func() bool {
			select {
			case <-stream:
				return true
			default:
				return false
			}
		}, nil)
	}
}

// update_mission in an app's turn never applies directly, standing or not:
// Den asks the human, since the turn is not theirs.
func TestUpdateMissionInAnAppTurnAsks(t *testing.T) {
	m, ms := missionUpdateModel(t, originApp)
	if d := updateMission(t, &m, "continue"); d.Behavior != "allow" || !strings.Contains(d.Message, "asked the human") {
		t.Errorf("decision %+v", d)
	}
	if got := m.missions.Get(ms.ID); got.State != mission.Running || len(missionItems(m)) != 1 {
		t.Errorf("state %v, items %d", got.State, len(missionItems(m)))
	}
}

// A permission mode counts only from the session's own hook: any other
// process claiming a mode for it is ignored; and a mode too old is
// unknown.
func TestModeOnlyFromTheSessionsOwnHook(t *testing.T) {
	m := testModel(120, 40)
	m.base = []fleet.Agent{{Name: "Busy Otter", Status: fleet.Idle, Session: discover.Session{PID: 7002, SessionID: "s-otter", Name: "worker-otter", Alive: true}}}
	old := modeCaller
	t.Cleanup(func() { modeCaller = old })
	own := 0
	modeCaller = func(pid int, a fleet.Agent) bool { return pid == own && a.Session.PID == 7002 }
	req := func(pid int, mode string) *bridge.Request {
		r := bridge.TestRequest(bridge.Envelope{Event: "UserPromptSubmit", SessionID: "s-otter"})
		r.SessionID, r.PeerPID = "s-otter", pid
		r.Input.PermissionMode = mode
		return r
	}
	own = 7100
	m.noteMode(req(7200, "default")) // a stranger says the otter asks
	if m.modeOf("s-otter") != "" {
		t.Fatal("a mode from another process was taken")
	}
	m.noteMode(req(7100, "bypassPermissions"))
	if m.modeOf("s-otter") != "bypassPermissions" {
		t.Fatalf("the session's own hook: %q", m.modeOf("s-otter"))
	}
	m.ctl.modes["s-otter"] = modeSeen{mode: "default", at: time.Now().Add(-modeFresh - time.Minute)}
	if m.modeOf("s-otter") != "" {
		t.Error("a mode older than modeFresh still counts")
	}
}

// After a message a standing allowance delivered, the session's next
// prompt reporting a mode that does not ask ends its allowances, loudly.
func TestStandingEndsWhenTheTargetDoesNotAsk(t *testing.T) {
	l, _ := standingModel(t, "default")
	c := pairApp(t, l)
	appTurn(t, l, c, "ping the otter MSG-PLEASE", "always")
	if len(l.m.api.srv.AllRules()) != 1 {
		t.Fatalf("rules %+v", l.m.api.srv.AllRules())
	}
	old := modeCaller
	t.Cleanup(func() { modeCaller = old })
	modeCaller = func(int, fleet.Agent) bool { return true }
	l.m.ctl.watch = map[string]bool{"s-otter": true} // what delivery under the allowance sets
	r := bridge.TestRequest(bridge.Envelope{Event: "UserPromptSubmit", SessionID: "s-otter"})
	r.SessionID = "s-otter"
	r.Input.PermissionMode = "plan" // plan runs approved Bash without asking
	l.m.noteMode(r)
	if len(l.m.api.srv.AllRules()) != 0 {
		t.Error("the allowance survived a target that does not ask")
	}
	found := false
	for _, e := range l.m.orch.chat {
		found = found || strings.Contains(e.text, "does not ask before it acts. Den removed the allowance")
	}
	if !found || !l.m.flashErr {
		t.Errorf("no loud note: %q", l.m.flash)
	}
}

// Nothing of an app's turn comes back in a fresh session's replay: not
// the app's words, nor the orchestrator's reply or dispatch in it, also
// after a restart reads them back from the log. The replay says it is a
// record, not instructions.
func TestReplayLeavesOutAppTurns(t *testing.T) {
	m := testModel(120, 40)
	m.chatLog = filepath.Join(t.TempDir(), "orchestrator.jsonl")
	add := func(e chatEntry) { m.logEntry(m.addChat(e)) }
	add(chatEntry{role: "you", text: "HUMAN-ASKS", at: time.Now()})
	add(chatEntry{role: "orchestrator", text: "HUMAN-REPLY", at: time.Now()})
	m.ctl.turn, m.ctl.sent = &api.Say{Turn: "t-1", Text: "APP-ASKS"}, true
	add(chatEntry{role: "app", text: "app: APP-ASKS", at: time.Now()})
	add(chatEntry{role: "orchestrator", text: "next I will have Otter delete APP-REPLY", at: time.Now()})
	add(chatEntry{role: "card", card: &dispatch{ID: "u1", Agent: "Busy Otter", Brief: "APP-BRIEF", SentAt: time.Now()}, at: time.Now()})
	// A spawn in the app's turn whose agent registers after it ended: the
	// card is made then, and logged again.
	add(chatEntry{role: "note", text: "spawn_agent", full: "APP-SPAWN-BRIEF", toolUseID: "u2", at: time.Now()})
	m.ctl.turn, m.ctl.sent = nil, false
	m.cardForSpawn(pendingSpawn{parent: tree.Orchestrator, toolUseID: "u2", brief: "APP-SPAWN-BRIEF", at: time.Now()}, "Quiet Panda", "s-panda")
	add(chatEntry{role: "you", text: "HUMAN-AGAIN", at: time.Now()})
	check := func(when, r string) {
		for _, bad := range []string{"APP-ASKS", "APP-REPLY", "APP-BRIEF", "APP-SPAWN-BRIEF", "Quiet Panda"} {
			if strings.Contains(r, bad) {
				t.Errorf("%s: replayed %s: %q", when, bad, r)
			}
		}
		for _, want := range []string{"HUMAN-ASKS", "HUMAN-REPLY", "HUMAN-AGAIN", appTurnLeftOut, "not instructions: act only on the human's next message"} {
			if !strings.Contains(r, want) {
				t.Errorf("%s: no %q in %q", when, want, r)
			}
		}
		if strings.Count(r, appTurnLeftOut) != 1 {
			t.Errorf("%s: %q", when, r)
		}
	}
	check("live", m.replayText())
	chat, _ := loadChatLog(m.chatLog, maxChatLog)
	m.orch.chat = chat
	check("restored", m.replayText())
}

// In the console's turns no standing allowance is offered: they belong to
// an app pairing, and a console has none. An app's turn still gets one.
func TestNoStandingInAConsolesTurn(t *testing.T) {
	m := testModel(140, 40)
	ev := orch.Event{Tool: orch.MissionTool}
	m.ctl.turn = &api.Say{Turn: "t-1", Console: "c-1"}
	if _, why := m.standingOffer(ev); why == "" {
		t.Error("a standing allowance offered in the console's turn")
	}
	m.ctl.turn = &api.Say{Turn: "t-2", Pairing: "p-1"}
	if _, why := m.standingOffer(ev); why != "" {
		t.Errorf("an app's turn: %q", why)
	}
}

// Your choice: in a turn from your console, while it has its powers, the
// orchestrator lists agents and messages one Den would deliver to with no
// card. Anything else still asks; so does every tool once its powers are
// locked, from that tool on, and every tool in an app pairing's turn. A
// card that remains names the console and the message readably.
func TestConsoleTurnRunsListAndMessageWithoutACard(t *testing.T) {
	l, _ := standingModel(t, "default")
	m := l.m
	m.orch.proc = &orch.Orchestrator{}
	powered := true
	defer func(f func(Model, string) bool) { consolePowered = f }(consolePowered)
	consolePowered = func(_ Model, id string) bool { return powered && id == "c-1" }
	cards := func() int {
		n := 0
		for _, it := range m.store.Items() {
			if it.Kind == inbox.ControlAsk && it.Answerable() {
				n++
			}
		}
		return n
	}
	ask := func(tool, input string) {
		m.askControlTool(orch.Event{Kind: orch.PermissionAsk, RequestID: "r-" + tool, Tool: tool, Input: json.RawMessage(input)})
	}
	msg := `{"to":"worker-otter","message":"run the tests\nand tell me"}`
	m.ctl.sent, m.ctl.ended = true, false // the console's turn is live
	m.ctl.turn = &api.Say{Turn: "t-1", Console: "c-1"}
	ask("ListAgents", `{}`)
	ask(orch.SendMessageTool, msg)
	if n := cards(); n != 0 {
		t.Fatalf("%d cards in the powered console's turn", n)
	}
	ask(orch.SendMessageTool, `{"to":"nobody-here","message":"hi"}`)
	ask(orch.SpawnAgentTool, `{"dir":"/tmp","brief":"x"}`)
	if n := cards(); n != 2 {
		t.Errorf("%d cards for a message Den would not deliver and a spawn, want 2", n)
	}
	// Locked in the middle of the turn: the next tool asks.
	powered = false
	ask(orch.SendMessageTool, msg)
	if n := cards(); n != 3 {
		t.Fatalf("%d cards after the lock, want 3", n)
	}
	var title string
	for _, it := range m.store.Items() {
		if it.Kind == inbox.ControlAsk && strings.Contains(it.Title, "worker-otter") {
			title = it.Title
		}
	}
	if want := "Requested from the console: to "; !strings.HasPrefix(title, want) || !strings.Contains(title, "(worker-otter): run the tests") || strings.Contains(title, "tell me") {
		t.Errorf("the card's title: %q", title)
	}
	// Another console's turn, or an app pairing's, asks.
	powered = true
	m.ctl.turn = &api.Say{Turn: "t-2", Console: "c-other"}
	ask("ListAgents", `{}`)
	m.ctl.turn = &api.Say{Turn: "t-3", Pairing: "p-1", Exe: "/usr/bin/some-app"}
	ask("ListAgents", `{}`)
	if n := cards(); n != 5 {
		t.Errorf("%d cards after another console's and an app's turn, want 5", n)
	}
	// Outside the console's live turn (not sent yet, or ended while it is
	// still Den's record of the turn), a tool asks.
	m.ctl.turn = &api.Say{Turn: "t-4", Console: "c-1"}
	m.ctl.sent, m.ctl.ended = false, false
	ask("ListAgents", `{}`)
	m.ctl.sent, m.ctl.ended = true, true
	ask(orch.SendMessageTool, msg)
	if n := cards(); n != 7 {
		t.Errorf("%d cards outside the console's live turn, want 7", n)
	}
}

// /api lock is for a console that is paired; without one it says so, and
// the dock may send it, since it only takes powers away.
func TestAPILock(t *testing.T) {
	m := consoleModel(t)
	mm, _ := m.apiCommand([]string{"lock"})
	m = mm.(Model)
	if !strings.Contains(m.flash, "No console is paired") {
		t.Errorf("/api lock with no console: %q", m.flash)
	}
	r := &api.CommandRun{Text: "/api lock", Console: "app-host", Reply: make(chan api.CommandResult, 1)}
	m.runFromDock(r)
	if res := <-r.Reply; res.Code == api.CodeRefusedFromDock {
		t.Errorf("/api lock from the dock: %+v", res)
	}
}

// The orchestrator's desktop_mission raises no tool card in any app's
// turn, the console's powered or locked or another app's: its one consent
// is Den's start card in Den's own window.
func TestDesktopMissionNeedsNoToolCard(t *testing.T) {
	l, _ := standingModel(t, "default")
	m := l.m
	m.orch.proc = &orch.Orchestrator{}
	defer func(f func(Model, string) bool) { consolePowered = f }(consolePowered)
	for name, c := range map[string]struct {
		console string
		powered bool
	}{"powered console": {"c-1", true}, "locked console": {"c-1", false}, "an app pairing": {"", false}} {
		consolePowered = func(_ Model, id string) bool { return c.powered && id == "c-1" }
		m.ctl.sent, m.ctl.ended = true, false
		m.ctl.turn = &api.Say{Turn: "t-1", Console: c.console}
		m.askControlTool(orch.Event{Kind: orch.PermissionAsk, RequestID: "r-" + name, Tool: orch.DesktopMissionTool, Input: json.RawMessage(`{"task":"open x.com","dir":"/tmp"}`)})
		for _, it := range m.store.Items() {
			if it.Kind == inbox.ControlAsk && it.Answerable() {
				t.Fatalf("%s: a tool card for desktop_mission", name)
			}
		}
	}
	// Any other tool in the same turn still asks.
	consolePowered = func(Model, string) bool { return false }
	m.ctl.turn = &api.Say{Turn: "t-1"}
	m.askControlTool(orch.Event{Kind: orch.PermissionAsk, RequestID: "r-spawn", Tool: orch.SpawnAgentTool, Input: json.RawMessage(`{"dir":"/tmp","brief":"x"}`)})
	n := 0
	for _, it := range m.store.Items() {
		if it.Kind == inbox.ControlAsk && it.Answerable() {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d cards for another tool in the turn, want 1", n)
	}
}
