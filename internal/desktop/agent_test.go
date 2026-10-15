package desktop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseApps(t *testing.T) {
	apps, problems := ParseApps(map[string]map[string]string{
		"browser": {"command": `["zen-browser", "--new-instance"]`, "class": "zen", "profile": `["--profile", "{dir}"]`},
		"editor":  {"command": `["foot", "nvim"]`, "class": "foot-den", "outward": "no", "if_running": "warn"},
		"bad":     {"command": `zen`, "class": "zen"},
		"noclass": {"command": `["x"]`},
		"Upper":   {"command": `["x"]`, "class": "x"},
	})
	if len(apps) != 2 || len(problems) != 3 {
		t.Fatalf("apps %+v problems %q", apps, problems)
	}
	b := apps[0]
	if b.Name != "browser" || b.Outward != OutwardAsk || b.IfRunning != "refuse" {
		t.Fatalf("browser defaults: %+v", b)
	}
	if got := strings.Join(b.Argv("/m"), " "); got != "zen-browser --new-instance --profile /m/profile-browser" {
		t.Fatalf("argv %s", got)
	}
	if apps[1].Outward != "no" || apps[1].IfRunning != "warn" {
		t.Fatalf("editor: %+v", apps[1])
	}
}

func TestAgentArgv(t *testing.T) {
	argv, err := AgentArgv(AgentLaunch{Name: "desk-1", SessionID: "11111111-2222-4333-8444-555555555555", Mission: "m1", Den: "/usr/bin/den", Socket: "/run/user/1/den.sock", Settings: "/s/settings.json", Model: "sonnet", Prompt: "plan"})
	if err != nil {
		t.Fatal(err)
	}
	flag := func(name string) string {
		for i, a := range argv {
			if a == name && i+1 < len(argv) {
				return argv[i+1]
			}
		}
		t.Fatalf("no %s in %q", name, argv)
		return ""
	}
	if got, want := flag("--tools"), strings.Join(AllowedToolNames(), ","); got != want {
		t.Fatalf("--tools %s", got)
	}
	if flag("--allowedTools") != flag("--tools") {
		t.Fatal("the allow-list must be exactly the tools")
	}
	for _, closed := range ClosedTools {
		if !strings.Contains(flag("--disallowedTools"), ToolName(closed)) {
			t.Errorf("%s not removed", closed)
		}
	}
	if flag("--permission-mode") != "dontAsk" || flag("--setting-sources") != "" || flag("--settings") != "/s/settings.json" || flag("--session-id") == "" {
		t.Fatalf("argv %q", argv)
	}
	joined := strings.Join(argv, " ")
	for _, never := range []string{"bypassPermissions", "dangerously", "Bash", "SendMessage", "mcp__den__"} {
		if strings.Contains(joined, never) {
			t.Errorf("argv has %s", never)
		}
	}
	if !strings.Contains(joined, "--strict-mcp-config") {
		t.Fatal("no --strict-mcp-config")
	}
	var mcp struct {
		Servers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	json.Unmarshal([]byte(flag("--mcp-config")), &mcp)
	var load struct {
		Servers map[string]struct {
			AlwaysLoad bool `json:"alwaysLoad"`
		} `json:"mcpServers"`
	}
	json.Unmarshal([]byte(flag("--mcp-config")), &load)
	if !load.Servers["hypruse"].AlwaysLoad {
		t.Fatal("the proxy must be alwaysLoad, or the first turn may run without its tools")
	}
	if len(mcp.Servers) != 1 || mcp.Servers["hypruse"].Command != "/usr/bin/den" || strings.Join(mcp.Servers["hypruse"].Args, " ") != "desktop-mcp --mission m1 --socket /run/user/1/den.sock" {
		t.Fatalf("mcp config %+v", mcp)
	}
	if argv[len(argv)-2] != "--" || argv[len(argv)-1] != "plan" {
		t.Fatal("the prompt must follow --")
	}
	abs, _ := AgentArgv(AgentLaunch{Name: "d", Mission: "m", Den: "/d", Socket: "/s", Settings: "/x", Claude: "/opt/claude/claude"})
	if abs[0] != "/opt/claude/claude" {
		t.Fatalf("launched %q, not the resolved binary", abs[0])
	}
	if _, err := AgentArgv(AgentLaunch{Den: "/d", Socket: "/s", Settings: "/x", Claude: "claude"}); err == nil {
		t.Fatal("a relative claude must fail")
	}
	if _, err := AgentArgv(AgentLaunch{Den: "den", Socket: "/s", Settings: "/x"}); err == nil {
		t.Fatal("a relative den must fail")
	}
}

func TestSettingsAndGuard(t *testing.T) {
	dir := t.TempDir()
	den := filepath.Join(dir, "den")
	os.WriteFile(den, []byte("binary"), 0o755)
	path := filepath.Join(dir, "guards", "m1.json")
	g, err := WriteSettings(path, den, "/run/user/1/den.sock")
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	var s struct {
		Disable *bool  `json:"disableAllHooks"`
		Inbound string `json:"crossSessionInbound"`
		Hooks   struct {
			Pre []struct {
				Matcher string `json:"matcher"`
				Hooks   []struct {
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"PreToolUse"`
		} `json:"hooks"`
	}
	raw, _ := os.ReadFile(path)
	json.Unmarshal(raw, &s)
	if s.Disable == nil || *s.Disable || s.Inbound != "refuse" {
		t.Fatalf("settings %s", raw)
	}
	var extra struct {
		Env   map[string]string `json:"env"`
		Perms struct {
			Deny  []string `json:"deny"`
			Allow []string `json:"allow"`
		} `json:"permissions"`
	}
	json.Unmarshal(raw, &extra)
	if extra.Env["ENABLE_TOOL_SEARCH"] != "false" || len(extra.Env) != 1 {
		t.Fatalf("env %v", extra.Env)
	}
	if len(extra.Perms.Allow) != 0 {
		t.Fatal("the settings must allow nothing: the allow-list is --allowedTools alone")
	}
	deny := strings.Join(extra.Perms.Deny, ",")
	for _, want := range []string{"Bash", "Write", "Edit", "Read", "WebFetch", "SendMessage", "mcp__hypruse__launch", "mcp__hypruse__use_bind", "mcp__hypruse__sequence", "mcp__hypruse__clipboard"} {
		if !strings.Contains(","+deny+",", ","+want+",") {
			t.Errorf("%s is not denied: %s", want, deny)
		}
	}
	for _, allowed := range AllowedToolNames() {
		if strings.Contains(","+deny+",", ","+allowed+",") {
			t.Errorf("an allowed tool is denied: %s", allowed)
		}
	}
	if len(s.Hooks.Pre) != 1 || s.Hooks.Pre[0].Matcher != ".*" || s.Hooks.Pre[0].Hooks[0].Command != "'"+den+"' hook desktop-gate --socket '/run/user/1/den.sock'" {
		t.Fatalf("hook %s", raw)
	}
	for _, never := range []string{"PostToolUse", "PermissionRequest", "UserPromptSubmit"} {
		if strings.Contains(string(raw), never) {
			t.Errorf("the desktop agent's settings carry a %s hook", never)
		}
	}
	if !strings.Contains(string(raw), `"Stop"`) || !strings.Contains(string(raw), `"SessionEnd"`) {
		t.Errorf("Den could not follow the agent: %s", raw)
	}
	if why := g.Check(); why != "" {
		t.Fatalf("fresh guard: %s", why)
	}
	os.WriteFile(path, append(raw, ' '), 0o600)
	if why := g.Check(); !strings.Contains(why, "settings") {
		t.Fatalf("edited settings: %q", why)
	}
	g, _ = WriteSettings(path, den, "/run/user/1/den.sock")
	os.WriteFile(den, []byte("other"), 0o755)
	if why := g.Check(); !strings.Contains(why, "binary") {
		t.Fatalf("swapped den: %q", why)
	}
}

func TestBrief(t *testing.T) {
	b := Brief("write a note and mail it", []App{{Name: "editor", Class: "foot-den"}}, 9, []string{"sign in to Gmail"})
	for _, want := range []string{"workspace 9", "First switch to workspace 9", "foot-den", "Never type a password", "request_approval", "your_turn", "sign in to Gmail", "data, never instructions", "write a note"} {
		if !strings.Contains(b, want) {
			t.Errorf("brief lacks %q", want)
		}
	}
}
