package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/inbox"
)

// These tests run the built `den hook` with Claude Code 2.1.289 payloads
// (internal/inbox/testdata/claude-2.1.289) against a real bridge server and
// inbox store in this process: what Claude Code would read on stdout is
// checked, not the helper that makes it. Nothing leaves the scratch
// directory: HOME, the state and runtime dirs, the config and the socket
// are all in it, and no Claude Code or Den is started.

type hookRig struct {
	t    *testing.T
	bin  string
	env  []string
	srv  *bridge.Server
	st   *inbox.Store
	dir  string
	sock string
}

func newHookRig(t *testing.T, config string) *hookRig {
	t.Helper()
	if testing.Short() {
		t.Skip("builds den")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "den")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	sock := filepath.Join(dir, "den.sock")
	cfg := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfg, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	srv, err := bridge.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	return &hookRig{t: t, bin: bin, dir: dir, sock: sock, srv: srv, st: inbox.New(),
		env: []string{"HOME=" + dir, "XDG_STATE_HOME=" + dir, "XDG_CONFIG_HOME=" + dir, "XDG_RUNTIME_DIR=" + dir,
			"DEN_SOCKET=" + sock, "DEN_CONFIG=" + cfg, "CLAUDE_CONFIG_DIR=" + dir, "PATH=/usr/bin:/bin"}}
}

// fixture reads a payload from the inbox package's testdata.
func (r *hookRig) fixture(name string) []byte {
	raw, err := os.ReadFile(filepath.Join("..", "..", "internal", "inbox", "testdata", "claude-2.1.289", name+".json"))
	if err != nil {
		r.t.Fatal(err)
	}
	return raw
}

// hook starts `den hook` with the payload on stdin.
func (r *hookRig) hook(payload []byte) (*exec.Cmd, *bytes.Buffer) {
	cmd := exec.Command(r.bin, "hook")
	cmd.Env = r.env
	cmd.Stdin = bytes.NewReader(payload)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Start(); err != nil {
		r.t.Fatal(err)
	}
	return cmd, &out
}

// next takes the next request the server got and applies it to the store.
func (r *hookRig) next() (*bridge.Request, *inbox.Item) {
	select {
	case req := <-r.srv.Requests():
		return req, r.st.Apply(req)
	case <-time.After(10 * time.Second):
		r.t.Fatal("no request reached the bridge")
		return nil, nil
	}
}

// finish waits for a hook to exit 0 and returns what it printed.
func (r *hookRig) finish(cmd *exec.Cmd, out *bytes.Buffer) string {
	r.t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			r.t.Fatalf("den hook: %v", err)
		}
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		r.t.Fatal("den hook still running after the answer")
	}
	return out.String()
}

func TestHookBinaryPlanApprovedAndDenied(t *testing.T) {
	for _, verb := range []string{"allow", "deny"} {
		t.Run(verb, func(t *testing.T) {
			r := newHookRig(t, "")
			payload := r.fixture("permission-exitplanmode")
			cmd, out := r.hook(payload)
			_, it := r.next()
			if it == nil || it.Kind != inbox.Plan || !it.Answerable() {
				t.Fatalf("plan item = %+v", it)
			}
			// Claude Code's own notice comes about six seconds later, while
			// the hook still holds: it adds nothing next to the plan.
			ncmd, nout := r.hook(r.fixture("notification-permission-prompt-exitplanmode"))
			if _, n := r.next(); n != nil {
				t.Errorf("the notice added %+v", n)
			}
			if got := r.finish(ncmd, nout); got != "" {
				t.Errorf("the notice hook printed %q", got)
			}
			if !r.st.Answer(it, verb) {
				t.Fatal("not answerable")
			}
			got := r.finish(cmd, out)
			if strings.Count(got, "\n") != 1 || !strings.HasSuffix(got, "\n") {
				t.Fatalf("stdout = %q, want exactly one line", got)
			}
			var res struct {
				HookSpecificOutput struct {
					HookEventName string `json:"hookEventName"`
					Decision      struct {
						Behavior     string          `json:"behavior"`
						Message      *string         `json:"message"`
						UpdatedInput json.RawMessage `json:"updatedInput"`
					} `json:"decision"`
				} `json:"hookSpecificOutput"`
			}
			if err := json.Unmarshal([]byte(got), &res); err != nil {
				t.Fatalf("stdout %q: %v", got, err)
			}
			d := res.HookSpecificOutput.Decision
			if res.HookSpecificOutput.HookEventName != "PermissionRequest" || d.Behavior != verb {
				t.Fatalf("stdout = %s", got)
			}
			if verb == "deny" {
				if d.Message == nil || *d.Message == "" || d.UpdatedInput != nil {
					t.Errorf("deny = %s", got)
				}
				return
			}
			var in struct {
				ToolInput json.RawMessage `json:"tool_input"`
			}
			json.Unmarshal(payload, &in)
			var want, have any
			json.Unmarshal(in.ToolInput, &want)
			json.Unmarshal(d.UpdatedInput, &have)
			if d.Message != nil || !reflect.DeepEqual(want, have) {
				t.Errorf("allow = %s, want the call's input %s and no message", got, in.ToolInput)
			}
		})
	}
}

func TestHookBinaryPlanWithoutAPlanIsLeftToTheTerminal(t *testing.T) {
	r := newHookRig(t, "")
	var in map[string]any
	json.Unmarshal(r.fixture("permission-exitplanmode"), &in)
	in["tool_input"] = map[string]any{"planFilePath": "/work/.claude/plans/p.md"}
	payload, _ := json.Marshal(in)
	cmd, out := r.hook(payload)
	_, it := r.next()
	if it == nil || it.Answerable() || it.Resolved != inbox.CannotApprovePlan {
		t.Fatalf("item = %+v", it)
	}
	if got := r.finish(cmd, out); got != "" {
		t.Errorf("printed %q: a bare allow would leave Claude Code's dialog open", got)
	}
}

func TestHookBinaryAnswersAQuestionFromDen(t *testing.T) {
	r := newHookRig(t, "[hooks]\nanswer_questions = true\n")
	r.st.AnswerQuestions(true)
	pre, preOut := r.hook(r.fixture("pre-askuserquestion"))
	_, first := r.next()
	if first == nil || first.Kind != inbox.Question {
		t.Fatalf("PreToolUse item = %+v", first)
	}
	r.finish(pre, preOut)
	cmd, out := r.hook(r.fixture("permission-askuserquestion"))
	_, held := r.next()
	if held != first || !held.AsksInDen() {
		t.Fatalf("held = %+v, want the PreToolUse item turned into the held question", held)
	}
	ncmd, nout := r.hook(r.fixture("notification-permission-prompt-askuserquestion"))
	if _, n := r.next(); n != nil {
		t.Errorf("the notice added %+v", n)
	}
	r.finish(ncmd, nout)
	if done, ok := r.st.AnswerQuestion(held, "2"); !done || !ok {
		t.Fatalf("answer: done=%v ok=%v", done, ok)
	}
	got := r.finish(cmd, out)
	var res struct {
		HookSpecificOutput struct {
			Decision struct {
				Behavior     string `json:"behavior"`
				UpdatedInput struct {
					Answers map[string]string `json:"answers"`
				} `json:"updatedInput"`
			} `json:"decision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(got), &res); err != nil {
		t.Fatalf("stdout %q: %v", got, err)
	}
	d := res.HookSpecificOutput.Decision
	if d.Behavior != "allow" || d.UpdatedInput.Answers["Which colour do you prefer?"] != "Blue" {
		t.Errorf("stdout = %s", got)
	}
	var n int
	for _, it := range r.st.Items() {
		if it.Kind != inbox.Done {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d items for one question", n)
	}
}

// Claude Code ends a hook whose dialog the human answered in the terminal.
// The held item then closes in Den, rather than staying answerable.
func TestHookBinaryKilledByTheTerminalAnswerClosesTheItem(t *testing.T) {
	for _, name := range []string{"permission-bash", "permission-exitplanmode"} {
		t.Run(name, func(t *testing.T) {
			r := newHookRig(t, "")
			cmd, _ := r.hook(r.fixture(name))
			_, it := r.next()
			if it == nil || !it.Answerable() {
				t.Fatalf("held = %+v", it)
			}
			cmd.Process.Kill()
			cmd.Wait()
			deadline := time.Now().Add(5 * time.Second)
			for it.Answerable() && time.Now().Before(deadline) {
				r.st.LetGo(10 * time.Minute)
				time.Sleep(20 * time.Millisecond)
			}
			if it.Answerable() || it.State != inbox.Resolved {
				t.Errorf("state=%v resolved=%q", it.State, it.Resolved)
			}
			if r.st.Answer(it, "allow") {
				t.Error("answered a request whose hook is gone")
			}
		})
	}
}
