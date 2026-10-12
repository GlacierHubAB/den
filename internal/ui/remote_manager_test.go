package ui

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/badge"
	"github.com/GlacierHubAB/den/internal/config"
	"github.com/GlacierHubAB/den/internal/events"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/notify"
	"github.com/GlacierHubAB/den/internal/remote"
	"github.com/GlacierHubAB/den/internal/theme"
)

// fakeServe is `den remote serve` in process: after Den's hello it sends
// hello, a snapshot of one working agent and one permission event, then
// holds the session open until Den ends it.
func fakeServe(t *testing.T, remoteNow time.Time) remote.Dialer {
	return func(ctx context.Context, h remote.Host) (*remote.Conn, error) {
		inR, inW := io.Pipe()
		outR, outW := io.Pipe()
		done := make(chan struct{})
		go func() {
			defer outW.Close()
			if _, err := bufio.NewReader(inR).ReadBytes('\n'); err != nil {
				return
			}
			send := func(v any) {
				b, _ := json.Marshal(v)
				outW.Write(append(b, '\n'))
			}
			send(remote.Hello{Type: remote.TypeHello, Proto: remote.Proto, Den: "dev", Hostname: "devbox", RemoteNow: remoteNow, Policy: remote.Observe})
			send(remote.Snapshot{Type: remote.TypeSnapshot, RemoteNow: remoteNow, Agents: []fleet.Meta{{
				Session: dupSession, Status: "working", Name: "Fizzy Moth", Repo: "den", Branch: "main",
				StatusAge: 60_000, StartedAge: 3_600_000, ActiveAge: 60_000,
			}}})
			ev := events.New(events.NeedsYou, dupSession)
			ev.Time, ev.Request, ev.Tool = remoteNow, "permission", "Bash"
			send(remote.Event{Type: remote.TypeEvent, Cursor: events.Cursor{Ino: 1, Off: 100}, Event: ev})
			select {
			case <-done:
			case <-ctx.Done():
			}
		}()
		var once sync.Once
		return &remote.Conn{
			In:  inW,
			Out: outR,
			Wait: func() error {
				<-done
				return &remote.Failure{Code: 0}
			},
			Kill: func() {
				once.Do(func() {
					close(done)
					inR.Close()
				})
			},
		}, nil
	}
}

// TestManagerToInbox runs the real Manager against an in-process serve
// whose clock is 10 minutes ahead: the remote agent joins the fleet with
// its chip and the right age, and its permission becomes an inbox item
// that cannot be answered, with an "on devbox" notification.
func TestManagerToInbox(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	path := filepath.Join(dir, "remotes.toml")
	if err := os.WriteFile(path, []byte("[hosts.devbox]\ntarget = \"devbox\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rc, err := remote.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	mgr := remote.NewManager(rc, remote.Options{StateDir: filepath.Join(dir, "remote"), EventsPath: filepath.Join(dir, "events.jsonl"), Dial: fakeServe(t, time.Now().Add(10*time.Minute))})
	defer mgr.Close()

	m := Model{st: newStyles(theme.Deepharness), store: inbox.New(), width: 140, height: 45, tab: tabFleet, cfg: config.Defaults(), hooksReady: true}
	m.input = newInput(m.st)
	m.colors = map[string]string{}
	m.att = newAttention()
	m.att.send = func(notify.Note) bool { return true }
	m.att.looking = func(string) bool { return false }
	m.remote.feed, m.remote.pump = mgr, newPump()
	m.syncRemote()
	m.remote.pump.start(mgr)
	mgr.Start()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		msg := waitRemote(m.remote.pump)().(remoteMsg)
		m.handleRemote(msg) // its batch also waits for the next update, which this loop does itself
		if msg.u.Kind == remote.EventArrived {
			break
		}
	}
	var it *inbox.Item
	for _, o := range m.store.Items() {
		if o.Host == "devbox" {
			it = o
		}
	}
	if it == nil {
		t.Fatalf("no remote item; hosts %+v", m.remote.hosts)
	}
	if it.Answerable() || it.Request() != nil || !strings.Contains(it.Title, "on devbox") {
		t.Errorf("item = %+v", it)
	}
	a, ok := m.agentFor(dupSession + "@devbox")
	if !ok || a.Status != fleet.NeedsYou || a.Stale {
		t.Fatalf("agent = %+v, %v", a, ok)
	}
	// The host's own age (1m), whatever its clock says.
	if age := a.Since(); age < 50*time.Second || age > 2*time.Minute {
		t.Errorf("age = %v", age)
	}
	out := frame(&m)
	// The critter name comes from the session id here, whatever the host called it.
	if !strings.Contains(out, badge.Name(dupSession)+" @devbox") || !strings.Contains(out, "1 need you") {
		t.Errorf("fleet:\n%s", out)
	}
}
