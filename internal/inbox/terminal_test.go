package inbox

import (
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
)

// Claude Code ends the held hook when its own dialog is answered or
// dismissed first: the item closes at once, whatever its kind, and takes
// no answer. A hook that hangs up at the end of Den's wait gave up itself,
// and the dialog is still up in the terminal.
func TestHeldRequestHookHungUp(t *testing.T) {
	const wait = 580 * time.Second
	for _, name := range []string{"permission-bash", "permission-exitplanmode", "permission-askuserquestion"} {
		t.Run(name, func(t *testing.T) {
			store := New()
			store.AnswerQuestions(true)
			r := fixture(t, name)
			it := store.Apply(r)
			if it == nil || !it.Answerable() {
				t.Fatalf("held = %+v", it)
			}
			if store.LetGo(wait) {
				t.Error("let go of a request still held")
			}
			bridge.TestHangUp(r)
			if store.Answer(it, "allow") {
				t.Error("answered a request whose hook hung up")
			}
			if !store.LetGo(wait) || it.State != Resolved || it.Resolved != ClosedInTerminal || it.Answerable() || store.Waiting() != 0 {
				t.Errorf("after the terminal answered: %+v, waiting %d", it, store.Waiting())
			}
		})
	}

	store := New()
	r := fixture(t, "permission-exitplanmode")
	it := store.Apply(r)
	it.At = time.Now().Add(-wait)
	bridge.TestHangUp(r)
	if !store.LetGo(wait) || it.State != InTerminal || it.Answerable() {
		t.Errorf("after Den's wait ran out: %+v", it)
	}
}

// The PostToolUse of another call of the turn leaves a held request alone.
func TestHeldRequestOtherCallRan(t *testing.T) {
	store := New()
	it := store.Apply(fixture(t, "permission-bash"))
	store.Apply(fixture(t, "post-exitplanmode"))
	if !it.Answerable() {
		t.Errorf("closed by another call's PostToolUse: %+v", it)
	}
}

// The real PostToolUse of a plan carries an empty tool_input: it still
// closes the plan the terminal approved first.
func TestCapturedPlanAnsweredInTheTerminalFirst(t *testing.T) {
	store := New()
	r := capturedAs(t, "permissionrequest-exitplanmode", "")
	it := store.Apply(r)
	if it == nil || !it.Answerable() {
		t.Fatalf("plan = %+v", it)
	}
	store.Apply(capturedAs(t, "posttooluse-exitplanmode-after-hook-allow-updatedinput", r.Input.SessionID))
	if it.State != Resolved || it.Answerable() || store.Answer(it, "allow") {
		t.Errorf("after the plan ran: %+v", it)
	}
}
