package remote

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestConfigRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "remotes.toml")
	os.WriteFile(p, []byte(`# mine
default_policy = "approve"
future_key = 3

[hosts.devbox]
target = "devbox"   # an alias
policy = "full"
color = "teal"

[hosts.gpu]
target = "user@10.0.0.5"
labels = false
den_path = "/opt/den/bin/den"
enabled = false
`), 0o644)
	c, err := LoadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Default() != Approve || len(c.Hosts) != 2 {
		t.Fatalf("config = %+v", c)
	}
	gpu, _ := c.Find("gpu")
	if gpu.Policy != Approve || gpu.Labels || gpu.Enabled || gpu.DenPath != "/opt/den/bin/den" || gpu.Target != "user@10.0.0.5" {
		t.Errorf("gpu = %+v", gpu)
	}
	if len(c.Enabled()) != 1 {
		t.Errorf("enabled = %+v", c.Enabled())
	}
	if err := c.Add(Host{Label: "b", Target: "b", Labels: true, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if b, _ := c.Find("b"); b.Policy != Approve {
		t.Errorf("new host policy %q, want the default", b.Policy)
	}
	if err := c.Save(p); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	for _, keep := range []string{"future_key = 3", `color = "teal"`, `default_policy = "approve"`, "[hosts.b]"} {
		if !strings.Contains(string(raw), keep) {
			t.Errorf("rewritten file lost %q:\n%s", keep, raw)
		}
	}
	again, err := LoadFile(p)
	if err != nil || len(again.Hosts) != 3 {
		t.Errorf("reload: %v %+v", err, again)
	}
}

func TestConfigRefuses(t *testing.T) {
	var c Config
	for _, h := range []Host{
		{Label: "local", Target: "x", Policy: Observe},
		{Label: "Dev", Target: "x", Policy: Observe},
		{Label: "dev_box", Target: "x", Policy: Observe},
		{Label: "dev", Target: "-oProxyCommand=evil", Policy: Observe},
		{Label: "dev", Target: "a b", Policy: Observe},
		{Label: "dev", Target: "x", Policy: "everything"},
		{Label: "dev", Target: "x", Policy: Observe, DenPath: "den"},
		{Label: "dev", Target: "x", Policy: Observe, DenPath: "/opt/$(evil)/den"},
	} {
		if err := c.Add(h); err == nil {
			t.Errorf("accepted %+v", h)
		}
	}
	c.Add(Host{Label: "dev", Target: "x", Policy: Observe})
	if err := c.Add(Host{Label: "dev", Target: "y", Policy: Observe}); err == nil {
		t.Error("accepted a duplicate label")
	}
	p := filepath.Join(t.TempDir(), "remotes.toml")
	os.WriteFile(p, []byte("[hosts.a]\ntarget = \"a\"\n[hosts.a]\ntarget = \"b\"\n"), 0o644)
	if _, err := LoadFile(p); err == nil {
		t.Error("loaded a file with a duplicate label")
	}
	if _, err := LoadFile(filepath.Join(t.TempDir(), "none.toml")); err != nil {
		t.Errorf("a missing file is an empty config: %v", err)
	}
}

func TestLabelFor(t *testing.T) {
	for in, want := range map[string]string{"devbox": "devbox", "me@10.0.0.5": "10-0-0-5", "Build.Example.com": "build-example-com"} {
		if got := LabelFor(in); got != want {
			t.Errorf("LabelFor(%q) = %q", in, got)
		}
	}
}

func TestSSHCommandHasTheFixedOptions(t *testing.T) {
	s := SSH{Bin: "ssh", ControlDir: "/run/user/1000/den/ssh"}
	cmd, err := s.Command(context.Background(), "devbox", ServeCommand(Host{}))
	if err != nil {
		t.Fatal(err)
	}
	args := cmd.Args[1:]
	for _, o := range []string{"BatchMode=yes", "StrictHostKeyChecking=yes", "ConnectTimeout=10", "ServerAliveInterval=15", "ServerAliveCountMax=3", "ForwardAgent=no", "ClearAllForwardings=yes", "ControlMaster=auto", "ControlPath=/run/user/1000/den/ssh/%C"} {
		if !slices.Contains(args, o) {
			t.Errorf("missing -o %s in %v", o, args)
		}
	}
	i := slices.Index(args, "--")
	if i < 0 || args[i+1] != "devbox" || len(args) != i+3 {
		t.Errorf("target is not the one argument after --: %v", args)
	}
	if !strings.Contains(args[i+2], "exec den remote serve --proto 1") {
		t.Errorf("remote command %q", args[i+2])
	}
	for _, bad := range []string{"accept-new", "StrictHostKeyChecking=no", "/dev/null", "-F"} {
		if slices.Contains(args, bad) || strings.Contains(strings.Join(args, " "), bad) {
			t.Errorf("args hold %q", bad)
		}
	}
	if _, err := s.Command(context.Background(), "-oProxyCommand=x", "true"); err == nil {
		t.Error("a target starting with - was run")
	}
}

func TestServeCommandForms(t *testing.T) {
	for path, want := range map[string]string{
		"/opt/den bin/den": `'/opt/den bin/den' remote serve --proto 1`,
		"~/bin/den":        `~/'bin/den' remote serve --proto 1`,
	} {
		if got := ServeCommand(Host{DenPath: path}); got != want {
			t.Errorf("ServeCommand(%q) = %q", path, got)
		}
	}
}

func TestDefaultSSHHonoursTheTestHooks(t *testing.T) {
	t.Setenv("DEN_SSH", "/tmp/x/ssh-wrapper")
	t.Setenv("DEN_SCP", "/tmp/x/scp-wrapper")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	s := DefaultSSH()
	if s.Bin != "/tmp/x/ssh-wrapper" || s.SCP != "/tmp/x/scp-wrapper" || s.ControlDir == "" {
		t.Errorf("ssh = %+v", s)
	}
	st, _ := os.Stat(s.ControlDir)
	if st.Mode().Perm() != 0o700 {
		t.Errorf("control dir mode %v", st.Mode().Perm())
	}
}

func TestVersions(t *testing.T) {
	for _, c := range []struct {
		a, b  string
		older bool
	}{
		{"v0.1.0", "v0.2.0", true},
		{"v0.1.0-96-g4f1ad22", "v0.1.0-97-gabc", true},
		{"den v0.1.0-96-g4f1ad22-dirty", "v0.1.0-96-g4f1ad22", false},
		{"v0.2.0", "v0.1.9-500-gx", false},
		{"dev", "v0.2.0", false},
		{"v0.1.0", "dev", false},
		// What a host says of itself never makes the plan claim it is older.
		{"v99999999999999999999999.0.0", "v0.2.0", false},
		{"v-1.0.0", "v0.2.0", false},
		{"v0.1.0-99999999999999999999-gx", "v0.2.0", true},
		{"v0.\x1b[2J1.0", "v0.2.0", false},
	} {
		if got := Older(c.a, c.b); got != c.older {
			t.Errorf("Older(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

func TestSpawnKeys(t *testing.T) {
	p := filepath.Join(t.TempDir(), "remotes.toml")
	os.WriteFile(p, []byte(`[hosts.devbox]
target = "devbox"
policy = "full"
spawn = "auto"
spawn_dirs = ["~/src", "/srv/work api"]
orchestrator = true
max_agents = 2

[hosts.plain]
target = "plain"
`), 0o644)
	c, err := LoadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := c.Find("devbox")
	if d.Spawn != SpawnAuto || !slices.Equal(d.SpawnDirs, []string{"~/src", "/srv/work api"}) || !d.Orchestrator || d.MaxSpawned() != 2 {
		t.Errorf("devbox = %+v", d)
	}
	plain, _ := c.Find("plain")
	if plain.Spawn != "" || plain.SpawnDirs != nil || plain.Orchestrator || plain.MaxSpawned() != DefaultMaxAgents {
		t.Errorf("plain = %+v: spawn must default to off, no dirs, not the orchestrator's, 4 agents", plain)
	}
	if err := plain.SpawnAllowed(); err == nil {
		t.Error("a host without spawn keys allows spawn")
	}
	if err := d.SpawnAllowed(); err != nil {
		t.Errorf("devbox: %v", err)
	}
	d.Policy = Observe
	if err := d.SpawnAllowed(); err == nil || !strings.Contains(err.Error(), "observe") {
		t.Errorf("observe host: %v", err)
	}
	d.Policy, d.SpawnDirs = Full, []string{}
	if err := d.SpawnAllowed(); err == nil {
		t.Error("an empty spawn_dirs allows spawn")
	}
	if err := c.Save(p); err != nil {
		t.Fatal(err)
	}
	again, err := LoadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if d2, _ := again.Find("devbox"); !slices.Equal(d2.SpawnDirs, []string{"~/src", "/srv/work api"}) || d2.Spawn != SpawnAuto || !d2.Orchestrator || d2.MaxAgents != 2 {
		t.Errorf("round trip: %+v", d2)
	}
	for _, bad := range []string{
		"spawn = \"always\"", "spawn_dirs = [\"src\"]", "spawn_dirs = [\"~/a/../b\"]", "max_agents = -1", "max_agents = 100", "spawn_dirs = \"~/src\"",
	} {
		os.WriteFile(p, []byte("[hosts.x]\ntarget = \"x\"\n"+bad+"\n"), 0o644)
		if _, err := LoadFile(p); err == nil {
			t.Errorf("%s: accepted", bad)
		}
	}
}

func TestSpawnNames(t *testing.T) {
	sid := NewSessionID()
	if CheckSessionID(sid) != nil || sid[14] != '4' {
		t.Fatalf("session id %q", sid)
	}
	name := TmuxName("api", sid)
	if CheckTmuxName(name, sid) != nil || CheckTmuxName(name, NewSessionID()) == nil {
		t.Errorf("tmux name %q", name)
	}
	for _, bad := range []string{"den-a.b-12345678", "den-a:b-12345678", "den--x-12345678", "den-x-1234567", "den-x-1234567g", "den-$(x)-12345678", "x"} {
		if CheckTmuxName(bad, "") == nil {
			t.Errorf("tmux name %q accepted", bad)
		}
	}
	taken := map[string]bool{"api": true, "api-2": true}
	if p := RemotePeer("API", "/x", taken); p != "api-3" {
		t.Errorf("peer %q", p)
	}
	if p := RemotePeer("", "~/src/My.Repo", nil); p != "my-repo" {
		t.Errorf("peer from dir %q", p)
	}
	long := RemotePeer(strings.Repeat("a", 40), "", map[string]bool{strings.Repeat("a", 32): true})
	if CheckPeer(long) != nil || len(long) > 32 {
		t.Errorf("long peer %q", long)
	}
	if CheckPeer(RemotePeer("$(touch /tmp/x);'", "", nil)) != nil {
		t.Error("a hostile name was not made safe")
	}
	if CheckSpawnModel("opus", false) != nil || CheckSpawnModel("claude-opus-5-5", false) == nil || CheckSpawnModel("claude-opus-5-5", true) != nil || CheckSpawnModel("x;y", true) == nil {
		t.Error("models")
	}
	if CheckBrief("hi \x1b[31m") == nil || CheckBrief(" ") == nil || CheckBrief(strings.Repeat("a", MaxSendText+1)) == nil || CheckBrief("line\nline\ttab") != nil {
		t.Error("briefs")
	}
	roots := []string{"~/src", "/srv/work"}
	for dir, want := range map[string]bool{
		"~/src": true, "~/src/api": true, "~/src/": true, "~/srcx": false, "~/src/../etc": false, "/srv/work/a": true,
		"/srv/workx": false, "~": false, "/srv": false, "src/api": false, "~/src/a\nb": false, "~other/src": false,
	} {
		if got := UnderSpawnDirs(dir, roots); got != want {
			t.Errorf("UnderSpawnDirs(%q) = %v", dir, got)
		}
	}
	if !UnderSpawnDirs("~/x", []string{"~"}) {
		t.Error("~ allows every folder in the home")
	}
}
