package spawn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestRunOpensTerminalThroughLauncher(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HYPRLAND_INSTANCE_SIGNATURE", "") // no window tracking in tests
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())  // an empty sessions dir
	SessionWait = 0
	var gotDir, gotTitle string
	var gotCmd []string
	Launch = func(d, title string, command []string) error {
		gotDir, gotTitle, gotCmd = d, title, command
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
	if len(gotCmd) != 4 || gotCmd[0] != "claude" || gotCmd[1] != "--name" || gotCmd[2] != "helper" {
		t.Errorf("command = %v", gotCmd)
	}
	if !strings.Contains(gotCmd[3], `peer "orc"`) || !strings.HasSuffix(gotCmd[3], "Fix the tests.") {
		t.Errorf("prompt = %q", gotCmd[3])
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
}
