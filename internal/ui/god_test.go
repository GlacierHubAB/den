package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/api"
	"github.com/GlacierHubAB/den/internal/inbox"
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
	m.dockFrom = "app-host"
	mm, _ := m.godCommand([]string{"on"})
	m = mm.(Model)
	m.dockFrom = ""
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

// In God mode Den answers its own cards in godAutoKinds, by rule, and
// only the human answers the kinds in godTTYKinds, in Den's window.
func TestGodModeCards(t *testing.T) {
	m := testModel(140, 40)
	m.base = append(m.base, m.agents...)
	cl := m.store.AddClose(m.agents[0].Session.SessionID, "Close Otter?", []string{"Otter"}, nil)
	m.godSweep()
	if cl.State == inbox.Resolved {
		t.Fatal("Den answered its own card with God mode off")
	}
	typeLine(&m, "/god on")
	typeLine(&m, godPhrase)
	m.godSweep()
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
