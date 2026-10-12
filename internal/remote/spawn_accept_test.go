package remote_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/remote"
)

// Remote spawn end to end: Den here, a private sshd as the host, the den
// built from this tree running serve there, a real tmux on a private server
// of the host's (its own name and TMUX_TMPDIR in the scratch dir), and a
// shell stand-in for claude that registers a session file and sleeps.

const acceptClaude = `#!/bin/sh
if [ "$1" = "--version" ]; then echo "2.1.289 (Claude Code)"; exit 0; fi
log="$HOME/fakelog"; mkdir -p "$log"
printf '%s\n' "$@" > "$log/argv.$$"
sid=""; name=""
while [ $# -gt 0 ]; do case "$1" in --session-id) sid=$2; shift;; --name) name=$2; shift;; esac; shift; done
now=$(date +%s)
printf '{"pid":%d,"sessionId":"%s","cwd":"%s","startedAt":%s000,"status":"idle","statusUpdatedAt":%s000,"name":"%s","version":"2.1.289"}' $$ "$sid" "$PWD" "$now" "$now" "$name" > "$CLAUDE_CONFIG_DIR/sessions/$$.json"
exec sleep 300
`

type spawnRig struct {
	*rig
	server, tmuxDir string
}

func newSpawnRig(t *testing.T) *spawnRig {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not found")
	}
	g := &spawnRig{rig: newRig(t)}
	b := make([]byte, 4)
	rand.Read(b)
	g.server = "dt-" + hex.EncodeToString(b)
	g.tmuxDir = filepath.Join(g.r.Dir, "tm")
	os.MkdirAll(g.tmuxDir, 0o700)
	g.r.SetEnv("DEN_TMUX_SERVER", g.server)
	g.r.SetEnv("TMUX_TMPDIR", g.tmuxDir)
	os.MkdirAll(filepath.Join(g.r.Home, ".local", "bin"), 0o755)
	os.WriteFile(filepath.Join(g.r.Home, ".local", "bin", "claude"), []byte(acceptClaude), 0o755)
	os.MkdirAll(filepath.Join(g.r.Home, "src", "api"), 0o755)
	g.l.Local.WriteRemotes("[hosts.b]\ntarget = \"b\"\npolicy = \"approve\"\nspawn = \"tmux\"\nspawn_dirs = [\"~/src\"]\n")
	t.Cleanup(func() { g.tmux("kill-server") })
	return g
}

// tmux runs tmux against the host's private server, as the host's user.
func (g *spawnRig) tmux(args ...string) (string, error) {
	c := exec.Command("tmux", append([]string{"-L", g.server}, args...)...)
	c.Env = append(g.r.Env(), "TMUX_TMPDIR="+g.tmuxDir, "TMUX=")
	out, err := c.CombinedOutput()
	return string(out), err
}

func TestSpawnOverSSHInTmuxThenAttach(t *testing.T) {
	g := newSpawnRig(t)
	g.start()
	g.live()
	g.wait("spawn capability", 10*time.Second, func() bool { c := g.status().SpawnCap; return c != nil && c.Tmux != "" })
	marker := "SECRET-BRIEF-" + remote.NewSessionID()[:8]
	ctx := context.Background()
	chk, err := g.m.Spawn(ctx, "b", remote.SpawnRequest{Dir: "~/src/api", Peer: "api", Check: true})
	if err != nil || chk.Dir != filepath.Join(g.r.Home, "src", "api") {
		t.Fatalf("check = %+v %v", chk, err)
	}
	res, err := g.m.Spawn(ctx, "b", remote.SpawnRequest{SessionID: chk.SessionID, Peer: chk.Peer, Dir: "~/src/api", Brief: marker + " build it"})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	tag, err := g.tmux("show-options", "-v", "-t", "="+res.TmuxName+":", "@den_spawn")
	if err != nil || strings.TrimSpace(tag) != res.SessionID {
		t.Fatalf("@den_spawn = %q %v", tag, err)
	}
	g.wait("the agent's row", 5*time.Second, func() bool {
		for _, a := range g.m.Agents() {
			if a.Session.SessionID == res.SessionID && a.Spawn == "" && a.Tmux == res.TmuxName {
				return true
			}
		}
		return false
	})
	// The brief is in a file only: no argument list, tmux or log has it.
	cmdlines, _ := filepath.Glob("/proc/[0-9]*/cmdline")
	for _, p := range cmdlines {
		if b, _ := os.ReadFile(p); strings.Contains(string(b), marker) {
			t.Errorf("%s holds the brief", p)
		}
	}
	starts, _ := g.tmux("list-sessions", "-F", "#{pane_start_command}")
	senv, _ := g.tmux("show-environment", "-t", "="+res.TmuxName)
	if strings.Contains(starts+senv, marker) {
		t.Error("tmux holds the brief")
	}
	if strings.Contains(senv, "SSH_CONNECTION=") {
		t.Errorf("ssh's environment reached the agent's session:\n%s", senv)
	}
	if logs, _ := os.ReadFile(filepath.Join(g.r.StateHome, "den", "remote-serve.log")); strings.Contains(string(logs), marker) {
		t.Error("serve's log holds the brief")
	}
	briefs, _ := filepath.Glob(filepath.Join(g.r.RuntimeDir, "den", "spawn", res.SessionID, "brief.md"))
	if len(briefs) != 1 {
		t.Fatalf("brief.md: %v", briefs)
	}

	// The attach window: ssh -t into den remote attach, in a terminal (a
	// private local tmux stands in for it).
	argv, err := g.m.AttachCommand("b", res.TmuxName)
	if err != nil {
		t.Fatal(err)
	}
	if argv[0] != g.l.SSH || !strings.HasSuffix(argv[len(argv)-1], "remote attach "+res.TmuxName) {
		t.Errorf("attach argv = %q", argv)
	}
	tm := g.l.Local.StartTUI(120, 40, nil, argv...)
	g.wait("an attached client", 15*time.Second, func() bool {
		out, _ := g.tmux("list-clients", "-t", "="+res.TmuxName)
		return strings.TrimSpace(out) != ""
	})
	// Closing the window only detaches.
	kill := exec.Command("tmux", "-L", g.l.Local.TmuxSocket, "kill-server")
	kill.Env = append(g.l.Local.Env(), "TMUX_TMPDIR="+filepath.Join(g.l.Dir, "tm"), "TMUX=")
	kill.Run()
	_ = tm
	g.wait("the client gone", 10*time.Second, func() bool {
		out, _ := g.tmux("list-clients", "-t", "="+res.TmuxName)
		return strings.TrimSpace(out) == ""
	})
	if _, err := g.tmux("has-session", "-t", "="+res.TmuxName); err != nil {
		t.Error("closing the window ended the agent")
	}
}

func TestAttachRefusesSessionsDenDidNotStart(t *testing.T) {
	g := newSpawnRig(t)
	if out, err := g.tmux("new-session", "-d", "-s", "den-mine-12345678", "sleep", "300"); err != nil {
		t.Fatalf("tmux: %v %s", err, out)
	}
	for _, name := range []string{"den-mine-12345678", "den-gone-12345678", "den-x;id-12345678"} {
		r := g.r.RunStdin(g.r.DenPath+" remote attach '"+name+"'", "\n")
		if r.Code == 0 {
			t.Errorf("%s: attached: %+v", name, r)
		}
	}
	r := g.r.RunStdin(g.r.DenPath+" remote attach den-mine-12345678", "\n")
	if !strings.Contains(r.Stderr, "not started by Den") {
		t.Errorf("untagged: %+v", r)
	}
	r = g.r.RunStdin(g.r.DenPath+" remote attach den-gone-12345678", "\n")
	if !strings.Contains(r.Stderr, "has ended") {
		t.Errorf("gone: %+v", r)
	}
}
