package spawn

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/hypr"
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
	Launch = func(d, title, marker string, command, env []string) error {
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
	Launch = func(d, title, marker string, command, env []string) error {
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

func TestWorktreeInputAcceptsABooleanOrAName(t *testing.T) {
	for in, want := range map[string]Worktree{`true`: WorktreeAuto, `false`: "", `null`: "", `"feature-x"`: "feature-x", `"true"`: WorktreeAuto, `" "`: "", `"false"`: ""} {
		var got Worktree
		if err := got.UnmarshalJSON([]byte(in)); err != nil || got != want {
			t.Errorf("%s -> %q (%v), want %q", in, got, err, want)
		}
	}
	var req struct {
		Worktree Worktree `json:"worktree"`
	}
	if err := json.Unmarshal([]byte(`{"worktree": true}`), &req); err != nil || req.Worktree != WorktreeAuto {
		t.Errorf("inside a struct: %q %v", req.Worktree, err)
	}
	if err := json.Unmarshal([]byte(`{"worktree": 3}`), &req); err != nil || req.Worktree != "" {
		t.Errorf("a number is ignored: %q %v", req.Worktree, err)
	}
}

func TestRunStartsAnAgentInItsOwnWorktree(t *testing.T) {
	repo := t.TempDir()
	if r, err := filepath.EvalSymlinks(repo); err == nil {
		repo = r
	}
	git := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=T", "-c", "user.email=t@example.com"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	t.Setenv("HYPRLAND_INSTANCE_SIGNATURE", "")
	cfgDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfgDir)
	os.WriteFile(filepath.Join(cfgDir, ".claude.json"), []byte(`{"projects":{"`+repo+`":{"hasTrustDialogAccepted":true}}}`), 0o644)
	SessionWait = 0
	var gotCmd []string
	Launch = func(d, title, marker string, command, env []string) error { gotCmd = command; return nil }
	t.Cleanup(func() { Launch = launch })

	// No commit yet: nothing to branch from.
	if _, err := Run(t.Context(), Request{Dir: repo, Agent: true, Name: "helper", Brief: "Fix.", Worktree: WorktreeAuto}); err == nil || !strings.Contains(err.Error(), "no commit yet") {
		t.Fatalf("no commit: err = %v", err)
	}
	git("commit", "-q", "--allow-empty", "-m", "first")
	if _, err := Run(t.Context(), Request{Dir: t.TempDir(), Agent: true, Brief: "Fix.", Worktree: WorktreeAuto}); err == nil || !strings.Contains(err.Error(), "not inside a git repository") {
		t.Fatalf("not a repository: err = %v", err)
	}

	// true: a worktree named after the agent, in a subfolder too.
	sub := filepath.Join(repo, "pkg")
	os.MkdirAll(sub, 0o755)
	res, err := Run(t.Context(), Request{Dir: sub, Agent: true, Name: "helper", Brief: "Fix the tests.", Orchestrator: "orc", Worktree: WorktreeAuto})
	if err != nil {
		t.Fatal(err)
	}
	if res.Worktree != "helper" || res.WorktreeBranch != "worktree-helper" || res.WorktreePath != filepath.Join(repo, ".claude", "worktrees", "helper") {
		t.Errorf("result = %+v", res)
	}
	if strings.Join(gotCmd[:5], " ") != "claude --name helper --worktree helper" || gotCmd[5] != "--" {
		t.Errorf("command = %v", gotCmd)
	}
	prompt := gotCmd[len(gotCmd)-1]
	for _, s := range []string{"Fix the tests.", "your own git worktree, on branch worktree-helper", "Do not merge"} {
		if !strings.Contains(prompt, s) {
			t.Errorf("prompt lacks %q: %q", s, prompt)
		}
	}
	res.Registered, res.Critter = true, "Mossy Lemur"
	if d := res.Describe(); !strings.Contains(d, "own git worktree") || !strings.Contains(d, "worktree-helper") {
		t.Errorf("describe = %q", d)
	}

	// An old worktree of that name exists: an automatic name takes the next free one.
	os.MkdirAll(filepath.Join(repo, ".claude", "worktrees", "helper"), 0o755)
	res, err = Run(t.Context(), Request{Dir: repo, Agent: true, Name: "helper", Brief: "Again.", Worktree: WorktreeAuto})
	if err != nil || res.Worktree != "helper-2" {
		t.Errorf("second: %+v %v", res, err)
	}
	// A name asked for is used as given, cleaned.
	res, err = Run(t.Context(), Request{Dir: repo, Agent: true, Name: "helper", Brief: "Again.", Worktree: "Feature X"})
	if err != nil || res.Worktree != "feature-x" || gotCmd[3] != "--worktree" || gotCmd[4] != "feature-x" {
		t.Errorf("named: %+v %v %v", res, err, gotCmd)
	}
	// Without it nothing changes.
	if _, err := Run(t.Context(), Request{Dir: repo, Agent: true, Name: "plain", Brief: "x"}); err != nil || strings.Contains(strings.Join(gotCmd, " "), "--worktree") {
		t.Errorf("plain: %v %v", err, gotCmd)
	}
}

// TestTitleIsSanitised: a name carrying an OSC 0 title sequence, as a
// model could pass to spawn_agent, never reaches the terminal's title.
func TestTitleIsSanitised(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HYPRLAND_INSTANCE_SIGNATURE", "")
	cfgDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfgDir)
	os.WriteFile(filepath.Join(cfgDir, ".claude.json"), []byte(`{"projects":{"`+dir+`":{"hasTrustDialogAccepted":true}}}`), 0o644)
	SessionWait = 0
	var gotTitle string
	Launch = func(d, title, marker string, command, env []string) error { gotTitle = title; return nil }
	t.Cleanup(func() { Launch = launch })

	if _, err := Run(t.Context(), Request{Dir: dir, Agent: true, Name: "x\x1b]0;pwned\x07\u202e", Brief: "Fix."}); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(gotTitle, "\x1b\x07\u202e") || strings.Contains(gotTitle, ";") {
		t.Errorf("title = %q", gotTitle)
	}
}

// TestAgentWindowMarker: an agent's terminal gets the marker class of its
// peer name; a plain terminal gets none, and a class already on a live
// window is not reused.
func TestAgentWindowMarker(t *testing.T) {
	t.Setenv("HYPRLAND_INSTANCE_SIGNATURE", "")
	cfgDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfgDir)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(cfgDir, ".claude.json"), []byte(`{"projects":{"`+dir+`":{"hasTrustDialogAccepted":true}}}`), 0o644)
	SessionWait = 0
	var got []string
	Launch = func(d, title, marker string, command, env []string) error { got = append(got, marker); return nil }
	t.Cleanup(func() { Launch = launch })
	if _, err := Run(t.Context(), Request{Dir: dir, Agent: true, Name: "helper", Brief: "Fix."}); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(t.Context(), Request{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "io.glacierhub.den.helper" || got[1] != "" {
		t.Errorf("markers = %q", got)
	}
	live := []hypr.Client{{Address: "0x1", InitialClass: "io.glacierhub.den.helper"}}
	if m := freeMarker("helper", live); m != "" {
		t.Errorf("a live marker was reused: %q", m)
	}
	if m := freeMarker("other", live); m != "io.glacierhub.den.other" {
		t.Errorf("free marker = %q", m)
	}
}

// TestGhosttyArgs: Den's own Ghostty instance for the window, its marker as
// the class, the command after -e.
func TestGhosttyArgs(t *testing.T) {
	got := strings.Join(ghosttyArgs("/usr/bin/ghostty", "/w", "den · helper", "io.glacierhub.den.helper", []string{"claude", "--name", "helper"}), " ")
	want := "/usr/bin/ghostty --gtk-single-instance=false --class=io.glacierhub.den.helper --working-directory=/w --title=den · helper -e claude --name helper"
	if got != want {
		t.Errorf("argv\n got %s\nwant %s", got, want)
	}
}
