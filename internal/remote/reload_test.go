package remote_test

import (
	"bytes"
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
