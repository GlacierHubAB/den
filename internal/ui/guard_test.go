package ui

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/peer"
	"github.com/GlacierHubAB/den/internal/permmode"
)

// watchedModel is guardedModel with a guard handed out for the otter's
// session, as godSpawn does, and sends caught on a channel (the self-test
// is sent off the model's goroutine).
func watchedModel(t *testing.T) (*Model, string, chan string) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	m, _ := guardedModel(t)
	sid := m.base[0].Session.SessionID
	m.base[0].Session.Socket = "/run/user/1000/cc-socks/42.sock"
	sent := make(chan string, 8)
	sendAs = func(sock, from, name, body string) error { sent <- body; return nil }
	den := filepath.Join(t.TempDir(), "den")
	os.WriteFile(den, []byte("binary"), 0o755)
	if _, err := m.guardAgent(sid, den, "/run/den.sock", agentHooks{Inbound: true}); err != nil {
		t.Fatal(err)
	}
	m.med.guards[sid].launched = time.Now()
	return m, sid, sent
}

// A guard counts once its agent shows up; Den then self-tests the hook,
// which passes when the hook asks Den about it, quietly.
func TestGuardSelfTestPasses(t *testing.T) {
	m, sid, sent := watchedModel(t)
	m.watchGuards(time.Now())
	g := m.med.guards[sid]
	if !g.registered || g.watch == nil || g.watch.selfTest == "" {
		t.Fatalf("not registered and self-tested: %+v", g)
	}
	if body := <-sent; body != selfTestBody {
		t.Errorf("self-test body %q", body)
	}
	if d := askInbound(m, sid, 4242, peer.Envelope(selfTestFrom, "den-self-test", selfTestBody)); d.Behavior == "allow" {
		t.Error("the self-test envelope was let in")
	}
	m.watchGuards(time.Now().Add(selfTestFor + time.Second))
	if g.unguarded != "" {
		t.Errorf("unguarded after a passed self-test: %q", g.unguarded)
	}
	for _, it := range m.store.Items() {
		if it.Kind == inbox.GuardNote {
			t.Error("a notice for Den's own self-test")
		}
	}
}

// A hook that never asks about the self-test leaves the agent unguarded,
// and Den offers to close it, once close-safety lets it.
func TestGuardSelfTestFails(t *testing.T) {
	m, sid, _ := watchedModel(t)
	m.base[0].Status = fleet.Idle
	m.watchGuards(time.Now())
	m.watchGuards(time.Now().Add(selfTestFor + time.Second))
	if m.unguarded(sid) == "" {
		t.Fatal("still guarded without a self-test answer")
	}
	found := false
	for _, it := range m.store.Items() {
		found = found || it.Kind == inbox.Close
	}
	if !found {
		var notes []string
		for _, e := range m.orch.chat {
			notes = append(notes, e.text)
		}
		t.Errorf("no close offered for the unguarded agent: %q", notes)
	}
	if names := m.unguardedAgents(); len(names) != 1 {
		t.Errorf("unguarded list %v", names)
	}
}

// A peer message in the transcript that Den's own process did not write
// got past the guard; Den's own deliveries did not.
func TestGuardTranscriptWatch(t *testing.T) {
	m, sid, _ := watchedModel(t)
	path := filepath.Join(t.TempDir(), "s.jsonl")
	m.base[0].Path = path
	old := denPID
	t.Cleanup(func() { denPID = old })
	denPID = 1000
	os.WriteFile(path, []byte(`{"type":"queue-operation","content":"x"}
{"type":"user","isMeta":true,"origin":{"kind":"peer","verifiedPeerPid":1000}}
`), 0o600)
	m.watchGuards(time.Now())
	if m.unguarded(sid) != "" {
		t.Fatalf("Den's own delivery marked the agent: %q", m.unguarded(sid))
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"type":"user","isMeta":true,"origin":{"kind":"peer","verifiedPeerPid":31337}}` + "\n")
	f.Close()
	m.watchGuards(time.Now())
	if m.unguarded(sid) == "" {
		t.Error("a message Den did not deliver went unnoticed")
	}
}

// A rewritten (shorter) transcript is read again from the start, and a
// line too long to parse is still searched for a peer message.
func TestGuardTranscriptRewrittenAndLong(t *testing.T) {
	m, sid, _ := watchedModel(t)
	path := filepath.Join(t.TempDir(), "s.jsonl")
	m.base[0].Path = path
	os.WriteFile(path, []byte(`{"type":"assistant","message":{"content":"`+strings.Repeat("x", 4096)+`"}}`+"\n"), 0o600)
	m.watchGuards(time.Now())
	os.WriteFile(path, []byte(`{"type":"user","origin":{"kind":"peer","verifiedPeerPid":31337}}`+"\n"), 0o600)
	m.watchGuards(time.Now())
	if m.unguarded(sid) == "" {
		t.Fatal("a rewritten transcript was not read again")
	}
	r := bufio.NewReaderSize(strings.NewReader(`{"type":"user","big":"`+strings.Repeat("y", 300)+`","origin":{"kind":"peer","verifiedPeerPid":1}}`+"\n"+`{"a":1}`+"\n"), 16)
	if line, _, long, err := readLine(r, 64); err != nil || !long || line == nil {
		t.Errorf("a long peer line: %v %v %v", line, long, err)
	}
	if line, _, long, err := readLine(r, 64); err != nil || long || string(line) != `{"a":1}`+"\n" {
		t.Errorf("the next line: %q %v %v", line, long, err)
	}
	// Any key order and spacing.
	r = bufio.NewReaderSize(strings.NewReader(`{"origin": {"verifiedPeerPid": 1, "kind" : "peer"}, "x":"`+strings.Repeat("y", 300)+`"}`+"\n"), 16)
	if line, _, long, err := readLine(r, 64); err != nil || !long || line == nil {
		t.Errorf("a long peer line, spaced and reordered: %v %v %v", line, long, err)
	}
	r = bufio.NewReaderSize(strings.NewReader(strings.Repeat("z", 300)+"\n"), 16)
	if line, _, long, _ := readLine(r, 64); !long || line != nil {
		t.Errorf("a long line with no peer origin: %q %v", line, long)
	}
}

// An agent in bypassPermissions Den holds no guard for is unguarded.
func TestBareBypassAgentIsUnguarded(t *testing.T) {
	m, _ := guardedModel(t)
	a := m.base[0]
	m.ctl.modes = map[string]modeSeen{a.Session.SessionID: {mode: permmode.Bypass, at: time.Now()}}
	if names := m.unguardedAgents(); len(names) != 1 || names[0] != a.Name {
		t.Errorf("unguarded %v", names)
	}
}

// The orchestrator runs on Den's send_message while God mode is on, and
// while a guarded agent still runs after it.
func TestSendLaunchWanted(t *testing.T) {
	m, sid, _ := watchedModel(t)
	if !m.sendLaunchWanted() {
		t.Error("a guarded agent runs, and the launch is not wanted")
	}
	delete(m.med.guards, sid)
	if m.sendLaunchWanted() {
		t.Error("wanted with nothing to mediate")
	}
	m.god.on = true
	if !m.sendLaunchWanted() {
		t.Error("not wanted in God mode")
	}
}
