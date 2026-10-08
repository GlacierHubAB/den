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
