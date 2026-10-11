package spawn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/discover"
)

func TestPeerName(t *testing.T) {
	live := []discover.Session{{Name: "den-site", Alive: true}, {Name: "den-site-2", Alive: true}, {Name: "old", Alive: false}}
	if got := PeerName("Den Site", "/x", live); got != "den-site-3" {
		t.Errorf("taken name should get the next free suffix, got %q", got)
	}
	if got := PeerName("old", "/x", live); got != "old" {
		t.Errorf("a dead session's name is free, got %q", got)
	}
	got := PeerName("", "/home/me/code/My Project", live)
	if !strings.HasPrefix(got, "my-project-") || len(got) != len("my-project-")+4 {
		t.Errorf("generated name = %q", got)
	}
}

func TestExpandDir(t *testing.T) {
	home, _ := os.UserHomeDir()
	dir := t.TempDir()
	if got, err := ExpandDir(dir); err != nil || got != dir {
		t.Errorf("absolute: %q %v", got, err)
	}
	if got, err := ExpandDir("~"); err != nil || got != home {
		t.Errorf("tilde: %q %v", got, err)
	}
	if _, err := ExpandDir(filepath.Join(dir, "missing")); err == nil {
		t.Error("missing directory should fail")
	}
	if _, err := ExpandDir(""); err == nil {
		t.Error("empty dir should fail")
	}
}

func TestTrusted(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfgDir)
	if Trusted("/srv/site") {
		t.Error("no config file: nothing is trusted")
	}
	os.WriteFile(filepath.Join(cfgDir, ".claude.json"), []byte(`{"projects":{"/srv/site":{"hasTrustDialogAccepted":true},"/home/me":{"hasTrustDialogAccepted":false}}}`), 0o644)
	for dir, want := range map[string]bool{"/srv/site": true, "/srv/site/sub/dir": true, "/srv": false, "/home/me": false, "/home/me/code": false} {
		if got := Trusted(dir); got != want {
			t.Errorf("Trusted(%s) = %v, want %v", dir, got, want)
		}
	}
}

func TestRunOpensTerminalThroughLauncher(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HYPRLAND_INSTANCE_SIGNATURE", "") // no window tracking in tests
	cfgDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfgDir) // an empty sessions dir, and a trusted folder
	os.WriteFile(filepath.Join(cfgDir, ".claude.json"), []byte(`{"projects":{"`+dir+`":{"hasTrustDialogAccepted":true}}}`), 0o644)
	SessionWait = 0
	var gotDir, gotTitle string
	var gotCmd, gotEnv []string
	Launch = func(d, title string, command, env []string) error {
		gotDir, gotTitle, gotCmd, gotEnv = d, title, command, env
		return nil
	}
	t.Cleanup(func() { Launch = launch })

	res, err := Run(t.Context(), Request{Dir: dir})
	if err != nil || len(gotCmd) != 0 || gotDir != dir || gotTitle != "den" {
		t.Fatalf("terminal: %+v %v (%q %q %v)", res, err, gotDir, gotTitle, gotCmd)
	}
	if !strings.HasPrefix(res.Describe(), "Opened a terminal in ") {
		t.Errorf("describe = %q", res.Describe())
	}

	res, err = Run(t.Context(), Request{Dir: dir, Agent: true, Name: "helper", Brief: "Fix the tests.", Orchestrator: "orc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(gotCmd) != 5 || gotCmd[0] != "claude" || gotCmd[1] != "--name" || gotCmd[2] != "helper" || gotCmd[3] != "--" {
		t.Errorf("command = %v", gotCmd)
	}
	if !strings.Contains(gotCmd[4], `peer "orc"`) || !strings.HasSuffix(gotCmd[4], "Fix the tests.") || strings.Contains(gotCmd[4], "spawn_agent") {
		t.Errorf("prompt = %q", gotCmd[4])
	}
	if len(gotEnv) != 1 || gotEnv[0] != "DEN_PEER=helper" {
		t.Errorf("env = %v", gotEnv)
	}
	if res.Registered || res.Name != "helper" || gotTitle != "den · helper" {
		t.Errorf("result = %+v title %q", res, gotTitle)
	}
	if d := res.Describe(); !strings.Contains(d, "has not registered yet") || !strings.Contains(d, `"helper"`) {
		t.Errorf("describe = %q", d)
	}
	if _, err := Run(t.Context(), Request{Dir: dir, Agent: true}); err == nil {
		t.Error("an agent without a brief should fail")
	}
	if _, err := Run(t.Context(), Request{Dir: dir, Agent: true, Brief: "x", Model: "gpt"}); err == nil || !strings.Contains(err.Error(), "haiku, sonnet, opus") {
		t.Errorf("an unknown model should fail naming the choices, got %v", err)
	}

	// A lead's teammate: on its model, with Den's tools, reporting to the lead.
	res, err = Run(t.Context(), Request{Dir: dir, Agent: true, Name: "designer", Brief: "Design it.", Model: "opus", Orchestrator: "orc", Lead: "pm", Spawner: "/opt/den"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"claude", "--name", "designer", "--model", "opus", "--mcp-config", MCPConfig("/opt/den"), "--allowedTools", "mcp__den__spawn_agent,mcp__den__open_terminal", "--"}
	if len(gotCmd) != len(want)+1 || strings.Join(gotCmd[:len(want)], " ") != strings.Join(want, " ") {
		t.Errorf("command = %v", gotCmd)
	}
	prompt := gotCmd[len(gotCmd)-1]
	for _, s := range []string{`your lead (peer "pm")`, `SendMessage to your lead, "pm"`, "spawn_agent", "opus for work that needs judgment", "Design it."} {
		if !strings.Contains(prompt, s) {
			t.Errorf("prompt lacks %q: %q", s, prompt)
		}
	}
	if res.Model != "opus" || !strings.Contains(res.Describe(), `"designer"`) {
		t.Errorf("result = %+v", res)
	}

	// An untrusted folder: no wait, and the answer says who has to act.
	os.WriteFile(filepath.Join(cfgDir, ".claude.json"), []byte(`{"projects":{}}`), 0o644)
	SessionWait = time.Minute
	res, err = Run(t.Context(), Request{Dir: dir, Agent: true, Brief: "Hi.", Name: "u"})
	if err != nil || !res.Untrusted || res.Registered {
		t.Fatalf("untrusted: %+v %v", res, err)
	}
	if d := res.Describe(); !strings.Contains(d, "whether to trust it") {
		t.Errorf("describe = %q", d)
	}
}

func TestChildEnvDropsWhatAParentSessionSet(t *testing.T) {
	in := []string{"HOME=/home/me", "CLAUDECODE=1", "CLAUDE_PID=42", "CLAUDE_EFFORT=high", "CLAUDE_CODE_SESSION_ID=abc", "CLAUDE_CODE_SESSION_ATTENDED=1",
		"CLAUDE_CODE_MESSAGING_SOCKET=/run/x.sock", "CLAUDE_CODE_MESSAGING_TOKEN=t", "CLAUDE_CODE_CHILD_SESSION=1", "CLAUDE_CODE_ENTRYPOINT=cli", "CLAUDE_CODE_EXECPATH=/opt/claude",
		"DEN_ORCHESTRATOR=1", "DEN_PEER=old", "CLAUDE_CONFIG_DIR=/home/me/.claude", "CLAUDE_CODE_PLUGIN_DIRS=~/.claude/mods", "ANTHROPIC_API_KEY=k"}
	got := strings.Join(childEnv(in, []string{"DEN_PEER=new"}), " ")
	want := "HOME=/home/me CLAUDE_CONFIG_DIR=/home/me/.claude CLAUDE_CODE_PLUGIN_DIRS=~/.claude/mods ANTHROPIC_API_KEY=k DEN_PEER=new"
	if got != want {
		t.Errorf("childEnv = %q, want %q", got, want)
	}
}

func TestCommandGivesApprovedSkillsFolder(t *testing.T) {
	got := Command(Request{Brief: "go", Model: "sonnet", AddDirs: []string{"/s/agents/ui-1"}, Spawner: "/bin/den"}, "ui")
	want := []string{"claude", "--name", "ui", "--add-dir", "/s/agents/ui-1", "--model", "sonnet"}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("command = %q, want it to start %q", got, want)
		}
	}
	if got[len(got)-2] != "--" {
		t.Errorf("the prompt is not after --: %q", got)
	}
}

func TestCommandResumes(t *testing.T) {
	got := Command(Request{Resume: "s1", Model: "claude-opus-5-5", AddDirs: []string{"/s/agents/ui-1"}, Spawner: "/bin/den"}, "ui")
	want := []string{"claude", "--resume", "s1", "--name", "ui", "--add-dir", "/s/agents/ui-1", "--model", "claude-opus-5-5", "--mcp-config", MCPConfig("/bin/den"), "--allowedTools", "mcp__den__spawn_agent,mcp__den__open_terminal"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("command = %q, want %q", got, want)
	}
	got = Command(Request{Resume: "s1", Fork: true}, "ui-2")
	if strings.Join(got, " ") != "claude --resume s1 --fork-session --name ui-2" {
		t.Errorf("fork command = %q", got)
	}
}

func TestRunResumesWithoutBrief(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HYPRLAND_INSTANCE_SIGNATURE", "")
	cfgDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfgDir)
	os.WriteFile(filepath.Join(cfgDir, ".claude.json"), []byte(`{"projects":{"`+dir+`":{"hasTrustDialogAccepted":true}}}`), 0o644)
	SessionWait = 0
	var gotCmd, gotEnv []string
	Launch = func(d, title string, command, env []string) error {
		gotCmd, gotEnv = command, env
		return nil
	}
	t.Cleanup(func() { Launch = launch })
	res, err := Run(t.Context(), Request{Dir: dir, Agent: true, Resume: "s1", Name: "demo-app-1a2b", Model: "claude-sonnet-5-5"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(gotCmd, " ") != "claude --resume s1 --name demo-app-1a2b --model claude-sonnet-5-5" {
		t.Errorf("command = %q", gotCmd)
	}
	if len(gotEnv) != 1 || gotEnv[0] != "DEN_PEER=demo-app-1a2b" || res.Name != "demo-app-1a2b" {
		t.Errorf("env %v result %+v", gotEnv, res)
	}
}
