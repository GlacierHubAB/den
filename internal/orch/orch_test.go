package orch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/spawn"
)

// fakeClaude speaks just enough stream-json to exercise the parser.
const fakeClaude = `#!/bin/sh
echo '{"type":"system","subtype":"init","session_id":"abc123","model":"claude-fable-5-1"}'
while IFS= read -r line; do
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Telling "}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Mossy."}}}'
  echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Telling Mossy."},{"type":"tool_use","id":"t1","name":"SendMessage","input":{"to":"user-45","message":"Rebase on main first.","summary":"rebase"}}]}}'
  echo '{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"Sent to user-45."}]}}'
  echo '{"type":"result","subtype":"success","is_error":false,"result":"Sent. Mossy Lemur will rebase first.","total_cost_usd":0.0123}'
done
`

func TestRoundTripWithFakeClaude(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "claude")
	if err := os.WriteFile(script, []byte(fakeClaude), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	o, err := Start(ctx, Options{Command: []string{script}, Dir: dir, HookCommand: testHook()})
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()

	next := func(k Kind) Event {
		t.Helper()
		for {
			select {
			case e, ok := <-o.Events():
				if !ok {
					t.Fatalf("events closed while waiting for %v", k)
				}
				if e.Kind == k {
					return e
				}
			case <-ctx.Done():
				t.Fatalf("timeout waiting for %v", k)
			}
		}
	}
	if e := next(Started); e.SessionID != "abc123" || e.Model != "claude-fable-5-1" {
		t.Errorf("started = %+v", e)
	}
	if err := o.Send("tell mossy to rebase"); err != nil {
		t.Fatal(err)
	}
	if e := next(Delta); e.Text != "Telling " {
		t.Errorf("delta = %+v", e)
	}
	if e := next(Text); e.Text != "Telling Mossy." {
		t.Errorf("text = %+v", e)
	}
	tu := next(ToolUse)
	in, ok := ParseSendMessage(tu.Input)
	if !ok || tu.Tool != "SendMessage" || in.To != "user-45" || in.Message != "Rebase on main first." {
		t.Errorf("tool_use = %+v %+v", tu, in)
	}
	if e := next(ToolResult); e.ToolUseID != "t1" || e.Text != "Sent to user-45." {
		t.Errorf("tool_result = %+v", e)
	}
	if e := next(Result); e.CostUSD != 0.0123 || e.Text == "" {
		t.Errorf("result = %+v", e)
	}
	o.Close()
	next(Exited)
}

// The orchestrator's command line, pinned. --system-prompt-snapshot off
// keeps a resume from sending the prompt an earlier launch recorded
// (Claude Code 2.1.289 does by default), so guidance turned off or changed
// takes effect.
func TestArgv(t *testing.T) {
	t.Setenv("DEN_ORCHESTRATOR_MODEL", "")
	got := Argv(Options{Command: []string{"claude"}, Name: "den-orchestrator", Spawn: true, Executable: "/usr/bin/den", Resume: "sess-1", Guidance: "Lead with the answer.\n", SettingsFile: "/run/user/1000/den/orchestrator-settings-0123456789abcdef.json"})
	want := []string{"claude", "-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--include-partial-messages",
		"--verbose",
		"--name", "den-orchestrator",
		"--tools", "ListAgents,SendMessage",
		"--append-system-prompt", Prompt("Lead with the answer.\n"),
		"--system-prompt-snapshot", "off",
		// Only Den's settings, from a private file (never inline: they can
		// hold the user's keys), and the hook events and replayed turns the
		// backstop reads.
		"--setting-sources", "",
		"--settings", "/run/user/1000/den/orchestrator-settings-0123456789abcdef.json",
		"--permission-mode", "dontAsk",
		"--include-hook-events",
		"--replay-user-messages",
		"--mcp-config", MCPConfig("/usr/bin/den"), "--strict-mcp-config",
		"--allowedTools", "ListAgents,SendMessage," + SpawnAgentTool + "," + OpenTerminalTool + "," + CloseAgentTool + "," + SkillsTool + "," + MissionTool,
		"--resume", "sess-1",
		"--model", "sonnet",
	}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("argv:\n got %q\nwant %q", got, want)
	}
	off := Argv(Options{Resume: "sess-1"})
	if strings.Contains(strings.Join(off, " "), "<human_guidance>\n") || !strings.Contains(strings.Join(off, " "), "--system-prompt-snapshot off") {
		t.Errorf("without guidance: %q", off)
	}
	if strings.Contains(strings.Join(Argv(Options{Model: "off"}), " "), "--model") {
		t.Error(`model "off" keeps Claude Code's default`)
	}
}

// A claude that refuses one of Den's flags exits before its init event;
// Exited carries its first stderr line, sanitised and capped.
func TestExitedCarriesFirstStderrLine(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "claude")
	long := strings.Repeat("x", 500)
	body := "#!/bin/sh\nprintf 'error: unknown option \\047--system-prompt-snapshot\\047\\033[31m\\n" + long + "\\n' >&2\nexit 1\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	o, err := Start(ctx, Options{Command: []string{script}, Dir: dir, HookCommand: testHook()})
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	for {
		select {
		case e, ok := <-o.Events():
			if !ok {
				t.Fatal("events closed without Exited")
			}
			if e.Kind != Exited {
				continue
			}
			if e.Stderr != "error: unknown option '--system-prompt-snapshot'" {
				t.Errorf("first stderr line %q", e.Stderr)
			}
			if strings.Contains(e.Text, long) || strings.ContainsRune(e.Text, 0x1b) {
				t.Errorf("Exited text is not capped and sanitised: %d bytes", len(e.Text))
			}
			return
		case <-ctx.Done():
			t.Fatal("timeout")
		}
	}
}

func TestSpawnInputWorktree(t *testing.T) {
	for in, want := range map[string]spawn.Worktree{
		`{"dir":"/r","worktree":true}`:      spawn.WorktreeAuto,
		`{"dir":"/r","worktree":"fix-one"}`: "fix-one",
		`{"dir":"/r","worktree":false}`:     "",
		`{"dir":"/r"}`:                      "",
	} {
		got, ok := ParseSpawn(json.RawMessage(in))
		if !ok || got.Worktree != want {
			t.Errorf("%s -> %q ok=%v, want %q", in, got.Worktree, ok, want)
		}
	}
	line := Describe(SpawnAgentTool, json.RawMessage(`{"dir":"/r","brief":"x","worktree":true}`))
	if !strings.Contains(line, "in its own worktree") {
		t.Errorf("describe = %q", line)
	}
}
