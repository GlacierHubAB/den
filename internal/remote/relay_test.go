package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/inbox"
)

// The acceptance tests for answering remote requests over io.Pipe: serve runs
// in this process with its den-remote.sock in a scratch dir, and "hooks" are
// bridge.AskFirst calls, as `den hook` makes them.

const (
	sidA = "11111111-2222-4333-8444-555555555555"
	sidB = "11111111-2222-4333-8444-666666666666"
	ctlA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	ctlB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// hookResult is what a held hook got back.
type hookResult struct {
	d   bridge.Decision
	ok  bool
	dur time.Duration
}

// hook asks as `den hook` would for a PermissionRequest, with no Den on
// this "host" (den.sock is absent), and reports what it got.
func hook(t *testing.T, sock, session, tool string, input any) <-chan hookResult {
	t.Helper()
	return hookWait(t, sock, session, tool, input, 20*time.Second)
}

// hookWait is hook with the hook's own wait, after which it hangs up.
func hookWait(t *testing.T, sock, session, tool string, input any, wait time.Duration) <-chan hookResult {
	t.Helper()
	raw, _ := json.Marshal(input)
	payload, _ := json.Marshal(map[string]any{"session_id": session, "hook_event_name": "PermissionRequest",
		"tool_name": tool, "tool_input": json.RawMessage(raw), "cwd": "/home/user/SECRET-cwd", "transcript_path": "/home/user/SECRET-transcript"})
	env := bridge.Envelope{Seq: time.Now().UnixNano(), Event: "PermissionRequest", SessionID: session, Payload: payload}
	out := make(chan hookResult, 1)
	go func() {
		start := time.Now()
		d, ok := bridge.AskFirst(filepath.Join(filepath.Dir(sock), "den.sock"), sock, env, wait)
		out <- hookResult{d, ok, time.Since(start)}
	}()
	return out
}

func got(t *testing.T, c <-chan hookResult, within time.Duration) hookResult {
	t.Helper()
	select {
	case r := <-c:
		return r
	case <-time.After(within):
		t.Fatalf("the hook did not return within %v", within)
	}
	return hookResult{}
}

// logs collects serve's relay log.
type logs struct {
	mu    sync.Mutex
	lines []string
}

func (l *logs) add(s string) {
	l.mu.Lock()
	l.lines = append(l.lines, s)
	l.mu.Unlock()
}

func (l *logs) has(sub string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range l.lines {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// served is one serve in this process, driven by the test as Den.
type served struct {
	t    *testing.T
	in   *io.PipeWriter
	msgs chan []byte
	errc chan error
	log  *logs
	stop context.CancelFunc
}

// serveDirect starts Serve with the request req and reads hello.
func serveDirect(t *testing.T, s *scratch, req Request, mod func(*ServeOptions)) (*served, Hello) {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	o := s.serveOptions()
	sv := &served{t: t, in: inW, msgs: make(chan []byte, 256), errc: make(chan error, 1), log: &logs{}}
	o.Log = sv.log.add
	no := false
	o.Questions = &no
	if mod != nil {
		mod(&o)
	}
	ctx, cancel := context.WithCancel(context.Background())
	sv.stop = cancel
	go func() { sv.errc <- Serve(ctx, inR, outW, o); outW.Close() }()
	go func() {
		lr := newLineReader(outR, MaxLine)
		for {
			line, err := lr.next()
			if err != nil {
				close(sv.msgs)
				return
			}
			sv.msgs <- line
		}
	}()
	t.Cleanup(func() { cancel(); inW.Close() })
	if req.Type == "" {
		req.Type, req.Proto = TypeHello, Proto
	}
	writeMsg(inW, req)
	var h Hello
	json.Unmarshal(sv.next(TypeHello), &h)
	return sv, h
}

// next returns the next message of type typ, skipping snapshots and
// events; it fails on any other type.
func (sv *served) next(typ string) []byte {
	sv.t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case line, ok := <-sv.msgs:
			if !ok {
				sv.t.Fatalf("serve ended waiting for %s", typ)
			}
			got, _, _ := peek(line)
			if got == typ {
				return line
			}
			if got != TypeSnapshot && got != TypeEvent {
				sv.t.Fatalf("got %s waiting for %s: %s", got, typ, line)
			}
		case <-deadline:
			sv.t.Fatalf("no %s from serve", typ)
		}
	}
}

func (sv *served) held() Held {
	sv.t.Helper()
	var h Held
	json.Unmarshal(sv.next(TypeHeld), &h)
	return h
}

func (sv *served) decide(h Held, behavior string) {
	writeMsg(sv.in, Decision{Type: TypeDecision, ID: h.ID, Hash: BodyHash(h.Body), Behavior: behavior, Message: "from the test"})
}

func approveHost() Host {
	return Host{Label: "b", Target: "b", Policy: Approve, Labels: true, Enabled: true}
}

func TestRelayAllowAndDenyRoundTrip(t *testing.T) {
	s := newScratch(t)
	sv, hello := serveDirect(t, s, Request{Policy: Full, Controller: ctlA}, nil)
	if hello.Policy != Full || hello.Attached != nil {
		t.Fatalf("hello = %+v", hello)
	}
	if st, err := os.Stat(s.sock); err != nil || st.Mode().Perm() != 0o600 || st.Mode()&os.ModeSocket == 0 {
		t.Fatalf("den-remote.sock = %v, %v", st, err)
	}
	allow := hook(t, s.sock, sidA, "Bash", map[string]any{"command": "go test ./...", "description": "Run the tests", "timeout": 5})
	h := sv.held()
	if h.Session != sidA || h.Tool != "Bash" || !isID(h.ID) || string(h.Body) != `{"command":"go test ./...","description":"Run the tests"}` {
		t.Fatalf("held = %+v %s", h, h.Body)
	}
	sv.decide(h, "allow")
	if r := got(t, allow, 2*time.Second); !r.ok || r.d.Behavior != "allow" {
		t.Errorf("allow: %+v", r)
	}
	deny := hook(t, s.sock, sidA, "Write", map[string]any{"file_path": "/home/user/proj/x.go", "content": "package x\n"})
	h = sv.held()
	sv.decide(h, "deny")
	if r := got(t, deny, 2*time.Second); !r.ok || r.d.Behavior != "deny" || r.d.Message != "from the test" {
		t.Errorf("deny: %+v", r)
	}
	// Nothing of the hook input outside the shown fields left the host.
	if bytes.Contains(h.Body, []byte("SECRET")) {
		t.Errorf("body carries more than the shown fields: %s", h.Body)
	}
}

func TestRelayReplayedDecisionIsIgnored(t *testing.T) {
	s := newScratch(t)
	sv, _ := serveDirect(t, s, Request{Policy: Approve, Controller: ctlA}, nil)
	first := hook(t, s.sock, sidA, "Bash", map[string]any{"command": "make"})
	h := sv.held()
	sv.decide(h, "deny")
	got(t, first, 2*time.Second)
	// The same decision again, then for the next request with the old id.
	second := hook(t, s.sock, sidA, "Bash", map[string]any{"command": "make"})
	h2 := sv.held()
	if h2.ID == h.ID {
		t.Fatal("two requests share an id")
	}
	sv.decide(h, "allow")
	waitFor(t, "the replay logged", func() bool { return sv.log.has("an id that is not open") })
	select {
	case r := <-second:
		t.Fatalf("a replayed decision answered another request: %+v", r)
	case <-time.After(200 * time.Millisecond):
	}
	sv.decide(h2, "allow")
	if r := got(t, second, 2*time.Second); r.d.Behavior != "allow" {
		t.Errorf("second = %+v", r)
	}
}

func TestRelayBodyHashMismatchIsRefused(t *testing.T) {
	s := newScratch(t)
	sv, _ := serveDirect(t, s, Request{Policy: Approve, Controller: ctlA}, nil)
	c := hook(t, s.sock, sidA, "Bash", map[string]any{"command": "rm -rf build"})
	h := sv.held()
	other := BodyHash([]byte(`{"command":"echo harmless"}`))
	writeMsg(sv.in, Decision{Type: TypeDecision, ID: h.ID, Hash: other, Behavior: "allow"})
	waitFor(t, "the mismatch logged", func() bool { return sv.log.has("body hash that does not match") })
	select {
	case r := <-c:
		t.Fatalf("a decision for another body counted: %+v", r)
	case <-time.After(200 * time.Millisecond):
	}
	// The request stays open for a decision on the body that was shown.
	sv.decide(h, "allow")
	if r := got(t, c, 2*time.Second); r.d.Behavior != "allow" {
		t.Errorf("after mismatch: %+v", r)
	}
}

func TestRelayDropMidWaitFailsOpen(t *testing.T) {
	s := newScratch(t)
	sv, _ := serveDirect(t, s, Request{Policy: Approve, Controller: ctlA}, nil)
	c := hook(t, s.sock, sidA, "Bash", map[string]any{"command": "sleep 1"})
	sv.held()
	start := time.Now()
	sv.in.Close() // the link drops: serve sees stdin EOF
	r := got(t, c, 3*time.Second)
	if r.ok {
		t.Errorf("the hook got a decision after the drop: %+v", r)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("fail open took %v", d)
	}
	if _, err := os.Lstat(s.sock); !os.IsNotExist(err) {
		t.Errorf("den-remote.sock left behind: %v", err)
	}
}

func TestRelaySilentDenFailsOpen(t *testing.T) {
	s := newScratch(t)
	_, _ = serveDirect(t, s, Request{Policy: Approve, Controller: ctlA}, func(o *ServeOptions) { o.Silence = 400 * time.Millisecond })
	c := hook(t, s.sock, sidA, "Bash", map[string]any{"command": "true"})
	if r := got(t, c, 3*time.Second); r.ok {
		t.Errorf("the hook got a decision from a silent Den: %+v", r)
	}
}

func TestRelayUnderObserveDoesNotHold(t *testing.T) {
	s := newScratch(t)
	sv, hello := serveDirect(t, s, Request{Policy: Observe, Controller: ctlA}, nil)
	if hello.Policy != Observe {
		t.Fatalf("hello = %+v", hello)
	}
	r := got(t, hook(t, s.sock, sidA, "Bash", map[string]any{"command": "ls"}), time.Second)
	if r.ok || r.dur > 100*time.Millisecond {
		t.Errorf("observe: %+v", r)
	}
	select {
	case line := <-sv.msgs:
		if typ, _, _ := peek(line); typ == TypeHeld {
			t.Errorf("held under observe: %s", line)
		}
	case <-time.After(100 * time.Millisecond):
	}
}

func TestRelayRefusesWhatIsNotAPermissionRequest(t *testing.T) {
	s := newScratch(t)
	sv, _ := serveDirect(t, s, Request{Policy: Approve, Controller: ctlA}, nil)
	for _, ev := range []string{bridge.CloseEvent, bridge.SkillsEvent, bridge.InstallEvent, "Stop"} {
		payload, _ := json.Marshal(map[string]any{"session_id": sidA, "hook_event_name": ev, "peers": []string{"all"}})
		c, err := net.Dial("unix", s.sock)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(bridge.Envelope{Event: ev, SessionID: sidA, Payload: payload})
		c.Write(append(b, '\n'))
		reply, _ := io.ReadAll(c)
		c.Close()
		if !strings.Contains(string(reply), "permission requests only") {
			t.Errorf("%s: reply %q", ev, reply)
		}
	}
	select {
	case line := <-sv.msgs:
		if typ, _, _ := peek(line); typ == TypeHeld {
			t.Errorf("relayed: %s", line)
		}
	case <-time.After(100 * time.Millisecond):
	}
	// Den may send only decisions and pings: anything else ends serve.
	writeMsg(sv.in, map[string]any{"type": bridge.CloseEvent, "peers": []string{"all"}})
	var e Error
	json.Unmarshal(sv.next(TypeError), &e)
	if e.Code != "request" {
		t.Errorf("error = %+v", e)
	}
}

func TestRelayAskUserQuestionRoundTrip(t *testing.T) {
	s := newScratch(t)
	sv, _ := serveDirect(t, s, Request{Policy: Approve, Controller: ctlA}, func(o *ServeOptions) { yes := true; o.Questions = &yes })
	asked := "Which colour\x1b[31m scheme?"
	input := map[string]any{"questions": []map[string]any{
		{"question": asked, "header": "Colours", "options": []map[string]string{{"label": "Warm"}, {"label": "Cool"}}},
	}}
	c := hook(t, s.sock, sidA, "AskUserQuestion", input)
	h := sv.held()
	if bytes.Contains(h.Body, []byte("\\u001b")) || !checkBody(h.Tool, h.Body) {
		t.Fatalf("body not sanitised: %s", h.Body)
	}
	writeMsg(sv.in, Decision{Type: TypeDecision, ID: h.ID, Hash: BodyHash(h.Body), Behavior: "allow",
		Answers: map[string]string{"Which colour scheme?": "Cool"}})
	r := got(t, c, 2*time.Second)
	if r.d.Behavior != "allow" || r.d.Answers[asked] != "Cool" {
		t.Errorf("answers went back as %+v", r.d)
	}
}

func TestRelayQuestionNotHeldWhenTheHostDoesNotAnswerQuestions(t *testing.T) {
	s := newScratch(t)
	serveDirect(t, s, Request{Policy: Approve, Controller: ctlA}, nil)
	input := map[string]any{"questions": []map[string]any{{"question": "Which?", "options": []map[string]string{{"label": "A"}}}}}
	if r := got(t, hook(t, s.sock, sidA, "AskUserQuestion", input), time.Second); !r.ok || r.d.Behavior != "" {
		t.Errorf("question with answer_questions off: %+v", r)
	}
}

func TestRelayCommandThatSanitisingWouldChangeIsNotRelayed(t *testing.T) {
	s := newScratch(t)
	serveDirect(t, s, Request{Policy: Approve, Controller: ctlA}, nil)
	r := got(t, hook(t, s.sock, sidA, "Bash", map[string]any{"command": "echo ok\x1b[2K; rm -rf ~"}), time.Second)
	if !r.ok || r.d.Behavior != "" {
		t.Errorf("a command with escapes was held: %+v", r)
	}
}

func TestRelaySecondControllerIsRefused(t *testing.T) {
	s := newScratch(t)
	first, _ := serveDirect(t, s, Request{Policy: Approve, Controller: ctlA}, nil)
	second, hello := serveDirect(t, s, Request{Policy: Approve, Controller: ctlB}, nil)
	if hello.Policy != Observe || hello.Attached == nil || hello.Attached.Pid != os.Getpid() || hello.Attached.Since.IsZero() {
		t.Fatalf("second hello = %+v", hello)
	}
	c := hook(t, s.sock, sidA, "Bash", map[string]any{"command": "ls"})
	h := first.held()
	// The second Den's decision goes nowhere: it holds nothing.
	second.decide(h, "allow")
	waitFor(t, "the second's decision dropped", func() bool { return second.log.has("nothing is relayed") })
	first.decide(h, "deny")
	if r := got(t, c, 2*time.Second); r.d.Behavior != "deny" {
		t.Errorf("hook = %+v", r)
	}
}

func TestRelayHandoverToTheSameDen(t *testing.T) {
	s := newScratch(t)
	old, _ := serveDirect(t, s, Request{Policy: Approve, Controller: ctlA}, nil)
	c := hook(t, s.sock, sidA, "Bash", map[string]any{"command": "go build ./..."})
	h := old.held()
	// The link died half-open: the old serve still holds the hook. The same
	// Den reconnects.
	fresh, hello := serveDirect(t, s, Request{Policy: Approve, Controller: ctlA}, nil)
	if hello.Policy != Approve || hello.Attached != nil {
		t.Fatalf("hello = %+v", hello)
	}
	again := fresh.held()
	if again.ID != h.ID || BodyHash(again.Body) != BodyHash(h.Body) {
		t.Fatalf("offered again as %+v, was %+v", again, h)
	}
	select {
	case err := <-old.errc:
		if err == nil || !strings.Contains(err.Error(), "handed over") {
			t.Errorf("old serve ended with %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the old serve did not end after handing over")
	}
	// A decision through the old connection is dead; the new one counts.
	old.decide(h, "allow")
	fresh.decide(again, "deny")
	if r := got(t, c, 2*time.Second); r.d.Behavior != "deny" {
		t.Errorf("hook = %+v", r)
	}
	// New hooks reach the new serve on the same path.
	c2 := hook(t, s.sock, sidB, "Bash", map[string]any{"command": "ls"})
	fresh.decide(fresh.held(), "allow")
	if r := got(t, c2, 2*time.Second); r.d.Behavior != "allow" {
		t.Errorf("after handover: %+v", r)
	}
}

// A handover that goes wrong half way fails open: the claimant closes
// every descriptor it was passed and starts clean on the path.
func TestRelayFailedHandoverFailsOpen(t *testing.T) {
	s := newScratch(t)
	// A fake holder: it takes one hook, then answers a claim with that
	// hook's descriptor and no list of requests.
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: s.sock, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ln.SetUnlinkOnClose(false)
	c := hook(t, s.sock, sidA, "Bash", map[string]any{"command": "ls"})
	hookConn, err := ln.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	newLineReader(hookConn, MaxHookLine).next()
	go func() {
		claimant, err := ln.AcceptUnix()
		if err != nil {
			return
		}
		newLineReader(claimant, MaxLine).next()
		f, _ := hookConn.File()
		claimant.WriteMsgUnix([]byte{'H'}, syscall.UnixRights(int(f.Fd())), nil)
		f.Close()
		hookConn.Close()
		claimant.Write([]byte("not json\n"))
		claimant.Close()
		ln.Close()
	}()
	sv, hello := serveDirect(t, s, Request{Policy: Approve, Controller: ctlA}, nil)
	if hello.Policy != Approve {
		t.Fatalf("hello = %+v", hello)
	}
	if r := got(t, c, 2*time.Second); r.ok {
		t.Errorf("the hook passed in a failed handover got %+v", r)
	}
	if !sv.log.has("handover failed") {
		t.Error("the failed handover was not logged")
	}
	c2 := hook(t, s.sock, sidB, "Bash", map[string]any{"command": "ls"})
	sv.decide(sv.held(), "allow")
	if r := got(t, c2, 2*time.Second); r.d.Behavior != "allow" {
		t.Errorf("the clean start does not relay: %+v", r)
	}
}

// managed is a Manager attached to an in-process serve over io.Pipe, with
// a reader of its updates.
type managed struct {
	m    *Manager
	d    *pipeDialer
	held chan *bridge.Request
	gone chan *bridge.Request
}

func manage(t *testing.T, s *scratch, h Host, controller string, mod func(*ServeOptions)) *managed {
	t.Helper()
	d := &pipeDialer{opts: func() ServeOptions {
		o := s.serveOptions()
		o.Log = func(string) {}
		no := false
		o.Questions = &no
		if mod != nil {
			mod(&o)
		}
		return o
	}}
	m := NewManager(Config{Hosts: []Host{h}}, Options{
		Version: "v0.2.0", StateDir: s.stateDir, EventsPath: s.localEvts, Dial: d.dial, Controller: controller,
		Backoff: 20 * time.Millisecond, MaxBackoff: 100 * time.Millisecond, Healthy: time.Second,
	})
	g := &managed{m: m, d: d, held: make(chan *bridge.Request, 64), gone: make(chan *bridge.Request, 64)}
	go func() {
		for u := range m.Updates() {
			switch u.Kind {
			case HeldArrived:
				g.held <- u.Request
			case HeldReleased:
				g.gone <- u.Request
			}
		}
	}()
	m.Start()
	t.Cleanup(m.Close)
	return g
}

func (g *managed) next(t *testing.T) *bridge.Request {
	t.Helper()
	select {
	case r := <-g.held:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("no held request reached Den")
	}
	return nil
}

func TestManagerRelaysAndAnswers(t *testing.T) {
	s := newScratch(t)
	g := manage(t, s, approveHost(), ctlA, nil)
	waitFor(t, "live", func() bool { return state(g.m).State == Live })
	c := hook(t, s.sock, sidA, "Bash", map[string]any{"command": "go vet ./..."})
	r := g.next(t)
	if r.Host != "b" || r.Event != "PermissionRequest" || r.Input.ToolName != "Bash" || r.SessionID != sidA || !r.WantsReply() {
		t.Fatalf("request = %+v", r)
	}
	r.Reply(bridge.Decision{Behavior: "allow"})
	if res := got(t, c, 2*time.Second); res.d.Behavior != "allow" {
		t.Errorf("hook = %+v", res)
	}
}

// Five hooks held at once, answered out of order, each get their own
// answer; then five more, and a drop releases all of them within a second
// on both sides.
func TestManagerFiveConcurrentOutOfOrderAndDrop(t *testing.T) {
	s := newScratch(t)
	g := manage(t, s, approveHost(), ctlA, nil)
	waitFor(t, "live", func() bool { return state(g.m).State == Live })
	cmds := []string{"one", "two", "three", "four", "five"}
	hooks := map[string]<-chan hookResult{}
	for _, cmd := range cmds {
		hooks[cmd] = hook(t, s.sock, sidA, "Bash", map[string]any{"command": "echo " + cmd})
	}
	reqs := map[string]*bridge.Request{}
	for range cmds {
		r := g.next(t)
		var in struct{ Command string }
		json.Unmarshal(r.Input.ToolInput, &in)
		reqs[strings.TrimPrefix(in.Command, "echo ")] = r
	}
	for _, cmd := range []string{"four", "two", "five", "one", "three"} {
		reqs[cmd].Reply(bridge.Decision{Behavior: "deny", Message: "no " + cmd})
	}
	for _, cmd := range cmds {
		if r := got(t, hooks[cmd], 2*time.Second); r.d.Message != "no "+cmd {
			t.Errorf("%s got %+v", cmd, r.d)
		}
	}

	for _, cmd := range cmds {
		hooks[cmd] = hook(t, s.sock, sidB, "Bash", map[string]any{"command": "echo " + cmd})
	}
	var held []*bridge.Request
	for range cmds {
		held = append(held, g.next(t))
	}
	start := time.Now()
	g.d.cut()
	for _, cmd := range cmds {
		if r := got(t, hooks[cmd], time.Second); r.ok {
			t.Errorf("%s got a decision after the drop: %+v", cmd, r)
		}
	}
	for range cmds {
		select {
		case <-g.gone:
		case <-time.After(time.Second):
			t.Fatal("Den did not release every held request")
		}
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("releasing 5 took %v", d)
	}
	for _, r := range held {
		if !r.HungUp() || !strings.Contains(r.Why(), "link lost") {
			t.Errorf("held request still answerable: %q", r.Why())
		}
	}
}

func TestManagerSecondControllerObserves(t *testing.T) {
	s := newScratch(t)
	first := manage(t, s, approveHost(), ctlA, nil)
	waitFor(t, "first live", func() bool { return state(first.m).State == Live })
	s2 := *s
	s2.stateDir, s2.localEvts = filepath.Join(t.TempDir(), "remote"), filepath.Join(t.TempDir(), "events.jsonl")
	second := manage(t, &s2, approveHost(), ctlB, nil)
	waitFor(t, "second attached elsewhere", func() bool { return state(second.m).State == AttachedElsewhere })
	st := state(second.m)
	if st.Applied != Observe || !strings.Contains(st.Reason, fmt.Sprintf("pid %d", os.Getpid())) {
		t.Errorf("second = %+v", st)
	}
	for _, a := range second.m.Agents() {
		if a.Stale {
			t.Error("a host attached elsewhere shows its live agents as stale")
		}
	}
	c := hook(t, s.sock, sidA, "Bash", map[string]any{"command": "ls"})
	first.next(t).Reply(bridge.Decision{Behavior: "allow"})
	select {
	case r := <-second.held:
		t.Fatalf("the second Den got a held request: %+v", r)
	default:
	}
	if r := got(t, c, 2*time.Second); r.d.Behavior != "allow" {
		t.Errorf("hook = %+v", r)
	}
}

// Den re-checks the policy: a held request from a host configured observe
// is dropped and flagged, even if the host says it applies approve.
func TestManagerDropsHeldUnderObserve(t *testing.T) {
	s := newScratch(t)
	lie := func(ctx context.Context, in io.Reader, out io.Writer) error {
		lr := newLineReader(in, MaxLine)
		lr.next()
		writeMsg(out, Hello{Type: TypeHello, Proto: Proto, Policy: Approve, RemoteNow: time.Now()})
		writeMsg(out, Held{Type: TypeHeld, ID: newID(), Session: sidA, Tool: "Bash", Body: json.RawMessage(`{"command":"ls"}`)})
		// CloseRequest-shaped input: an unknown type, and a held request
		// with extra fields naming another event.
		writeMsg(out, map[string]any{"type": bridge.CloseEvent, "peers": []string{"all"}})
		writeMsg(out, map[string]any{"type": TypeHeld, "id": newID(), "session": sidA, "tool": "Bash", "body": map[string]string{"command": "ls"}, "event": bridge.InstallEvent})
		<-ctx.Done()
		return nil
	}
	h := hostB()
	g := manage(t, s, h, ctlA, nil)
	g.d.setServe(lie)
	g.m.Reconnect("b")
	waitFor(t, "flags", func() bool {
		f := strings.Join(state(g.m).Flags, "|")
		return strings.Contains(f, "under observe") && strings.Contains(f, "unknown message type CloseRequest")
	})

	// Under approve, the extra field is flagged, and what reaches the
	// inbox is a PermissionRequest whatever the host said.
	s2 := newScratch(t)
	g2 := manage(t, s2, approveHost(), ctlA, nil)
	g2.d.setServe(lie)
	g2.m.Reconnect("b")
	r := g2.next(t)
	if r.Event != "PermissionRequest" || r.Input.HookEventName != "PermissionRequest" || r.Host != "b" {
		t.Errorf("request = %+v", r)
	}
	waitFor(t, "extra field flagged", func() bool {
		return strings.Contains(strings.Join(state(g2.m).Flags, "|"), "unknown fields in held: event")
	})
}

// /always on a remote request is the human's rule for that host and
// session only, held in the local inbox; its auto-allow still goes down
// the pipe bound to the new request's id and body.
func TestAlwaysIsScopedToTheHost(t *testing.T) {
	s := newScratch(t)
	g := manage(t, s, approveHost(), ctlA, nil)
	waitFor(t, "live", func() bool { return state(g.m).State == Live })
	store := inbox.New()
	c := hook(t, s.sock, sidA, "Bash", map[string]any{"command": "go test ./..."})
	it := store.Apply(g.next(t))
	if it == nil || it.Host != "b" || !it.Answerable() || !store.Answer(it, "always") {
		t.Fatalf("item = %+v", it)
	}
	if r := got(t, c, 2*time.Second); r.d.Behavior != "allow" {
		t.Fatalf("first = %+v", r)
	}
	c = hook(t, s.sock, sidA, "Bash", map[string]any{"command": "go test ./internal/..."})
	if it := store.Apply(g.next(t)); it != nil {
		t.Fatalf("the rule did not cover the same host and session: %+v", it)
	}
	if r := got(t, c, 2*time.Second); r.d.Behavior != "allow" {
		t.Errorf("auto-allowed = %+v", r)
	}
	// The same session id on this machine is another agent.
	payload, _ := json.Marshal(map[string]any{"session_id": sidA, "hook_event_name": "PermissionRequest", "tool_name": "Bash", "tool_input": map[string]string{"command": "go test ./x"}})
	local := bridge.TestRequest(bridge.Envelope{Event: "PermissionRequest", SessionID: sidA, Payload: payload})
	if it := store.Apply(local); it == nil {
		t.Error("devbox's rule answered a local agent")
	}
}

// After a drop, a held item is released and not answerable; Reply on it
// sends nothing.
func TestLinkLostItemIsNotAnswerable(t *testing.T) {
	s := newScratch(t)
	g := manage(t, s, approveHost(), ctlA, nil)
	waitFor(t, "live", func() bool { return state(g.m).State == Live })
	store := inbox.New()
	hook(t, s.sock, sidA, "Bash", map[string]any{"command": "ls"})
	it := store.Apply(g.next(t))
	g.d.cut()
	<-g.gone
	store.LetGo()
	if it.Answerable() || it.State != inbox.InTerminal || !strings.Contains(it.Resolved, "link lost") {
		t.Errorf("item after drop = %+v", it)
	}
	if store.Answer(it, "allow") {
		t.Error("answered a released item")
	}
}

// A hook that stops waiting (answered in the terminal, or its own wait ran
// out) makes Den let its item go; a late reply sends nothing.
func TestManagerHookGoneReleasesTheItem(t *testing.T) {
	s := newScratch(t)
	g := manage(t, s, approveHost(), ctlA, nil)
	waitFor(t, "live", func() bool { return state(g.m).State == Live })
	c := hookWait(t, s.sock, sidA, "Bash", map[string]any{"command": "ls"}, 300*time.Millisecond)
	r := g.next(t)
	got(t, c, 2*time.Second)
	select {
	case gone := <-g.gone:
		if gone != r || !strings.Contains(r.Why(), "no longer waiting on b") {
			t.Errorf("released %+v: %q", gone, r.Why())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Den did not hear the hook was gone")
	}
	r.Reply(bridge.Decision{Behavior: "allow"}) // goes nowhere, harmlessly
	if state(g.m).State != Live {
		t.Errorf("host after a late reply: %+v", state(g.m))
	}
}
