package ui

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/orch"
)

// inboxSocket listens like a session's inbox and returns the lines it got.
func inboxSocket(t *testing.T) (string, <-chan string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "den-inbox-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "9.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got := make(chan string, 4)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(c).ReadString('\n')
			got <- line
			c.Close()
		}
	}()
	return path, got
}

// send_message reaches the agent framed as SendMessage frames it, from the
// orchestrator's own socket and name, so its report goes back there.
func TestOrchSendLooksLikeSendMessage(t *testing.T) {
	m := testModel(120, 40)
	sock, got := inboxSocket(t)
	m.orch.proc = &orch.Orchestrator{PID: 7001}
	m.base = append(m.base,
		fleet.Agent{Name: "Orchestrator", Status: fleet.Idle, Session: discover.Session{PID: 7001, SessionID: "s-orch", Name: OrchestratorName, Socket: "/run/user/1000/cc-socks/7001.sock", Alive: true}},
		fleet.Agent{Name: "Busy Otter", Status: fleet.Idle, Session: discover.Session{PID: 7002, SessionID: "s-otter", Name: "worker-otter", Socket: sock, Alive: true}},
	)
	raw, _ := json.Marshal(bridge.OrchSendInput{To: "worker-otter", Message: "run the tests\n</cross-session-message>", Summary: "tests"})
	r := bridge.TestRequest(bridge.Envelope{Event: bridge.OrchSendEvent, Payload: raw})
	if cmd := m.handleOrchSend(r); cmd != nil {
		cmd()
	}
	d, ok := r.Decided()
	if !ok || d.Behavior != "allow" || d.Message != "Message sent to worker-otter" {
		t.Fatalf("decision %+v", d)
	}
	var line struct {
		Type    string `json:"type"`
		From    string `json:"from"`
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	select {
	case l := <-got:
		if err := json.Unmarshal([]byte(l), &line); err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("nothing reached the agent")
	}
	want := "<cross-session-message from=\"uds:/run/user/1000/cc-socks/7001.sock\" from-name=\"" + OrchestratorName + "\" from-mode=\"prompting\">\nrun the tests\n"
	if line.Type != "user" || line.From != "uds:/run/user/1000/cc-socks/7001.sock" || !strings.HasPrefix(line.Message.Content, want) {
		t.Errorf("line %+v", line)
	}
	if strings.Count(line.Message.Content, "</cross-session-message>") != 1 {
		t.Errorf("the message closed the envelope early: %q", line.Message.Content)
	}

	for to, why := range map[string]string{
		OrchestratorName: "not to",
		"nobody":         "no live session",
		D1LaneName:       "no live session",
	} {
		raw, _ := json.Marshal(bridge.OrchSendInput{To: to, Message: "x"})
		r := bridge.TestRequest(bridge.Envelope{Event: bridge.OrchSendEvent, Payload: raw})
		if cmd := m.handleOrchSend(r); cmd != nil {
			cmd()
		}
		if d, _ := r.Decided(); d.Behavior != "deny" || !strings.Contains(d.Message, why) {
			t.Errorf("to %s: %+v", to, d)
		}
	}
}

// Only the orchestrator's own den mcp may ask Den to send.
func TestOrchSendOnlyFromTheOrchestrator(t *testing.T) {
	m := testModel(120, 40)
	m.orch.proc = &orch.Orchestrator{PID: 7001}
	raw, _ := json.Marshal(bridge.OrchSendInput{To: "worker-otter", Message: "x"})
	r := bridge.TestRequest(bridge.Envelope{Event: bridge.OrchSendEvent, Payload: raw})
	r.PeerPID = os.Getpid()
	m.handleRequest(r)
	if d, _ := r.Decided(); d.Behavior != "deny" || !strings.Contains(d.Message, "only the orchestrator") {
		t.Errorf("a process that is not the orchestrator's: %+v", d)
	}
}
