package remote_test

// The acceptance tests for remote hosts, run for real: the
// Manager under test talks to a private sshd (internal/sshtest) as the ssh
// ProxyCommand, with scratch keys, homes and sockets, no network and no
// listening port, and the remote runs the den built from this tree.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/events"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/remote"
	"github.com/GlacierHubAB/den/internal/sshtest"
)

func TestMain(m *testing.M) {
	remote.ShortenCloserWaitsForTests()
	os.Exit(sshtest.Main(m))
}

const (
	idA = sshtest.SessionA
	idB = sshtest.SessionB
)

// rig is one lab with host "b", the local side applied to the test process,
// and a Manager collecting its updates.
type rig struct {
	t    *testing.T
	l    *sshtest.Lab
	r    *sshtest.Remote
	m    *remote.Manager
	mu   sync.Mutex
	ups  []remote.Update
	stop func()
	opts func(*remote.Options) // adjusts the Manager's options before start
}

// newRig makes the lab and writes remotes.toml for host b (observe).
func newRig(t *testing.T) *rig {
	t.Helper()
	l := sshtest.New(t, "b")
	l.Local.Apply()
	l.Local.WriteRemotes(l.RemotesTOML("observe"))
	return &rig{t: t, l: l, r: l.Remote("b")}
}

// start creates the Manager the way Den does, with a short backoff, and
// collects its updates.
func (g *rig) start() {
	g.t.Helper()
	cfg, err := remote.Load()
	if err != nil {
		g.t.Fatal(err)
	}
	o := remote.Options{Version: sshtest.Version()[len("den "):], Backoff: 300 * time.Millisecond, MaxBackoff: time.Second}
	if g.opts != nil {
		g.opts(&o)
	}
	g.m = remote.NewManager(cfg, o)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case u := <-g.m.Updates():
				g.mu.Lock()
				g.ups = append(g.ups, u)
				g.mu.Unlock()
			case <-stop:
				return
			}
		}
	}()
	g.m.Start()
	m, once := g.m, sync.Once{}
	g.stop = func() { once.Do(func() { m.Close(); close(stop); <-done }) } // Manager.Close is not idempotent
	g.t.Cleanup(g.stop)
}

func (g *rig) status() remote.HostStatus {
	hs := g.m.Hosts()
	if len(hs) != 1 {
		g.t.Fatalf("hosts = %v", hs)
	}
	return hs[0]
}

func (g *rig) wait(what string, d time.Duration, cond func() bool) time.Duration {
	g.t.Helper()
	start := time.Now()
	if !sshtest.WaitFor(d, cond) {
		g.t.Fatalf("timed out after %v waiting for %s (host: %+v)\nsshd log:\n%s", d, what, g.statusQuiet(), g.r.SSHDLog())
	}
	return time.Since(start)
}

func (g *rig) statusQuiet() remote.HostStatus {
	if hs := g.m.Hosts(); len(hs) == 1 {
		return hs[0]
	}
	return remote.HostStatus{}
}

func (g *rig) live() {
	g.t.Helper()
	g.wait("live", 15*time.Second, func() bool { return g.status().State == remote.Live })
}

// eventsFor returns the local events.jsonl lines that carry host b.
func (g *rig) localEvents() []events.Event {
	raw, _ := os.ReadFile(events.Path())
	var out []events.Event
	for _, line := range strings.Split(string(raw), "\n") {
		var ev events.Event
		if json.Unmarshal([]byte(line), &ev) == nil && ev.Host == "b" {
			out = append(out, ev)
		}
	}
	return out
}

func (g *rig) arrived() []remote.Update {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []remote.Update
	for _, u := range g.ups {
		if u.Kind == remote.EventArrived {
			out = append(out, u)
		}
	}
	return out
}

func (g *rig) denList() []map[string]any {
	g.t.Helper()
	out, raw := g.denListRaw(g.l.Den)
	_ = raw
	return out
}

func (g *rig) denListRaw(bin string) ([]map[string]any, []byte) {
	g.t.Helper()
	c := exec.Command(bin, "list", "-json")
	c.Dir, c.Env = g.l.Local.Home, g.l.Local.Env()
	raw, err := c.Output()
	if err != nil {
		g.t.Fatalf("den list -json: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		g.t.Fatalf("den list -json: %v: %s", err, raw)
	}
	return rows, raw
}

// 1. Two sessions on host b show with @b within 2 s of their files appearing,
// and den list -json carries Host:"b".
func TestAccept1RemoteSessionsAppear(t *testing.T) {
	g := newRig(t)
	g.start()
	g.live()
	t0 := time.Now()
	g.r.SeedSession(sshtest.SessionOpts{ID: idA, Name: "alpha", Role: "coder", Task: "fix it"})
	g.r.SeedSession(sshtest.SessionOpts{ID: idB, Name: "beta", Status: "busy"})
	took := g.wait("two agents on b", 2*time.Second, func() bool { return len(g.m.Agents()) == 2 })
	t.Logf("two remote sessions visible after %v (limit 2s); since seeding %v", took.Round(time.Millisecond), time.Since(t0).Round(time.Millisecond))
	for _, a := range g.m.Agents() {
		if a.Host != "b" || a.HostState != "live" || a.Stale {
			t.Errorf("agent %s: host %q state %q stale %v", a.Name, a.Host, a.HostState, a.Stale)
		}
		if fleet.HostName(a.Host) != "b" {
			t.Errorf("HostName = %q", fleet.HostName(a.Host))
		}
	}
	rows := g.denList()
	n := 0
	for _, row := range rows {
		if row["Host"] == "b" {
			n++
		}
	}
	if n != 2 {
		t.Errorf("den list -json has %d rows with Host b, want 2: %v", n, rows)
	}
}

// 2 (transport half). A remote PermissionRequest hook returns in its normal
// time with no decision, and the event arrives at the local Den with Host b.
func TestAccept2PermissionRequestEventArrives(t *testing.T) {
	g := newRig(t)
	g.r.SeedSession(sshtest.SessionOpts{ID: idA})
	g.start()
	g.live()

	start := time.Now()
	res := g.r.Hook("PermissionRequest", map[string]any{"session_id": idA, "tool_name": "Bash",
		"tool_input": map[string]any{"command": "ls"}})
	took := time.Since(start)
	t.Logf("remote PermissionRequest hook returned in %v", took.Round(time.Millisecond))
	if res.Code != 0 || res.Stdout != "" {
		t.Errorf("hook: exit %d stdout %q (it must give no decision)", res.Code, res.Stdout)
	}
	if took > 3*time.Second {
		t.Errorf("hook took %v: it must not wait for anyone", took)
	}
	g.wait("EventArrived", 5*time.Second, func() bool { return len(g.arrived()) == 1 })
	ev := g.arrived()[0]
	if ev.Host != "b" || ev.Event.Host != "b" || ev.Event.Kind != events.NeedsYou || ev.Event.Request != "permission" || ev.Event.Tool != "Bash" || ev.Event.Session != idA {
		t.Errorf("update = %+v", ev)
	}
	if got := g.localEvents(); len(got) != 1 || got[0].Host != "b" {
		t.Errorf("local events.jsonl = %+v", got)
	}
}

// 3. SECRET markers in prompt, reply, command, cwd and title never appear in
// the bytes that cross the pipe, nor in Den's local state.
func TestAccept3NoSecretCrossesOrLands(t *testing.T) {
	g := newRig(t)
	g.r.CapturePipe(true)
	o := sshtest.SecretOpts()
	o.Role, o.Task = "coder", "tidy"
	g.r.SeedSession(o)
	g.start()
	g.live()
	g.wait("the agent", 5*time.Second, func() bool { return len(g.m.Agents()) == 1 })

	// The hooks run on the host directly, not over the captured ssh.
	g.r.HookLocal("UserPromptSubmit", map[string]any{"cwd": o.CWD, "prompt": o.Prompt})
	g.r.HookLocal("PermissionRequest", map[string]any{"cwd": o.CWD, "tool_name": "Bash", "tool_input": map[string]any{"command": o.Command}})
	g.r.HookLocal("Notification", map[string]any{"cwd": o.CWD, "notification_type": "permission_prompt", "message": "SECRET-message", "title": o.Title})
	g.r.HookLocal("Stop", map[string]any{"cwd": o.CWD, "last_assistant_message": o.Reply})
	g.wait("the events", 5*time.Second, func() bool { return len(g.arrived()) >= 3 })
	time.Sleep(500 * time.Millisecond) // let a late snapshot cross too

	out, in := g.r.PipeOut(), g.r.PipeIn()
	if !bytes.Contains(out, []byte(`"snapshot"`)) || !bytes.Contains(out, []byte(`"event"`)) || !bytes.Contains(in, []byte(`"hello"`)) {
		t.Fatalf("capture is empty or incomplete (%d bytes out, %d in): the test would prove nothing", len(out), len(in))
	}
	t.Logf("captured %d bytes from the host and %d to it; none contains SECRET", len(out), len(in))
	if bytes.Contains(out, []byte("SECRET")) || bytes.Contains(in, []byte("SECRET")) {
		t.Errorf("a SECRET marker crossed the pipe:\n%s", out)
	}
	if hits := g.l.Local.Grep("SECRET"); len(hits) > 0 {
		t.Errorf("SECRET found in local state: %v", hits)
	}
	if bytes.Contains(bytes.ToLower(out), []byte("/home/user")) {
		t.Errorf("a path crossed the pipe")
	}
	_, raw := g.denListRaw(g.l.Den)
	if bytes.Contains(raw, []byte("SECRET")) {
		t.Errorf("den list -json carries SECRET: %s", raw)
	}
}

// 4. Killing ssh: the host goes offline within 45 s, its agents are dimmed
// (last known, stale), and events that happened while it was away arrive
// once after it comes back, also across a restart of the local Den.
func TestAccept4OfflineAndBackfillOnce(t *testing.T) {
	g := newRig(t)
	g.r.SeedSession(sshtest.SessionOpts{ID: idA, Status: "busy"})
	g.start()
	g.live()
	g.wait("the agent", 5*time.Second, func() bool { return len(g.m.Agents()) == 1 })

	// One event while live, from the agent the host lists, then the link
	// is cut (client killed, new connections refused so the offline state
	// can be observed). Sessions 2 and 3 are not listed: their events are
	// backfill, logged but never shown.
	id := func(n int) string {
		if n == 1 {
			return idA
		}
		return fmt.Sprintf("11111111-2222-4333-8444-55555555550%d", n)
	}
	g.r.Hook("UserPromptSubmit", map[string]any{"session_id": id(1), "prompt": "x"})
	g.wait("event 1", 5*time.Second, func() bool { return len(g.localEvents()) == 1 })

	g.r.Down()
	cut := time.Now()
	if g.l.KillSSH() == 0 {
		t.Fatal("no ssh to kill")
	}
	g.wait("offline", 45*time.Second, func() bool { return g.status().State == remote.Offline })
	t.Logf("host offline %v after the ssh process was killed (limit 45s)", time.Since(cut).Round(time.Millisecond))
	for _, a := range g.m.Agents() {
		if a.HostState != "offline" || !a.Stale {
			t.Errorf("agent %s after the link dropped: state %q stale %v (must be dimmed)", a.Name, a.HostState, a.Stale)
		}
	}
	for _, row := range g.denList() {
		if row["Host"] == "b" && (row["HostState"] != "offline" || row["Stale"] != true) {
			t.Errorf("den list row for a last-known agent: %v", row)
		}
	}

	// Events on the host while nobody is attached.
	g.r.HookLocal("UserPromptSubmit", map[string]any{"session_id": id(2)})
	g.r.HookLocal("Stop", map[string]any{"session_id": id(3)})
	g.r.Up()
	g.m.Reconnect("b")
	g.live()
	g.wait("backfill", 10*time.Second, func() bool { return len(g.localEvents()) >= 3 })
	time.Sleep(time.Second) // a duplicate would show by now
	assertOnce := func(when string) {
		t.Helper()
		seen := map[string]int{}
		for _, ev := range g.localEvents() {
			seen[ev.Session]++
		}
		if len(seen) != 3 || seen[id(1)] != 1 || seen[id(2)] != 1 || seen[id(3)] != 1 {
			t.Errorf("%s: events per session = %v, want each of three once", when, seen)
		}
	}
	assertOnce("after reconnect")
	if a := g.m.Agents(); len(a) != 1 || a[0].Stale || a[0].HostState != "live" {
		t.Errorf("agent after reconnect: %+v", a)
	}

	// A restart of the local Den repeats nothing: the cursor is on disk.
	g.stop()
	g.start()
	g.live()
	time.Sleep(time.Second)
	assertOnce("after a local restart")
}

// 5. A different host key: needs attention, no retry loop, nothing accepted.
func TestAccept5HostKeyMismatch(t *testing.T) {
	g := newRig(t)
	before, _ := os.ReadFile(g.r.KnownHosts)
	g.r.SwapHostKey()
	g.start()
	g.wait("needs attention", 15*time.Second, func() bool { return g.status().State == remote.NeedsAttention })
	st := g.status()
	if !strings.Contains(st.Hint, "ssh b") || !strings.Contains(st.Reason, "host key") {
		t.Errorf("hint %q reason %q", st.Hint, st.Reason)
	}
	n := g.r.Connections()
	time.Sleep(3 * time.Second) // ten backoff periods
	if got := g.r.Connections(); got != n {
		t.Errorf("connection attempts went %d -> %d: Den is retrying a refused host key", n, got)
	}
	if g.status().State != remote.NeedsAttention {
		t.Errorf("state moved to %s", g.status().State)
	}
	if after, _ := os.ReadFile(g.r.KnownHosts); !bytes.Equal(before, after) {
		t.Errorf("known_hosts changed (a key was accepted):\n%s", after)
	}
	t.Logf("%d connection attempt(s), then none for 3 s", n)

	// The human fixes it (restores the key) and retries.
	g.r.RestoreHostKey()
	g.m.Reconnect("b")
	g.live()
}

// 6. Hostile hosts: control characters in a role are stripped; an unknown
// message type is dropped and flagged; an oversize line drops the host. Never
// a crash, never an escape in what the views read, never an execution.
func TestAccept6ControlCharactersInARole(t *testing.T) {
	g := newRig(t)
	g.r.SeedSession(sshtest.SessionOpts{ID: idA, Role: "x"})
	g.start()
	g.live()
	evil := "coder\x1b]52;c;Zm9v\x07\x1b[2J\u202egnirts\u009b31m\r\n"
	g.r.Sessions()[0].SetBadge(evil, "task\x1b[31m")
	g.wait("sanitised role", 5*time.Second, func() bool {
		a := g.m.Agents()
		return len(a) == 1 && strings.HasPrefix(a[0].Labels.Role, "coder")
	})
	a := g.m.Agents()[0]
	for _, s := range []string{a.Labels.Role, a.Labels.Task, a.Name} {
		if hasControl(s) {
			t.Errorf("control characters survived: %q", s)
		}
	}
	_, raw := g.denListRaw(g.l.Den)
	if bytes.ContainsAny(raw, "\x1b\x07") || bytes.Contains(raw, []byte(`\u001b`)) {
		t.Errorf("den list -json carries an escape: %s", raw)
	}
	t.Logf("role as shown: %q", a.Labels.Role)
}

// stubDen is a fake `den` on the host that speaks just enough protocol, then
// sends what a hostile host would. body is shell run after the hello.
// hasControl reports C0, DEL, C1 and bidi controls in s.
func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
			return true
		}
	}
	return false
}

func stubDen(body string) string {
	return `#!/bin/sh
read -r _request
now=$(date -u +%Y-%m-%dT%H:%M:%S.000Z)
printf '{"type":"hello","proto":1,"den":"v0.2.0","hostname":"stub","remote_now":"%s","policy":"observe","labels":true}\n' "$now"
` + body + `
cat >/dev/null
`
}

func TestAccept6HostileHost(t *testing.T) {
	t.Run("unknown type and fields are dropped and flagged", func(t *testing.T) {
		g := stubRig(t, `
cat <<'EOF'
{"type":"exec","cmd":"touch %[1]s"}
{"type":"snapshot","remote_now":"%[2]s","evil":1,"agents":[{"index":0,"session":"s1","status":"working","name":"Evil\u001b]52;c;Zm9v\u0007\u001b[2J","role":"r\u001b[2Jx","status_age_ms":0,"started_age_ms":0,"last_active_ms":-1,"rce":"x"}]}
EOF
`)
		g.start()
		g.wait("the agent", 10*time.Second, func() bool { return len(g.m.Agents()) == 1 })
		// The unknown type alone raises two flags; wait for both names.
		g.wait("flags", 5*time.Second, func() bool {
			flags := strings.Join(g.status().Flags, " | ")
			return strings.Contains(flags, "exec") && strings.Contains(flags, "evil")
		})
		if st := g.status(); st.State != remote.Live {
			t.Errorf("state = %s (%s): a single unknown message must not drop the host", st.State, st.Reason)
		}
		flags := strings.Join(g.status().Flags, " | ")
		if !strings.Contains(flags, "exec") || !strings.Contains(flags, "evil") {
			t.Errorf("flags = %s", flags)
		}
		if a := g.m.Agents()[0]; hasControl(a.Name + a.Labels.Role) {
			t.Errorf("escape survived: %q %q", a.Name, a.Labels.Role)
		}
		if _, err := os.Stat(filepath.Join(g.l.Dir, "pwned")); err == nil {
			t.Fatal("Den executed something the host sent")
		}
	})
	t.Run("an oversize line drops the host as flooding", func(t *testing.T) {
		g := stubRig(t, `head -c 300000 /dev/zero | tr '\0' a; echo`)
		g.start()
		g.wait("offline", 15*time.Second, func() bool { st := g.status(); return st.State == remote.Offline && st.Reason != "" })
		if st := g.status(); !strings.Contains(st.Reason, "flooding") || len(st.Flags) == 0 {
			t.Errorf("reason %q flags %v", st.Reason, st.Flags)
		}
	})
	t.Run("a line that is not JSON is flagged, not fatal", func(t *testing.T) {
		g := stubRig(t, `echo 'not json at all'; echo '{"type":'`)
		g.start()
		g.wait("flags", 10*time.Second, func() bool { return len(g.status().Flags) > 0 })
		if g.status().State != remote.Live {
			t.Errorf("state = %s", g.status().State)
		}
	})
}

// stubRig is a rig whose host runs a stub den instead of the real one.
func stubRig(t *testing.T, body string) *rig {
	t.Helper()
	l := sshtest.New(t, "b")
	r := l.Remote("b")
	marker := filepath.Join(l.Dir, "pwned")
	body = strings.ReplaceAll(strings.ReplaceAll(body, "%[1]s", marker), "%[2]s", "2026-10-12T12:00:00.000Z")
	stub := filepath.Join(r.Home, "stubden")
	if err := os.WriteFile(stub, []byte(stubDen(body)), 0o755); err != nil {
		t.Fatal(err)
	}
	l.Local.Apply()
	l.Local.WriteRemotes(fmt.Sprintf("[hosts.b]\ntarget = \"b\"\npolicy = \"observe\"\nden_path = %q\n", stub))
	return &rig{t: t, l: l, r: r}
}

// 7. A remote clock 10 minutes ahead: ages are right and nothing is stalled.
func TestAccept7ClockSkew(t *testing.T) {
	g := newRig(t)
	g.r.SetEnv("DEN_TEST_REMOTE_NOW_OFFSET", "10m")
	g.r.SeedSession(sshtest.SessionOpts{ID: idA, Status: "busy"})
	g.start()
	g.live()
	g.wait("the agent", 5*time.Second, func() bool { return len(g.m.Agents()) == 1 })
	off := g.status().Offset
	t.Logf("measured clock offset: %v", off)
	if d := off - 10*time.Minute; d < -5*time.Second || d > 5*time.Second {
		t.Errorf("offset = %v, want about +10m", off)
	}
	a := g.m.Agents()[0]
	now := time.Now()
	// The session started when its process did, just now on the remote's
	// own clock; across the +10m skew a wrong age would be about 10m off.
	if d := now.Sub(a.Session.Started()); d < -5*time.Second || d > 15*time.Second {
		t.Errorf("started %v ago, want a few seconds (the session's process started when the test began)", d)
	}
	if since := now.Sub(a.Session.StatusChanged()); since < -time.Second || since > 10*time.Second {
		t.Errorf("status changed %v ago: ages must come from the remote's own clock, not across the skew", since)
	}
	if a.Stale || a.Status.Key() == "stalled" {
		t.Errorf("agent marked stale/stalled: %+v", a.Status)
	}

	g.r.Hook("PermissionRequest", map[string]any{"session_id": idA, "tool_name": "Bash"})
	g.wait("the event", 5*time.Second, func() bool { return len(g.arrived()) == 1 })
	ev := g.arrived()[0].Event
	if d := time.Since(ev.Time); d < -5*time.Second || d > 5*time.Second {
		t.Errorf("event time is %v from local now: the offset was not removed (event %v)", d, ev.Time)
	}
	for _, e := range g.localEvents() {
		if e.Time.After(time.Now().Add(5 * time.Second)) {
			t.Errorf("a future event time landed in the local stream: %v", e.Time)
		}
	}
}

// 8. With no remotes configured nothing changes: den list -json is
// byte-for-byte what it was before the remote work, and has no host keys.
func TestAccept8NoRemotesUnchanged(t *testing.T) {
	l := sshtest.New(t, "b")
	l.Local.Apply()
	// New wrote no remotes.toml for the local Den: there are none.
	if _, err := os.Stat(l.Local.RemotesPath()); err == nil {
		t.Fatal("remotes.toml exists")
	}
	l.Local.SeedSession(sshtest.SessionOpts{ID: idA, Name: "alpha", Role: "coder", Task: "a task", Status: "busy"})
	l.Local.SeedSession(sshtest.SessionOpts{ID: idB, Name: "beta"})
	g := &rig{t: t, l: l}

	rows, now := g.denListRaw(l.Den)
	if len(rows) != 2 {
		t.Fatalf("rows = %d: %s", len(rows), now)
	}
	for _, key := range []string{"Host", "HostState", "Stale"} {
		if bytes.Contains(now, []byte(`"`+key+`"`)) {
			t.Errorf("%s is in the output without any remote: %s", key, now)
		}
	}
	// An empty remotes.toml is the same as none.
	l.Local.WriteRemotes("")
	if _, again := g.denListRaw(l.Den); !bytes.Equal(now, again) {
		t.Errorf("an empty remotes.toml changed the output:\n%s\n%s", now, again)
	}
	// Against the tree before any remote work.
	old := sshtest.BuildDenAt(t, preRemoteRev)
	if _, before := g.denListRaw(old); !bytes.Equal(before, now) {
		t.Errorf("den list -json differs from the pre-remote build:\n before: %s\n now:    %s", before, now)
	}
}

// preRemoteRev is the last public commit before remote support (dev with
// the sanitising and git hardening it builds on). Its den list -json is the
// baseline acceptance test 8 compares with. A clone that lacks it (a
// shallow checkout) skips that comparison.
const preRemoteRev = "1235527f1cb3e86968b37d2dbf914e6e53d27623"

// Install over scp (SFTP mode, through the forced command) lands in the
// scratch home, with its sha256 verified on the host.
func TestInstallOverScpVerifiesSHA256(t *testing.T) {
	g := newRig(t)
	g.r.WithoutDen()
	ssh := remote.DefaultSSH()
	h := remote.Host{Label: "b", Target: "b", Policy: remote.Observe, Labels: true, Enabled: true}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	plan, err := remote.PlanInstall(ctx, ssh, h, "v9.9.9")
	if err != nil || !plan.Copy {
		t.Fatalf("plan: %+v %v", plan, err)
	}
	// The plan would copy the running test binary; copy the built den instead
	// (which can answer `den version`).
	raw, err := os.ReadFile(g.l.Den)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	plan.Local, plan.SHA256 = g.l.Den, hex.EncodeToString(sum[:])

	out, err := ssh.Install(ctx, plan)
	if err != nil {
		t.Fatalf("install: %v\nsshd log:\n%s", err, g.r.SSHDLog())
	}
	if out != sshtest.Version() {
		t.Errorf("installed den says %q, want %q", out, sshtest.Version())
	}
	got, err := os.ReadFile(g.r.DenPath)
	if err != nil {
		t.Fatalf("the install did not land in the scratch home: %v", err)
	}
	if s := sha256.Sum256(got); hex.EncodeToString(s[:]) != plan.SHA256 {
		t.Error("installed bytes differ")
	}
	if left, _ := filepath.Glob(filepath.Join(g.r.Home, ".local", "bin", ".den-install-*")); len(left) > 0 {
		t.Errorf("temporary copy left behind: %v", left)
	}
	if real := sshtest.RealHome(); real != "" && !strings.HasPrefix(g.l.Dir, real) {
		if _, err := os.Stat(filepath.Join(real, ".local", "bin", plan.Temp)); err == nil {
			t.Errorf("the temporary file landed in the REAL home")
		}
	}
}

// A link that goes silent without closing (no reset, no EOF) is found by the
// keepalives (ServerAliveInterval 15, ServerAliveCountMax 3). Slow, so it runs
// only with DEN_SLOW_TESTS=1 (and never under -short).
func TestSilentLinkGoesOfflineByKeepalive(t *testing.T) {
	if testing.Short() || os.Getenv("DEN_SLOW_TESTS") != "1" {
		t.Skip("slow (about 60 s): set DEN_SLOW_TESTS=1")
	}
	g := newRig(t)
	g.r.SeedSession(sshtest.SessionOpts{ID: idA, Status: "busy"})
	g.start()
	g.live()
	g.wait("the agent", 5*time.Second, func() bool { return len(g.m.Agents()) == 1 })
	g.r.Down() // so the reconnect attempt after the drop fails and the state can be seen

	if g.r.Freeze() == 0 {
		t.Fatal("no sshd to freeze")
	}
	frozen := time.Now()
	time.Sleep(5 * time.Second)
	if st := g.status(); st.State != remote.Live {
		t.Fatalf("after 5 s of silence the state is already %s (%s): the link was not silent, it was closed", st.State, st.Reason)
	}
	took := g.wait("offline by keepalive", 75*time.Second, func() bool { return g.status().State == remote.Offline })
	total := time.Since(frozen)
	t.Logf("silent link: offline %v after the link stopped carrying bytes (reason %q); limit 45-60 s", total.Round(time.Second), g.status().Reason)
	_ = took
	// ssh counts silence from the last byte it received, and probes at
	// ServerAliveInterval; it gives up when the probe after CountMax unanswered
	// ones also goes unanswered: 3+1 intervals = 60 s, minus up to 15 s for
	// bytes received after the stall began (serve's periodic snapshot). So the
	// real window is 45 to 60 s after the last traffic, not a flat 45 s.
	if total < 40*time.Second || total > 66*time.Second {
		t.Errorf("offline after %v, want 45 to 60 s after the last traffic", total)
	}
	for _, a := range g.m.Agents() {
		if a.HostState != "offline" || !a.Stale {
			t.Errorf("agent after the silent drop: state %q stale %v", a.HostState, a.Stale)
		}
	}
}
