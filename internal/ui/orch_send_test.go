package ui

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/api"
	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/orch"
)

// inboxSocket listens like pid's inbox, where Claude Code binds it
// ($XDG_RUNTIME_DIR/cc-socks), and returns the lines it got.
func inboxSocket(t *testing.T, pid int) (string, <-chan string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "den-run-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("XDG_RUNTIME_DIR", dir)
	os.Mkdir(filepath.Join(dir, "cc-socks"), 0o700)
	path := filepath.Join(dir, "cc-socks", strconv.Itoa(pid)+".sock")
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
	defer func(f func(int, int64, string) (api.AgentProc, bool)) { sessionProc = f }(sessionProc)
	sessionProc = func(pid int, _ int64, _ string) (api.AgentProc, bool) { return api.AgentProc{PID: pid}, pid != 7003 }
	m := testModel(120, 40)
	sock, got := inboxSocket(t, 7002)
	orchSock := "/run/user/" + strconv.Itoa(os.Getuid()) + "/cc-socks/7001.sock"
	m.orch.proc = &orch.Orchestrator{PID: 7001}
	m.orch.version = "2.1.296"
	m.orch.control = true
	m.base = append(m.base,
		fleet.Agent{Name: "Orchestrator", Status: fleet.Idle, Session: discover.Session{PID: 7001, SessionID: "s-orch", Name: OrchestratorName, Socket: orchSock, Alive: true}},
		fleet.Agent{Name: "Busy Otter", Status: fleet.Idle, Session: discover.Session{PID: 7002, SessionID: "s-otter", Name: "worker-otter", Socket: sock, Version: "2.1.296", Alive: true}},
		fleet.Agent{Name: "Old Otter", Status: fleet.Idle, Session: discover.Session{PID: 7004, SessionID: "s-old", Name: "worker-old", Socket: filepath.Join(filepath.Dir(sock), "7004.sock"), Version: "2.1.250", Alive: true}},
		fleet.Agent{Name: "Odd Otter", Status: fleet.Idle, Session: discover.Session{PID: 7005, SessionID: "s-odd", Name: "worker-odd", Socket: "/tmp/elsewhere/7005.sock", Version: "2.1.296", Alive: true}},
		fleet.Agent{Name: "Reused", Status: fleet.Idle, Session: discover.Session{PID: 7003, SessionID: "s-re", Name: "worker-reused", Socket: filepath.Join(filepath.Dir(sock), "7003.sock"), Version: "2.1.296", Alive: true}},
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
	want := "<cross-session-message from=\"uds:" + orchSock + "\" from-name=\"" + OrchestratorName + "\" from-mode=\"prompting\">\nrun the tests\n"
	if line.Type != "user" || line.From != "uds:"+orchSock || !strings.HasPrefix(line.Message.Content, want) {
		t.Errorf("line %+v", line)
	}
	if strings.Count(line.Message.Content, "</cross-session-message>") != 1 {
		t.Errorf("the message closed the envelope early: %q", line.Message.Content)
	}

	for to, why := range map[string]string{
		OrchestratorName: "not to",
		"nobody":         "no live session",
		D1LaneName:       "no live session",
		"worker-old":     "has not checked send_message",
		"worker-odd":     "no inbox socket where Claude Code puts one",
		"worker-reused":  "not the one its session started with",
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

// On a Claude Code release send_message was not checked on, Den sends
// nothing.
func TestOrchSendNeedsACheckedRelease(t *testing.T) {
	m := testModel(120, 40)
	m.orch.proc = &orch.Orchestrator{PID: 7001}
	m.orch.version = "2.1.300"
	m.orch.control = true
	raw, _ := json.Marshal(bridge.OrchSendInput{To: "worker-otter", Message: "x"})
	r := bridge.TestRequest(bridge.Envelope{Event: bridge.OrchSendEvent, Payload: raw})
	m.handleOrchSend(r)
	if d, _ := r.Decided(); d.Behavior != "deny" || !strings.Contains(d.Message, "2.1.289, 2.1.296") {
		t.Errorf("decision %+v", d)
	}
}

// Without control the orchestrator has SendMessage and no send_message:
// Den refuses one, whoever asks.
func TestOrchSendOnlyWithControl(t *testing.T) {
	m := testModel(120, 40)
	m.orch.proc = &orch.Orchestrator{PID: 7001}
	m.orch.version = "2.1.296"
	raw, _ := json.Marshal(bridge.OrchSendInput{To: "worker-otter", Message: "x"})
	r := bridge.TestRequest(bridge.Envelope{Event: bridge.OrchSendEvent, Payload: raw})
	m.handleOrchSend(r)
	if d, _ := r.Decided(); d.Behavior != "deny" || !strings.Contains(d.Message, "only while a paired app has control") {
		t.Errorf("decision %+v", d)
	}
}
