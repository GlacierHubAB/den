package orch

import "testing"

// blockedNotice is Claude Code's notice when the inbound hook refuses a
// peer's message that was queued behind a running turn.
const blockedNotice = `{"type":"system","subtype":"informational","content":"UserPromptSubmit operation blocked by hook: Den passed the message on to the orchestrator as a report\n\nOriginal prompt: <cross-session-message from=\"uds:/tmp/cc-socks-1000/9.sock\" from-name=\"x\" from-mode=\"p\">\nhi\n</cross-session-message>","level":"warning"}`

// The human's 2026-10-17 wedge: a peer's message queued during an app's
// turn is refused, and the app's own result follows (num_turns 2, no
// result of the refused prompt's own). It is the app's result: the turn
// ends, and the refusal is still reported, before it.
func TestMidTurnRefusalDoesNotSwallowTheTurnsResult(t *testing.T) {
	o, _ := controlOrch(true)
	o.SendApp("t-1", "run my desktop")
	feed(o, replayOf("run my desktop"), blockedNotice,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"The desktop agent is running."}]}}`,
		resultLine)
	var kinds []Kind
	var res *Event
	for len(o.events) > 0 {
		e := <-o.events
		kinds = append(kinds, e.Kind)
		if e.Kind == Result {
			e := e
			res = &e
		}
	}
	if res == nil || res.Turn != "t-1" {
		t.Fatalf("the app's result was not reported as its own: %+v (events %v)", res, kinds)
	}
	if o.AppTurn() != "" {
		t.Fatal("the app's turn is still open after its own result")
	}
	blocked := -1
	for i, k := range kinds {
		if k == Blocked {
			blocked = i
		}
		if k == Result && blocked < 0 {
			t.Fatal("the refusal was not reported before the result")
		}
	}
	// The next app turn can start.
	if err := o.SendApp("t-2", "hello"); err != nil {
		t.Fatalf("the next app turn: %v", err)
	}
}

// A refused prompt on its own still gives Blocked, not a result: its
// result says so (num_turns 0, the hook's notice as its text).
func TestRefusedPromptAloneIsStillBlocked(t *testing.T) {
	for name, result := range map[string]string{
		"num_turns 0":    `{"type":"result","subtype":"success","is_error":false,"result":"anything","num_turns":0}`,
		"notice as text": `{"type":"result","subtype":"success","is_error":false,"result":"UserPromptSubmit operation blocked by hook: x"}`,
	} {
		o, _ := controlOrch(true)
		feed(o, blockedNotice, result)
		var kinds []Kind
		for len(o.events) > 0 {
			kinds = append(kinds, (<-o.events).Kind)
		}
		if len(kinds) != 1 || kinds[0] != Blocked {
			t.Errorf("%s: events %v", name, kinds)
		}
	}
}

// CloseAppTurn closes an open app turn (Den's watchdog), once.
func TestCloseAppTurn(t *testing.T) {
	o, _ := controlOrch(true)
	if o.CloseAppTurn() != "" {
		t.Fatal("closed a turn that was not open")
	}
	o.SendApp("t-1", "hello")
	if got := o.CloseAppTurn(); got != "t-1" || o.AppTurn() != "" {
		t.Fatalf("closed %q, open %q", got, o.AppTurn())
	}
}
