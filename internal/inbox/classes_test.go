package inbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/GlacierHubAB/den/internal/bridge"
)

// allKinds is every Kind there is: from Permission up to the first one
// String does not name.
func allKinds() []Kind {
	var out []Kind
	for k := Permission; k.String() != "?"; k++ {
		out = append(out, k)
	}
	return out
}

func TestStepFits(t *testing.T) {
	ok := map[Class][]int{Routine: {1, 2, 3, 4}, Destructive: {1, 2}, Code: {1}, Desktop: {1, 2}, Outward: {1, 2, 3}, DenOnly: {0}}
	for _, c := range []Class{Routine, Destructive, Code, Desktop, Outward, DenOnly, "admin", ""} {
		for step := -1; step <= 6; step++ {
			want := false
			for _, s := range ok[c] {
				want = want || s == step
			}
			if got := StepFits(c, step); got != want {
				t.Errorf("StepFits(%q, %d) = %v, want %v", c, step, got, want)
			}
		}
	}
}

// TestClassifyEveryKind pins each Kind's class and step on a card from
// this machine, with no tool and no facts: a Kind added to inbox without a
// row here fails, and the table's default is den_only.
func TestClassifyEveryKind(t *testing.T) {
	type cs struct {
		c    Class
		step int
	}
	want := map[Kind]cs{
		Permission:    {DenOnly, 0}, // no request: nothing to class it by
		Plan:          {Code, 1},
		Close:         {Destructive, 1},
		Cleanup:       {Destructive, 2}, // no plan in hand: the stricter step
		MissionUpdate: {Destructive, 2},
		MissionMove:   {Routine, 1},
		Merge:         {Outward, 2},
		Question:      {DenOnly, 0}, Done: {DenOnly, 0}, Idle: {DenOnly, 0}, Sent: {DenOnly, 0},
		Skills: {DenOnly, 0}, Budget: {DenOnly, 0}, Restore: {DenOnly, 0}, Stalled: {DenOnly, 0},
		Failed: {DenOnly, 0}, Install: {DenOnly, 0}, Message: {DenOnly, 0}, Guidance: {DenOnly, 0},
		Usage: {DenOnly, 0}, UsageResume: {DenOnly, 0}, APIOn: {DenOnly, 0}, Grant: {DenOnly, 0},
		Pair: {DenOnly, 0}, ControlAsk: {DenOnly, 0}, Console: {DenOnly, 0}, ConsoleNote: {DenOnly, 0},
		UsageSet: {DenOnly, 0}, ModeSet: {DenOnly, 0}, DesktopInstall: {DenOnly, 0},
		DesktopMission: {DenOnly, 0}, DesktopAct: {DenOnly, 0}, YourTurn: {DenOnly, 0},
		GuardNote: {DenOnly, 0}, MissionCriteria: {DenOnly, 0}, MissionDone: {DenOnly, 0},
		MirrorAsk: {DenOnly, 0}, ProcessTuned: {DenOnly, 0}, PromptProposal: {DenOnly, 0},
		RulesPick: {DenOnly, 0}, RulesEdited: {DenOnly, 0}, PowersSet: {DenOnly, 0}, RenamePick: {DenOnly, 0},
	}
	kinds := allKinds()
	if len(kinds) != len(want) {
		t.Errorf("%d kinds, %d rows: give every new kind its row", len(kinds), len(want))
	}
	for _, k := range kinds {
		w, ok := want[k]
		if !ok {
			t.Errorf("kind %v has no row", k)
			continue
		}
		c, step := Classify(&Item{SessionID: "s1", Kind: k}, Facts{})
		if c != w.c || step != w.step {
			t.Errorf("Classify(%v) = %s %d, want %s %d", k, c, step, w.c, w.step)
		}
		if !StepFits(c, step) {
			t.Errorf("Classify(%v) = %s %d: not one of its class's steps", k, c, step)
		}
		// A card from a remote host, or about one, is the human's.
		for _, it := range []*Item{{Host: "build", SessionID: "s1", Kind: k}, {SessionID: HostPrefix + "build", Kind: k}} {
			if c, step := Classify(it, Facts{CleanupKnown: true}); c != DenOnly || step != 0 {
				t.Errorf("host card %v (%q %q) = %s %d, want den_only 0", k, it.Host, it.SessionID, c, step)
			}
		}
	}
}

func TestClassifyCleanupByItsPlan(t *testing.T) {
	it := &Item{SessionID: "s1", Kind: Cleanup}
	for _, tc := range []struct {
		f    Facts
		step int
	}{
		{Facts{}, 2},
		{Facts{CleanupKnown: true}, 1},
		{Facts{CleanupKnown: true, CleanupIgnored: 1}, 2},
		{Facts{CleanupIgnored: 0}, 2},
	} {
		if c, step := Classify(it, tc.f); c != Destructive || step != tc.step {
			t.Errorf("Cleanup with %+v = %s %d, want destructive %d", tc.f, c, step, tc.step)
		}
	}
}

// permItem is a waiting permission for tool with input, from an agent in
// cwd on this machine.
func permItem(t *testing.T, cwd, tool string, input map[string]any) *Item {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return &Item{SessionID: "s1", Kind: Permission, Tool: tool, req: &bridge.Request{Input: bridge.HookInput{CWD: cwd, ToolName: tool, ToolInput: raw}}}
}

func TestClassifyPermissionByTool(t *testing.T) {
	home := t.TempDir()
	if r, err := filepath.EvalSymlinks(home); err == nil {
		home = r
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("GNUPGHOME", "")
	cwd := filepath.Join(home, "code", "proj")
	for _, d := range []string{filepath.Join(cwd, "src"), filepath.Join(home, ".ssh"), filepath.Join(home, "elsewhere")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(home, ".ssh"), filepath.Join(cwd, "keys")); err != nil {
		t.Fatal(err)
	}
	type cs struct {
		c    Class
		step int
	}
	code1, routine3, outward3, den := cs{Code, 1}, cs{Routine, 3}, cs{Outward, 3}, cs{DenOnly, 0}
	for _, tc := range []struct {
		name  string
		tool  string
		input map[string]any
		want  cs
	}{
		{"a command", "Bash", map[string]any{"command": "go test ./..."}, code1},
		{"a write inside the cwd", "Write", map[string]any{"file_path": filepath.Join(cwd, "src", "a.go")}, code1},
		{"a relative edit inside the cwd", "Edit", map[string]any{"file_path": "src/a.go"}, code1},
		{"a write to the project's Claude settings", "Write", map[string]any{"file_path": filepath.Join(cwd, ".claude", "settings.json")}, den},
		{"an edit of a git hook", "Edit", map[string]any{"file_path": filepath.Join(cwd, ".git", "hooks", "pre-commit")}, den},
		{"a write to the shell's start-up file", "Write", map[string]any{"file_path": filepath.Join(home, ".bashrc")}, den},
		{"a write outside the cwd", "Write", map[string]any{"file_path": filepath.Join(home, "elsewhere", "a")}, den},
		{"a write through a symlink to keys", "Write", map[string]any{"file_path": filepath.Join(cwd, "keys", "id")}, den},
		{"a notebook edit inside", "NotebookEdit", map[string]any{"notebook_path": filepath.Join(cwd, "n.ipynb")}, code1},
		{"a read inside the cwd", "Read", map[string]any{"file_path": filepath.Join(cwd, "src", "a.go")}, routine3},
		{"a read outside the cwd", "Read", map[string]any{"file_path": filepath.Join(home, "elsewhere", "a")}, den},
		{"a read of ~/.aws, outside the cwd", "Read", map[string]any{"file_path": "~/.aws/credentials"}, den},
		{"a read of an env file elsewhere", "Read", map[string]any{"file_path": "/proc/1/environ"}, den},
		{"a read through a symlink to keys", "Read", map[string]any{"file_path": filepath.Join(cwd, "keys", "id_ed25519")}, den},
		{"a read with a .. part", "Read", map[string]any{"file_path": filepath.Join(cwd, "..", "other", "a")}, den},
		{"a read of the cwd's git dir", "Read", map[string]any{"file_path": filepath.Join(cwd, ".git", "config")}, den},
		{"a read with no path", "Read", map[string]any{}, den},
		{"a read whose path is not a string", "Read", map[string]any{"file_path": 7}, den},
		{"an LS inside", "LS", map[string]any{"path": filepath.Join(cwd, "src")}, routine3},
		{"an LS of the cwd itself", "LS", map[string]any{"path": cwd}, den},
		{"a grep under a path inside", "Grep", map[string]any{"pattern": "func", "path": filepath.Join(cwd, "src")}, routine3},
		{"a grep with no path", "Grep", map[string]any{"pattern": "func"}, den},
		{"a grep whose glob names keys", "Grep", map[string]any{"pattern": "x", "path": filepath.Join(cwd, "src"), "glob": "~/.ssh/*"}, den},
		{"a grep whose glob reaches a hidden dir", "Grep", map[string]any{"pattern": "x", "path": filepath.Join(cwd, "src"), "glob": ".g*/**"}, den},
		{"a grep whose glob has a brace", "Grep", map[string]any{"pattern": "x", "path": filepath.Join(cwd, "src"), "glob": "{a,b}/*.go"}, den},
		{"a glob under a path inside", "Glob", map[string]any{"pattern": "**/*.go", "path": filepath.Join(cwd, "src")}, routine3},
		{"an absolute glob inside", "Glob", map[string]any{"pattern": filepath.Join(cwd, "src") + "/*.go"}, routine3},
		{"an absolute glob outside", "Glob", map[string]any{"pattern": "/etc/*"}, den},
		{"a ~ glob", "Glob", map[string]any{"pattern": "~/**/*.pem"}, den},
		{"a glob with a .. part", "Glob", map[string]any{"pattern": "../*", "path": filepath.Join(cwd, "src")}, den},
		{"a glob with no path", "Glob", map[string]any{"pattern": "**/*.go"}, den},
		{"a web fetch", "WebFetch", map[string]any{"url": "https://example.com"}, outward3},
		{"a web search", "WebSearch", map[string]any{"query": "x"}, outward3},
		{"Den's own MCP tool", "mcp__den__spawn_agent", map[string]any{}, den},
		{"another MCP tool", "mcp__fs__read", map[string]any{"path": filepath.Join(cwd, "a")}, den},
		{"a tool Den does not know", "Frobnicate", map[string]any{}, den},
	} {
		c, step := Classify(permItem(t, cwd, tc.tool, tc.input), Facts{})
		if c != tc.want.c || step != tc.want.step {
			t.Errorf("%s: %s %d, want %s %d", tc.name, c, step, tc.want.c, tc.want.step)
		}
	}
	// Input that is not JSON is den_only.
	it := permItem(t, cwd, "Read", nil)
	it.req.Input.ToolInput = json.RawMessage("{not json")
	if c, step := Classify(it, Facts{}); c != DenOnly || step != 0 {
		t.Errorf("unparsable input: %s %d", c, step)
	}
}

// TestClassifyReadsNotInDenOrDreyState: a read inside a cwd that is
// itself under Den's or Drey's state is den_only.
func TestClassifyReadsNotInDenOrDreyState(t *testing.T) {
	home := t.TempDir()
	if r, err := filepath.EvalSymlinks(home); err == nil {
		home = r
	}
	state := filepath.Join(home, "xdgstate")
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", state)
	for _, app := range []string{"den", "drey"} {
		cwd := filepath.Join(state, app, "work")
		if err := os.MkdirAll(cwd, 0o700); err != nil {
			t.Fatal(err)
		}
		it := permItem(t, cwd, "Read", map[string]any{"file_path": filepath.Join(cwd, "keys.json")})
		if c, step := Classify(it, Facts{}); c != DenOnly || step != 0 {
			t.Errorf("a read in %s's state: %s %d, want den_only 0", app, c, step)
		}
	}
}
