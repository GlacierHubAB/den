package ui

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/inbox"
)

// The settle guard is tested on its own; elsewhere ^Y acts at once.
func TestMain(m *testing.M) {
	answerSettle = 0
	os.Exit(m.Run())
}

// ^Y and ^N act on the item the human selected, found by its id, and only
// while it is still open: never on whatever slid into its row.

// heldPerm puts a Bash permission waiting on Den in the inbox.
func heldPerm(m *Model, sid, command string) *bridge.Request {
	in := map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": command}, "session_id": sid, "hook_event_name": "PermissionRequest"}
	p, _ := json.Marshal(in)
	r := bridge.TestRequest(bridge.Envelope{Event: "PermissionRequest", SessionID: sid, Payload: p})
	m.store.Apply(r)
	m.refresh()
	return r
}

func cmdOf(r *bridge.Request) string {
	var in struct {
		ToolInput struct {
			Command string `json:"command"`
		} `json:"tool_input"`
	}
	_ = json.Unmarshal(r.Payload, &in)
	return in.ToolInput.Command
}

func itemOf(m Model, r *bridge.Request) *inbox.Item {
	for _, it := range m.store.Items() {
		if it.SessionID == r.SessionID && strings.Contains(strings.Join(it.Body, "\n")+it.Title, cmdOf(r)) {
			return it
		}
	}
	return nil
}

func decided(r *bridge.Request) bool { _, ok := r.Decided(); return ok }

func selectReq(t *testing.T, m *Model, r *bridge.Request) {
	t.Helper()
	it := itemOf(*m, r)
	if it == nil {
		t.Fatalf("no item for %q", cmdOf(r))
	}
	m.selectItem(it)
	_ = m.View()
}

func inboxModel() Model {
	m := testModel(140, 40)
	m.tab = tabInbox
	return m
}

func TestAnswerIgnoresTheSelectedItemResolvedBeforeTheKey(t *testing.T) {
	m := inboxModel()
	sid := m.agents[0].Session.SessionID
	a := heldPerm(&m, sid, "echo first")
	b := heldPerm(&m, sid, "echo second")
	selectReq(t, &m, a)
	// a is answered in its terminal between the render and the keypress.
	m.store.Settle(itemOf(m, a), false, "answered in the terminal")
	m.refresh()
	for _, k := range []rune{'y', 'n'} {
		got := ctrl(m, k)
		if decided(b) {
			t.Fatalf("^%c answered the other request", k)
		}
		if !strings.Contains(got.flash, "That request was answered; select one to answer.") {
			t.Errorf("^%c flash = %q", k, got.flash)
		}
	}
	mm, _ := m.command("/always")
	if decided(b) || !strings.Contains(mm.(Model).flash, "was answered") {
		t.Errorf("/always: flash %q", mm.(Model).flash)
	}
	// Even with no refresh between the change and the key.
	m2 := inboxModel()
	a = heldPerm(&m2, sid, "echo first")
	b = heldPerm(&m2, sid, "echo second")
	selectReq(t, &m2, a)
	m2.store.Settle(itemOf(m2, a), false, "answered in the terminal")
	if ctrl(m2, 'y'); decided(b) {
		t.Fatal("^Y answered the other request without a refresh")
	}
}

func TestSelectionFollowsTheItemWhenOthersArriveAbove(t *testing.T) {
	m := inboxModel()
	sid := m.agents[0].Session.SessionID
	runAll(m.handleRequest(req("Stop", sid, map[string]any{"last_assistant_message": "Finished."})))
	m.refresh()
	m.selectInbox(0) // the notice
	_ = m.View()
	p := heldPerm(&m, sid, "rm -rf build") // waiting: sorts above the notice
	if it := m.inboxSelected(); it == nil || it.Kind != inbox.Done {
		t.Fatalf("selection moved to %+v", it)
	}
	if got := ctrl(m, 'y'); decided(p) || !strings.Contains(got.flash, "Nothing is waiting") {
		t.Errorf("^Y on the notice: decided %v, flash %q", decided(p), got.flash)
	}
	// The human selects it: then ^Y answers it.
	selectReq(t, &m, p)
	if ctrl(m, 'y'); !decided(p) {
		t.Error("^Y on the selected request did nothing")
	}
}

func TestSelectionFollowsTheItemWhenTheListReorders(t *testing.T) {
	m := inboxModel()
	sid := m.agents[0].Session.SessionID
	a := heldPerm(&m, sid, "echo first")
	b := heldPerm(&m, sid, "echo second")
	selectReq(t, &m, b) // second row
	// a resolves: it sinks below b, and b moves to the first row.
	m.store.Settle(itemOf(m, a), false, "answered in the terminal")
	m.refresh()
	if m.inboxCur != 0 {
		t.Errorf("cursor %d, want b's new row 0", m.inboxCur)
	}
	if ctrl(m, 'y'); !decided(b) {
		t.Fatal("^Y did not answer the selected request after the reorder")
	}
	if d, _ := b.Decided(); d.Behavior != "allow" {
		t.Errorf("b got %+v", d)
	}
}

func TestSelectedItemRemovedLeavesNothingSelected(t *testing.T) {
	m := inboxModel()
	sid := m.agents[0].Session.SessionID
	a := heldPerm(&m, sid, "echo first")
	b := heldPerm(&m, sid, "echo second")
	c := heldPerm(&m, sid, "echo third")
	selectReq(t, &m, a)
	it := itemOf(m, a)
	m.store.Settle(it, false, "answered in the terminal")
	m.store.Dismiss(it)
	m.refresh()
	got := ctrl(m, 'y')
	if decided(b) || decided(c) {
		t.Fatal("^Y answered a request the human never selected")
	}
	if !strings.Contains(got.flash, "The item you selected is gone; select one to answer.") {
		t.Errorf("flash = %q", got.flash)
	}
	if got.inboxCur != -1 {
		t.Errorf("a row is still highlighted: cursor %d", got.inboxCur)
	}
	// Moving selects again.
	m.move(1)
	if sel := m.inboxSelected(); sel == nil || sel != itemOf(m, b) {
		t.Fatalf("after ↓ selected %+v", sel)
	}
	if ctrl(m, 'y'); !decided(b) || decided(c) {
		t.Error("^Y after a fresh selection")
	}
}

func TestQuestionAnswerGoesOnlyToTheSelectedQuestion(t *testing.T) {
	m, r := heldQuestion(t, true)
	m.refresh()
	first := m.inboxSelected()
	if first == nil || !first.AsksInDen() {
		t.Fatalf("selected %+v", first)
	}
	// A second agent asks too; then the first question goes back to its terminal.
	sid2 := "22222222-2222-4333-8444-555555555555"
	m.store.Apply(req("PreToolUse", sid2, map[string]any{"tool_name": twoAsks["tool_name"], "tool_input": twoAsks["tool_input"]}))
	in := map[string]any{"tool_name": twoAsks["tool_name"], "tool_input": twoAsks["tool_input"], "session_id": sid2, "hook_event_name": "PermissionRequest"}
	p, _ := json.Marshal(in)
	r2 := bridge.TestRequest(bridge.Envelope{Event: "PermissionRequest", SessionID: sid2, Payload: p})
	m.store.Apply(r2)
	m.refresh()
	m.store.Settle(first, false, "answered in the terminal")
	m.refresh()
	m = enter(m, "1")
	m = enter(m, "1")
	if decided(r2) {
		t.Fatal("typed answers went to a question the human did not select")
	}
	_ = r
	// The human selects the second question: then it is answered.
	m.input.Reset()
	m.tab = tabInbox
	for _, it := range m.store.Items() {
		if it.SessionID == sid2 {
			m.selectItem(it)
		}
	}
	m = enter(m, "1")
	m = enter(m, "1")
	if !decided(r2) {
		t.Errorf("the selected question was not answered; flash %q", m.flash)
	}
}

func TestAnswerWaitsWhenTheTopRequestJustChanged(t *testing.T) {
	answerSettle = 700 * time.Millisecond
	t.Cleanup(func() { answerSettle = 0 })
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	answerNow = func() time.Time { return now }
	t.Cleanup(func() { answerNow = time.Now })

	m := testModel(140, 40)
	m.tab = tabFleet
	sid := m.agents[0].Session.SessionID
	a := heldPerm(&m, sid, "echo first")
	now = now.Add(5 * time.Second)
	b := heldPerm(&m, sid, "echo second")
	// a is answered in its terminal; b becomes the top request.
	now = now.Add(time.Second)
	m.store.Settle(itemOf(m, a), false, "answered in the terminal")
	m.refresh()
	now = now.Add(200 * time.Millisecond)
	got := ctrl(m, 'y')
	if decided(b) || !strings.Contains(got.flash, "A new request just came up") {
		t.Fatalf("^Y right after b took a's place: decided %v, flash %q", decided(b), got.flash)
	}
	// Pressed again, it answers b.
	if ctrl(got, 'y'); !decided(b) {
		t.Error("the second ^Y did not answer")
	}
	// A request that has been on top a while is answered at once.
	c := heldPerm(&m, sid, "echo third")
	m.store.Settle(itemOf(m, b), true, "allowed")
	m.refresh()
	now = now.Add(2 * time.Second)
	if ctrl(m, 'y'); !decided(c) {
		t.Error("a settled top request was not answered")
	}
}
