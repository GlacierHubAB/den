package inbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/bridge"
)

// The hook payloads in testdata/claude-2.1.289 are shaped like the ones the
// real Claude Code sends (see the README there). These tests drive the
// inbox with them, in the order and with the overlaps the real thing shows:
// its own dialog is up at once, the hook still holds, and a permission_prompt
// notice follows about six seconds later.

// fixture is the request Den's server hands the inbox for a payload: one
// that waits for a reply for a PermissionRequest, a plain notice otherwise.
func fixture(t *testing.T, name string) *bridge.Request {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "claude-2.1.289", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var in bridge.HookInput
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	env := bridge.Envelope{Event: in.HookEventName, SessionID: in.SessionID, Payload: raw}
	if env.Event == "PermissionRequest" {
		return bridge.TestRequest(env)
	}
	return &bridge.Request{Envelope: env, Input: in}
}

// open lists the items that still wait on the human.
func open(s *Store) []*Item {
	var out []*Item
	for _, it := range s.Items() {
		if it.State != Resolved && it.Kind <= Question {
			out = append(out, it)
		}
	}
	return out
}

func TestFixturesAreNeutral(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("testdata", "claude-2.1.289", "*.json"))
	if len(files) < 15 {
		t.Fatalf("only %d fixtures", len(files))
	}
	for _, f := range files {
		raw, _ := os.ReadFile(f)
		var in bridge.HookInput
		if err := json.Unmarshal(raw, &in); err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		if in.SessionID != "11111111-2222-4333-8444-555555555555" {
			t.Errorf("%s: session %q", f, in.SessionID)
		}
		if bad := notNeutral(string(raw)); bad != "" {
			t.Errorf("%s: %s", f, bad)
		}
	}
}

func TestRealPlanHeldAndAnswered(t *testing.T) {
	store := New()
	store.Apply(fixture(t, "user-prompt-submit"))
	store.Apply(fixture(t, "pre-exitplanmode"))
	r := fixture(t, "permission-exitplanmode")
	it := store.Apply(r)
	if it == nil || it.Kind != Plan || !it.Answerable() || it.Title != "Plan ready for review" {
		t.Fatalf("plan = %+v", it)
	}
	if len(it.Body) == 0 || !strings.Contains(strings.Join(it.Body, "\n"), "plan.txt") {
		t.Errorf("the plan's text is not shown: %q", it.Body)
	}
	// About six seconds later Claude Code's own notice for the same dialog.
	if n := store.Apply(fixture(t, "notification-permission-prompt-exitplanmode")); n != nil {
		t.Errorf("the permission_prompt notice added %+v next to the plan", n)
	}
	if got := open(store); len(got) != 1 || got[0] != it {
		t.Fatalf("open items = %+v, want just the plan", got)
	}
	if !store.Answer(it, "allow") {
		t.Fatal("the plan could not be answered")
	}
	if d, ok := r.Decided(); !ok || d.Behavior != "allow" {
		t.Errorf("hook decision = %+v %v", d, ok)
	}
	store.Apply(fixture(t, "post-exitplanmode"))
	if got := open(store); len(got) != 0 {
		t.Errorf("still open after the plan was answered: %+v", got)
	}
	// The turn ends and Claude Code asks for input five seconds later.
	store.Apply(fixture(t, "stop"))
	if idle := store.Apply(fixture(t, "notification-idle-prompt")); idle == nil || idle.Kind != Idle {
		t.Errorf("idle_prompt = %+v", idle)
	}
}

func TestRealPlanNoticeFirstThenHeld(t *testing.T) {
	// The notice can also win the race (a slow hook start): the held plan
	// then replaces it rather than showing next to it.
	store := New()
	store.Apply(fixture(t, "notification-permission-prompt-exitplanmode"))
	it := store.Apply(fixture(t, "permission-exitplanmode"))
	if got := open(store); len(got) != 1 || got[0] != it {
		t.Fatalf("open items = %+v", got)
	}
}

func TestRealAskUserQuestionIsOneItem(t *testing.T) {
	for _, asks := range []bool{true, false} {
		name := "held for den"
		if !asks {
			name = "left to the terminal"
		}
		t.Run(name, func(t *testing.T) {
			store := New()
			store.AnswerQuestions(asks)
			store.Apply(fixture(t, "user-prompt-submit"))
			store.Apply(fixture(t, "pre-askuserquestion"))
			r := fixture(t, "permission-askuserquestion")
			store.Apply(r)
			store.Apply(fixture(t, "notification-permission-prompt-askuserquestion"))

			got := open(store)
			if len(got) != 1 || got[0].Kind != Question {
				t.Fatalf("open items = %d: %+v, want one question", len(got), got)
			}
			if asks {
				if !got[0].AsksInDen() || got[0].Title != "Which colour do you prefer?" || len(got[0].Options) != 2 {
					t.Fatalf("question = %+v", got[0])
				}
				if done, ok := store.AnswerQuestion(got[0], "2"); !ok || !done {
					t.Fatalf("answer: done=%v ok=%v", done, ok)
				}
				d, decided := r.Decided()
				if !decided || d.Behavior != "allow" || d.Answers["Which colour do you prefer?"] != "Blue" {
					t.Errorf("hook decision = %+v %v", d, decided)
				}
			} else if got[0].Answerable() {
				t.Error("answerable although Den does not hold the question")
			}
			store.Apply(fixture(t, "post-askuserquestion"))
			if left := open(store); len(left) != 0 {
				t.Errorf("open after the answer: %+v", left)
			}
		})
	}
}

func TestRealAnsweredInTheTerminalFirst(t *testing.T) {
	// Claude Code shows its dialog at once; the human answers it there while
	// Den still holds the hook. The tool then runs (PostToolUse) and the
	// item in Den must close, not stay answerable until the turn ends.
	for _, tc := range []struct {
		name, perm, post string
		kind             Kind
	}{
		{"a command", "permission-bash", "post-bash", Permission},
		{"a plan", "permission-exitplanmode", "post-exitplanmode", Plan},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := New()
			store.Apply(fixture(t, "user-prompt-submit"))
			it := store.Apply(fixture(t, tc.perm))
			if it == nil || it.Kind != tc.kind || !it.Answerable() {
				t.Fatalf("held = %+v", it)
			}
			store.Apply(fixture(t, "notification-permission-prompt-bash"))
			store.Apply(fixture(t, tc.post))
			if it.State != Resolved || it.Answerable() {
				t.Errorf("after PostToolUse: state=%v answerable=%v (%q)", it.State, it.Answerable(), it.Resolved)
			}
			if store.Waiting() != 0 {
				t.Errorf("waiting = %d", store.Waiting())
			}
			// A late ^Y must not send an allow to a hook that no longer matters.
			if store.Answer(it, "allow") {
				t.Error("answered an item the terminal already answered")
			}
		})
	}
}
