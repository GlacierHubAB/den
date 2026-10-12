package remote

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/sshtest"
)

// Remote spawn on the host side over io.Pipe: serve runs in this process,
// tmux is real but on a private server (its own name and TMUX_TMPDIR in a
// scratch dir), the den tmux starts is the one built from this tree, and
// claude is a shell stand-in that registers a session file and sleeps.

// spawnHost is one test's host: scratch home, runtime dir, tmux server.
type spawnHost struct {
	*scratch
	home, run, tmuxDir, server, log, bin, den string
}

const fakeClaude = `#!/bin/sh
if [ "$1" = "--version" ]; then echo "${FAKE_CLAUDE_VERSION:-2.1.289} (Claude Code)"; exit 0; fi
log="$HOME/fakelog"; mkdir -p "$log"
printf '%s\n' "$@" > "$log/argv.$$"
env > "$log/env.$$"
pwd > "$log/cwd.$$"
[ -e "$HOME/noregister" ] && exec sleep 300
sid=""; name=""
while [ $# -gt 0 ]; do case "$1" in --session-id) sid=$2; shift;; --name) name=$2; shift;; esac; shift; done
now=$(date +%s)
printf '{"pid":%d,"sessionId":"%s","cwd":"%s","startedAt":%s000,"status":"idle","statusUpdatedAt":%s000,"name":"%s","version":"2.1.289"}' $$ "$sid" "$PWD" "$now" "$now" "$name" > "$CLAUDE_CONFIG_DIR/sessions/$$.json"
if [ -e "$HOME/loggedout" ]; then
  d="$CLAUDE_CONFIG_DIR/projects/x"; mkdir -p "$d"
  printf '{"type":"user","message":{"role":"user","content":"hi"}}\n{"type":"assistant","isApiErrorMessage":true,"error":"authentication_failed","message":{"content":[{"type":"text","text":"Not logged in"}]}}\n' > "$d/$sid.jsonl"
fi
exec sleep 300
`

func newSpawnHost(t *testing.T) *spawnHost {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not found")
	}
	h := &spawnHost{scratch: newScratch(t)}
	root := shortDir(t)
	h.home = filepath.Join(root, "h")
	h.run = filepath.Join(root, "r")
	h.tmuxDir = filepath.Join(root, "t")
	h.bin = filepath.Join(root, "b")
	for _, d := range []string{h.home, h.run, h.tmuxDir, h.bin} {
		os.MkdirAll(d, 0o700)
	}
	b := make([]byte, 4)
	rand.Read(b)
	h.server = "dt-" + hex.EncodeToString(b)
	h.log = filepath.Join(h.home, "fakelog")
	os.WriteFile(filepath.Join(h.bin, "claude"), []byte(fakeClaude), 0o755)
	t.Setenv("HOME", h.home)
	t.Setenv("XDG_RUNTIME_DIR", h.run)
	t.Setenv("TMUX_TMPDIR", h.tmuxDir)
	t.Setenv("TMUX", "")
	t.Setenv("DEN_TMUX_SERVER", h.server)
	t.Setenv("PATH", h.bin+":"+os.Getenv("PATH"))
	h.den = sshtest.BuildDen(t)
	t.Cleanup(func() { h.tmux("kill-server") })
	return h
}

func (h *spawnHost) tmux(args ...string) (string, error) {
	cmd := exec.Command("tmux", append([]string{"-L", h.server}, args...)...)
	cmd.Env = append(os.Environ(), "TMUX=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// project makes a folder under the home and returns its ~/ name.
func (h *spawnHost) project(name string) string {
	os.MkdirAll(filepath.Join(h.home, "src", name), 0o755)
	return "~/src/" + name
}

func (h *spawnHost) serve(t *testing.T, policy Policy, sp *SpawnPolicy, mod func(*ServeOptions)) (*served, Hello) {
	t.Helper()
	return serveDirect(t, h.scratch, Request{Policy: policy, Labels: true, Spawn: sp, Controller: "ctl-test"}, func(o *ServeOptions) {
		o.Owner = &OwnerSpawn{Spawn: true}
		o.Executable = h.den
		o.SpawnWait = 10 * time.Second
		if mod != nil {
			mod(o)
		}
	})
}

func spawnPolicy(dirs ...string) *SpawnPolicy {
	if len(dirs) == 0 {
		dirs = []string{"~/src"}
	}
	return &SpawnPolicy{Route: SpawnTmux, Dirs: dirs, Max: 4}
}

func spawnCmd(id, peer, dir, brief string) SpawnCmd {
	sid := NewSessionID()
	return SpawnCmd{Type: TypeSpawn, ID: id, SessionID: sid, Peer: peer, TmuxName: TmuxName(peer, sid), Dir: dir, Route: "tmux", Brief: brief}
}

func (sv *served) spawn(c SpawnCmd) Result {
	sv.t.Helper()
	writeMsg(sv.in, c)
	var r Result
	deadline := time.After(20 * time.Second)
	for {
		select {
		case line, ok := <-sv.msgs:
			if !ok {
				sv.t.Fatalf("serve ended waiting for the spawn's result")
			}
			if typ, _, _ := peek(line); typ == TypeResult {
				json.Unmarshal(line, &r)
				if r.ID != c.ID {
					sv.t.Fatalf("result for %q, want %q", r.ID, c.ID)
				}
				return r
			}
		case <-deadline:
			sv.t.Fatalf("no result for spawn %s", c.ID)
		}
	}
}

// secretBrief carries a marker unique to this run, so a grep of every
// process on the machine finds only this test's.
var secretMarker = "SECRET-BRIEF-" + NewSessionID()[:8]

var secretBrief = secretMarker + " fix the login; $(touch /tmp/den-spawn-pwned) `id`; 'quote' \"dq\"\nline two"

func TestSpawnStartsClaudeInTaggedTmuxWithTheBriefOnlyInAFile(t *testing.T) {
	h := newSpawnHost(t)
	dir := h.project("api")
	sv, hello := h.serve(t, Approve, spawnPolicy(), nil)
	if hello.Spawn == nil || hello.Spawn.Tmux == "" || hello.Spawn.Claude != "2.1.289" || hello.Spawn.Window != h.den {
		t.Fatalf("hello spawn = %+v", hello.Spawn)
	}
	c := spawnCmd("s1", "api", dir, secretBrief)
	if !strings.Contains(secretBrief, secretMarker) {
		t.Fatal("marker")
	}
	c.Model = "sonnet"
	r := sv.spawn(c)
	if !r.OK || r.Route != "tmux" || r.TmuxName != c.TmuxName || r.Dir != filepath.Join(h.home, "src", "api") {
		t.Fatalf("result = %+v", r)
	}
	if !r.Untrusted || r.Warning == "" {
		t.Errorf("an untrusted folder must be said: %+v", r)
	}
	tag, err := h.tmux("show-options", "-v", "-t", "="+c.TmuxName+":", "@den_spawn")
	if err != nil || strings.TrimSpace(tag) != c.SessionID {
		t.Fatalf("@den_spawn = %q %v", tag, err)
	}
	argv := h.fakeFile(t, "argv")
	stage := filepath.Join(h.run, "den", "spawn", c.SessionID)
	want := []string{"--session-id", c.SessionID, "--name", "api", "--model", "sonnet", "--add-dir", stage, "--",
		"Your brief from Den is in " + filepath.Join(stage, "brief.md") + ". Read it and follow it."}
	if got := strings.Split(strings.TrimSpace(argv), "\n"); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("claude argv\n got %q\nwant %q", got, want)
	}
	if b, err := os.ReadFile(filepath.Join(stage, "brief.md")); err != nil || string(b) != secretBrief {
		t.Errorf("brief.md = %q %v", b, err)
	}
	for _, p := range []string{filepath.Join(stage, "brief.md"), stage} {
		st, _ := os.Stat(p)
		if st.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s mode %v", p, st.Mode())
		}
	}
	if _, err := os.Stat(filepath.Join(stage, "spec.json")); err == nil {
		t.Error("spec.json was not claimed")
	}
	if cwd := strings.TrimSpace(h.fakeFile(t, "cwd")); cwd != filepath.Join(h.home, "src", "api") {
		t.Errorf("claude ran in %q", cwd)
	}
	env := h.fakeFile(t, "env")
	for _, bad := range []string{secretMarker, "DEN_SPAWN_DIR=", "SSH_CONNECTION", "CLAUDECODE="} {
		if strings.Contains(env, bad) {
			t.Errorf("claude's environment holds %q", bad)
		}
	}
	if !strings.Contains(env, "DEN_PEER=api") || !strings.Contains(env, "DEN_SPAWN="+c.SessionID) {
		t.Errorf("claude's environment lacks Den's markers:\n%s", env)
	}
	// Nothing of the brief in tmux's view of the session or in any argv.
	out, _ := h.tmux("list-sessions", "-F", "#{session_name} #{pane_start_command}")
	senv, _ := h.tmux("show-environment", "-t", "="+c.TmuxName)
	genv, _ := h.tmux("show-environment", "-g")
	for what, s := range map[string]string{"pane_start_command": out, "session env": senv, "server env": genv, "argv": argv} {
		if strings.Contains(s, secretMarker) {
			t.Errorf("the brief shows in %s", what)
		}
	}
	if strings.Contains(genv, "SSH_") || strings.Contains(senv, "SSH_CONNECTION=") {
		t.Errorf("ssh's environment leaked into the tmux server:\n%s", genv)
	}
	cmdlines, _ := filepath.Glob("/proc/[0-9]*/cmdline")
	for _, p := range cmdlines {
		if b, _ := os.ReadFile(p); strings.Contains(string(b), secretMarker) {
			t.Errorf("%s holds the brief", p)
		}
	}
	if _, err := os.Stat("/tmp/den-spawn-pwned"); err == nil {
		t.Error("a shell ran the brief")
	}
	if sv.log.has("SECRET") {
		t.Error("serve's log holds the brief")
	}
	ledger, _ := os.ReadFile(filepath.Join(filepath.Dir(h.remoteEvts), "remote-spawns.json"))
	if strings.Contains(string(ledger), "SECRET") || !strings.Contains(string(ledger), c.SessionID) {
		t.Errorf("ledger = %s", ledger)
	}
	if st, _ := os.Stat(filepath.Join(filepath.Dir(h.remoteEvts), "remote-spawns.json")); st.Mode().Perm() != 0o600 {
		t.Errorf("ledger mode %v", st.Mode())
	}
}

// fakeFile is the one file the claude stand-in wrote of kind.
func (h *spawnHost) fakeFile(t *testing.T, kind string) string {
	t.Helper()
	var files []string
	sshtest.WaitFor(5*time.Second, func() bool {
		files, _ = filepath.Glob(filepath.Join(h.log, kind+".*"))
		return len(files) > 0
	})
	if len(files) != 1 {
		t.Fatalf("claude ran %d times", len(files))
	}
	b, _ := os.ReadFile(files[0])
	return string(b)
}

func TestSpawnCheckResolvesAndStartsNothing(t *testing.T) {
	h := newSpawnHost(t)
	dir := h.project("web")
	sv, _ := h.serve(t, Full, spawnPolicy(), nil)
	c := spawnCmd("c1", "web", dir, "")
	c.Check = true
	r := sv.spawn(c)
	if !r.OK || r.Dir != filepath.Join(h.home, "src", "web") || r.Route != "tmux" {
		t.Fatalf("check = %+v", r)
	}
	if out, err := h.tmux("list-sessions"); err == nil && strings.TrimSpace(out) != "" {
		t.Errorf("a check started tmux: %s", out)
	}
	if ents, _ := os.ReadDir(filepath.Join(h.run, "den", "spawn")); len(ents) != 0 {
		t.Errorf("a check staged something: %v", ents)
	}
}

func TestSpawnGuardsOnServe(t *testing.T) {
	h := newSpawnHost(t)
	dir := h.project("api")
	outside := filepath.Join(h.home, "secret")
	os.MkdirAll(outside, 0o755)
	os.Symlink(outside, filepath.Join(h.home, "src", "escape"))
	os.WriteFile(filepath.Join(h.home, "src", "file"), nil, 0o644)

	refuse := func(name string, policy Policy, sp *SpawnPolicy, mod func(*ServeOptions), c SpawnCmd, want string) {
		t.Helper()
		sv, _ := h.serve(t, policy, sp, mod)
		r := sv.spawn(c)
		if r.OK || !strings.Contains(r.Error, want) {
			t.Errorf("%s: %+v, want an error with %q", name, r, want)
		}
		sv.end()
	}
	ok := func() SpawnCmd { return spawnCmd("g", "api", dir, "do it") }
	refuse("observe", Observe, spawnPolicy(), nil, ok(), "observe")
	refuse("no spawn from Den", Approve, nil, nil, ok(), "allows no spawn")
	refuse("owner veto", Approve, spawnPolicy(), func(o *ServeOptions) { o.Owner = &OwnerSpawn{Spawn: false} }, ok(), "own config refuses")
	refuse("owner dirs", Approve, spawnPolicy(), func(o *ServeOptions) { o.Owner = &OwnerSpawn{Spawn: true, Dirs: []string{"~/elsewhere"}} }, ok(), "remote.spawn_dirs")
	refuse("terminal route", Approve, &SpawnPolicy{Route: SpawnTerminal, Dirs: []string{"~/src"}}, nil, ok(), "tmux route")
	refuse("empty dirs", Approve, spawnPolicy("~/nothing"), nil, ok(), "spawn_dirs")
	bad := func(f func(*SpawnCmd)) SpawnCmd { c := ok(); f(&c); return c }
	for name, c := range map[string]SpawnCmd{
		"dotdot":      bad(func(c *SpawnCmd) { c.Dir = "~/src/../secret" }),
		"symlink out": bad(func(c *SpawnCmd) { c.Dir = "~/src/escape" }),
		"a file":      bad(func(c *SpawnCmd) { c.Dir = "~/src/file" }),
		"relative":    bad(func(c *SpawnCmd) { c.Dir = "src/api" }),
		"~other":      bad(func(c *SpawnCmd) { c.Dir = "~root/src" }),
		"newline dir": bad(func(c *SpawnCmd) { c.Dir = "~/src/a\nb" }),
		"peer shell":  bad(func(c *SpawnCmd) { c.Peer = "a;b"; c.TmuxName = "den-a;b-" + c.SessionID[:8] }),
		"peer dot":    bad(func(c *SpawnCmd) { c.Peer = "a.b"; c.TmuxName = TmuxName("a.b", c.SessionID) }),
		"tmux colon":  bad(func(c *SpawnCmd) { c.TmuxName = "den-api:1-" + c.SessionID[:8] }),
		"tmux other":  bad(func(c *SpawnCmd) { c.TmuxName = TmuxName("web", c.SessionID) }),
		"uuid":        bad(func(c *SpawnCmd) { c.SessionID = "$(touch /tmp/x)" }),
		"model":       bad(func(c *SpawnCmd) { c.Model = "gpt" }),
		"worktree":    bad(func(c *SpawnCmd) { c.Worktree = "../x" }),
		"escape":      bad(func(c *SpawnCmd) { c.Brief = "hi \x1b]52;c;aGk=\x07" }),
		"big brief":   bad(func(c *SpawnCmd) { c.Brief = strings.Repeat("a", MaxSendText+1) }),
		"skills":      bad(func(c *SpawnCmd) { c.Skills = json.RawMessage(`[{"name":"x"}]`) }),
		"resume":      bad(func(c *SpawnCmd) { c.Resume = true }),
	} {
		refuse(name, Approve, spawnPolicy(), nil, c, "")
	}
	if out, err := h.tmux("list-sessions"); err == nil && strings.TrimSpace(out) != "" {
		t.Errorf("a refused spawn started tmux: %s", out)
	}
	if _, err := os.Stat("/tmp/x"); err == nil {
		t.Error("a shell ran a name")
	}
}

func TestSpawnMaxAgentsAndCollision(t *testing.T) {
	h := newSpawnHost(t)
	dir := h.project("api")
	sv, _ := h.serve(t, Approve, &SpawnPolicy{Route: SpawnAuto, Dirs: []string{"~/src"}, Max: 1}, nil)
	first := spawnCmd("m1", "api", dir, "one")
	if r := sv.spawn(first); !r.OK {
		t.Fatalf("first spawn: %+v", r)
	}
	if r := sv.spawn(spawnCmd("m2", "api-2", dir, "two")); r.OK || !strings.Contains(r.Error, "max_agents") || !strings.Contains(r.Error, first.TmuxName) {
		t.Errorf("max_agents: %+v", r)
	}
	// A session of that name made by someone else: refused, and left alone.
	c := spawnCmd("d1", "web", dir, "three")
	h.tmux("new-session", "-d", "-s", c.TmuxName, "sleep", "300")
	sv2, _ := h.serve(t, Approve, spawnPolicy(), nil)
	if r := sv2.spawn(c); r.OK || r.Code != "duplicate" {
		t.Errorf("collision: %+v", r)
	}
	if _, err := h.tmux("has-session", "-t", "="+c.TmuxName); err != nil {
		t.Error("the existing session was touched")
	}
	if tag, err := h.tmux("show-options", "-v", "-t", "="+c.TmuxName+":", "@den_spawn"); err == nil && strings.TrimSpace(tag) != "" {
		t.Errorf("the existing session was tagged: %q", tag)
	}
}

func TestSpawnLeftoverIsListedAndCleanedOnlyWhenTagged(t *testing.T) {
	h := newSpawnHost(t)
	dir := h.project("api")
	os.WriteFile(filepath.Join(h.home, "noregister"), nil, 0o644) // claude hangs before its session file, as at a login screen
	sv, _ := h.serve(t, Full, spawnPolicy(), func(o *ServeOptions) { o.SpawnWait, o.SnapshotEvery = 2*time.Second, 300*time.Millisecond })
	c := spawnCmd("l1", "api", dir, "never registers")
	if r := sv.spawn(c); r.OK || r.Code != "no_session" {
		t.Fatalf("result = %+v", r)
	}
	h.tmux("new-session", "-d", "-s", "den-mine-12345678", "sleep", "300") // the human's own, untagged
	var snap Snapshot
	sshtest.WaitFor(20*time.Second, func() bool {
		json.Unmarshal(sv.next(TypeSnapshot), &snap)
		return len(snap.Spawned) > 0
	})
	if len(snap.Spawned) != 1 || snap.Spawned[0].TmuxName != c.TmuxName || snap.Spawned[0].Live {
		t.Fatalf("spawned = %+v", snap.Spawned)
	}
	writeMsg(sv.in, CleanupCmd{Type: TypeCleanup, ID: "x1", SessionID: "11111111-2222-4333-8444-555555555555", TmuxName: "den-mine-12345678"})
	if r := sv.result("x1"); r.OK {
		t.Errorf("cleaned an untagged session: %+v", r)
	}
	writeMsg(sv.in, CleanupCmd{Type: TypeCleanup, ID: "x2", SessionID: c.SessionID, TmuxName: c.TmuxName})
	if r := sv.result("x2"); !r.OK {
		t.Errorf("cleanup: %+v", r)
	}
	if _, err := h.tmux("has-session", "-t", "="+c.TmuxName); err == nil {
		t.Error("the leftover is still there")
	}
	if _, err := h.tmux("has-session", "-t", "=den-mine-12345678"); err != nil {
		t.Error("the human's own session was touched")
	}
	if _, err := os.Stat(filepath.Join(h.run, "den", "spawn", c.SessionID)); err == nil {
		t.Error("the leftover's brief stayed")
	}
}

func TestSpawnBriefIsRemovedAfterTheFirstTurnAndReported(t *testing.T) {
	h := newSpawnHost(t)
	dir := h.project("api")
	sv, _ := h.serve(t, Approve, spawnPolicy(), nil)
	c := spawnCmd("b1", "api", dir, "the brief")
	if r := sv.spawn(c); !r.OK {
		t.Fatalf("spawn: %+v", r)
	}
	brief := filepath.Join(h.run, "den", "spawn", c.SessionID, "brief.md")
	files, _ := filepath.Glob(filepath.Join(h.cfg, "sessions", "*.json"))
	if len(files) != 1 {
		t.Fatalf("session files %v", files)
	}
	set := func(status string) {
		raw, _ := os.ReadFile(files[0])
		var m map[string]any
		json.Unmarshal(raw, &m)
		m["status"] = status
		raw, _ = json.Marshal(m)
		os.WriteFile(files[0], raw, 0o600)
	}
	ledger := filepath.Join(filepath.Dir(h.remoteEvts), "remote-spawns.json")
	set("busy")
	if !sshtest.WaitFor(10*time.Second, func() bool {
		sv.drain()
		b, _ := os.ReadFile(ledger)
		return strings.Contains(string(b), `"busy":true`)
	}) {
		t.Fatal("serve did not see the first turn start")
	}
	if _, err := os.Stat(brief); err != nil {
		t.Fatal("the brief went before the first turn ended")
	}
	set("idle")
	if !sshtest.WaitFor(10*time.Second, func() bool { sv.drain(); _, err := os.Stat(brief); return err != nil }) {
		t.Fatal("the brief stayed after the first turn")
	}
	sv.end()
	// A later serve of the same Den reports the spawn: started, brief read.
	_, hello := h.serve(t, Approve, spawnPolicy(), nil)
	var got *SpawnState
	for i := range hello.Spawns {
		if hello.Spawns[i].SessionID == c.SessionID {
			got = &hello.Spawns[i]
		}
	}
	if got == nil || !got.Started || !got.Delivered || got.TmuxName != c.TmuxName {
		t.Fatalf("spawns = %+v", hello.Spawns)
	}
	_, other := serveDirect(t, h.scratch, Request{Policy: Approve, Spawn: spawnPolicy(), Controller: "another"}, func(o *ServeOptions) {
		o.Owner, o.Executable = &OwnerSpawn{Spawn: true}, h.den
	})
	if len(other.Spawns) != 0 {
		t.Errorf("another Den learns of this one's spawns: %+v", other.Spawns)
	}
}

func TestOldDenSeesNoSpawnOnTheWire(t *testing.T) {
	h := newSpawnHost(t)
	sv, hello := serveDirect(t, h.scratch, Request{Policy: Full, Labels: true}, func(o *ServeOptions) { o.Owner = &OwnerSpawn{Spawn: true} })
	if hello.Spawn != nil || hello.Spawns != nil {
		t.Errorf("hello carries spawn to a Den that did not ask: %+v", hello)
	}
	raw := sv.next(TypeSnapshot)
	if strings.Contains(string(raw), "spawned") {
		t.Errorf("snapshot carries spawned: %s", raw)
	}
}

// drain reads whatever serve has sent, without waiting.
func (sv *served) drain() {
	for {
		select {
		case <-sv.msgs:
		default:
			return
		}
	}
}

// spawnManaged is a Manager attached to the in-process host, its spawn
// notices collected.
type spawnManaged struct {
	m       *Manager
	d       *pipeDialer
	mu      sync.Mutex
	notices []string
}

func (h *spawnHost) manage(t *testing.T, host Host, mod func(*ServeOptions)) *spawnManaged {
	t.Helper()
	d := &pipeDialer{opts: func() ServeOptions {
		o := h.serveOptions()
		o.Log = func(string) {}
		no := false
		o.Questions = &no
		o.Owner = &OwnerSpawn{Spawn: true}
		o.Executable = h.den
		o.SpawnWait = 10 * time.Second
		if mod != nil {
			mod(&o)
		}
		return o
	}}
	ssh := SSH{Bin: "ssh"}
	m := NewManager(Config{Hosts: []Host{host}}, Options{
		Version: "v0.2.0", StateDir: h.stateDir, EventsPath: h.localEvts, Dial: d.dial, Controller: "ctl-local",
		Backoff: 20 * time.Millisecond, MaxBackoff: 100 * time.Millisecond, Healthy: time.Second, SSH: &ssh, SpawnWait: 20 * time.Second,
	})
	g := &spawnManaged{m: m, d: d}
	go func() {
		for u := range m.Updates() {
			if u.Kind == SpawnNotice {
				g.mu.Lock()
				g.notices = append(g.notices, u.Note)
				g.mu.Unlock()
			}
		}
	}()
	m.Start()
	t.Cleanup(m.Close)
	if !sshtest.WaitFor(10*time.Second, func() bool { hs := m.Hosts(); return hs[0].State == Live && hs[0].SpawnCap != nil }) {
		t.Fatalf("host = %+v", m.Hosts())
	}
	return g
}

func spawnHostEntry(mod func(*Host)) Host {
	h := Host{Label: "b", Target: "b", Policy: Approve, Labels: true, Enabled: true, Spawn: SpawnTmux, SpawnDirs: []string{"~/src"}}
	if mod != nil {
		mod(&h)
	}
	return h
}

func TestManagerSpawnChecksThenStartsAndTheRowAppears(t *testing.T) {
	h := newSpawnHost(t)
	dir := h.project("api")
	g := h.manage(t, spawnHostEntry(nil), nil)
	ctx := context.Background()
	chk, err := g.m.Spawn(ctx, "b", SpawnRequest{Dir: dir, Peer: "API Worker", Check: true})
	if err != nil || chk.Peer != "api-worker" || chk.Dir != filepath.Join(h.home, "src", "api") || chk.Critter == "" || chk.Route != "tmux" || !chk.Untrusted {
		t.Fatalf("check = %+v, %v", chk, err)
	}
	if out, err := h.tmux("list-sessions"); err == nil && strings.TrimSpace(out) != "" {
		t.Fatalf("a check started tmux: %s", out)
	}
	res, err := g.m.Spawn(ctx, "b", SpawnRequest{SessionID: chk.SessionID, Peer: chk.Peer, Dir: dir, Brief: "Fix it.", Model: "haiku"})
	if err != nil || res.SessionID != chk.SessionID || res.TmuxName != TmuxName("api-worker", chk.SessionID) {
		t.Fatalf("spawn = %+v, %v", res, err)
	}
	var row fleet.Agent
	if !sshtest.WaitFor(10*time.Second, func() bool {
		for _, a := range g.m.Agents() {
			if a.Session.SessionID == res.SessionID && a.Spawn == "" && a.Tmux == res.TmuxName {
				row = a
				return true
			}
		}
		return false
	}) {
		t.Fatalf("no row for the agent: %+v", g.m.Agents())
	}
	if row.Name != res.Critter || row.Host != "b" {
		t.Errorf("row = %+v", row)
	}
	argv, err := g.m.AttachCommand("b", res.TmuxName)
	want := "'" + h.den + "' remote attach " + res.TmuxName
	if err != nil || argv[0] != "ssh" || argv[len(argv)-1] != want || argv[len(argv)-2] != "b" || argv[len(argv)-3] != "--" || argv[len(argv)-4] != "-t" ||
		!slices.Contains(argv, "BatchMode=yes") || !slices.Contains(argv, "StrictHostKeyChecking=yes") {
		t.Errorf("attach = %q %v", argv, err)
	}
	if _, err := g.m.AttachCommand("b", "den-x;rm-12345678"); err == nil {
		t.Error("attach took a bad name")
	}
}

func TestManagerSpawnRefusesLocally(t *testing.T) {
	h := newSpawnHost(t)
	dir := h.project("api")
	g := h.manage(t, spawnHostEntry(func(x *Host) { x.MaxAgents = 1 }), nil)
	ctx := context.Background()
	for name, req := range map[string]SpawnRequest{
		"orchestrator": {Dir: dir, Brief: "x", Orchestrator: true},
		"outside":      {Dir: "~/elsewhere", Brief: "x"},
		"dotdot":       {Dir: "~/src/../x", Brief: "x"},
		"brief":        {Dir: dir, Brief: "\x1b[2J"},
		"model":        {Dir: dir, Brief: "x", Model: "gpt"},
		"terminal":     {Dir: dir, Brief: "x", Route: "terminal"},
	} {
		if _, err := g.m.Spawn(ctx, "b", req); err == nil {
			t.Errorf("%s: started", name)
		} else if name == "orchestrator" && err.Error() != ProtectedNote("b") {
			t.Errorf("protected: %v", err)
		}
	}
	if _, err := g.m.Spawn(ctx, "b", SpawnRequest{Dir: dir, Brief: "one"}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.m.Spawn(ctx, "b", SpawnRequest{Dir: dir, Brief: "two"}); err == nil || !strings.Contains(err.Error(), "max_agents") {
		t.Errorf("max_agents: %v", err)
	}
	if out, _ := h.tmux("list-sessions", "-F", "#{session_name}"); len(strings.Fields(out)) != 1 {
		t.Errorf("sessions: %q", out)
	}
}

func TestManagerSpawnOnAnOldHostSaysUpdate(t *testing.T) {
	h := newSpawnHost(t)
	g := &spawnManaged{}
	d := &pipeDialer{opts: h.serveOptions}
	d.setServe(func(ctx context.Context, in io.Reader, out io.Writer) error {
		// A den from before spawn: it ignores the request's spawn key.
		lr := newLineReader(in, MaxLine)
		lr.next()
		writeMsg(out, Hello{Type: TypeHello, Proto: Proto, Den: "v0.3.0", Policy: Approve, Labels: true})
		writeMsg(out, Snapshot{Type: TypeSnapshot, Agents: []fleet.Meta{}})
		<-ctx.Done()
		return nil
	})
	g.m = NewManager(Config{Hosts: []Host{spawnHostEntry(nil)}}, Options{StateDir: h.stateDir, EventsPath: h.localEvts, Dial: d.dial, Controller: "c"})
	go func() {
		for range g.m.Updates() {
		}
	}()
	g.m.Start()
	t.Cleanup(g.m.Close)
	sshtest.WaitFor(5*time.Second, func() bool { return g.m.Hosts()[0].State == Live })
	_, err := g.m.Spawn(context.Background(), "b", SpawnRequest{Dir: "~/src", Brief: "x"})
	if err == nil || !strings.Contains(err.Error(), "update it") {
		t.Errorf("old host: %v", err)
	}
}

func TestManagerSpawnDropMidSpawnIsNeverRepeated(t *testing.T) {
	h := newSpawnHost(t)
	dir := h.project("api")
	g := h.manage(t, spawnHostEntry(nil), func(o *ServeOptions) { o.SnapshotEvery = 300 * time.Millisecond })
	// The link dies right after the spawn goes out, before its result.
	var once sync.Once
	g.d.mu.Lock()
	kills := g.d.kills
	g.d.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		_, err := g.m.Spawn(context.Background(), "b", SpawnRequest{Dir: dir, Brief: "once only"})
		done <- err
	}()
	sshtest.WaitFor(5*time.Second, func() bool {
		out, _ := h.tmux("list-sessions", "-F", "#{session_name}")
		if strings.TrimSpace(out) != "" {
			once.Do(func() { kills[len(kills)-1]() })
			return true
		}
		return false
	})
	if err := <-done; err == nil || !strings.Contains(err.Error(), "not known") {
		t.Fatalf("spawn across a drop: %v", err)
	}
	var row fleet.Agent
	sshtest.WaitFor(15*time.Second, func() bool {
		for _, a := range g.m.Agents() {
			if a.Host == "b" && a.Spawn == "" && a.Tmux != "" {
				row = a
				return true
			}
		}
		return false
	})
	if row.Tmux == "" {
		t.Fatalf("after the reconnect the agent was not found: %+v %q", g.m.Agents(), g.notices)
	}
	if n := len(h.claudeRuns(t)); n != 1 {
		t.Errorf("claude ran %d times", n)
	}
	if out, _ := h.tmux("list-sessions", "-F", "#{session_name}"); len(strings.Fields(out)) != 1 {
		t.Errorf("sessions after the drop: %q", out)
	}
}

func TestManagerSpawnNeverStartedIsSaidOnce(t *testing.T) {
	h := newSpawnHost(t)
	g := h.manage(t, spawnHostEntry(nil), nil)
	sid := NewSessionID()
	g.m.setPending("b", SpawnCmd{SessionID: sid, Peer: "api", TmuxName: TmuxName("api", sid)}, "unknown")
	g.m.Reconnect("b")
	g.d.mu.Lock()
	g.d.kills[len(g.d.kills)-1]()
	g.d.mu.Unlock()
	if !sshtest.WaitFor(10*time.Second, func() bool { g.mu.Lock(); defer g.mu.Unlock(); return len(g.notices) == 1 }) {
		t.Fatalf("notices = %q", g.notices)
	}
	if !strings.Contains(g.notices[0], "was not started") {
		t.Errorf("notice = %q", g.notices[0])
	}
	for _, a := range g.m.Agents() {
		if a.Session.SessionID == sid {
			t.Errorf("the row stayed: %+v", a)
		}
	}
	if out, err := h.tmux("list-sessions"); err == nil && strings.TrimSpace(out) != "" {
		t.Errorf("a spawn was repeated: %s", out)
	}
}

// claudeRuns are the claude stand-in's runs so far.
func (h *spawnHost) claudeRuns(t *testing.T) []string {
	files, _ := filepath.Glob(filepath.Join(h.log, "argv.*"))
	return files
}

func TestSpawnSaysWhenClaudeIsNotLoggedIn(t *testing.T) {
	h := newSpawnHost(t)
	dir := h.project("api")
	os.WriteFile(filepath.Join(h.home, "loggedout"), nil, 0o644)
	sv, _ := h.serve(t, Approve, spawnPolicy(), nil)
	r := sv.spawn(spawnCmd("n1", "api", dir, "hello"))
	if !r.OK || !strings.Contains(r.Warning, "not logged in") {
		t.Errorf("result = %+v", r)
	}
}

func TestSpawnRefusesAnOldClaudeWithItsVersion(t *testing.T) {
	h := newSpawnHost(t)
	dir := h.project("api")
	t.Setenv("FAKE_CLAUDE_VERSION", "2.1.200")
	sv, hello := h.serve(t, Approve, spawnPolicy(), nil)
	if hello.Spawn == nil || hello.Spawn.Claude != "2.1.200" {
		t.Fatalf("hello spawn = %+v", hello.Spawn)
	}
	c := spawnCmd("v1", "api", dir, "x")
	c.Check = true
	if r := sv.spawn(c); r.OK || !strings.Contains(r.Error, "2.1.200") || !strings.Contains(r.Error, MinClaudeSpawn) {
		t.Errorf("result = %+v", r)
	}
}

// end stops serve and waits until it has exited, so the next serve of the
// test gets the host's socket.
func (sv *served) end() {
	sv.stop()
	sv.in.Close()
	select {
	case <-sv.errc:
	case <-time.After(10 * time.Second):
		sv.t.Fatal("serve did not exit")
	}
}
