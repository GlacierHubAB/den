package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/cost"
	"github.com/GlacierHubAB/den/internal/events"
	"github.com/GlacierHubAB/den/internal/fleet"
)

// scratch is a fake remote host in a scratch Claude config dir: session
// files, a badge store, transcripts seeded with SECRET markers, and its own
// events.jsonl. The local side's state lives in its own scratch dirs.
type scratch struct {
	cfg        string // CLAUDE_CONFIG_DIR of the "remote"
	remoteEvts string // the remote's events.jsonl
	stateDir   string // the local Den's remote state
	localEvts  string // the local Den's events.jsonl
	sock       string // the "remote" den-remote.sock
}

// shortDir is a scratch dir with a path short enough for a Unix socket.
func shortDir(t *testing.T) string {
	t.Helper()
	base := filepath.Join("/tmp", fmt.Sprintf("den-test-%d", os.Getuid()))
	os.MkdirAll(base, 0o700)
	d, err := os.MkdirTemp(base, "rs")
	if err != nil {
		t.Fatal(err)
	}
	os.Chmod(d, 0o700)
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func newScratch(t *testing.T) *scratch {
	t.Helper()
	root := t.TempDir()
	s := &scratch{
		cfg:        filepath.Join(root, "claude"),
		remoteEvts: filepath.Join(root, "rstate", "den", "events.jsonl"),
		stateDir:   filepath.Join(root, "lstate", "den", "remote"),
		localEvts:  filepath.Join(root, "lstate", "den", "events.jsonl"),
	}
	t.Setenv("CLAUDE_CONFIG_DIR", s.cfg)
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "lstate"))
	s.sock = filepath.Join(shortDir(t), "den-remote.sock")
	t.Setenv("DEN_REMOTE_SOCKET", s.sock) // never the real runtime dir
	t.Setenv("DEN_SOCKET", filepath.Join(filepath.Dir(s.sock), "den.sock"))
	os.MkdirAll(filepath.Join(s.cfg, "sessions"), 0o755)
	os.MkdirAll(filepath.Join(s.cfg, "plugins", "store"), 0o755)
	os.MkdirAll(filepath.Dir(s.remoteEvts), 0o755)
	return s
}

// session writes a live session file (this test's pid) with SECRET
// markers in its cwd and transcript.
func (s *scratch) session(t *testing.T, n int, id, role string) {
	t.Helper()
	cwd := filepath.Join(filepath.Dir(s.cfg), "SECRET-cwd", fmt.Sprint(n))
	os.MkdirAll(cwd, 0o755)
	now := time.Now()
	sess := fmt.Sprintf(`{"pid":%d,"sessionId":%q,"cwd":%q,"startedAt":%d,"status":"busy","statusUpdatedAt":%d,"messagingSocketPath":"/tmp/SECRET-sock","version":"2.1.300"}`,
		os.Getpid(), id, cwd, now.Add(-time.Hour).UnixMilli(), now.Add(-time.Minute).UnixMilli())
	os.WriteFile(filepath.Join(s.cfg, "sessions", fmt.Sprintf("%d.json", n)), []byte(sess), 0o600)
	proj := filepath.Join(s.cfg, "projects", "-SECRET-proj")
	os.MkdirAll(proj, 0o755)
	tr := `{"type":"user","message":{"role":"user","content":"SECRET-prompt"}}
{"type":"assistant","message":{"id":"m1","model":"claude-fable-5-1","content":[{"type":"text","text":"SECRET-reply"},{"type":"tool_use","name":"Bash","input":{"command":"SECRET-command"}}],"usage":{"input_tokens":10,"output_tokens":5}}}
{"type":"ai-title","aiTitle":"SECRET-title"}
`
	os.WriteFile(filepath.Join(proj, id+".jsonl"), []byte(tr), 0o600)
	if role != "" {
		store := filepath.Join(s.cfg, "plugins", "store", "agent-badge_x.json")
		m := map[string]any{}
		if raw, err := os.ReadFile(store); err == nil {
			json.Unmarshal(raw, &m)
		}
		m["badge:"+id] = map[string]string{"role": role, "task": "fix the login"}
		raw, _ := json.Marshal(m)
		os.WriteFile(store, raw, 0o600)
	}
}

func (s *scratch) event(t *testing.T, kind, session string) {
	t.Helper()
	if err := events.Append(s.remoteEvts, events.New(kind, session)); err != nil {
		t.Fatal(err)
	}
}

func (s *scratch) serveOptions() ServeOptions {
	return ServeOptions{
		Version:    "v0.2.0",
		EventsPath: s.remoteEvts,
		Watch:      []string{filepath.Join(s.cfg, "sessions"), filepath.Join(s.cfg, "plugins", "store")},
		Debounce:   20 * time.Millisecond,
		PollEvents: 20 * time.Millisecond,
		EventPace:  time.Microsecond,
		Orphaned:   func() bool { return false },
		Socket:     s.sock,
	}
}

// pipeDialer connects the Manager to an in-process Serve over io.Pipe, and
// records every byte serve wrote (the grep test reads it). down makes
// dialing fail like an unreachable host.
type pipeDialer struct {
	opts  func() ServeOptions
	serve func(ctx context.Context, in io.Reader, out io.Writer) error // replaces Serve when set
	down  atomic.Bool
	fail  error // returned by Dial when set
	dials atomic.Int32

	mu    sync.Mutex
	wire  strings.Builder
	kills []func()
}

// setServe replaces Serve for the next dials; a test may call it while the
// Manager is dialling.
func (d *pipeDialer) setServe(f func(ctx context.Context, in io.Reader, out io.Writer) error) {
	d.mu.Lock()
	d.serve = f
	d.mu.Unlock()
}

type tee struct {
	w io.Writer
	d *pipeDialer
}

func (t tee) Write(p []byte) (int, error) {
	t.d.mu.Lock()
	t.d.wire.Write(p)
	t.d.mu.Unlock()
	return t.w.Write(p)
}

func (d *pipeDialer) dial(ctx context.Context, h Host) (*Conn, error) {
	d.dials.Add(1)
	if d.fail != nil {
		return nil, d.fail
	}
	if d.down.Load() {
		return nil, &Failure{Code: 255, Stderr: "ssh: connect to host b port 22: Connection refused"}
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	sctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	d.mu.Lock()
	serve := d.serve
	d.mu.Unlock()
	go func() {
		var err error
		if serve != nil {
			err = serve(sctx, inR, tee{outW, d})
		} else {
			err = Serve(sctx, inR, tee{outW, d}, d.opts())
		}
		outW.Close()
		inR.Close()
		done <- err
	}()
	kill := func() {
		cancel()
		inW.Close()
		outR.Close()
	}
	d.mu.Lock()
	d.kills = append(d.kills, kill)
	d.mu.Unlock()
	var once sync.Once
	var werr error
	return &Conn{In: inW, Out: outR, Kill: kill, Wait: func() error {
		once.Do(func() {
			if err := <-done; err != nil {
				werr = &Failure{Code: 1, Stderr: err.Error()}
			} else {
				werr = &Failure{Code: 0}
			}
		})
		return werr
	}}, nil
}

// cut kills every open connection, like the ssh process dying.
func (d *pipeDialer) cut() {
	d.mu.Lock()
	kills := d.kills
	d.kills = nil
	d.mu.Unlock()
	for _, k := range kills {
		k()
	}
}

func (d *pipeDialer) wireBytes() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.wire.String()
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (s *scratch) manager(d *pipeDialer, h Host) *Manager {
	return NewManager(Config{Hosts: []Host{h}}, Options{
		Version: "v0.2.0", StateDir: s.stateDir, EventsPath: s.localEvts, Dial: d.dial,
		Backoff: 20 * time.Millisecond, MaxBackoff: 100 * time.Millisecond, Healthy: time.Second,
	})
}

func hostB() Host {
	return Host{Label: "b", Target: "b", Policy: Observe, Labels: true, Enabled: true}
}

func state(m *Manager) HostStatus { return m.Hosts()[0] }

func localLines(t *testing.T, path string) []events.Event {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []events.Event
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var ev events.Event
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			t.Fatalf("bad local line %q", sc.Text())
		}
		out = append(out, ev)
	}
	return out
}

func TestServeHandshakeCapsThePolicy(t *testing.T) {
	s := newScratch(t)
	s.session(t, 1, "11111111-2222-4333-8444-555555555555", "coder")
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { Serve(ctx, inR, outW, s.serveOptions()); outW.Close() }()
	writeMsg(inW, Request{Type: TypeHello, Proto: 1, Policy: Full, Labels: true})
	lr := newLineReader(outR, MaxLine)
	line, err := lr.next()
	if err != nil {
		t.Fatal(err)
	}
	var h Hello
	json.Unmarshal(line, &h)
	if h.Type != TypeHello || h.Proto != 1 || h.Policy != Approve || h.Den != "v0.2.0" || h.Claude != "2.1.300" {
		t.Errorf("hello = %s", line)
	}
	line, _ = lr.next()
	var sn Snapshot
	json.Unmarshal(line, &sn)
	if len(sn.Agents) != 1 || sn.Agents[0].Role != "coder" || sn.Agents[0].Status != "working" {
		t.Errorf("snapshot = %s", line)
	}
	// stdin EOF ends serve.
	inW.Close()
	done := make(chan struct{})
	go func() { io.Copy(io.Discard, outR); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("serve did not exit on stdin EOF")
	}
}

func TestServeRefusesAnotherProto(t *testing.T) {
	s := newScratch(t)
	in := strings.NewReader(`{"type":"hello","proto":2,"policy":"observe"}` + "\n")
	var out strings.Builder
	err := Serve(context.Background(), in, &out, s.serveOptions())
	if err == nil || !strings.Contains(out.String(), `"code":"proto"`) {
		t.Errorf("err %v, out %s", err, out.String())
	}
}

// TestNoSecretCrossesTheWire is acceptance test 3 without ssh: what serve
// writes, and what the local side saves, carry no SECRET marker.
func TestNoSecretCrossesTheWire(t *testing.T) {
	s := newScratch(t)
	s.session(t, 1, "11111111-2222-4333-8444-555555555555", "coder")
	s.session(t, 2, "22222222-2222-4333-8444-555555555555", "")
	d := &pipeDialer{opts: s.serveOptions}
	m := s.manager(d, hostB())
	m.Start()
	defer m.Close()
	waitFor(t, "two agents", func() bool { return len(m.Agents()) == 2 })
	s.event(t, events.NeedsYou, "11111111-2222-4333-8444-555555555555")
	waitFor(t, "the event", func() bool { return len(localLines(t, s.localEvts)) == 1 })

	if w := d.wireBytes(); strings.Contains(w, "SECRET") || !strings.Contains(w, `"snapshot"`) {
		t.Errorf("the wire carried content: %s", w)
	}
	filepath.Walk(filepath.Dir(s.localEvts), func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			raw, _ := os.ReadFile(p)
			if strings.Contains(string(raw), "SECRET") {
				t.Errorf("%s holds a SECRET marker", p)
			}
		}
		return nil
	})
	st, _ := os.Stat(s.stateDir)
	if st.Mode().Perm() != 0o700 {
		t.Errorf("state dir mode %v", st.Mode().Perm())
	}
	fi, _ := os.Stat(knownPath(s.stateDir, "b"))
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("state file mode %v", fi.Mode().Perm())
	}
	for _, a := range m.Agents() {
		if a.Host != "b" || a.Stale || a.HostState != "live" {
			t.Errorf("agent %s host %q stale %v state %q", a.Name, a.Host, a.Stale, a.HostState)
		}
	}
}

// TestEventsArriveExactlyOnce is the cursor test: cut the link, write
// events on the host, reconnect, and each appears once; then restart the
// local Den and nothing is repeated.
func TestEventsArriveExactlyOnce(t *testing.T) {
	s := newScratch(t)
	id := "11111111-2222-4333-8444-555555555555"
	s.session(t, 1, id, "")
	s.event(t, events.Done, id) // before the first connection: not backfilled
	d := &pipeDialer{opts: s.serveOptions}
	m := s.manager(d, hostB())
	m.Start()
	waitFor(t, "live", func() bool { return state(m).State == Live })
	s.event(t, events.TurnStart, id)
	waitFor(t, "first event", func() bool { return len(localLines(t, s.localEvts)) == 1 })

	d.down.Store(true)
	d.cut()
	waitFor(t, "offline", func() bool { return state(m).State == Offline })
	if a := m.Agents(); len(a) != 1 || !a[0].Stale || a[0].HostState != "offline" {
		t.Errorf("offline agents = %+v", a)
	}
	s.event(t, events.NeedsYou, id)
	s.event(t, events.Done, id)
	os.Rename(s.remoteEvts, s.remoteEvts+".1") // and the stream moves aside meanwhile
	s.event(t, events.TurnStart, id)
	d.down.Store(false)
	m.Reconnect("b")
	waitFor(t, "backfill", func() bool { return len(localLines(t, s.localEvts)) == 4 })
	time.Sleep(100 * time.Millisecond)
	m.Close()

	// A new Den, from the saved cursor.
	s.event(t, events.Done, id)
	m = s.manager(d, hostB())
	m.Start()
	defer m.Close()
	waitFor(t, "after restart", func() bool { return len(localLines(t, s.localEvts)) == 5 })
	time.Sleep(150 * time.Millisecond)
	got := localLines(t, s.localEvts)
	var kinds []string
	for _, ev := range got {
		kinds = append(kinds, ev.Kind)
		if ev.Host != "b" || ev.V != 1 || ev.Session != id {
			t.Errorf("event %+v", ev)
		}
	}
	if fmt.Sprint(kinds) != "[turn_start needs_you done turn_start done]" {
		t.Errorf("events = %v", kinds)
	}
}

func TestClockSkewUsesTheRemotesAges(t *testing.T) {
	s := newScratch(t)
	id := "11111111-2222-4333-8444-555555555555"
	s.session(t, 1, id, "")
	d := &pipeDialer{opts: func() ServeOptions {
		o := s.serveOptions()
		o.Skew = 10 * time.Minute
		return o
	}}
	m := s.manager(d, hostB())
	m.Start()
	defer m.Close()
	waitFor(t, "agent", func() bool { return len(m.Agents()) == 1 })
	a := m.Agents()[0]
	// The session's status changed a minute ago by the remote's own clock;
	// with a ten-minute skew a naive comparison would say -9 or 11 minutes.
	if since := a.Since(); since < 50*time.Second || since > 2*time.Minute {
		t.Errorf("since = %v", since)
	}
	if off := state(m).Offset; off < 9*time.Minute || off > 11*time.Minute {
		t.Errorf("offset = %v", off)
	}
	// An event the skewed host stamps now lands on the local clock.
	s.event(t, events.Done, id)
	waitFor(t, "event", func() bool { return len(localLines(t, s.localEvts)) == 1 })
	if got := localLines(t, s.localEvts)[0].Time; time.Since(got).Abs() > 5*time.Second {
		t.Errorf("event time %v is not local now", got)
	}
}

func TestLabelsFalseWithholdsRoleOnBothSides(t *testing.T) {
	s := newScratch(t)
	s.session(t, 1, "11111111-2222-4333-8444-555555555555", "coder")
	h := hostB()
	h.Labels = false
	d := &pipeDialer{opts: s.serveOptions}
	m := s.manager(d, h)
	m.Start()
	waitFor(t, "agent", func() bool { return len(m.Agents()) == 1 })
	if strings.Contains(d.wireBytes(), "coder") || m.Agents()[0].Labels.Role != "" {
		t.Error("labels = false still sent the role")
	}
	m.Close()

	// A host that sends them anyway: the local check drops them.
	d = &pipeDialer{serve: func(ctx context.Context, in io.Reader, out io.Writer) error {
		newLineReader(in, MaxLine).next()
		writeMsg(out, Hello{Type: TypeHello, Proto: 1, Policy: Full, RemoteNow: time.Now()})
		writeMsg(out, Snapshot{Type: TypeSnapshot, RemoteNow: time.Now(), Agents: []fleet.Meta{{Session: "s1", Status: "idle", Role: "coder", Task: "t"}}})
		<-ctx.Done()
		return nil
	}}
	m = s.manager(d, h)
	m.Start()
	defer m.Close()
	waitFor(t, "the policy flag", func() bool { return len(state(m).Flags) > 0 && state(m).Agents == 1 })
	if a := m.Agents()[0]; a.Labels.Role != "" || a.Labels.Task != "" {
		t.Errorf("local recheck kept %q %q", a.Labels.Role, a.Labels.Task)
	}
	if st := state(m); st.Applied != Observe || len(st.Flags) == 0 {
		t.Errorf("applied %q flags %v", st.Applied, st.Flags)
	}
}

// TestHostileHost is acceptance test 6 without ssh: an unknown type, an
// unknown field and control characters are dropped, flagged or sanitised;
// an oversize line drops the host to offline as flooding.
func TestHostileHost(t *testing.T) {
	s := newScratch(t)
	oversize := make(chan struct{})
	d := &pipeDialer{serve: func(ctx context.Context, in io.Reader, out io.Writer) error {
		newLineReader(in, MaxLine).next()
		writeMsg(out, Hello{Type: TypeHello, Proto: 1, Policy: Observe, Hostname: "dev\x1b]0;pwn\x07box", RemoteNow: time.Now()})
		io.WriteString(out, `{"type":"exec","cmd":"rm -rf ~"}`+"\n")
		io.WriteString(out, `{"type":"snapshot","remote_now":"2026-10-12T12:00:00Z","agents":[{"session":"s1","status":"working","name":"Mossy\u001b[2J Lemur","role":"co\u001b]52;c;aGk=\u0007der","path":"/SECRET"}],"tail":"x"}`+"\n")
		io.WriteString(out, `{"type":"event","cursor":{"ino":1,"off":1},"event":{"v":1,"kind":"close_request","session":"s1"}}`+"\n")
		<-oversize
		io.WriteString(out, `{"type":"snapshot","agents":"`+strings.Repeat("x", MaxLine+10)+`"}`+"\n")
		<-ctx.Done()
		return nil
	}}
	m := s.manager(d, hostB())
	m.Start()
	defer m.Close()
	waitFor(t, "agent", func() bool { return len(m.Agents()) == 1 })
	a := m.Agents()[0]
	if a.Name != "Mossy Lemur" || a.Labels.Role != "coder" || state(m).Hostname != "devbox" {
		t.Errorf("not sanitised: name %q role %q host %q", a.Name, a.Labels.Role, state(m).Hostname)
	}
	// The event comes after the snapshot that made the agent appear, so its
	// flag may not be there yet.
	wants := []string{"unknown message type exec", "unknown fields in snapshot: tail", "unknown kind"}
	flagged := func() bool {
		flags := strings.Join(state(m).Flags, "; ")
		for _, want := range wants {
			if !strings.Contains(flags, want) {
				return false
			}
		}
		return true
	}
	deadline := time.Now().Add(5 * time.Second)
	for !flagged() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !flagged() {
		t.Errorf("flags %q lack one of %q", strings.Join(state(m).Flags, "; "), wants)
	}
	if len(localLines(t, s.localEvts)) != 0 {
		t.Error("an unknown event kind was logged")
	}
	close(oversize)
	waitFor(t, "flooding", func() bool { return strings.Contains(state(m).Reason, "flooding") })
}

// The cap is lowered for the test: with the real one (MaxRate) the host must
// get more than 2000 messages a second through the pipe, which a slow
// machine under -race does not, and the test would wait in vain.
func TestFloodingByRate(t *testing.T) {
	const rate = 50
	s := newScratch(t)
	d := &pipeDialer{serve: func(ctx context.Context, in io.Reader, out io.Writer) error {
		newLineReader(in, MaxLine).next()
		writeMsg(out, Hello{Type: TypeHello, Proto: 1, Policy: Observe, RemoteNow: time.Now()})
		for i := 0; i < 2*rate && ctx.Err() == nil; i++ {
			if writeMsg(out, Snapshot{Type: TypeSnapshot, RemoteNow: time.Now()}) != nil {
				break
			}
		}
		<-ctx.Done()
		return nil
	}}
	m := NewManager(Config{Hosts: []Host{hostB()}}, Options{
		Version: "v0.2.0", StateDir: s.stateDir, EventsPath: s.localEvts, Dial: d.dial,
		Backoff: 20 * time.Millisecond, MaxBackoff: 100 * time.Millisecond, Healthy: time.Second,
		MaxRate: rate,
	})
	m.Start()
	defer m.Close()
	waitFor(t, "flooding", func() bool { return strings.Contains(state(m).Reason, "flooding") })
}

func TestNeedsAttentionIsNotRetried(t *testing.T) {
	s := newScratch(t)
	d := &pipeDialer{fail: &Failure{Code: 255, Stderr: "Host key verification failed."}}
	m := s.manager(d, hostB())
	m.Start()
	defer m.Close()
	waitFor(t, "needs attention", func() bool { return state(m).State == NeedsAttention })
	time.Sleep(300 * time.Millisecond) // many backoff periods
	if n := d.dials.Load(); n != 1 {
		t.Errorf("dialled %d times", n)
	}
	if !strings.Contains(state(m).Hint, "ssh b") {
		t.Errorf("hint = %q", state(m).Hint)
	}
	m.Reconnect("b")
	waitFor(t, "a retry", func() bool { return d.dials.Load() == 2 })
}

func TestOfflineBacksOff(t *testing.T) {
	s := newScratch(t)
	d := &pipeDialer{}
	d.down.Store(true)
	m := s.manager(d, hostB())
	m.Start()
	defer m.Close()
	time.Sleep(400 * time.Millisecond)
	// 20, 40, 80, 100, 100 ms: about 5 dials in 400 ms, never a tight loop.
	if n := d.dials.Load(); n < 3 || n > 8 {
		t.Errorf("dialled %d times in 400 ms", n)
	}
	// Between dials the host is offline; a read during a dial would say
	// connecting, so wait for the backoff.
	waitFor(t, "offline", func() bool {
		st := state(m)
		return st.State == Offline && strings.Contains(st.Reason, "Connection refused")
	})
}

func TestVersionMismatch(t *testing.T) {
	for stderr, code := range map[string]int{"sh: 1: exec: den: not found": 127, `den: unknown command "remote"`: 1} {
		st, _ := Classify(&Failure{Code: code, Stderr: stderr})
		if st != VersionMismatch {
			t.Errorf("%q → %s", stderr, st)
		}
	}
	for stderr, want := range map[string]State{
		"Host key verification failed.":                        NeedsAttention,
		"user@b: Permission denied (publickey).":               NeedsAttention,
		"ssh: connect to host b port 22: Connection timed out": Offline,
		"Timeout, server b not responding.":                    Offline,
		"No ED25519 host key is known for b and you have requested strict checking.\r\nHost key verification failed.": NeedsAttention,
	} {
		if st, _ := Classify(&Failure{Code: 255, Stderr: stderr}); st != want {
			t.Errorf("%q → %s, want %s", stderr, st, want)
		}
	}
	if st, _ := Classify(errors.New("exec: ssh: not found")); st != Offline {
		t.Errorf("no ssh → %s", st)
	}
}

func TestLastKnownMarksStale(t *testing.T) {
	s := newScratch(t)
	now := time.Now()
	saveKnown(s.stateDir, Known{Label: "b", State: Live, Heartbeat: now, Received: now, Agents: []fleet.Meta{{Session: "s1", Status: "working", Role: "r"}}})
	saveKnown(s.stateDir, Known{Label: "c", State: Live, Heartbeat: now.Add(-time.Hour), Received: now, Agents: []fleet.Meta{{Session: "s2", Status: "working"}}})
	saveKnown(s.stateDir, Known{Label: "d", State: Offline, Heartbeat: now, Received: now, Agents: []fleet.Meta{{Session: "s3", Status: "idle"}}})
	cfg := Config{Hosts: []Host{
		{Label: "b", Target: "b", Policy: Observe, Enabled: true, Labels: false},
		{Label: "c", Target: "c", Policy: Observe, Enabled: true, Labels: true},
		{Label: "d", Target: "d", Policy: Observe, Enabled: true, Labels: true},
		{Label: "e", Target: "e", Policy: Observe, Enabled: false},
	}}
	got := map[string]fleet.Agent{}
	for _, a := range LastKnown(cfg, fleetPrices(), now) {
		got[a.Host] = a
	}
	if len(got) != 3 || got["b"].Stale || got["b"].Labels.Role != "" || !got["c"].Stale || got["c"].HostState != "detached" || !got["d"].Stale || got["d"].HostState != "offline" {
		t.Errorf("last known = %+v", got)
	}
}

func fleetPrices() cost.Table { return cost.NewTable(nil) }

// numbered writes n stalled events on the host, Minutes 1..n, so order
// and duplicates can be checked.
func (s *scratch) numbered(t *testing.T, from, n int, id string) {
	t.Helper()
	for i := from; i < from+n; i++ {
		ev := events.New(events.Stalled, id)
		ev.Minutes = i
		if err := events.Append(s.remoteEvts, ev); err != nil {
			t.Fatal(err)
		}
	}
}

// collect reads Updates until want events arrived, pausing after each
// update like a busy UI, and returns the events' numbers in order.
func collect(t *testing.T, m *Manager, want int, pause time.Duration) []int {
	t.Helper()
	var got []int
	deadline := time.After(15 * time.Second)
	for len(got) < want {
		select {
		case u := <-m.Updates():
			if u.Kind == EventArrived {
				if u.Host != "b" || u.Event.Host != "b" {
					t.Errorf("event from %q/%q", u.Host, u.Event.Host)
				}
				got = append(got, u.Event.Minutes)
			}
			time.Sleep(pause)
		case <-deadline:
			t.Fatalf("got %d of %d events", len(got), want)
		}
	}
	return got
}

func inOrder(t *testing.T, got []int, n int) {
	t.Helper()
	if len(got) != n {
		t.Fatalf("got %d events, want %d: %v", len(got), n, got)
	}
	for i, v := range got {
		if v != i+1 {
			t.Fatalf("event %d is number %d: %v", i, v, got)
		}
	}
}

// A slow reader loses no event and sees them in order; snapshots and host
// changes may be coalesced meanwhile.
func TestSlowReaderGetsEveryEventInOrder(t *testing.T) {
	s := newScratch(t)
	id := "11111111-2222-4333-8444-555555555555"
	s.session(t, 1, id, "")
	d := &pipeDialer{opts: s.serveOptions}
	m := s.manager(d, hostB())
	m.Start()
	defer m.Close()
	waitFor(t, "live", func() bool { return state(m).State == Live })
	s.numbered(t, 1, 200, id)
	for i := 2; i < 6; i++ { // fleet changes in between
		s.session(t, i, fmt.Sprintf("%d1111111-2222-4333-8444-555555555555", i), "")
	}
	inOrder(t, collect(t, m, 200, 2*time.Millisecond), 200)
	if st := state(m); st.State != Live {
		t.Errorf("a slow reader took the host down: %+v", st)
	}
}

// Past the backlog bound the host goes offline as flooding instead of
// dropping events; once the reader catches up it reconnects from its
// cursor, and every event arrives once, in order.
func TestBacklogBoundTakesTheHostOfflineNotTheEvents(t *testing.T) {
	s := newScratch(t)
	id := "11111111-2222-4333-8444-555555555555"
	s.session(t, 1, id, "")
	d := &pipeDialer{opts: s.serveOptions}
	m := NewManager(Config{Hosts: []Host{hostB()}}, Options{
		Version: "v0.2.0", StateDir: s.stateDir, EventsPath: s.localEvts, Dial: d.dial,
		Backoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond, MaxBacklog: 10,
	})
	m.Start()
	defer m.Close()
	waitFor(t, "live", func() bool { return state(m).State == Live })
	s.numbered(t, 1, 40, id) // nobody reads yet
	waitFor(t, "flooding", func() bool {
		st := state(m)
		return st.State != Live && strings.Contains(st.Reason, "flooding")
	})
	if n := len(localLines(t, s.localEvts)); n > 12 {
		t.Errorf("kept reading past the bound: %d events taken", n)
	}
	inOrder(t, collect(t, m, 40, 0), 40)
	if n := len(localLines(t, s.localEvts)); n != 40 {
		t.Errorf("events.jsonl has %d lines, want 40", n)
	}
	waitFor(t, "live again", func() bool { return state(m).State == Live })
}

func TestServeRefusesAnythingAfterHello(t *testing.T) {
	s := newScratch(t)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	errc := make(chan error, 1)
	go func() { errc <- Serve(context.Background(), inR, outW, s.serveOptions()); outW.Close() }()
	writeMsg(inW, Request{Type: TypeHello, Proto: 1, Policy: Observe})
	go io.WriteString(inW, `{"type":"send","text":"rm -rf ~"}`+"\n")
	var last string
	sc := bufio.NewScanner(outR)
	for sc.Scan() {
		last = sc.Text()
	}
	if err := <-errc; err == nil || !strings.Contains(last, `"code":"request"`) {
		t.Errorf("serve took a second message: err %v, last %s", err, last)
	}
}

func TestCloseTwice(t *testing.T) {
	s := newScratch(t)
	m := s.manager(&pipeDialer{opts: s.serveOptions}, hostB())
	m.Start()
	m.Close()
	m.Close() // must not panic
	if _, open := <-m.Updates(); open {
		t.Error("Updates still open after Close")
	}
	NewManager(Config{}, Options{StateDir: s.stateDir}).Close() // never started
}
