package remote_test

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/remote"
	"github.com/GlacierHubAB/den/internal/sshtest"
)

// A policy lowered in remotes.toml applies without restarting Den: the
// content the old policy let through is dropped at once, the session
// reconnects asking for the new policy, and nothing it no longer allows
// crosses after that. With a watch, editing the file is enough; without
// one, /remote connect (Reconnect) reads it.
func TestLoweredPolicyAppliesWithoutARestart(t *testing.T) {
	for _, tc := range []struct {
		name   string
		watch  time.Duration
		policy string
	}{
		{"watched, to approve", 100 * time.Millisecond, "approve"},
		{"reconnect, to observe", 0, "observe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := fullRig(t)
			g.opts = func(o *remote.Options) { o.Reload, o.Watch = remote.Load, tc.watch }
			g.r.CapturePipe(true)
			g.r.SeedSession(sshtest.SecretOpts())
			g.start()
			g.live()
			g.wait("full content", 10*time.Second, func() bool { a := g.m.Agents(); return len(a) == 1 && a[0].Title == "SECRET-title" })

			g.l.Local.WriteRemotes(g.l.RemotesTOML(tc.policy))
			if tc.watch == 0 {
				time.Sleep(300 * time.Millisecond)
				if a := g.m.Agents(); len(a) != 1 || a[0].Title != "SECRET-title" {
					t.Fatal("the file was read with no watch and no reconnect: the test would prove nothing")
				}
				g.m.Reconnect("b")
				if a := g.m.Agents(); len(a) != 1 || a[0].Title != "" || len(a[0].Tail) != 0 {
					t.Errorf("content kept after Reconnect read the lower policy: %+v", a)
				}
			}
			want := remote.Policy(tc.policy)
			g.wait("content dropped", 5*time.Second, func() bool {
				a := g.m.Agents()
				return len(a) == 1 && a[0].Title == "" && a[0].RemoteCWD == "" && len(a[0].Tail) == 0 && g.status().Policy == want
			})
			g.wait("live under the new policy", 15*time.Second, func() bool { s := g.status(); return s.State == remote.Live && s.Applied == want })
			// What the host sent since the new session's hello.
			since := func() []byte {
				out := g.r.PipeOut()
				return out[max(0, bytes.LastIndex(out, []byte(`"type":"hello"`))):]
			}
			g.wait("a snapshot under it", 10*time.Second, func() bool { return bytes.Contains(since(), []byte(`"snapshot"`)) })
			time.Sleep(500 * time.Millisecond)
			for _, secret := range []string{"SECRET-title", "SECRET-prompt", "SECRET-reply", "SECRET-cwd"} {
				if bytes.Contains(since(), []byte(secret)) {
					t.Errorf("%s crossed under %s", secret, tc.policy)
				}
			}
			if a := g.m.Agents(); len(a) != 1 || a[0].Title != "" || len(a[0].Tail) != 0 {
				t.Errorf("content after the reconnect: %+v", a)
			}
			if _, err := g.m.Diff("b", g.m.Agents()[0].Session.SessionID); err == nil {
				t.Errorf("a diff was allowed under %s", tc.policy)
			}
			if hits := g.l.Local.Grep("SECRET"); len(hits) > 0 {
				t.Errorf("SECRET in local state: %v", hits)
			}
		})
	}
}

// A raise written to remotes.toml waits for the human: the watch and a
// Reconnect (as after an install) leave the host as it was and say what the
// file asks for; Connect, the human's /remote connect, applies it.
func TestRaisedPolicyWaitsForConnect(t *testing.T) {
	g := newRig(t)
	g.l.Local.WriteRemotes(g.l.RemotesTOML("approve"))
	g.opts = func(o *remote.Options) { o.Reload, o.Watch = remote.Load, 50*time.Millisecond }
	g.r.SeedSession(sshtest.SecretOpts())
	g.start()
	g.live()
	g.wait("the agent", 10*time.Second, func() bool { return len(g.m.Agents()) == 1 })

	g.l.Local.WriteRemotes(g.l.RemotesTOML("full"))
	pending := "remotes.toml asks for full; /remote connect b to apply"
	g.wait("the raise noted", 5*time.Second, func() bool { return g.status().Pending == pending })
	g.m.Reconnect("b")
	time.Sleep(time.Second)
	if s := g.status(); s.Policy != remote.Approve || s.Applied != remote.Approve || s.Pending != pending {
		t.Errorf("the raise applied without /remote connect: %+v", s)
	}
	if a := g.m.Agents(); len(a) != 1 || a[0].Title != "" {
		t.Errorf("content under approve: %+v", a)
	}
	if _, err := g.m.Diff("b", g.m.Agents()[0].Session.SessionID); err == nil {
		t.Error("a diff was allowed before the raise was applied")
	}

	g.m.Connect("b", pending)
	g.wait("full after /remote connect", 15*time.Second, func() bool {
		s := g.status()
		return s.State == remote.Live && s.Applied == remote.Full && s.Pending == ""
	})
	g.wait("full content", 10*time.Second, func() bool { a := g.m.Agents(); return len(a) == 1 && a[0].Title == "SECRET-title" })
}

// remotes.toml pointing a host somewhere else is a raise: the link to the
// old target ends at once, nothing dials the new one through the watch or a
// Reconnect, and Connect, the human's /remote connect, dials it.
func TestNewTargetWaitsForConnect(t *testing.T) {
	g := newRig(t)
	g.l.Local.WriteRemotes(g.l.RemotesTOML("approve"))
	var mu sync.Mutex
	var dialed []string
	g.opts = func(o *remote.Options) {
		o.Reload, o.Watch = remote.Load, 50*time.Millisecond
		ssh := remote.SSHDialer(remote.DefaultSSH())
		o.Dial = func(ctx context.Context, h remote.Host) (*remote.Conn, error) {
			mu.Lock()
			dialed = append(dialed, h.Target)
			mu.Unlock()
			return ssh(ctx, h)
		}
	}
	dials := func(target string) int {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, d := range dialed {
			if d == target {
				n++
			}
		}
		return n
	}
	g.start()
	g.live()

	g.l.Local.WriteRemotes(strings.Replace(g.l.RemotesTOML("approve"), `target = "b"`, `target = "elsewhere"`, 1))
	pending := "remotes.toml points b at elsewhere; /remote connect b to apply"
	g.wait("the old link dropped and the change noted", 5*time.Second, func() bool {
		s := g.status()
		return s.State != remote.Live && s.State != remote.Connecting && s.Pending == pending
	})
	before := dials("b")
	g.m.Reconnect("b")
	time.Sleep(time.Second)
	if n := dials("elsewhere"); n > 0 {
		t.Fatalf("the new target was dialed %d times without /remote connect", n)
	}
	if n := dials("b"); n > before {
		t.Errorf("the old target was dialed again after the file left it")
	}
	if s := g.status(); s.State == remote.Live || s.Target != "b" {
		t.Errorf("status = %+v", s)
	}

	g.m.Connect("b", pending)
	g.wait("the new target dialed after /remote connect", 5*time.Second, func() bool { return dials("elsewhere") > 0 })
	if s := g.status(); s.Pending != "" || s.Target != "elsewhere" {
		t.Errorf("after Connect: %+v", s)
	}
}
