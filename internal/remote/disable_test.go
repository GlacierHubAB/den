package remote

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// Disabling or removing a host in remotes.toml, the strongest way to lower
// trust, ends its link at once: no session, no action, until it is enabled
// again and the human connects it.
func TestDisablingAHostEndsItsLinkAtOnce(t *testing.T) {
	for _, how := range []string{"disabled", "removed"} {
		t.Run(how, func(t *testing.T) {
			s := newScratch(t)
			d := &pipeDialer{serve: func(ctx context.Context, in io.Reader, out io.Writer) error {
				newLineReader(in, MaxLine).next()
				writeMsg(out, Hello{Type: TypeHello, Proto: 1, Policy: Approve, RemoteNow: time.Now()})
				<-ctx.Done()
				return nil
			}}
			h := hostB()
			h.Policy = Approve
			var mu sync.Mutex
			cfg := Config{Hosts: []Host{h}}
			m := NewManager(cfg, Options{
				Version: "v0.2.0", StateDir: s.stateDir, EventsPath: s.localEvts, Dial: d.dial,
				Backoff: 20 * time.Millisecond, MaxBackoff: 100 * time.Millisecond, Healthy: time.Second,
				Reload: func() (Config, error) { mu.Lock(); defer mu.Unlock(); return cfg, nil }, Watch: 50 * time.Millisecond,
			})
			go func() {
				for range m.Updates() {
				}
			}()
			m.Start()
			t.Cleanup(m.Close)
			waitFor(t, "live", func() bool { return state(m).State == Live })
			mu.Lock()
			off := h
			off.Enabled = false
			cfg = Config{Hosts: []Host{off}}
			if how == "removed" {
				cfg = Config{}
			}
			mu.Unlock()
			waitFor(t, "the link ended", func() bool { return state(m).State == Disabled })
			want := how + " in remotes.toml"
			if how == "removed" {
				want = "removed from remotes.toml"
			}
			if err := m.Actionable("b"); err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("Actionable = %v", err)
			}
			if st := state(m); st.Reason != want {
				t.Errorf("reason %q", st.Reason)
			}
			// Reconnect does not bring it back, nor does enabling it alone.
			m.Reconnect("b")
			mu.Lock()
			cfg = Config{Hosts: []Host{h}}
			mu.Unlock()
			time.Sleep(300 * time.Millisecond)
			if st := state(m); st.State != Disabled || !strings.Contains(st.Pending, "enables b again") {
				t.Errorf("after enabling: state %s pending %q", st.State, st.Pending)
			}
			// The human's /remote connect does.
			m.Connect("b", state(m).Pending)
			waitFor(t, "live again", func() bool { return state(m).State == Live })
			if err := m.Actionable("b"); err != nil {
				t.Errorf("Actionable after connect = %v", err)
			}
		})
	}
}
