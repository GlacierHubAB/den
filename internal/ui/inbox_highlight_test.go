package ui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/inbox"
)

// The row drawn as selected, and the only row with answer keys, is the
// item ^Y and ^N act on, whatever arrives, resolves or moves in between.

// paneRow is one inbox item as drawn: its text and whether it is drawn
// selected.
type paneRow struct {
	text     string
	selected bool
}

// paneRows renders the Inbox pane as the human sees it.
func paneRows(t *testing.T, m Model) []paneRow {
	t.Helper()
	sel, _, _, _, _, _, _, _ := m.rowStyles(true)
	mark, _, _ := strings.Cut(sel.Render("x"), "x")
	if mark == "" {
		t.Fatal("a selected row has no style of its own")
	}
	var rows []paneRow
	for _, block := range strings.Split(m.inboxPane(120, 0), "\n\n") {
		rows = append(rows, paneRow{text: ansi.Strip(block), selected: strings.Contains(block, mark)})
	}
	return rows
}

// rowFor is the row that shows command, or a notice's text.
func rowFor(t *testing.T, rows []paneRow, text string) paneRow {
	t.Helper()
	for _, r := range rows {
		if strings.Contains(r.text, text) {
			return r
		}
	}
	t.Fatalf("no row shows %q", text)
	return paneRow{}
}

// checkBound fails unless the only row drawn as selected, and the only row
// with ^Y on it, is the one ^Y acts on (bound, "" for none).
func checkBound(t *testing.T, m Model, bound string) {
	t.Helper()
	for _, r := range paneRows(t, m) {
		is := bound != "" && strings.Contains(r.text, bound)
		if r.selected != is {
			t.Errorf("row drawn selected %v, bound %v:\n%s", r.selected, is, r.text)
		}
		if strings.Contains(r.text, "^Y") && !is {
			t.Errorf("answer keys on a row ^Y does not act on:\n%s", r.text)
		}
	}
	if it := m.inboxSelected(); bound != "" && (it == nil || !strings.Contains(strings.Join(it.Body, "\n")+it.Title, bound)) {
		t.Errorf("^Y acts on %+v, want the row with %q", it, bound)
	}
}

// arrive puts a permission request in the inbox the way the hook bridge
// does, with or without the refresh that follows it.
func arrive(m *Model, sid, command string, refresh bool) *bridge.Request {
	if refresh {
		return heldPerm(m, sid, command)
	}
	r := heldPerm(&Model{store: inbox.New()}, sid, command)
	m.store.Apply(r)
	return r
}

func TestNewRequestIsMarkedNotBound(t *testing.T) {
	for _, refresh := range []bool{true, false} {
		name := map[bool]string{true: "refreshed", false: "before a refresh"}[refresh]
		t.Run("above the selection, "+name, func(t *testing.T) {
			m := inboxModel()
			sid := m.agents[0].Session.SessionID
			runAll(m.handleRequest(req("Stop", sid, map[string]any{"last_assistant_message": "First done."})))
			runAll(m.handleRequest(req("Stop", sid, map[string]any{"last_assistant_message": "Second done."})))
			m.refresh()
			m.selectInbox(1) // the older notice
			checkBound(t, m, "First done.")
			p := arrive(&m, sid, "rm -rf build", refresh)
			checkBound(t, m, "First done.")
			if r := rowFor(t, paneRows(t, m), "rm -rf build"); !strings.Contains(r.text, "new request · select it to answer") {
				t.Errorf("the new request is not marked:\n%s", r.text)
			}
			if ctrl(m, 'y'); decided(p) {
				t.Error("^Y answered a request the human never selected")
			}
		})
		t.Run("below the selection, "+name, func(t *testing.T) {
			m := inboxModel()
			sid := m.agents[0].Session.SessionID
			a := heldPerm(&m, sid, "echo first")
			selectReq(t, &m, a)
			b := arrive(&m, sid, "echo second", refresh)
			checkBound(t, m, "echo first")
			if !strings.Contains(m.View().Content, "Inbox · 1 waiting, not selected") {
				t.Error("the pane title does not count the request that is not selected")
			}
			if ctrl(m, 'y'); !decided(a) || decided(b) {
				t.Errorf("^Y: first decided %v, second decided %v", decided(a), decided(b))
			}
		})
		t.Run("at the top, after an answer, "+name, func(t *testing.T) {
			m := inboxModel()
			sid := m.agents[0].Session.SessionID
			a := heldPerm(&m, sid, "echo first")
			selectReq(t, &m, a)
			m = ctrl(m, 'y') // answered: a stays selected, now resolved
			c := arrive(&m, sid, "echo third", refresh)
			checkBound(t, m, "echo first")
			got := ctrl(m, 'y')
			if decided(c) {
				t.Fatal("^Y answered the request that just arrived")
			}
			want := "The request you selected was already answered; nothing was sent. A request is waiting: select it to answer (↑ ↓)."
			if got.flash != want {
				t.Errorf("flash = %q", got.flash)
			}
			selectReq(t, &m, c)
			checkBound(t, m, "echo third")
			if ctrl(m, 'y'); !decided(c) {
				t.Error("^Y on the selected request did nothing")
			}
		})
	}
}

func TestItemResolvingUnderTheCursorLosesItsKeys(t *testing.T) {
	m := inboxModel()
	sid := m.agents[0].Session.SessionID
	a := heldPerm(&m, sid, "echo first")
	heldPerm(&m, sid, "echo second")
	selectReq(t, &m, a)
	m.store.Settle(itemOf(m, a), false, "answered in the terminal")
	checkBound(t, m, "echo first") // before any refresh
	if r := rowFor(t, paneRows(t, m), "echo first"); strings.Contains(r.text, "^Y") {
		t.Errorf("the answered request still shows keys:\n%s", r.text)
	}
	m.refresh()
	checkBound(t, m, "echo first")
}

func TestHighlightFollowsTheItemWhenTheListReordersBeforeARefresh(t *testing.T) {
	m := inboxModel()
	sid := m.agents[0].Session.SessionID
	a := heldPerm(&m, sid, "echo first")
	b := heldPerm(&m, sid, "echo second")
	selectReq(t, &m, b) // second row
	// a resolves and sinks below b; the view draws before Den refreshes.
	m.store.Settle(itemOf(m, a), false, "answered in the terminal")
	checkBound(t, m, "echo second")
	if ctrl(m, 'y'); !decided(b) {
		t.Error("^Y did not answer the highlighted request")
	}
}

func TestRapidKeysAfterAnArrivalKeepTheSettleGuard(t *testing.T) {
	answerSettle = 700 * time.Millisecond
	t.Cleanup(func() { answerSettle = 0 })
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	answerNow = func() time.Time { return now }
	t.Cleanup(func() { answerNow = time.Now })

	// Nothing chosen yet: the first row is the selection, and a request
	// that arrives there is held back from ^Y for a moment, even when the
	// key comes before Den has refreshed.
	m := inboxModel()
	sid := m.agents[0].Session.SessionID
	m.inboxCur = 0
	r := arrive(&m, sid, "echo first", false)
	checkBound(t, m, "echo first")
	now = now.Add(100 * time.Millisecond)
	got := ctrl(m, 'y')
	if decided(r) || !strings.Contains(got.flash, "A new request just came up") {
		t.Fatalf("^Y right after the arrival: decided %v, flash %q", decided(r), got.flash)
	}
	if ctrl(got, 'y'); !decided(r) {
		t.Error("the second ^Y did not answer")
	}

	// With an item chosen, keys pressed fast after an arrival stay on it.
	m = inboxModel()
	a := heldPerm(&m, sid, "echo second")
	selectReq(t, &m, a)
	b := arrive(&m, sid, "echo third", true)
	for range 3 {
		m = ctrl(m, 'n')
	}
	if d, _ := a.Decided(); d.Behavior != "deny" || decided(b) {
		t.Errorf("fast ^N: second %+v, third decided %v", d, decided(b))
	}
}

func TestFleetDetailShowsKeysOnlyOnTheRequestTheyAnswer(t *testing.T) {
	m := testModel(140, 40)
	m.base = sample()[:2]
	m.base[1].Session.SessionID = "fedcba9876543210"
	m.refresh()
	first, second := m.agents[0].Session.SessionID, m.agents[1].Session.SessionID
	a := heldPerm(&m, first, "echo first")
	b := heldPerm(&m, second, "echo second")
	for i, ag := range m.agents {
		if ag.Session.SessionID == second {
			m.cursor = i
		}
	}
	out := ansi.Strip(m.View().Content)
	if !strings.Contains(out, "echo second") {
		t.Fatalf("the selected agent's request is not shown:\n%s", out)
	}
	if strings.Contains(out, "^Y allow") {
		t.Errorf("^Y is shown on a request it does not answer:\n%s", out)
	}
	if !strings.Contains(out, "answer "+m.nameFor(first)+"'s request first") {
		t.Errorf("the detail does not say which request ^Y answers:\n%s", out)
	}
	// ^Y answers the first agent's request, which this view does not
	// show: it opens the Inbox on it instead, and answers only from there.
	m = ctrl(m, 'y')
	if decided(a) || decided(b) || m.tab != tabInbox || m.inboxSelected() != itemOf(m, a) || !strings.Contains(m.flash, "read it here first") {
		t.Fatalf("^Y from Fleet: first decided %v, second decided %v, tab %v, flash %q", decided(a), decided(b), m.tab, m.flash)
	}
	if ctrl(m, 'y'); !decided(a) || decided(b) {
		t.Errorf("^Y in the Inbox: first decided %v, second decided %v", decided(a), decided(b))
	}
}

func TestRefusalSaysWhatTheSelectedItemIs(t *testing.T) {
	m := inboxModel()
	sid := m.agents[0].Session.SessionID
	a := heldPerm(&m, sid, "echo first")
	b := heldPerm(&m, sid, "echo second")
	selectReq(t, &m, a)
	m.store.Release(itemOf(m, a), "you were at that terminal")
	m.refresh()
	checkBound(t, m, "echo first")
	got := ctrl(m, 'y')
	want := "The request you selected can only be answered in its terminal (/open); nothing was sent. A request is waiting: select it to answer (↑ ↓)."
	if decided(b) || got.flash != want {
		t.Errorf("^Y on a request in its terminal: decided %v, flash %q", decided(b), got.flash)
	}
}

// askQuestion puts a second agent's question waiting in Den.
func askQuestion(m *Model, sid string) *bridge.Request {
	m.store.Apply(req("PreToolUse", sid, map[string]any{"tool_name": twoAsks["tool_name"], "tool_input": twoAsks["tool_input"]}))
	in := map[string]any{"tool_name": twoAsks["tool_name"], "tool_input": twoAsks["tool_input"], "session_id": sid, "hook_event_name": "PermissionRequest"}
	p, _ := json.Marshal(in)
	r := bridge.TestRequest(bridge.Envelope{Event: "PermissionRequest", SessionID: sid, Payload: p})
	m.store.Apply(r)
	return r
}

func TestTypedAnswerWithAStaleSelectionGoesNowhere(t *testing.T) {
	const sid2 = "22222222-2222-4333-8444-555555555555"
	for _, stale := range []string{"back in its terminal", "dismissed"} {
		t.Run(stale, func(t *testing.T) {
			m, _ := heldQuestion(t, true)
			m.refresh()
			first := m.inboxSelected()
			if first == nil || !first.AsksInDen() {
				t.Fatalf("selected %+v", first)
			}
			r2 := askQuestion(&m, sid2)
			m.refresh()
			m.store.Release(first, "you chose the terminal")
			if stale == "dismissed" {
				m.store.Dismiss(first)
			}
			m.refresh()
			m = enter(m, "2")
			if decided(r2) {
				t.Fatal("the typed answer went to a question the human did not select")
			}
			if got := m.input.Value(); got != "2" {
				t.Errorf("the typed text left the line (sent on as a prompt?): line %q, flash %q", got, m.flash)
			}
			if !strings.Contains(m.flash, "Nothing was sent: the item you selected is not a waiting question.") {
				t.Errorf("flash = %q", m.flash)
			}
			// @Name still messages an agent on purpose.
			m.input.Reset()
			if m = enter(m, "@Nobody hello"); !strings.Contains(m.flash, `No agent called "Nobody"`) {
				t.Errorf("@Name did not go to the agent lookup: %q", m.flash)
			}
		})
	}
}

func TestTypedAnswerRightAfterAQuestionArrivesIsHeld(t *testing.T) {
	answerSettle = 700 * time.Millisecond
	t.Cleanup(func() { answerSettle = 0 })
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	answerNow = func() time.Time { return now }
	t.Cleanup(func() { answerNow = time.Now })

	// Nothing chosen yet; the question lands in the first row before Den
	// has refreshed, and a digit typed for something else meets it.
	m := inboxModel()
	m.store.AnswerQuestions(true)
	m.inboxCur = 0
	r := askQuestion(&m, m.agents[0].Session.SessionID)
	now = now.Add(100 * time.Millisecond)
	m = enter(m, "1")
	if decided(r) || !strings.Contains(m.flash, "A new question just came up") {
		t.Fatalf("Enter right after the question arrived: decided %v, flash %q", decided(r), m.flash)
	}
	mm, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if it := m.inboxSelected(); it == nil || it.Kind != inbox.Question || strings.Contains(m.flash, "came up") {
		t.Fatalf("the second Enter did not answer: flash %q", m.flash)
	}
	if !strings.Contains(m.flash, "Next question for") {
		t.Errorf("flash = %q", m.flash)
	}
}
