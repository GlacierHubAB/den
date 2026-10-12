package remote_test

// The acceptance tests for answering remote requests, run for real over a
// private sshd: `den hook` on host b holds a PermissionRequest at
// den-remote.sock, `den remote serve` relays it, and the Manager under test
// answers it. The hooks run the test-built den in host b's environment
// directly (host b is this machine), so their output and timing are the hook's
// own.

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/remote"
	"github.com/GlacierHubAB/den/internal/sshtest"
)

// hookOut is what `den hook` printed and how long it took.
type hookOut struct {
	stdout string
	dur    time.Duration
	err    error
}

// approveRig is a rig whose host b is configured approve.
func approveRig(t *testing.T) *rig {
	t.Helper()
	g := newRig(t)
	g.l.Local.WriteRemotes(g.l.RemotesTOML("approve"))
	return g
}

// hold runs `den hook` on host b for a PermissionRequest, as Claude Code
// would, and returns what it prints once it exits.
func (g *rig) hold(session, tool string, input map[string]any) <-chan hookOut {
	g.t.Helper()
	b, _ := json.Marshal(map[string]any{"session_id": session, "hook_event_name": "PermissionRequest",
		"tool_name": tool, "tool_input": input, "cwd": "/home/user/proj"})
	out := make(chan hookOut, 1)
	go func() {
		c := exec.Command(g.l.Den, "hook")
		c.Dir, c.Env, c.Stdin = g.r.Home, g.r.Env(), strings.NewReader(string(b))
		start := time.Now()
		stdout, err := c.Output()
		out <- hookOut{string(stdout), time.Since(start), err}
	}()
	return out
}

func (g *rig) hookDone(c <-chan hookOut, within time.Duration) hookOut {
	g.t.Helper()
	select {
	case o := <-c:
		if o.err != nil {
			g.t.Errorf("den hook exited with %v (it must always exit 0)", o.err)
		}
		return o
	case <-time.After(within):
		g.t.Fatalf("den hook did not return within %v", within)
	}
	return hookOut{}
}

// held waits for the n-th HeldArrived update (from 1).
func (g *rig) held(n int) *bridge.Request {
	g.t.Helper()
	var r *bridge.Request
	g.wait("a held request", 10*time.Second, func() bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		i := 0
		for _, u := range g.ups {
			if u.Kind == remote.HeldArrived {
				if i++; i == n {
					r = u.Request
					return true
				}
			}
		}
		return false
	})
	return r
}

func (g *rig) count(kind remote.UpdateKind) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, u := range g.ups {
		if u.Kind == kind {
			n++
		}
	}
	return n
}

// decision reads the behavior a hook printed, "" for nothing.
func decision(stdout string) string {
	var out struct {
		HookSpecificOutput struct {
			Decision struct{ Behavior, Message string }
		}
	}
	json.Unmarshal([]byte(stdout), &out)
	return out.HookSpecificOutput.Decision.Behavior
}

func TestAcceptS2AllowAndDeny(t *testing.T) {
	g := approveRig(t)
	g.start()
	g.live()
	if st := g.status(); st.Applied != remote.Approve {
		t.Fatalf("applied = %q", st.Applied)
	}
	c := g.hold(idA, "Bash", map[string]any{"command": "go test ./..."})
	r := g.held(1)
	if r.Host != "b" || r.Input.ToolName != "Bash" || !strings.Contains(string(r.Input.ToolInput), "go test ./...") {
		t.Fatalf("request = %+v %s", r, r.Input.ToolInput)
	}
	r.Reply(bridge.Decision{Behavior: "allow"})
	o := g.hookDone(c, 5*time.Second)
	if decision(o.stdout) != "allow" {
		t.Errorf("allow: hook printed %q", o.stdout)
	}
	t.Logf("held request answered across ssh in %v", o.dur.Round(time.Millisecond))

	c = g.hold(idA, "Edit", map[string]any{"file_path": "/home/user/proj/a.go", "old_string": "a", "new_string": "b"})
	g.held(2).Reply(bridge.Decision{Behavior: "deny", Message: "Denied from den by the user."})
	if o := g.hookDone(c, 5*time.Second); decision(o.stdout) != "deny" || !strings.Contains(o.stdout, "Denied from den") {
		t.Errorf("deny: hook printed %q", o.stdout)
	}
}

// The local ssh client dies mid-wait: sshd sees the pipe close, serve its
// stdin EOF, and the hook fails open (exit 0, nothing printed) within ~2 s.
// Den lets the item go as "link lost".
func TestAcceptS2DropMidWaitFailsOpen(t *testing.T) {
	g := approveRig(t)
	g.start()
	g.live()
	c := g.hold(idA, "Bash", map[string]any{"command": "sleep 1"})
	r := g.held(1)
	start := time.Now()
	g.l.KillSSH()
	o := g.hookDone(c, 5*time.Second)
	if o.stdout != "" {
		t.Errorf("hook printed %q after the drop", o.stdout)
	}
	if d := time.Since(start); d > 2500*time.Millisecond {
		t.Errorf("fail open took %v", d)
	}
	t.Logf("hook failed open %v after the drop", time.Since(start).Round(time.Millisecond))
	g.wait("released", 5*time.Second, func() bool { return r.HungUp() })
	if !strings.Contains(r.Why(), "link lost") {
		t.Errorf("why = %q", r.Why())
	}
	g.wait("socket removed", 5*time.Second, func() bool {
		_, err := os.Lstat(g.r.RemoteSock)
		return os.IsNotExist(err)
	})
}

// Five hooks held at once through one ssh session,
// answered out of order, each get their own; a drop then releases five
// more within a second.
func TestAcceptS2FiveConcurrentOutOfOrderAndDrop(t *testing.T) {
	g := approveRig(t)
	g.start()
	g.live()
	words := []string{"one", "two", "three", "four", "five"}
	hooks := map[string]<-chan hookOut{}
	for _, w := range words {
		hooks[w] = g.hold(idA, "Bash", map[string]any{"command": "echo " + w})
	}
	reqs := map[string]*bridge.Request{}
	for i := range words {
		r := g.held(i + 1)
		var in struct{ Command string }
		json.Unmarshal(r.Input.ToolInput, &in)
		reqs[strings.TrimPrefix(in.Command, "echo ")] = r
	}
	for i, w := range []string{"three", "five", "one", "four", "two"} {
		b := "allow"
		if i%2 == 1 {
			b = "deny"
		}
		reqs[w].Reply(bridge.Decision{Behavior: b, Message: "for " + w})
	}
	for _, w := range words {
		o := g.hookDone(hooks[w], 5*time.Second)
		if !strings.Contains(o.stdout, "for "+w) {
			t.Errorf("%s printed %q", w, o.stdout)
		}
	}
	for _, w := range words {
		hooks[w] = g.hold(idB, "Bash", map[string]any{"command": "echo " + w})
	}
	g.held(10)
	start := time.Now()
	g.l.KillSSH()
	for _, w := range words {
		if o := g.hookDone(hooks[w], 3*time.Second); o.stdout != "" {
			t.Errorf("%s printed %q after the drop", w, o.stdout)
		}
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("releasing five held hooks took %v", d)
	}
	g.wait("all released locally", 3*time.Second, func() bool { return g.count(remote.HeldReleased) >= 5 })
}

// A second laptop's Den is refused: it observes ("attached elsewhere",
// naming the serve's pid) and never gets the held request.
func TestAcceptS2SecondControllerRefused(t *testing.T) {
	g := approveRig(t)
	g.start()
	g.live()
	cfg, err := remote.Load()
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(g.l.Dir, "laptop2")
	m2 := remote.NewManager(cfg, remote.Options{Version: sshtest.Version()[len("den "):], StateDir: other,
		EventsPath: filepath.Join(other, "events.jsonl"), Backoff: 300 * time.Millisecond})
	got2 := make(chan *bridge.Request, 8)
	go func() {
		for u := range m2.Updates() {
			if u.Kind == remote.HeldArrived {
				got2 <- u.Request
			}
		}
	}()
	m2.Start()
	defer m2.Close()
	if !sshtest.WaitFor(15*time.Second, func() bool { return m2.Hosts()[0].State == remote.AttachedElsewhere }) {
		t.Fatalf("second Den: %+v", m2.Hosts()[0])
	}
	st := m2.Hosts()[0]
	if st.Applied != remote.Observe || !strings.Contains(st.Reason, "pid") {
		t.Errorf("second Den's host = %+v", st)
	}
	t.Logf("second Den: %s", st.Reason)
	c := g.hold(idA, "Bash", map[string]any{"command": "ls"})
	g.held(1).Reply(bridge.Decision{Behavior: "allow"})
	if o := g.hookDone(c, 5*time.Second); decision(o.stdout) != "allow" {
		t.Errorf("hook printed %q", o.stdout)
	}
	select {
	case r := <-got2:
		t.Errorf("the second Den got %+v", r)
	default:
	}
}

// The link dies half-open: the old serve never hears EOF and still holds
// the hook. Den reconnects; the new serve takes the held hook over and
// offers it again with the same id; the answer through the new
// connection reaches the hook.
func TestAcceptS2ReconnectOffersAgainWithTheSameID(t *testing.T) {
	g := approveRig(t)
	g.start()
	g.live()
	c := g.hold(idA, "Bash", map[string]any{"command": "make release"})
	first := g.held(1)
	g.r.HalfOpen()
	g.l.KillSSH()
	g.wait("released locally", 5*time.Second, func() bool { return first.HungUp() })
	again := g.held(2)
	if again.RemoteID != first.RemoteID || again == first {
		t.Fatalf("offered again as %q, was %q", again.RemoteID, first.RemoteID)
	}
	first.Reply(bridge.Decision{Behavior: "allow"}) // the old connection is dead: goes nowhere
	again.Reply(bridge.Decision{Behavior: "deny", Message: "no"})
	if o := g.hookDone(c, 5*time.Second); decision(o.stdout) != "deny" {
		t.Errorf("hook printed %q", o.stdout)
	}
	log, _ := os.ReadFile(filepath.Join(g.r.StateHome, "den", "remote-serve.log"))
	if !strings.Contains(string(log), "handed 1 held requests over") || !strings.Contains(string(log), "took over 1 held requests") {
		t.Errorf("no handover in the serve log:\n%s", log)
	}
	g.r.Thaw()
}

// A Den running on host b takes precedence: the hook asks it (den.sock)
// and never reaches den-remote.sock.
func TestAcceptS2DenOnTheHostTakesPrecedence(t *testing.T) {
	g := approveRig(t)
	g.start()
	g.live()
	ev := exec.Command(g.l.Den, "events")
	ev.Dir, ev.Env = g.r.Home, append(g.r.Env(), "DEN_EVENTS_DECISION=deny")
	ev.Stdout, ev.Stderr = io.Discard, io.Discard
	if err := ev.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { ev.Process.Kill(); ev.Wait() }()
	g.wait("den events on host b", 5*time.Second, func() bool { _, err := os.Stat(g.r.Socket); return err == nil })
	o := g.hookDone(g.hold(idA, "Bash", map[string]any{"command": "ls"}), 5*time.Second)
	if decision(o.stdout) != "deny" {
		t.Errorf("hook printed %q, want the host's own Den's deny", o.stdout)
	}
	time.Sleep(300 * time.Millisecond)
	if n := g.count(remote.HeldArrived); n != 0 {
		t.Errorf("%d requests reached the remote Den", n)
	}
}

// An AskUserQuestion answered in Den reaches Claude Code as the hook's
// updatedInput, keyed by the question as asked. The host must allow it.
func TestAcceptS2AskUserQuestionRoundTrip(t *testing.T) {
	g := approveRig(t)
	g.r.WriteFile(".config/den/config.toml", "[hooks]\nanswer_questions = true\n")
	g.start()
	g.live()
	input := map[string]any{"questions": []map[string]any{
		{"question": "Which colour scheme?", "header": "Colours", "options": []map[string]string{{"label": "Warm"}, {"label": "Cool"}}},
	}}
	c := g.hold(idA, "AskUserQuestion", input)
	r := g.held(1)
	r.Reply(bridge.Decision{Behavior: "allow", Answers: map[string]string{"Which colour scheme?": "Cool"}})
	o := g.hookDone(c, 5*time.Second)
	if !strings.Contains(o.stdout, `"Which colour scheme?":"Cool"`) || decision(o.stdout) != "allow" {
		t.Errorf("hook printed %q", o.stdout)
	}
}

// serve driven by hand over ssh, as a Den that misbehaves would: a
// replayed decision and one for another body are dropped and logged.
func TestAcceptS2ReplayAndHashMismatchOverSSH(t *testing.T) {
	g := approveRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := g.r.Cmd(ctx, "den remote serve --proto 1")
	in, _ := cmd.StdinPipe()
	outp, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { in.Close(); cmd.Wait() }()
	send := func(v any) { b, _ := json.Marshal(v); in.Write(append(b, '\n')) }
	send(remote.Request{Type: remote.TypeHello, Proto: remote.Proto, Policy: remote.Approve, Controller: "cccccccccccccccccccccccccccccccc"})
	msgs := make(chan []byte, 64)
	go func() {
		sc := bufio.NewScanner(outp)
		sc.Buffer(nil, remote.MaxLine+1)
		for sc.Scan() {
			msgs <- append([]byte(nil), sc.Bytes()...)
		}
		close(msgs)
	}()
	nextHeld := func() remote.Held {
		for {
			select {
			case line, ok := <-msgs:
				if !ok {
					t.Fatal("serve ended")
				}
				var h remote.Held
				if json.Unmarshal(line, &h) == nil && h.Type == remote.TypeHeld {
					return h
				}
			case <-time.After(10 * time.Second):
				t.Fatal("no held request")
			}
		}
	}
	g.wait("den-remote.sock", 10*time.Second, func() bool { _, err := os.Stat(g.r.RemoteSock); return err == nil })
	c := g.hold(idA, "Bash", map[string]any{"command": "deploy"})
	h := nextHeld()
	send(remote.Decision{Type: remote.TypeDecision, ID: h.ID, Hash: remote.BodyHash([]byte(`{"command":"ls"}`)), Behavior: "allow"})
	send(remote.Ping{Type: remote.TypePing})
	select {
	case o := <-c:
		t.Fatalf("a decision for another body counted: %q", o.stdout)
	case <-time.After(500 * time.Millisecond):
	}
	send(remote.Decision{Type: remote.TypeDecision, ID: h.ID, Hash: remote.BodyHash(h.Body), Behavior: "deny"})
	if o := g.hookDone(c, 5*time.Second); decision(o.stdout) != "deny" {
		t.Fatalf("hook printed %q", o.stdout)
	}
	c = g.hold(idA, "Bash", map[string]any{"command": "deploy"})
	h2 := nextHeld()
	send(remote.Decision{Type: remote.TypeDecision, ID: h.ID, Hash: remote.BodyHash(h.Body), Behavior: "allow"}) // replay
	select {
	case o := <-c:
		t.Fatalf("a replayed decision counted: %q", o.stdout)
	case <-time.After(500 * time.Millisecond):
	}
	send(remote.Decision{Type: remote.TypeDecision, ID: h2.ID, Hash: remote.BodyHash(h2.Body), Behavior: "allow"})
	if o := g.hookDone(c, 5*time.Second); decision(o.stdout) != "allow" {
		t.Errorf("hook printed %q", o.stdout)
	}
	log, _ := os.ReadFile(filepath.Join(g.r.StateHome, "den", "remote-serve.log"))
	if !strings.Contains(string(log), "body hash that does not match") || !strings.Contains(string(log), "an id that is not open") {
		t.Errorf("serve log:\n%s", log)
	}
	if strings.Contains(string(log), "deploy") {
		t.Errorf("the log carries a command:\n%s", log)
	}
}
