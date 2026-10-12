package remote_test

// Adversarial tests for answering remote requests, written to break the
// approval path from outside, independent of the tests its author wrote.
// Hostile hosts are stub `den` scripts that speak just enough protocol (their
// lines still go through the real Manager over a real private sshd) or
// processes that write to den-remote.sock directly; the decision path is
// driven through the real serve. "Nothing answerable that the human did not
// see in full" is checked at the Manager's edge: a request that arrives as a
// bridge.Request is something the inbox will offer.

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
	"syscall"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/remote"
	"github.com/GlacierHubAB/den/internal/sshtest"
)

// stubApprove is a fake den that says hello with the approve policy, sends
// lines, then records everything Den writes to it in stubin (in the lab).
func stubApprove(lines []string) string {
	return `#!/bin/sh
read -r _request
now=$(date -u +%Y-%m-%dT%H:%M:%S.000Z)
printf '{"type":"hello","proto":1,"den":"v0.2.0","hostname":"stub","remote_now":"%s","policy":"approve","labels":true}\n' "$now"
cat <<'STUBEOF'
` + strings.Join(lines, "\n") + `
STUBEOF
cat >> "$STUBIN"
`
}

// stubApproveRig runs the Manager against such a stub, host b under approve.
func stubApproveRig(t *testing.T, lines ...string) *rig {
	t.Helper()
	l := sshtest.New(t, "b")
	r := l.Remote("b")
	stub := filepath.Join(r.Home, "stubden")
	in := filepath.Join(l.Dir, "stubin")
	script := strings.Replace(stubApprove(lines), `"$STUBIN"`, "'"+in+"'", 1)
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	l.Local.Apply()
	l.Local.WriteRemotes(fmt.Sprintf("[hosts.b]\ntarget = \"b\"\npolicy = \"approve\"\nden_path = %q\n", stub))
	return &rig{t: t, l: l, r: r}
}

func (g *rig) stubIn() string {
	b, _ := os.ReadFile(filepath.Join(g.l.Dir, "stubin"))
	return string(b)
}

func j(v any) string { b, _ := json.Marshal(v); return string(b) }

const (
	idX = "0123456789abcdef0123456789abcdef"
	id1 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1"
	id2 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa2"
)

func held(id, session, tool string, body any) string {
	return j(map[string]any{"type": "held", "id": id, "session": session, "tool": tool, "body": body})
}

// heldIDs are the remote ids of the HeldArrived requests so far.
func (g *rig) heldIDs() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []string
	for _, u := range g.ups {
		if u.Kind == remote.HeldArrived {
			out = append(out, u.Request.RemoteID)
		}
	}
	return out
}

func (g *rig) flagged(sub string) bool {
	for _, f := range g.statusQuiet().Flags {
		if strings.Contains(f, sub) {
			return true
		}
	}
	return false
}

// A host forges held requests: malformed ids, sessions and tools with
// controls, bodies the policy does not allow, fields claiming another host.
// Only well-formed requests become something the inbox can offer, and they
// are always this host's.
func TestAdvForgedHeldRequests(t *testing.T) {
	g := stubApproveRig(t,
		held("not-hex", idA, "Bash", map[string]any{"command": "ls"}),
		held(id1, "s\u001b[2J", "Bash", map[string]any{"command": "ls"}),
		held(id1, idA, "Bash\u001b]52;c;eA==\u0007", map[string]any{"command": "ls"}),
		held(id1, idA, "Bash", map[string]any{"command": "ls \u001b[2J"}),      // sanitising would change it
		held(id1, idA, "Bash", map[string]any{"command": "a\r\nb"}),            // CRLF is normalised
		held(id1, idA, "Bash", map[string]any{"command": "ls", "cwd": "/etc"}), // a key Den does not show
		held(id1, idA, "Bash", map[string]any{"command": 7}),                   // not a string
		held(id1, idA, "Bash", "rm -rf /"),                                     // not an object
		held(id1, idA, "AskUserQuestion", map[string]any{"questions": "x"}),    // not questions
		held(id1, idA, "Bash", map[string]any{"command": "ls \u202e/etc"}),     // bidi override
		j(map[string]any{"type": "held", "id": id2, "session": idB, "tool": "Bash", // claims another host, session and controller
			"host": "c", "controller": "ffffffffffffffffffffffffffffffff", "body": map[string]any{"command": "ok"}}),
		held(id1, idA, "Read", map[string]any{"file_path": "/home/user/a.go"}), // a good one
	)
	g.start()
	g.live()
	g.wait("the good requests", 10*time.Second, func() bool { return len(g.heldIDs()) >= 2 })
	time.Sleep(500 * time.Millisecond)
	if ids := g.heldIDs(); len(ids) != 2 || ids[0] != id2 || ids[1] != id1 {
		t.Fatalf("requests that arrived: %v, want only the two well-formed ones", ids)
	}
	g.mu.Lock()
	for _, u := range g.ups {
		if u.Kind == remote.HeldArrived && u.Request.Host != "b" {
			t.Errorf("a request claims host %q", u.Request.Host)
		}
	}
	g.mu.Unlock()
	for _, want := range []string{"malformed id", "malformed session or tool", "does not allow", "unknown fields in held"} {
		if !g.flagged(want) {
			t.Errorf("not flagged: %q; flags %v", want, g.status().Flags)
		}
	}
	if st := g.status(); st.State != remote.Live {
		t.Errorf("state %s: forged requests must be dropped, not take the host down", st.State)
	}
}

// A line over the cap drops the host; a body under the cap but with deep
// nesting or many keys is refused.
func TestAdvOversizeAndDeepBodies(t *testing.T) {
	deep := "x"
	for i := 0; i < 20; i++ {
		deep = j(map[string]any{"a": json.RawMessage(strings.TrimSpace(deepOrString(deep)))})
	}
	g := stubApproveRig(t,
		held(id1, idA, "mcp__x__y", json.RawMessage(deep)),
		held(id2, idA, "mcp__x__y", map[string]any{"k": strings.Repeat("v", 20<<10)}),
	)
	g.start()
	g.live()
	time.Sleep(time.Second)
	if ids := g.heldIDs(); len(ids) != 0 {
		t.Errorf("a too deep or too large argument list became answerable: %v", ids)
	}
	g2 := stubApproveRig(t, strings.Repeat("a", 300<<10))
	g2.start()
	g2.wait("flooding", 15*time.Second, func() bool {
		st := g2.status()
		return st.State == remote.Offline && strings.Contains(st.Reason, "flooding")
	})
}

func deepOrString(s string) string {
	if strings.HasPrefix(s, "{") {
		return s
	}
	return `"` + s + `"`
}

// Messages shaped like local control requests are unknown types: dropped and
// flagged, never acted on, and the local Den's socket is never touched.
func TestAdvEnvelopeShapedMessages(t *testing.T) {
	g := stubApproveRig(t,
		j(map[string]any{"type": "CloseRequest", "session": idA, "force": true}),
		j(map[string]any{"type": "SkillsRequest", "role": "x"}),
		j(map[string]any{"type": "InstallRequest", "host": "b", "path": "/tmp/pwned"}),
		j(map[string]any{"Event": "CloseRequest", "SessionID": idA, "Payload": map[string]any{"session": idA}}),
		j(map[string]any{"type": "decision", "id": id1, "behavior": "allow"}),
		j(map[string]any{"type": "ping"}),
		j(map[string]any{"type": "hello", "proto": 1, "policy": "full"}),
	)
	g.start()
	g.live()
	g.wait("flags", 10*time.Second, func() bool { return len(g.status().Flags) >= 5 })
	for _, want := range []string{"CloseRequest", "SkillsRequest", "InstallRequest", "decision", "hello"} {
		if !g.flagged(want) {
			t.Errorf("not flagged: %q; flags %v", want, g.status().Flags)
		}
	}
	if st := g.status(); st.State != remote.Live {
		t.Errorf("state %s", st.State)
	}
	if n := len(g.heldIDs()); n != 0 {
		t.Errorf("%d requests", n)
	}
	if _, err := os.Stat(g.l.Local.Socket); err == nil {
		t.Errorf("the local den.sock exists: something listened or was created")
	}
	if _, err := os.Stat("/tmp/pwned"); err == nil {
		t.Errorf("an InstallRequest-shaped message acted")
	}
}

// More than 32 held requests: the 33rd and later are refused, the host is
// not dropped.
func TestAdvStubFloodOfHeld(t *testing.T) {
	var lines []string
	for i := 0; i < 40; i++ {
		lines = append(lines, held(fmt.Sprintf("%032x", i+1), idA, "Bash", map[string]any{"command": fmt.Sprintf("echo %d", i)}))
	}
	g := stubApproveRig(t, lines...)
	g.start()
	g.live()
	g.wait("held", 10*time.Second, func() bool { return len(g.heldIDs()) >= 32 })
	time.Sleep(500 * time.Millisecond)
	if n := len(g.heldIDs()); n != 32 {
		t.Errorf("%d requests held, want 32", n)
	}
	if !g.flagged("more held requests than a serve holds") || g.status().State != remote.Live {
		t.Errorf("flags %v state %s", g.status().Flags, g.status().State)
	}
}

// held / gone interleaved for one id. A reply to a request after its gone
// must not reach the host; a re-offered id is its own request.
func TestAdvInterleavedGoneAndHeld(t *testing.T) {
	body := map[string]any{"command": "ls"}
	g := stubApproveRig(t,
		held(idX, idA, "Bash", body),
		j(map[string]any{"type": "gone", "id": idX}),
		held(idX, idA, "Bash", body),                                                // the same id again, after gone: a new request
		held(idX, idA, "Bash", body),                                                // while open: offered twice
		j(map[string]any{"type": "gone", "id": "ffffffffffffffffffffffffffffffff"}), // unknown
		j(map[string]any{"type": "gone", "id": 7}),                                  // malformed
	)
	g.start()
	g.live()
	g.wait("two arrivals", 10*time.Second, func() bool { return len(g.heldIDs()) >= 2 })
	time.Sleep(500 * time.Millisecond)
	g.mu.Lock()
	var reqs []*bridge.Request
	for _, u := range g.ups {
		if u.Kind == remote.HeldArrived {
			reqs = append(reqs, u.Request)
		}
	}
	g.mu.Unlock()
	if len(reqs) != 2 {
		t.Fatalf("%d requests arrived, want 2 (offer, gone, offer again)", len(reqs))
	}
	if !g.flagged("offered twice") {
		t.Errorf("a duplicate offer was not flagged: %v", g.status().Flags)
	}
	reqs[0].Reply(bridge.Decision{Behavior: "allow"}) // its gone came first
	time.Sleep(300 * time.Millisecond)
	if n := strings.Count(g.stubIn(), `"type":"decision"`); n != 0 {
		t.Fatalf("a reply to a request that was gone reached the host (%d decisions)", n)
	}
	reqs[1].Reply(bridge.Decision{Behavior: "deny"})
	reqs[1].Reply(bridge.Decision{Behavior: "allow"}) // a second answer
	g.wait("the decision", 5*time.Second, func() bool { return strings.Contains(g.stubIn(), `"type":"decision"`) })
	time.Sleep(300 * time.Millisecond)
	in := g.stubIn()
	if n := strings.Count(in, `"type":"decision"`); n != 1 {
		t.Errorf("%d decisions reached the host, want exactly 1:\n%s", n, in)
	}
	if !strings.Contains(in, `"behavior":"deny"`) || !strings.Contains(in, `"hash":"`+remote.BodyHash([]byte(j(body)))+`"`) {
		t.Errorf("decision = %s", in)
	}
}

// holdCmd is hold with a handle on the hook process, to hang it up.
func (g *rig) holdCmd(session, tool string, input map[string]any) (*exec.Cmd, <-chan hookOut) {
	g.t.Helper()
	b, _ := json.Marshal(map[string]any{"session_id": session, "hook_event_name": "PermissionRequest",
		"tool_name": tool, "tool_input": input, "cwd": "/home/user/proj"})
	c := exec.Command(g.l.Den, "hook")
	c.Dir, c.Env, c.Stdin = g.r.Home, g.r.Env(), strings.NewReader(string(b))
	var out strings.Builder
	c.Stdout = &out
	if err := c.Start(); err != nil {
		g.t.Fatal(err)
	}
	ch := make(chan hookOut, 1)
	start := time.Now()
	go func() { err := c.Wait(); ch <- hookOut{out.String(), time.Since(start), err} }()
	return c, ch
}

func decisions(b []byte) int { return strings.Count(string(b), `"type":"decision"`) }

// The decision path through the real serve: a duplicate answer, an answer
// after the hook hung up, and an answer on an old connection.
func TestAdvDecisionPath(t *testing.T) {
	t.Run("a second answer does not reach the hook", func(t *testing.T) {
		g := approveRig(t)
		g.r.CapturePipe(true)
		g.start()
		g.live()
		c := g.hold(idA, "Bash", map[string]any{"command": "ls"})
		r := g.held(1)
		r.Reply(bridge.Decision{Behavior: "allow"})
		r.Reply(bridge.Decision{Behavior: "deny"})
		if o := g.hookDone(c, 5*time.Second); decision(o.stdout) != "allow" {
			t.Errorf("hook printed %q", o.stdout)
		}
		time.Sleep(500 * time.Millisecond)
		if n := decisions(g.r.PipeIn()); n != 1 {
			t.Errorf("%d decisions were written to serve, want 1", n)
		}
	})
	t.Run("an answer after the hook hung up goes nowhere", func(t *testing.T) {
		g := approveRig(t)
		g.r.CapturePipe(true)
		g.start()
		g.live()
		cmd, ch := g.holdCmd(idA, "Bash", map[string]any{"command": "ls"})
		r := g.held(1)
		cmd.Process.Kill()
		<-ch
		g.wait("the item let go", 5*time.Second, func() bool { return g.count(remote.HeldReleased) == 1 })
		r.Reply(bridge.Decision{Behavior: "allow"})
		time.Sleep(500 * time.Millisecond)
		if n := decisions(g.r.PipeIn()); n != 0 {
			t.Errorf("a decision for a hook that hung up was written to serve (%d)", n)
		}
		if !strings.Contains(r.Why(), "no longer waiting") {
			t.Errorf("why = %q", r.Why())
		}
	})
	t.Run("an answer to a request of a lost connection goes nowhere", func(t *testing.T) {
		g := approveRig(t)
		g.r.CapturePipe(true)
		g.start()
		g.live()
		c := g.hold(idA, "Bash", map[string]any{"command": "ls"})
		r := g.held(1)
		g.l.KillSSH()
		o := g.hookDone(c, 10*time.Second)
		if o.stdout != "" {
			t.Errorf("the hook printed %q after the link was cut", o.stdout)
		}
		g.wait("the item let go", 10*time.Second, func() bool { return g.count(remote.HeldReleased) == 1 })
		g.live()
		before := len(g.r.PipeIn())
		r.Reply(bridge.Decision{Behavior: "allow"})
		time.Sleep(500 * time.Millisecond)
		if n := decisions(g.r.PipeIn()[before:]); n != 0 {
			t.Errorf("a stale reply was written to the new connection (%d)", n)
		}
		if !strings.Contains(r.Why(), "link lost") {
			t.Errorf("why = %q", r.Why())
		}
		// And a fresh request on the new connection still works.
		c = g.hold(idA, "Bash", map[string]any{"command": "ls"})
		g.held(2).Reply(bridge.Decision{Behavior: "deny"})
		if o := g.hookDone(c, 5*time.Second); decision(o.stdout) != "deny" {
			t.Errorf("after reconnect: %q", o.stdout)
		}
	})
}

// manualServe runs `den remote serve` over ssh, driven by hand like a Den
// that misbehaves; lines it prints arrive on msgs.
type manualServe struct {
	cmd  *exec.Cmd
	in   io.WriteCloser
	msgs chan []byte
	done chan struct{}
}

func (g *rig) manual(policy remote.Policy) *manualServe {
	g.t.Helper()
	cmd := g.r.Cmd(context.Background(), "den remote serve --proto 1")
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		g.t.Fatal(err)
	}
	ms := &manualServe{cmd: cmd, in: in, msgs: make(chan []byte, 256), done: make(chan struct{})}
	go func() {
		sc := bufio.NewScanner(out)
		sc.Buffer(nil, remote.MaxLine+1)
		for sc.Scan() {
			ms.msgs <- append([]byte(nil), sc.Bytes()...)
		}
		close(ms.msgs)
		cmd.Wait()
		close(ms.done)
	}()
	g.t.Cleanup(func() { in.Close(); cmd.Process.Kill() })
	ms.send(remote.Request{Type: remote.TypeHello, Proto: remote.Proto, Policy: policy, Controller: "cccccccccccccccccccccccccccccccc"})
	if policy.Valid() && policy != remote.Observe { // under observe there is no relay
		g.wait("den-remote.sock", 10*time.Second, func() bool { _, err := os.Stat(g.r.RemoteSock); return err == nil })
	}
	return ms
}

func (m *manualServe) send(v any) { b, _ := json.Marshal(v); m.in.Write(append(b, '\n')) }

func (m *manualServe) nextHeld(t *testing.T) remote.Held {
	t.Helper()
	for {
		select {
		case line, ok := <-m.msgs:
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

func serveLog(g *rig) string {
	b, _ := os.ReadFile(filepath.Join(g.r.StateHome, "den", "remote-serve.log"))
	return string(b)
}

// A Den that sends forged decisions: another request's hash, an unknown
// behavior, answers for a non-question, a huge message with controls; then a
// message type serve does not read at all.
func TestAdvForgedDecisionsToServe(t *testing.T) {
	g := approveRig(t)
	ms := g.manual(remote.Approve)
	c1 := g.hold(idA, "Bash", map[string]any{"command": "first"})
	h1 := ms.nextHeld(t)
	c2 := g.hold(idB, "Bash", map[string]any{"command": "second"})
	h2 := ms.nextHeld(t)

	for _, d := range []remote.Decision{
		{Type: remote.TypeDecision, ID: h2.ID, Hash: remote.BodyHash(h1.Body), Behavior: "allow"}, // swapped hash
		{Type: remote.TypeDecision, ID: h1.ID, Hash: remote.BodyHash(h2.Body), Behavior: "allow"},
		{Type: remote.TypeDecision, ID: h1.ID, Hash: remote.BodyHash(h1.Body), Behavior: "approve"},                                     // not a behavior
		{Type: remote.TypeDecision, ID: h1.ID, Hash: remote.BodyHash(h1.Body), Behavior: "allow", Answers: map[string]string{"q": "a"}}, // answers on a Bash
		{Type: remote.TypeDecision, ID: strings.ToUpper(h1.ID), Hash: remote.BodyHash(h1.Body), Behavior: "allow"},                      // id case
		{Type: remote.TypeDecision, ID: h1.ID + "0", Hash: remote.BodyHash(h1.Body), Behavior: "allow"},
		{Type: remote.TypeDecision, ID: h1.ID, Behavior: "allow"}, // no hash
	} {
		ms.send(d)
	}
	select {
	case o := <-c1:
		t.Fatalf("a forged decision counted for request 1: %q", o.stdout)
	case o := <-c2:
		t.Fatalf("a forged decision counted for request 2: %q", o.stdout)
	case <-time.After(700 * time.Millisecond):
	}

	// A long message full of controls is cut and cleaned before the hook prints it.
	evil := strings.Repeat("no \x1b[2J\x1b]52;c;eA==\x07 ", 400)
	ms.send(remote.Decision{Type: remote.TypeDecision, ID: h1.ID, Hash: remote.BodyHash(h1.Body), Behavior: "deny", Message: evil})
	o := g.hookDone(c1, 5*time.Second)
	if decision(o.stdout) != "deny" || strings.ContainsAny(o.stdout, "\x1b\x07") || len(o.stdout) > 1500 {
		t.Errorf("hook printed %d bytes %q", len(o.stdout), o.stdout)
	}

	// A line serve does not read ends it, and the held request goes to its terminal.
	ms.send(map[string]any{"type": "exec", "cmd": "touch /tmp/pwned"})
	o = g.hookDone(c2, 10*time.Second)
	if o.stdout != "" {
		t.Errorf("request 2 printed %q when serve ended", o.stdout)
	}
	select {
	case <-ms.done:
	case <-time.After(10 * time.Second):
		t.Error("serve did not exit after an unknown message")
	}
	log := serveLog(g)
	for _, want := range []string{"body hash that does not match", "an unknown behavior", "answers for a request that is not an allowed question", "an id that is not open"} {
		if !strings.Contains(log, want) {
			t.Errorf("serve log lacks %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "first") || strings.Contains(log, "second") {
		t.Errorf("the log carries a command:\n%s", log)
	}
}

// dial opens den-remote.sock as a hook would and sends line.
func dialSock(t *testing.T, path, line string) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if line != "" {
		c.Write([]byte(line + "\n"))
	}
	return c
}

func readLine(c net.Conn, d time.Duration) string {
	c.SetReadDeadline(time.Now().Add(d))
	s, _ := bufio.NewReader(c).ReadString('\n')
	return strings.TrimSpace(s)
}

func permEnv(session, payloadSession string, tool string, input map[string]any, extra map[string]any) string {
	p := map[string]any{"session_id": payloadSession, "hook_event_name": "PermissionRequest", "tool_name": tool, "tool_input": input}
	for k, v := range extra {
		p[k] = v
	}
	return j(map[string]any{"Seq": 1, "Event": "PermissionRequest", "SessionID": session, "Payload": p})
}

// Another process on the host writes to den-remote.sock. (Same uid: a second
// uid cannot be had without root; see TestAdvSocketPrivacy for what is
// checked instead.)
func TestAdvHostileWriterOnTheSocket(t *testing.T) {
	g := approveRig(t)
	g.start()
	g.live()
	g.wait("den-remote.sock", 10*time.Second, func() bool { _, err := os.Stat(g.r.RemoteSock); return err == nil })
	sock := g.r.RemoteSock

	// A real request is held first; nothing below may steal or answer it.
	c := g.hold(idA, "Bash", map[string]any{"command": "real"})
	real := g.held(1)

	notRelayed := func(name, line string) {
		t.Helper()
		conn := dialSock(t, sock, line)
		defer conn.Close()
		got := readLine(conn, 6*time.Second)
		// Either a refusal naming no decision, or the "no decision" release.
		if strings.Contains(got, `"behavior":"allow"`) || strings.Contains(got, `"behavior":"deny"`) {
			t.Errorf("%s: got a decision %q", name, got)
		}
		time.Sleep(100 * time.Millisecond)
	}
	for _, ev := range []string{"CloseRequest", "SkillsRequest", "InstallRequest", "DenRemoteServe2", "SessionStart", "Stop", ""} {
		notRelayed("envelope "+ev, j(map[string]any{"Seq": 1, "Event": ev, "SessionID": idA, "Payload": map[string]any{"session": idA}}))
	}
	notRelayed("session mismatch", permEnv(idB, idA, "Bash", map[string]any{"command": "ls"}, nil))
	notRelayed("subagent", permEnv(idA, idA, "Bash", map[string]any{"command": "ls"}, map[string]any{"agent_id": "sub"}))
	notRelayed("not json", "{{{")
	for name, cmd := range map[string]string{
		"escape":       "ls \x1b[2J",
		"crlf":         "a\r\nb",
		"nul":          "a\x00b",
		"bidi":         "ls \u202e/etc",
		"c1":           "ls \u009b31m",
		"over 16 KB":   strings.Repeat("a", 16<<10+1),
		"invalid utf8": "ls \xff\xfe",
	} {
		notRelayed("command with "+name, permEnv(idA, idA, "Bash", map[string]any{"command": cmd}, nil))
	}
	notRelayed("2 MB line", strings.Repeat("a", 2<<20))
	// A claim from another controller (or none) learns who holds the socket and takes nothing.
	for _, ctrl := range []string{"ffffffffffffffffffffffffffffffff", ""} {
		conn := dialSock(t, sock, j(map[string]any{"Event": "DenRemoteServe", "Payload": map[string]any{"controller": ctrl, "pid": 1}}))
		reply := readLine(conn, 3*time.Second)
		conn.Close()
		if strings.Contains(reply, "handover") || !strings.Contains(reply, "owner") {
			t.Errorf("claim by controller %q: %q", ctrl, reply)
		}
	}

	if n := g.count(remote.HeldArrived); n != 1 {
		t.Errorf("%d requests reached the inbox, want only the real one", n)
	}
	// The real request survived all of it and still works.
	real.Reply(bridge.Decision{Behavior: "allow"})
	if o := g.hookDone(c, 5*time.Second); decision(o.stdout) != "allow" {
		t.Errorf("real request: %q", o.stdout)
	}
	if st := g.status(); st.State != remote.Live {
		t.Errorf("state %s", st.State)
	}
	log := serveLog(g)
	if strings.Contains(log, "/etc") || strings.Contains(log, "real") {
		t.Errorf("serve log carries request content:\n%s", log)
	}
}

// Real hooks past the cap: 32 are held, the rest fail open at once.
func TestAdvFloodOfRealHooks(t *testing.T) {
	g := approveRig(t)
	g.start()
	g.live()
	g.wait("den-remote.sock", 10*time.Second, func() bool { _, err := os.Stat(g.r.RemoteSock); return err == nil })
	var chans []<-chan hookOut
	for i := 0; i < 40; i++ {
		chans = append(chans, g.hold(idA, "Bash", map[string]any{"command": fmt.Sprintf("echo %d", i)}))
	}
	g.wait("32 held", 20*time.Second, func() bool { return g.count(remote.HeldArrived) >= 32 })
	time.Sleep(time.Second)
	if n := g.count(remote.HeldArrived); n != 32 {
		t.Errorf("%d held, want 32", n)
	}
	early := 0
	finished := make([]bool, len(chans))
	for i, c := range chans {
		select {
		case o := <-c:
			finished[i] = true
			early++
			if o.stdout != "" || o.err != nil {
				t.Errorf("a refused hook printed %q (%v)", o.stdout, o.err)
			}
		default:
		}
	}
	if early != 8 {
		t.Errorf("%d hooks returned at once, want the 8 over the cap", early)
	}
	g.mu.Lock()
	var reqs []*bridge.Request
	for _, u := range g.ups {
		if u.Kind == remote.HeldArrived {
			reqs = append(reqs, u.Request)
		}
	}
	g.mu.Unlock()
	for _, r := range reqs {
		r.Reply(bridge.Decision{Behavior: "deny"})
	}
	for i, c := range chans {
		if finished[i] {
			continue
		}
		select {
		case o := <-c:
			if decision(o.stdout) != "deny" {
				t.Errorf("hook %d printed %q", i, o.stdout)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("a hook was never answered")
		}
	}
}

// The socket and its directory are private, and the claim to be another
// user is checked by SO_PEERCRED. Not testable without a second uid: that
// a different uid's connection is refused (needs root or a second account).
func TestAdvSocketPrivacy(t *testing.T) {
	g := approveRig(t)
	g.start()
	g.live()
	g.wait("den-remote.sock", 10*time.Second, func() bool { _, err := os.Stat(g.r.RemoteSock); return err == nil })
	st, err := os.Stat(g.r.RemoteSock)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 || st.Mode()&os.ModeSocket == 0 {
		t.Errorf("den-remote.sock mode %v", st.Mode())
	}
	if sys := st.Sys().(*syscall.Stat_t); int(sys.Uid) != os.Getuid() {
		t.Errorf("owner uid %d", sys.Uid)
	}
	dir, _ := os.Stat(filepath.Dir(g.r.RemoteSock))
	if dir.Mode().Perm() != 0o700 {
		t.Errorf("the socket's directory has mode %v", dir.Mode().Perm())
	}
	// The default umask does not matter: the socket is 0600 even when serve runs with 000.
	t.Log("not testable without a second uid: that a connection from another user is refused (SO_PEERCRED)")
}

// serve is SIGKILLed mid-wait: the hook exits 0 with nothing, promptly; the
// stale socket does not stop the next serve; the next request works.
func TestAdvServeKilledMidWait(t *testing.T) {
	g := approveRig(t)
	g.start()
	g.live()
	c := g.hold(idA, "Bash", map[string]any{"command": "ls"})
	r := g.held(1)
	pids := g.r.ServePIDs()
	if len(pids) == 0 {
		t.Fatal("found no serve process")
	}
	killed := time.Now()
	for _, p := range pids {
		syscall.Kill(p, syscall.SIGKILL)
	}
	o := g.hookDone(c, 5*time.Second)
	t.Logf("hook returned %v after serve was SIGKILLed, printing %q", time.Since(killed).Round(time.Millisecond), o.stdout)
	if o.stdout != "" {
		t.Errorf("the hook printed %q", o.stdout)
	}
	if time.Since(killed) > 3*time.Second {
		t.Errorf("the hook took %v to fail open", time.Since(killed))
	}
	g.wait("the item let go", 15*time.Second, func() bool { return g.count(remote.HeldReleased) >= 1 })
	if !strings.Contains(r.Why(), "link lost") {
		t.Errorf("why = %q", r.Why())
	}
	g.wait("reconnect", 20*time.Second, func() bool { return g.status().State == remote.Live })
	c = g.hold(idA, "Bash", map[string]any{"command": "ls"})
	g.held(2).Reply(bridge.Decision{Behavior: "allow"})
	if o := g.hookDone(c, 5*time.Second); decision(o.stdout) != "allow" {
		t.Errorf("after the stale socket: %q", o.stdout)
	}
}

// A serve that hears nothing from Den for its Silence (60 s) lets the held
// requests go and exits: a half-open link does not hold them. Slow.
func TestAdvWatchdogSilentDen(t *testing.T) {
	if testing.Short() || os.Getenv("DEN_SLOW_TESTS") != "1" {
		t.Skip("slow (about 70 s): set DEN_SLOW_TESTS=1")
	}
	g := approveRig(t)
	ms := g.manual(remote.Approve)
	c := g.hold(idA, "Bash", map[string]any{"command": "ls"})
	ms.nextHeld(t)
	start := time.Now()
	o := g.hookDone(c, 90*time.Second)
	took := time.Since(start)
	t.Logf("the hook was released %v after Den fell silent, printing %q", took.Round(time.Second), o.stdout)
	if o.stdout != "" {
		t.Errorf("printed %q", o.stdout)
	}
	if took < 40*time.Second || took > 80*time.Second {
		t.Errorf("released after %v, want about 60 s", took)
	}
	select {
	case <-ms.done:
	case <-time.After(20 * time.Second):
		t.Error("serve did not exit")
	}
}

// A Den on the host wins: one that starts after a request is already held
// takes the next hook; a stale den.sock file does not block the relay.
func TestAdvPrecedence(t *testing.T) {
	t.Run("a Den that starts later", func(t *testing.T) {
		g := approveRig(t)
		g.start()
		g.live()
		c1 := g.hold(idA, "Bash", map[string]any{"command": "first"})
		r1 := g.held(1)
		ev := exec.Command(g.l.Den, "events")
		ev.Dir, ev.Env = g.r.Home, append(g.r.Env(), "DEN_EVENTS_DECISION=deny")
		ev.Stdout, ev.Stderr = io.Discard, io.Discard
		if err := ev.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { ev.Process.Kill(); ev.Wait() }()
		g.wait("the host's Den", 5*time.Second, func() bool { _, err := os.Stat(g.r.Socket); return err == nil })
		o := g.hookDone(g.hold(idB, "Bash", map[string]any{"command": "second"}), 5*time.Second)
		if decision(o.stdout) != "deny" {
			t.Errorf("the host's Den did not take the second hook: %q", o.stdout)
		}
		if n := g.count(remote.HeldArrived); n != 1 {
			t.Errorf("%d requests reached the local Den, want the first only", n)
		}
		r1.Reply(bridge.Decision{Behavior: "allow"})
		if o := g.hookDone(c1, 5*time.Second); decision(o.stdout) != "allow" {
			t.Errorf("the first request: %q", o.stdout)
		}
	})
	t.Run("a stale den.sock", func(t *testing.T) {
		g := approveRig(t)
		g.start()
		g.live()
		ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: g.r.Socket, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		ln.SetUnlinkOnClose(false)
		ln.Close() // the file stays, nobody listens
		c := g.hold(idA, "Bash", map[string]any{"command": "ls"})
		g.held(1).Reply(bridge.Decision{Behavior: "allow"})
		if o := g.hookDone(c, 5*time.Second); decision(o.stdout) != "allow" {
			t.Errorf("%q", o.stdout)
		}
	})
}
