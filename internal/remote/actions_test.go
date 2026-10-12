package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/closer"
	"github.com/GlacierHubAB/den/internal/peer"
)

// Send and close over io.Pipe: serve runs in this process with a fake "host"
// (session files in a scratch CLAUDE_CONFIG_DIR, a Unix socket standing in
// for Claude Code's messagingSocketPath, a fake process table), and the
// test is Den.

// fakeProcs is closer.Ops over a table of pretend processes.
type fakeProcs struct {
	mu       sync.Mutex
	alive    map[int]bool
	stubborn map[int]bool // ignores SIGTERM
	signals  []string
}

func newFakeProcs(pids ...int) *fakeProcs {
	f := &fakeProcs{alive: map[int]bool{}, stubborn: map[int]bool{}}
	for _, p := range pids {
		f.alive[p] = true
	}
	return f
}

func (f *fakeProcs) Signal(pid int, sig syscall.Signal) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.signals = append(f.signals, fmt.Sprintf("%d:%d", pid, sig))
	if sig == syscall.SIGKILL || !f.stubborn[pid] {
		f.alive[pid] = false
	}
	return nil
}
func (f *fakeProcs) Alive(pid int) bool       { f.mu.Lock(); defer f.mu.Unlock(); return f.alive[pid] }
func (f *fakeProcs) CloseWindow(string) error { panic("a remote close must never touch a window") }
func (f *fakeProcs) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.signals...)
}

// inbox is a Unix socket standing in for a session's messagingSocketPath: it
// records every line written to it.
type inboxSock struct {
	path  string
	lines chan string
}

func newInboxSock(t *testing.T) *inboxSock {
	t.Helper()
	p := filepath.Join(shortDir(t), "in.sock")
	ln, err := net.Listen("unix", p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	s := &inboxSock{path: p, lines: make(chan string, 16)}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(c).ReadString('\n')
			c.Close()
			s.lines <- line
		}
	}()
	return s
}

// hostSession writes a session file; pid is the process it names (a real,
// live one is needed for the host to count it alive).
func (s *scratch) hostSession(t *testing.T, n int, id string, pid int, status, socket string) {
	t.Helper()
	now := time.Now()
	sess := fmt.Sprintf(`{"pid":%d,"sessionId":%q,"cwd":"/home/user/work","startedAt":%d,"status":%q,"statusUpdatedAt":%d,"messagingSocketPath":%q,"version":"2.1.300"}`,
		pid, id, now.Add(-time.Hour).UnixMilli(), status, now.Add(-time.Minute).UnixMilli(), socket)
	if err := os.WriteFile(filepath.Join(s.cfg, "sessions", fmt.Sprintf("%d.json", n)), []byte(sess), 0o600); err != nil {
		t.Fatal(err)
	}
}

// fakeStop is where a test says it closes stubborn processes. The closer's
// waits are package variables, and serve's action goroutines can still be
// reading them when the next test starts, so they are set once, before any
// test runs (TestMain, ShortenCloserWaitsForTests), never per test.
func fakeStop(t *testing.T) { t.Helper() }

// ShortenCloserWaitsForTests (exported so the external test package's TestMain can call it) makes the closer's graceful wait short for the whole
// test binary. It is long enough that a stubborn process still holds its
// slot while the cap test sends one more command.
func ShortenCloserWaitsForTests() { closer.StopWait = time.Second }

// actHost starts serve under the policy with fake processes. A session's pid
// must be alive to the real kill(0) too, so the tests use their own pid and a
// Self that is not it.
func actHost(t *testing.T, s *scratch, policy Policy, procs *fakeProcs) *served {
	t.Helper()
	sv, h := serveDirect(t, s, Request{Policy: policy, Labels: true}, func(o *ServeOptions) {
		o.CloseOps = procs
		o.CloseSelf = closer.Self{PID: 1 << 30}
	})
	if h.Policy != policy {
		t.Fatalf("serve applies %s, want %s", h.Policy, policy)
	}
	return sv
}

func (sv *served) result(id string) Result {
	sv.t.Helper()
	var r Result
	json.Unmarshal(sv.next(TypeResult), &r)
	if r.ID != id {
		sv.t.Fatalf("result for %q, want %q: %+v", r.ID, id, r)
	}
	return r
}

func (sv *served) send(id, session, text string) {
	writeMsg(sv.in, SendCmd{Type: TypeSend, ID: id, Session: session, Text: text})
}

func (sv *served) closeAgent(id, session string, force bool) {
	writeMsg(sv.in, CloseCmd{Type: TypeClose, ID: id, Session: session, Force: force})
}

func TestServeSendPostsTheExactLineToTheSessionsSocket(t *testing.T) {
	s := newScratch(t)
	sock := newInboxSock(t)
	s.hostSession(t, 1, sidA, os.Getpid(), "idle", sock.path)
	sv := actHost(t, s, Approve, newFakeProcs())

	text := "rebase on main; \"quote\" $(not a shell) `x`\nsecond line"
	sv.send("s1", sidA, text)
	if r := sv.result("s1"); !r.OK || r.Error != "" {
		t.Fatalf("result = %+v", r)
	}
	want, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": peer.Compose(text)}})
	select {
	case line := <-sock.lines:
		if line != string(want)+"\n" {
			t.Errorf("the socket got\n%q\nwant\n%q", line, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("nothing reached the session's socket")
	}
}

func TestServeSendRefusals(t *testing.T) {
	s := newScratch(t)
	sock := newInboxSock(t)
	s.hostSession(t, 1, sidA, os.Getpid(), "idle", sock.path)
	s.hostSession(t, 2, sidB, 2147483000, "idle", sock.path) // a pid nobody has
	sv := actHost(t, s, Approve, newFakeProcs())

	cases := []struct{ name, session, text, want string }{
		{"unknown session", "11111111-2222-4333-8444-000000000000", "hi", "no live session"},
		{"a prefix is not a session", sidA[:8], "hi", "no live session"},
		{"dead pid", sidB, "hi", "no live session"},
		{"empty text", sidA, "  \n", "nothing to send"},
		{"oversize text", sidA, strings.Repeat("x", MaxSendText+1), "over 64 KB"},
		{"control characters in the id", sidA + "\x1b[2J", "hi", "no such session"},
	}
	for i, c := range cases {
		id := fmt.Sprintf("r%d", i)
		sv.send(id, c.session, c.text)
		r := sv.result(id)
		if r.OK || !strings.Contains(r.Error, c.want) {
			t.Errorf("%s: result = %+v, want an error with %q", c.name, r, c.want)
		}
	}
	select {
	case line := <-sock.lines:
		t.Errorf("a refused send reached the socket: %q", line)
	case <-time.After(200 * time.Millisecond):
	}
	// The text of exactly the cap is fine.
	sv.send("ok", sidA, strings.Repeat("y", MaxSendText))
	if r := sv.result("ok"); !r.OK {
		t.Errorf("a text of exactly %d bytes: %+v", MaxSendText, r)
	}
}

func TestServeSendNoSocketOrGoneSocketDoesNotLeakThePath(t *testing.T) {
	s := newScratch(t)
	gone := filepath.Join(shortDir(t), "SECRET-gone.sock")
	s.hostSession(t, 1, sidA, os.Getpid(), "idle", "")
	s.hostSession(t, 2, sidB, os.Getpid(), "idle", gone)
	sv := actHost(t, s, Approve, newFakeProcs())
	sv.send("a", sidA, "hi")
	if r := sv.result("a"); r.OK || !strings.Contains(r.Error, "no inbox socket") {
		t.Errorf("no socket: %+v", r)
	}
	sv.send("b", sidB, "hi")
	if r := sv.result("b"); r.OK || strings.Contains(r.Error, "SECRET") || !strings.Contains(r.Error, "socket") {
		t.Errorf("gone socket: %+v", r)
	}
}

func TestServeCloseRules(t *testing.T) {
	fakeStop(t)
	s := newScratch(t)
	pid := os.Getpid() // a live process to the real kill(0); the fake table decides what happens to it
	s.hostSession(t, 1, sidA, pid, "busy", "")
	procs := newFakeProcs(pid)
	sv := actHost(t, s, Approve, procs)

	sv.closeAgent("w", sidA, false)
	if r := sv.result("w"); r.OK || !strings.Contains(r.Error, "working") || !strings.Contains(r.Error, "--force") {
		t.Errorf("a working agent without force: %+v", r)
	}
	if len(procs.sent()) != 0 {
		t.Fatalf("signals sent for a refused close: %v", procs.sent())
	}

	sv.closeAgent("u", "11111111-2222-4333-8444-000000000000", true)
	if r := sv.result("u"); r.OK || !strings.Contains(r.Error, "no live session") {
		t.Errorf("an unknown session: %+v", r)
	}

	sv.closeAgent("f", sidA, true)
	if r := sv.result("f"); !r.OK || r.Killed {
		t.Errorf("a forced close of a working agent that stops: %+v", r)
	}
	if got := procs.sent(); len(got) != 1 || got[0] != fmt.Sprintf("%d:%d", pid, syscall.SIGTERM) {
		t.Errorf("signals = %v, want one SIGTERM", got)
	}
}

func TestServeCloseIdleStubbornNeedsForceForSIGKILL(t *testing.T) {
	fakeStop(t)
	s := newScratch(t)
	pid := os.Getpid()
	s.hostSession(t, 1, sidA, pid, "idle", "")
	procs := newFakeProcs(pid)
	procs.stubborn[pid] = true
	sv := actHost(t, s, Approve, procs)

	sv.closeAgent("n", sidA, false)
	if r := sv.result("n"); r.OK || !strings.Contains(r.Error, "still running") {
		t.Errorf("a stubborn process without force: %+v", r)
	}
	if got := procs.sent(); len(got) != 1 {
		t.Errorf("without force only SIGTERM: %v", got)
	}
	sv.closeAgent("k", sidA, true)
	if r := sv.result("k"); !r.OK || !r.Killed {
		t.Errorf("a stubborn process with force: %+v", r)
	}
	got := procs.sent()
	if got[len(got)-1] != fmt.Sprintf("%d:%d", pid, syscall.SIGKILL) {
		t.Errorf("signals = %v, want SIGKILL last", got)
	}
}

func TestServeCloseRefusesItselfAndPidOne(t *testing.T) {
	s := newScratch(t)
	s.hostSession(t, 1, sidA, os.Getpid(), "idle", "")
	procs := newFakeProcs(os.Getpid())
	sv, _ := serveDirect(t, s, Request{Policy: Approve, Labels: true}, func(o *ServeOptions) {
		o.CloseOps = procs
		o.CloseSelf = closer.Self{PID: os.Getpid()}
	})
	sv.closeAgent("me", sidA, true)
	if r := sv.result("me"); r.OK || !strings.Contains(r.Error, "Den itself") {
		t.Errorf("closing serve's own process: %+v", r)
	}
	s.hostSession(t, 2, sidB, 1, "idle", "")
	sv.closeAgent("one", sidB, true)
	if r := sv.result("one"); r.OK || !strings.Contains(r.Error, "no live session") {
		t.Errorf("pid 1: %+v", r)
	}
	if len(procs.sent()) != 0 {
		t.Errorf("signals = %v", procs.sent())
	}
}

func TestServeObserveRefusesBothAndKeepsServing(t *testing.T) {
	s := newScratch(t)
	sock := newInboxSock(t)
	s.hostSession(t, 1, sidA, os.Getpid(), "idle", sock.path)
	procs := newFakeProcs(os.Getpid())
	sv := actHost(t, s, Observe, procs)
	sv.send("a", sidA, "hi")
	if r := sv.result("a"); r.OK || !strings.Contains(r.Error, "observe") {
		t.Errorf("send under observe: %+v", r)
	}
	sv.closeAgent("b", sidA, true)
	if r := sv.result("b"); r.OK || !strings.Contains(r.Error, "observe") {
		t.Errorf("close under observe: %+v", r)
	}
	select {
	case line := <-sock.lines:
		t.Errorf("observe let a message through: %q", line)
	case <-time.After(200 * time.Millisecond):
	}
	if len(procs.sent()) != 0 {
		t.Errorf("observe let a signal through: %v", procs.sent())
	}
}

func TestServeRefusesUnknownCommandsAndFields(t *testing.T) {
	for name, line := range map[string]string{
		"exec":           `{"type":"exec","cmd":"id"}`,
		"shell in send":  `{"type":"send","id":"x","session":"` + sidA + `","text":"hi","cmd":"id"}`,
		"argv in close":  `{"type":"close","id":"x","session":"` + sidA + `","argv":["sh"]}`,
		"send no id":     `{"type":"send","session":"` + sidA + `","text":"hi"}`,
		"close bad type": `{"type":"close","id":"x","session":7}`,
	} {
		t.Run(name, func(t *testing.T) {
			s := newScratch(t)
			s.hostSession(t, 1, sidA, os.Getpid(), "idle", "")
			procs := newFakeProcs(os.Getpid())
			sv := actHost(t, s, Approve, procs)
			sv.in.Write([]byte(line + "\n"))
			var e Error
			json.Unmarshal(sv.next(TypeError), &e)
			if e.Code != "request" {
				t.Errorf("error = %+v", e)
			}
			select {
			case err := <-sv.errc:
				if err == nil {
					t.Error("serve ended without an error")
				}
			case <-time.After(3 * time.Second):
				t.Error("serve kept going after a command outside the allowlist")
			}
			if len(procs.sent()) != 0 {
				t.Errorf("signals = %v", procs.sent())
			}
		})
	}
}

func TestServeActionsAreCappedAtOnce(t *testing.T) {
	fakeStop(t)
	s := newScratch(t)
	s.hostSession(t, 1, sidA, os.Getpid(), "idle", "")
	procs := newFakeProcs(os.Getpid())
	procs.stubborn[os.Getpid()] = true
	sv := actHost(t, s, Approve, procs)
	for i := 0; i < MaxActions; i++ {
		sv.closeAgent(fmt.Sprintf("c%d", i), sidA, false)
	}
	sv.closeAgent("extra", sidA, false)
	if r := sv.result("extra"); r.OK || !strings.Contains(r.Error, "too many actions") {
		t.Errorf("the action over the cap: %+v", r)
	}
}

// liveHost runs a Manager against an in-process serve with fake processes
// and waits for it to be live.
func liveHost(t *testing.T, s *scratch, h Host, procs *fakeProcs, mod func(*Options)) *managed {
	t.Helper()
	g := manage(t, s, h, ctlA, func(o *ServeOptions) {
		o.CloseOps = procs
		o.CloseSelf = closer.Self{PID: 1 << 30}
	})
	waitFor(t, "the host to be live", func() bool { return state(g.m).State == Live })
	return g
}

func TestManagerSendAndCloseRoundTrip(t *testing.T) {
	fakeStop(t)
	s := newScratch(t)
	sock := newInboxSock(t)
	s.hostSession(t, 1, sidA, os.Getpid(), "idle", sock.path)
	procs := newFakeProcs(os.Getpid())
	g := liveHost(t, s, approveHost(), procs, nil)

	if err := g.m.Send("b", sidA, "please rebase"); err != nil {
		t.Fatal(err)
	}
	select {
	case line := <-sock.lines:
		if !strings.Contains(line, "please rebase") {
			t.Errorf("line = %q", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("nothing reached the socket")
	}
	if err := g.m.Send("b", "11111111-2222-4333-8444-000000000000", "hi"); err == nil || !strings.Contains(err.Error(), "no live session") {
		t.Errorf("send to an unknown session: %v", err)
	}
	killed, err := g.m.CloseAgent("b", sidA, false)
	if err != nil || killed {
		t.Fatalf("close: killed %v, err %v", killed, err)
	}
	if got := procs.sent(); len(got) != 1 {
		t.Errorf("signals = %v", got)
	}
	if err := g.m.Send("b", sidA, strings.Repeat("x", MaxSendText+1)); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("an oversize send: %v", err)
	}
}

func TestManagerRefusesUnderObserveBeforeSending(t *testing.T) {
	s := newScratch(t)
	sock := newInboxSock(t)
	s.hostSession(t, 1, sidA, os.Getpid(), "idle", sock.path)
	procs := newFakeProcs(os.Getpid())
	h := approveHost()
	h.Policy = Observe
	g := liveHost(t, s, h, procs, nil)
	if err := g.m.Send("b", sidA, "hi"); err == nil || !strings.Contains(err.Error(), "observe") || !strings.Contains(err.Error(), "nothing was sent") {
		t.Errorf("send under observe: %v", err)
	}
	if _, err := g.m.CloseAgent("b", sidA, true); err == nil || !strings.Contains(err.Error(), "observe") || !strings.Contains(err.Error(), "nothing was closed") {
		t.Errorf("close under observe: %v", err)
	}
	if err := g.m.Send("nowhere", sidA, "hi"); err == nil || !strings.Contains(err.Error(), "no remote host") {
		t.Errorf("an unknown host: %v", err)
	}
	if g.m.Actionable("b") == nil {
		t.Error("Actionable says a host under observe takes actions")
	}
	select {
	case line := <-sock.lines:
		t.Errorf("reached the socket: %q", line)
	case <-time.After(200 * time.Millisecond):
	}
	if len(procs.sent()) != 0 {
		t.Errorf("signals = %v", procs.sent())
	}
}

func TestManagerOfflineSaysNothingWasSent(t *testing.T) {
	s := newScratch(t)
	d := &pipeDialer{opts: s.serveOptions}
	d.down.Store(true)
	// A long backoff keeps the host offline, not connecting, under -race.
	m := NewManager(Config{Hosts: []Host{approveHost()}}, Options{Version: "v0.2.0", StateDir: s.stateDir, EventsPath: s.localEvts, Dial: d.dial,
		Backoff: time.Hour, MaxBackoff: time.Hour})
	m.Start()
	defer m.Close()
	// A host reads Offline before its first attempt too; wait for the attempt's
	// failure, so it is not Connecting when Send looks.
	waitFor(t, "offline after a failed attempt", func() bool { st := state(m); return st.State == Offline && st.Reason != "" })
	if err := m.Send("b", sidA, "hi"); err == nil || err.Error() != "b is offline; nothing was sent" {
		t.Errorf("send: %v", err)
	}
	if _, err := m.CloseAgent("b", sidA, false); err == nil || err.Error() != "b is offline; nothing was closed" {
		t.Errorf("close: %v", err)
	}
}

// answeringServe is a serve that says hello and then lets handle see each
// line Den writes.
func answeringServe(policy Policy, handle func(line []byte, out func(any), end func())) func(context.Context, io.Reader, io.Writer) error {
	return func(ctx context.Context, in io.Reader, out io.Writer) error {
		lr := newLineReader(in, MaxLine)
		if _, err := lr.next(); err != nil {
			return err
		}
		writeMsg(out, Hello{Type: TypeHello, Proto: Proto, Den: "v0.2.0", Hostname: "b", RemoteNow: time.Now(), Policy: policy, Labels: true})
		done := make(chan struct{})
		var once sync.Once
		end := func() { once.Do(func() { close(done) }) }
		go func() {
			for {
				line, err := lr.next()
				if err != nil {
					end()
					return
				}
				handle(line, func(v any) { writeMsg(out, v) }, end)
			}
		}()
		select {
		case <-done:
		case <-ctx.Done():
		}
		return nil
	}
}

func TestManagerLinkLostWhileWaitingAndTimeout(t *testing.T) {
	s := newScratch(t)
	d := &pipeDialer{opts: s.serveOptions}
	d.serve = answeringServe(Approve, func(line []byte, out func(any), end func()) {
		var m struct{ Type, Text string }
		json.Unmarshal(line, &m)
		if m.Text == "drop" {
			end() // the link dies with the request in flight
		}
	})
	m := NewManager(Config{Hosts: []Host{approveHost()}}, Options{
		Version: "v0.2.0", StateDir: s.stateDir, EventsPath: s.localEvts, Dial: d.dial, SendWait: 300 * time.Millisecond,
		Backoff: time.Hour, MaxBackoff: time.Hour,
	})
	m.Start()
	defer m.Close()
	waitFor(t, "live", func() bool { return state(m).State == Live })
	start := time.Now()
	err := m.Send("b", sidA, "drop")
	if err == nil || !strings.Contains(err.Error(), "link to b ended") || !strings.Contains(err.Error(), "not known") {
		t.Errorf("link lost: %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("a lost link took %v to report", time.Since(start))
	}
}

func TestManagerTimeoutSaysItIsNotKnown(t *testing.T) {
	s := newScratch(t)
	d := &pipeDialer{opts: s.serveOptions}
	d.setServe(answeringServe(Approve, func([]byte, func(any), func()) {})) // never answers
	m := NewManager(Config{Hosts: []Host{approveHost()}}, Options{
		Version: "v0.2.0", StateDir: s.stateDir, EventsPath: s.localEvts, Dial: d.dial, SendWait: 200 * time.Millisecond, CloseWait: 200 * time.Millisecond,
	})
	m.Start()
	defer m.Close()
	waitFor(t, "live", func() bool { return state(m).State == Live })
	if err := m.Send("b", sidA, "hi"); err == nil || !strings.Contains(err.Error(), "did not answer") || !strings.Contains(err.Error(), "not known") {
		t.Errorf("timeout: %v", err)
	}
}

func TestManagerFlagsAResultForNothingOpenAndSanitisesTheError(t *testing.T) {
	s := newScratch(t)
	d := &pipeDialer{opts: s.serveOptions}
	d.serve = answeringServe(Approve, func(line []byte, out func(any), end func()) {
		var c SendCmd
		json.Unmarshal(line, &c)
		out(Result{Type: TypeResult, ID: "not-an-open-id", OK: true})
		out(Result{Type: TypeResult, ID: c.ID, Error: "\x1b]52;c;SECRET\x07nope"})
	})
	m := NewManager(Config{Hosts: []Host{approveHost()}}, Options{Version: "v0.2.0", StateDir: s.stateDir, EventsPath: s.localEvts, Dial: d.dial})
	m.Start()
	defer m.Close()
	waitFor(t, "live", func() bool { return state(m).State == Live })
	err := m.Send("b", sidA, "hi")
	if err == nil || strings.ContainsAny(err.Error(), "\x1b\x07") {
		t.Errorf("error = %q", err)
	}
	waitFor(t, "the flag", func() bool {
		for _, f := range state(m).Flags {
			if strings.Contains(f, "result for a request that is not open") {
				return true
			}
		}
		return false
	})
}

func TestSnapshotCarriesDirtyOnlyUnderApprove(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	repo := filepath.Join(t.TempDir(), "w")
	os.MkdirAll(repo, 0o755)
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "u@example.com"}, {"config", "user.name", "u"}} {
		if out, err := exec.Command(git, append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Skipf("git %v: %v %s", args, err, out)
		}
	}
	os.WriteFile(filepath.Join(repo, "SECRET-file.txt"), []byte("x"), 0o644)

	for _, c := range []struct {
		policy Policy
		want   string
	}{{Approve, "true"}, {Observe, "nil"}} {
		s := newScratch(t)
		now := time.Now()
		sess := fmt.Sprintf(`{"pid":%d,"sessionId":%q,"cwd":%q,"startedAt":%d,"status":"idle","statusUpdatedAt":%d,"version":"2.1.300"}`,
			os.Getpid(), sidA, repo, now.Add(-time.Hour).UnixMilli(), now.Add(-time.Minute).UnixMilli())
		os.WriteFile(filepath.Join(s.cfg, "sessions", "1.json"), []byte(sess), 0o600)
		sv, _ := serveDirect(t, s, Request{Policy: c.policy, Labels: true}, func(o *ServeOptions) { o.SnapshotEvery = 100 * time.Millisecond })
		seen := "nil"
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) && seen == "nil" && c.policy == Approve {
			var snap Snapshot
			json.Unmarshal(sv.next(TypeSnapshot), &snap)
			if len(snap.Agents) == 1 && snap.Agents[0].Dirty != nil {
				seen = fmt.Sprint(*snap.Agents[0].Dirty)
			}
		}
		if c.policy == Observe {
			var snap Snapshot
			json.Unmarshal(sv.next(TypeSnapshot), &snap)
			if len(snap.Agents) == 1 && snap.Agents[0].Dirty != nil {
				seen = "set"
			}
		}
		if seen != c.want {
			t.Errorf("under %s the snapshot's dirty = %s, want %s", c.policy, seen, c.want)
		}
	}
}

// A text that fits MaxSendText but whose escaped form passes MaxLine is
// reported as too large, not as the host being offline.
func TestManagerSendTooLargeOnceEncodedIsNotOffline(t *testing.T) {
	s := newScratch(t)
	d := &pipeDialer{opts: s.serveOptions}
	d.setServe(answeringServe(Approve, func([]byte, func(any), func()) {}))
	m := NewManager(Config{Hosts: []Host{approveHost()}}, Options{Version: "v0.2.0", StateDir: s.stateDir, EventsPath: s.localEvts, Dial: d.dial})
	m.Start()
	defer m.Close()
	waitFor(t, "live", func() bool { return state(m).State == Live })
	err := m.Send("b", sidA, strings.Repeat("\x01", MaxSendText)) // each byte escapes to six
	if err == nil || !strings.Contains(err.Error(), "too large") || strings.Contains(err.Error(), "offline") {
		t.Errorf("err = %v", err)
	}
}
