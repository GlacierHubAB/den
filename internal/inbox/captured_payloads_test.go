package inbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/bridge"
)

// The payloads in testdata/captured-2.1.289 were recorded from a real
// Claude Code run (see the README there). capturedAs reads one and puts it
// on another session id, so a flow can span payloads recorded in different
// runs.
func capturedAs(t *testing.T, name, session string) *bridge.Request {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "captured-2.1.289", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if session != "" {
		m["session_id"], _ = json.Marshal(session)
	}
	raw, _ = json.Marshal(m)
	var in bridge.HookInput
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	env := bridge.Envelope{Event: in.HookEventName, SessionID: in.SessionID, Payload: raw}
	if env.Event == "PermissionRequest" {
		return bridge.TestRequest(env)
	}
	return &bridge.Request{Envelope: env, Input: in}
}

func TestCapturedCommandHeldThenNotice(t *testing.T) {
	store := New()
	store.Apply(capturedAs(t, "userpromptsubmit", ""))
	it := store.Apply(capturedAs(t, "permissionrequest-bash", ""))
	if it == nil || it.Kind != Permission || !it.Answerable() || len(it.Body) == 0 || it.Body[0] != "$ touch bx.txt" {
		t.Fatalf("item = %+v", it)
	}
	// The real notice names no tool: "Claude needs your permission".
	if n := store.Apply(capturedAs(t, "notification-permission_prompt-after-held-bash", "")); n != nil {
		t.Errorf("the notice added %+v", n)
	}
	if got := open(store); len(got) != 1 {
		t.Errorf("open items = %+v", got)
	}
}

func TestCapturedPlanHeldAnsweredAndRan(t *testing.T) {
	store := New()
	r := capturedAs(t, "permissionrequest-exitplanmode", "")
	it := store.Apply(r)
	if it == nil || it.Kind != Plan || !it.Answerable() {
		t.Fatalf("plan = %+v", it)
	}
	if !store.Answer(it, "allow") {
		t.Fatal("not answerable")
	}
	// What Claude Code then sends: the tool ran, its input empty and the plan
	// in the response; later the turn ends and it waits for the human.
	sess := r.Input.SessionID
	store.Apply(capturedAs(t, "posttooluse-exitplanmode-after-hook-allow-updatedinput", sess))
	store.Apply(capturedAs(t, "stop", sess))
	if got := open(store); len(got) != 0 {
		t.Errorf("open after the plan ran: %+v", got)
	}
	if idle := store.Apply(capturedAs(t, "notification-idle_prompt", sess)); idle == nil || idle.Kind != Idle {
		t.Errorf("idle_prompt = %+v", idle)
	}
	store.Apply(capturedAs(t, "sessionend", sess))
	for _, it := range store.Items() {
		if it.State != Resolved {
			t.Errorf("open after the session ended: %+v", it)
		}
	}
}

func TestCapturedQuestionAnsweredInTheTerminal(t *testing.T) {
	for _, asks := range []bool{true, false} {
		store := New()
		store.AnswerQuestions(asks)
		store.Apply(capturedAs(t, "pretooluse-askuserquestion", ""))
		store.Apply(capturedAs(t, "permissionrequest-askuserquestion", ""))
		store.Apply(capturedAs(t, "notification-permission_prompt-after-held-askuserquestion", ""))
		if got := open(store); len(got) != 1 || got[0].Kind != Question {
			t.Fatalf("asks=%v: open items = %+v, want one question", asks, got)
		}
		// The human answers in Claude Code's own dialog.
		store.Apply(capturedAs(t, "posttooluse-askuserquestion-answered-in-terminal", ""))
		if got := open(store); len(got) != 0 {
			t.Errorf("asks=%v: still open after the terminal answer: %+v", asks, got)
		}
	}
}

func TestCapturedCommandAnsweredInTheTerminalFirst(t *testing.T) {
	store := New()
	held := capturedAs(t, "permissionrequest-bash", "")
	it := store.Apply(held)
	if it == nil || !it.Answerable() {
		t.Fatalf("held = %+v", it)
	}
	store.Apply(capturedAs(t, "notification-permission_prompt-after-held-bash", ""))

	// Another call of the turn runs beside the held one (the recording's
	// Write): the held request still waits.
	other := capturedAs(t, "posttooluse-write", held.Input.SessionID)
	store.Apply(other)
	if it.State != Waiting || !it.Answerable() {
		t.Fatalf("a different call closed the held one: state=%v", it.State)
	}

	// The human allowed it in Claude Code's dialog and the command ran: a
	// PostToolUse for the same tool and input (the recording has a Write's;
	// this carries the Bash call's own).
	ran := capturedAs(t, "posttooluse-write", held.Input.SessionID)
	ran.Input.ToolName, ran.Input.ToolInput = "Bash", held.Input.ToolInput
	store.Apply(ran)
	if it.State != Resolved || it.Answerable() || store.Answer(it, "allow") {
		t.Errorf("after the command ran: state=%v answerable=%v", it.State, it.Answerable())
	}
}

func TestCapturedPayloadsAreNeutral(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("testdata", "captured-2.1.289", "*.json"))
	for _, f := range files {
		raw, _ := os.ReadFile(f)
		if bad := notNeutral(string(raw)); bad != "" {
			t.Errorf("%s: %s", f, bad)
		}
	}
}

// notNeutral says what in a fixture is not neutral: a home directory other
// than /home/user, or a scratch directory of a real run.
func notNeutral(text string) string {
	for rest := text; ; {
		i := strings.Index(rest, "/home/")
		if i < 0 {
			break
		}
		rest = rest[i+len("/home/"):]
		if !strings.HasPrefix(rest, "user/") {
			return "a home directory other than /home/user"
		}
	}
	if strings.Contains(text, "/tmp/claude-") {
		return "a scratch directory of a real run"
	}
	return ""
}
