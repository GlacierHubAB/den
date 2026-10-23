package events

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/badge"
	"github.com/GlacierHubAB/den/internal/bridge"
)

const sid = "11111111-2222-4333-8444-555555555555"

func hook(event string, in map[string]any) bridge.HookInput {
	in["session_id"] = sid
	in["hook_event_name"] = event
	raw, _ := json.Marshal(in)
	var h bridge.HookInput
	json.Unmarshal(raw, &h)
	return h
}

func TestFromHookCarriesMetadataOnly(t *testing.T) {
	secret := "cat ~/.ssh/id_ed25519 # deploy key"
	cases := []struct {
		in   bridge.HookInput
		want string // kind/request/tool/error/reason
	}{
		{hook("SessionStart", map[string]any{"source": "startup"}), "session_start////startup"},
		{hook("SessionEnd", map[string]any{"end_reason": "logout"}), "session_end////logout"},
		{hook("SessionEnd", map[string]any{"reason": "prompt_input_exit"}), "session_end////prompt_input_exit"}, // what Claude Code 2.1.289 sends
		{hook("UserPromptSubmit", map[string]any{"prompt": secret}), "turn_start////"},
		{hook("Stop", map[string]any{"last_assistant_message": secret}), "done////"},
		{hook("StopFailure", map[string]any{"error": "rate_limit", "last_assistant_message": secret}), "turn_failed///rate_limit/"},
		{hook("PostToolUseFailure", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": secret}, "error": secret, "is_interrupt": false}), "tool_failed//Bash//"},
		{hook("PermissionRequest", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": secret}}), "needs_you/permission/Bash//"},
		{hook("PermissionRequest", map[string]any{"tool_name": "ExitPlanMode", "tool_input": map[string]any{"plan": secret}}), "needs_you/plan///"},
		{hook("PreToolUse", map[string]any{"tool_name": "AskUserQuestion", "tool_input": map[string]any{"questions": secret}}), "needs_you/question///"},
		{hook("Notification", map[string]any{"notification_type": "elicitation_dialog", "message": secret}), "needs_you/question///"},
		{hook("Notification", map[string]any{"notification_type": "idle_prompt", "message": secret}), "idle////"},
	}
	for _, c := range cases {
		ev, ok := FromHook(c.in)
		got := strings.Join([]string{ev.Kind, ev.Request, ev.Tool, ev.Error, ev.Reason}, "/")
		if !ok || got != c.want {
			t.Errorf("%s: got %q (%v), want %q", c.in.HookEventName, got, ok, c.want)
		}
		raw, _ := json.Marshal(ev)
		if strings.Contains(string(raw), "ssh") || strings.Contains(string(raw), "deploy") {
			t.Errorf("%s leaks: %s", c.in.HookEventName, raw)
		}
		if ev.V != 1 || ev.Session != sid || ev.Agent != badge.Name(sid) || ev.Time.IsZero() {
			t.Errorf("%s: envelope %+v", c.in.HookEventName, ev)
		}
	}
	for _, in := range []bridge.HookInput{
		hook("PostToolUse", map[string]any{"tool_name": "Bash"}),
		hook("PreToolUse", map[string]any{"tool_name": "Bash"}),
		hook("Notification", map[string]any{"notification_type": "permission_prompt"}),
		// Claude Code 2.1.289 asks a question with PreToolUse, then about
		// 60 ms later its PermissionRequest: the question once, not also as
		// a permission for AskUserQuestion.
		hook("PermissionRequest", map[string]any{"tool_name": "AskUserQuestion", "tool_input": map[string]any{"questions": secret}}),
		{HookEventName: "Stop"}, // no session
	} {
		if _, ok := FromHook(in); ok {
			t.Errorf("%s %s should stay out", in.HookEventName, in.ToolName)
		}
	}
}

func TestReaderFollowsAppendsAndRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "den", "events.jsonl")
	Append(path, New(Done, "before")) // already there: skipped
	rd := Open(path)
	stop := make(chan struct{})
	got := make(chan string, 10)
	go rd.Run(stop, func(raw []byte, ev Event) { got <- ev.Session })
	defer close(stop)
	Append(path, New(Done, "one"))
	os.Rename(path, path+".1")
	Append(path, New(Done, "two"))
	for _, want := range []string{"one", "two"} {
		select {
		case s := <-got:
			if s != want {
				t.Fatalf("got %s, want %s", s, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("no %s", want)
		}
	}
}

func TestReaderWaitsForAMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "den", "events.jsonl")
	rd := Open(path)
	stop := make(chan struct{})
	defer close(stop)
	got := make(chan string, 1)
	go rd.Run(stop, func(raw []byte, ev Event) { got <- string(raw) })
	Append(path, New(NeedsYou, "first"))
	select {
	case raw := <-got:
		if !strings.HasPrefix(raw, `{"v":1,`) || strings.Contains(raw, "\n") {
			t.Errorf("raw = %q", raw)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("nothing read")
	}
}

func TestAppendRotates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	os.WriteFile(path, make([]byte, MaxSize+1), 0o600)
	Append(path, New(Done, sid))
	if st, _ := os.Stat(path + ".1"); st == nil || st.Size() != MaxSize+1 {
		t.Error("old stream should move aside")
	}
	if raw, _ := os.ReadFile(path); strings.Count(string(raw), "\n") != 1 {
		t.Errorf("new stream = %q", raw)
	}
}

func TestRunOn(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	out := filepath.Join(dir, "out")
	ev := New(NeedsYou, sid)
	ev.Request, ev.Tool = "permission", "Bash"
	if err := RunOn(`printf '%s|%s|%s|%s\n' "$DEN_EVENT" "$DEN_AGENT" "$DEN_REQUEST" "$DEN_TOOL" > `+out+`; cat >> `+out, ev); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(out)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 || lines[0] != "needs_you|"+badge.Name(sid)+"|permission|Bash" || !strings.HasPrefix(lines[1], `{"v":1,`) {
		t.Errorf("out = %q", raw)
	}
	if RunOn("exit 3", ev) == nil {
		t.Error("a failing command reports")
	}
	OnTimeout = 100 * time.Millisecond
	defer func() { OnTimeout = 30 * time.Second }()
	if err := RunOn("sleep 5", ev); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("err = %v", err)
	}
	log, _ := os.ReadFile(filepath.Join(dir, "den", "hooks.log"))
	if strings.Count(string(log), "[on] command failed") != 2 || !strings.Contains(string(log), "on.needs_you") {
		t.Errorf("hooks.log = %q", log)
	}
}

func TestEnvHasTheHostOnlyForRemoteEvents(t *testing.T) {
	ev := New(Done, "s1")
	if strings.Contains(strings.Join(Env(ev), " "), "DEN_HOST") {
		t.Error("a local event has DEN_HOST")
	}
	ev.Host = "devbox"
	if !slices.Contains(Env(ev), "DEN_HOST=devbox") {
		t.Errorf("env = %v", Env(ev))
	}
}

// An event appended to the old file after the reader reached its end, and
// then moved aside with it, is still read, before the new file's: the
// reader reads the old file once more when it sees the move. The file is
// written and moved at that exact point, from the reader's own loop.
func TestReaderReadsTheOldFileBeforeTheNew(t *testing.T) {
	path := filepath.Join(t.TempDir(), "den", "events.jsonl")
	Append(path, New(Done, "before")) // already there: skipped
	rd := Open(path)
	moved := false
	rd.atEOF = func() {
		if moved {
			return
		}
		moved = true
		Append(path, New(Done, "last-in-old"))
		Append(path, New(Done, "half"))
		// A line without its end, in the moved file: never completed, so
		// never read as an event.
		f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		f.WriteString(`{"v":1,"kind":"done","session":"cut`)
		f.Close()
		os.Rename(path, path+".1")
		Append(path, New(Done, "first-in-new"))
	}
	stop := make(chan struct{})
	var got []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		rd.Run(stop, func(raw []byte, ev Event) {
			got = append(got, ev.Session)
			if ev.Session == "first-in-new" {
				close(stop)
			}
		})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		close(stop)
		<-done
		t.Fatalf("the new file's event never came: %v", got)
	}
	if strings.Join(got, ",") != "last-in-old,half,first-in-new" {
		t.Errorf("events %v, want last-in-old, half, then first-in-new", got)
	}
}

func TestTurnStartNamesWhereThePromptCameFrom(t *testing.T) {
	envelope := "<cross-session-message from=\"uds:/run/user/1000/cc-socks/4242.sock\" from-name=\"peer\" from-mode=\"prompting\">\nrebase on main, password hunter2\n</cross-session-message>"
	cases := []struct{ name, prompt, want string }{
		{"a peer's message", envelope, SourcePeer},
		{"an envelope somewhere in the prompt", "look at this: <Cross-Session-Message from=\"x\">hunter2", SourcePeer},
		{"typed text", "fix the failing test, password hunter2", SourceTerminal},
		{"a slash command", "/compact hunter2", SourceTerminal},
		{"no prompt", "", ""},
	}
	for _, c := range cases {
		ev, ok := FromHook(hook("UserPromptSubmit", map[string]any{"prompt": c.prompt}))
		if !ok || ev.Kind != TurnStart || ev.Source != c.want {
			t.Errorf("%s: kind %q source %q (%v), want %q", c.name, ev.Kind, ev.Source, ok, c.want)
		}
		raw, _ := json.Marshal(ev)
		if strings.Contains(string(raw), "hunter2") || strings.Contains(string(raw), "rebase") || strings.Contains(string(raw), "failing") {
			t.Errorf("%s: the event keeps the prompt: %s", c.name, raw)
		}
	}
	if ev, _ := FromHook(hook("UserPromptSubmit", map[string]any{})); ev.Source != "" {
		t.Errorf("a hook with no prompt field has source %q", ev.Source)
	}
	// Only a turn_start has a source.
	for _, ev := range []Event{
		mustHook(t, hook("Stop", map[string]any{"prompt": envelope})),
		mustHook(t, hook("SessionStart", map[string]any{"prompt": envelope, "source": "startup"})),
		mustHook(t, hook("PostToolUseFailure", map[string]any{"prompt": envelope, "tool_name": "Bash"})),
	} {
		if ev.Source != "" {
			t.Errorf("%s has source %q", ev.Kind, ev.Source)
		}
	}
}

func mustHook(t *testing.T, in bridge.HookInput) Event {
	t.Helper()
	ev, ok := FromHook(in)
	if !ok {
		t.Fatalf("%s left out", in.HookEventName)
	}
	return ev
}

func TestEventsWithoutASourceStillParse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "den", "events.jsonl")
	old := `{"v":1,"time":"2026-10-11T21:22:46.685Z","kind":"turn_start","session":"11111111-2222-4333-8444-555555555555","agent":"Mossy Lemur"}`
	var ev Event
	if err := json.Unmarshal([]byte(old), &ev); err != nil || ev.Kind != TurnStart || ev.Source != "" {
		t.Fatalf("an event from before the field: %+v, %v", ev, err)
	}
	if raw, _ := json.Marshal(ev); strings.Contains(string(raw), "source") {
		t.Errorf("an event with no source writes one: %s", raw)
	}
	// A reader that follows the stream passes the new line, and the old one, whole.
	rd := Open(path)
	stop := make(chan struct{})
	defer close(stop)
	got := make(chan Event, 2)
	go rd.Run(stop, func(raw []byte, ev Event) { got <- ev })
	new := New(TurnStart, sid)
	new.Source = SourceTerminal
	Append(path, new)
	Append(path, New(TurnStart, sid))
	for _, want := range []string{SourceTerminal, ""} {
		select {
		case ev := <-got:
			if ev.Source != want {
				t.Errorf("source %q, want %q", ev.Source, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("no event with source %q", want)
		}
	}
}
