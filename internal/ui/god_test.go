package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/api"
	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/permmode"
)

// typeLine submits text from Den's own command line.
func typeLine(m *Model, text string) {
	m.input.SetValue(text)
	mm, _ := m.submit()
	*m = mm.(Model)
}

// God mode goes on only by /god on and then the phrase, typed in Den's
// TUI within the minute; anything else leaves it off, and the line after
// /god on goes nowhere else.
func TestGodModeOnlyByThePhrase(t *testing.T) {
	m := testModel(140, 40)
	if m.godOn() {
		t.Fatal("God mode on at start")
	}
	typeLine(&m, godPhrase)
	if m.godOn() {
		t.Fatal("the phrase alone turned it on")
	}
	typeLine(&m, "/god on")
	if m.godOn() || m.god.armedAt.IsZero() {
		t.Fatal("/god on alone turned it on, or did not wait for the phrase")
	}
	chats := len(m.orch.chat)
	typeLine(&m, "Let agents act without asking")
	if m.godOn() || !m.god.armedAt.IsZero() {
		t.Error("a near phrase turned it on, or left it waiting")
	}
	for _, e := range m.orch.chat[chats:] {
		if e.role == "you" {
			t.Errorf("the line after /god on reached the chat: %q", e.text)
		}
	}
	typeLine(&m, "/god on")
	m.god.armedAt = time.Now().Add(-godArmFor - time.Second)
	typeLine(&m, godPhrase)
	if m.godOn() {
		t.Error("the phrase after the minute turned it on")
	}
	typeLine(&m, "/god on")
	typeLine(&m, godPhrase)
	if !m.godOn() || m.god.since.IsZero() {
		t.Fatal("/god on and the phrase did not turn it on")
	}
	if !strings.Contains(strings.Join(m.header(160), "\n"), "GOD MODE") {
		t.Error("the header does not show God mode")
	}
	typeLine(&m, "/god off")
	if m.godOn() {
		t.Error("/god off left it on")
	}
}

// The dock may turn God mode off and never on; nor may den.sock or an
// app's turn; any unpair turns it off.
func TestGodModeFromOutside(t *testing.T) {
	m := testModel(140, 40)
	run := func(text string) api.CommandResult {
		r := &api.CommandRun{Text: text, Console: "app-host", Reply: make(chan api.CommandResult, 1)}
		m.runFromDock(r)
		return <-r.Reply
	}
	for _, line := range []string{"/god on", "/GOD ON", "/god", "/god on " + godPhrase} {
		if r := run(line); r.Code != api.CodeRefusedFromDock {
			t.Errorf("%q from the dock: %+v", line, r)
		}
	}
	if m.godOn() || !m.god.armedAt.IsZero() {
		t.Fatal("the dock armed or turned on God mode")
	}
	// Even if a dock line reached command(), /god on is refused there.
	m.fromDock = true // with an empty name, too
	mm, _ := m.godCommand([]string{"on"})
	m = mm.(Model)
	m.fromDock = false
	if !m.god.armedAt.IsZero() {
		t.Error("/god on from the dock armed God mode")
	}
	on := func() {
		typeLine(&m, "/god on")
		typeLine(&m, godPhrase)
		if !m.godOn() {
			t.Fatal("not on")
		}
	}
	on()
	if r := run("/god off"); r.Code != "" || m.godOn() {
		t.Errorf("/god off from the dock: %+v, on %v", r, m.godOn())
	}
	on()
	m.applyAPIAct(api.Act{GodOff: "the console was unpaired"})
	if m.godOn() {
		t.Error("an unpair left God mode on")
	}
}

// den mcp's mode question: in God mode, an agent the orchestrator starts
// gets bypassPermissions with a guard Den wrote (a fresh session id and its
// --settings file); one a lead starts, or any with God mode off, gets the
// global mode.
func TestGodModeSpawnsGuarded(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	m := testModel(140, 40)
	m.orch.control = true // the orchestrator is on Den's send_message
	old := fromOrchestrator
	t.Cleanup(func() { fromOrchestrator = old })
	fromOrchestrator = func(_ *Model, pid int) bool { return pid == 4242 }
	askFrom := func(pid int, lead string) bridge.Decision {
		payload, _ := json.Marshal(map[string]string{"lead": lead})
		r := bridge.TestRequest(bridge.Envelope{Event: bridge.ModeEvent, Payload: payload})
		r.PeerPID = pid
		m.modeQuery(r)
		d, _ := r.Decided()
		return d
	}
	ask := func(lead string) bridge.Decision { return askFrom(4242, lead) }
	if d := ask(""); d.Message == permmode.Bypass {
		t.Fatalf("bypass with God mode off: %+v", d)
	}
	typeLine(&m, "/god on")
	typeLine(&m, godPhrase)
	m.orch.control = false
	if d := ask(""); d.Message == permmode.Bypass {
		t.Fatalf("bypass before the orchestrator is on send_message: %+v", d)
	}
	m.orch.control = true
	d := ask("")
	sid, path := d.Answers["session_id"], d.Answers["settings"]
	if d.Message != permmode.Bypass || len(sid) != 36 || !filepath.IsAbs(path) {
		t.Fatalf("God mode's agent: %+v", d)
	}
	if g := m.med.guards[sid]; g == nil || g.settings != path || g.launched.IsZero() {
		t.Errorf("no guard recorded for %s", sid)
	}
	if raw, err := os.ReadFile(path); err != nil || !strings.Contains(string(raw), `"disableAllHooks":false`) {
		t.Errorf("settings %s: %v", raw, err)
	}
	if d2 := ask(""); d2.Answers["session_id"] == sid {
		t.Error("a session id given out twice")
	}
	// Another process saying it is the orchestrator's spawn gets the
	// global mode, and no guard is written.
	n := len(m.med.guards)
	if d := askFrom(7777, ""); d.Message == permmode.Bypass || d.Answers != nil || len(m.med.guards) != n {
		t.Errorf("an unrelated caller: %+v, guards %d", d, len(m.med.guards))
	}
	// At most guardsWaitingMax wait for their agent; a stale one goes.
	for i := 0; i < guardsWaitingMax; i++ {
		ask("")
	}
	if d := ask(""); d.Message == permmode.Bypass || m.guardsWaiting() > guardsWaitingMax {
		t.Errorf("past the cap: %+v, waiting %d", d, m.guardsWaiting())
	}
	for _, g := range m.med.guards {
		g.launched = time.Now().Add(-guardWait - time.Second)
	}
	m.dropStaleGuards()
	if len(m.med.guards) != 0 {
		t.Errorf("stale guards kept: %d", len(m.med.guards))
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("a stale guard's file kept: %v", err)
	}
}

// In God mode Den answers its own cards in godAutoKinds, by rule, and
// only the human answers the kinds in godTTYKinds, in Den's window.
func TestGodModeCards(t *testing.T) {
	m := testModel(140, 40)
	m.base = append(m.base, m.agents...)
	before := m.store.AddClose(m.agents[0].Session.SessionID, "Close Lemur?", []string{"Lemur"}, nil)
	before.At = time.Now().Add(-time.Hour)
	m.godSweep()
	if before.State == inbox.Resolved {
		t.Fatal("Den answered its own card with God mode off")
	}
	typeLine(&m, "/god on")
	typeLine(&m, godPhrase)
	cl := m.store.AddClose(m.agents[0].Session.SessionID, "Close Otter?", []string{"Otter"}, nil)
	m.godSweep()
	if before.State == inbox.Resolved {
		t.Error("Den answered a card that was waiting before God mode went on")
	}
	if cl.State != inbox.Resolved || cl.AnsweredBy != "rule" {
		t.Errorf("Den did not answer the close card by rule: state %v, by %q", cl.State, cl.AnsweredBy)
	}
	found := false
	for _, e := range m.orch.chat {
		found = found || strings.HasPrefix(e.text, "God mode: Den allowed close: ")
	}
	if !found {
		t.Error("no chat line for Den's own answer")
	}
	for k := range godTTYKinds {
		it := &inbox.Item{ID: "x", Kind: k, SessionID: "s"}
		if card := m.dockCard(it); card.DockAnswers || !card.TTYOnly {
			t.Errorf("%v in God mode: dock_answers %v, tty only %v", k, card.DockAnswers, card.TTYOnly)
		}
	}
	typeLine(&m, "/god off")
	if card := m.dockCard(&inbox.Item{ID: "x", Kind: inbox.Budget, SessionID: "s"}); card.TTYOnly {
		t.Error("TTY only with God mode off")
	}
}
