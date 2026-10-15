package ui

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/orch"
)

// controlClaude is a fake claude for control mode: it replays each user
// turn as Claude Code does (--replay-user-messages), and for a turn that
// says SPAWN-PLEASE wants mcp__den__spawn_agent, asking its host and
// waiting for the answer. Every line Den writes goes to its folder.
const controlClaude = `#!/bin/sh
D=$(dirname "$0")
echo '{"type":"system","subtype":"init","session_id":"s-ctl","model":"m","claude_code_version":"2.1.296","tools":["ListAgents"],"mcp_servers":[]}'
while IFS= read -r line; do
  case "$line" in
    *'"control_response"'*) printf '%s\n' "$line" >> "$D/answers"; continue ;;
    *'"control_request"'*) printf '%s\n' "$line" >> "$D/controls"
      case "$line" in *'"interrupt"'*) echo '{"type":"result","subtype":"error_during_execution","is_error":true,"result":"","num_turns":1}' ;; esac
      continue ;;
  esac
  printf '%s\n' "$line" >> "$D/turns"
  printf '%s\n' "$line" | sed 's/}$/,"isReplay":true}/'
  case "$line" in
    *SLOW-PLEASE*) continue ;;
    *MSG-PLEASE*)
      echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"u2","name":"mcp__den__send_message","input":{"to":"worker-otter","message":"run the tests"}}]}}'
      echo '{"type":"control_request","request_id":"r2","request":{"subtype":"can_use_tool","tool_name":"mcp__den__send_message","tool_use_id":"u2","input":{"to":"worker-otter","message":"run the tests"}}}'
      while IFS= read -r ans; do printf '%s\n' "$ans" >> "$D/answers"; case "$ans" in *'"r2"'*) break ;; esac; done
      err=true; case "$ans" in *'"allow"'*) err=false ;; esac
      echo '{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"u2","content":"sent","is_error":'$err'}]}}'
      ;;
    *SPAWN-PLEASE*)
      echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"u1","name":"mcp__den__spawn_agent","input":{"dir":"/tmp","brief":"x"}}]}}'
      echo '{"type":"control_request","request_id":"r1","request":{"subtype":"can_use_tool","tool_name":"mcp__den__spawn_agent","tool_use_id":"u1","input":{"dir":"/tmp","brief":"x"}}}'
      while IFS= read -r ans; do printf '%s\n' "$ans" >> "$D/answers"; case "$ans" in *'"r1"'*) break ;; esac; done
      err=true; case "$ans" in *'"allow"'*) err=false ;; esac
      echo '{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"u1","content":"started","is_error":'$err'}]}}'
      ;;
  esac
  echo '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Otter is busy."}}}'
  echo '{"type":"result","subtype":"success","is_error":false,"result":"Otter is busy.","num_turns":1}'
done
`

// loop is a small Bubble Tea runtime for a test: commands run on their
// own goroutines, their messages and den-api.sock's acts come back to the
// model one at a time, on the test's goroutine.
type loop struct {
	t    *testing.T
	m    *Model
	msgs chan tea.Msg
}

func (l *loop) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		if msg := cmd(); msg != nil {
			l.msgs <- msg
		}
	}()
}

func (l *loop) feed(msg tea.Msg) {
	if b, ok := msg.(tea.BatchMsg); ok {
		for _, c := range b {
			l.run(c)
		}
		return
	}
	if _, ok := msg.(tickMsg); ok {
		return
	}
	mm, cmd := l.m.Update(msg)
	*l.m = mm.(Model)
	l.run(cmd)
}

// until runs the loop until done says so, answering cards with do.
func (l *loop) until(what string, done func() bool, do func()) {
	l.t.Helper()
	deadline := time.After(10 * time.Second)
	for !done() {
		select {
		case msg := <-l.msgs:
			l.feed(msg)
		case act := <-l.m.api.srv.Acts():
			l.run(l.m.applyAPIAct(act))
		case <-time.After(20 * time.Millisecond):
			if do != nil {
				do()
			}
		case <-deadline:
			l.t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func waitingOf(m *Model, k inbox.Kind) *inbox.Item {
	for _, it := range m.store.Items() {
		if it.Kind == k && it.State == inbox.Waiting {
			return it
		}
	}
	return nil
}

// answerCard reads a waiting card of kind k to its end and answers it.
func answerCard(t *testing.T, m *Model, k inbox.Kind, verb string) bool {
	it := waitingOf(m, k)
	if it == nil {
		return false
	}
	m.tab = tabInbox
	m.refresh()
	m.selectItem(it)
	readToEnd(t, m, it)
	answerGuidance(m, verb)
	return true
}

func mac(key []byte, dir, conn, nonce string) string {
	h := hmac.New(sha256.New, key)
	h.Write([]byte("pair-auth\n" + dir + "\n" + conn + "\n" + nonce))
	return hex.EncodeToString(h.Sum(nil))
}

// controlModel is a model with the API on and a fake claude for the
// orchestrator; launches records the options of every start.
func controlModel(t *testing.T) (*loop, string, *[]orch.Options) {
	apiEnv(t)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	dir := t.TempDir()
	script := filepath.Join(dir, "claude")
	os.WriteFile(script, []byte(controlClaude), 0o755)
	hook := filepath.Join(dir, "den-hook")
	os.WriteFile(hook, []byte("#!/bin/sh\ncat >/dev/null\necho '{\"decision\":\"block\"}'\n"), 0o755)
	var mu sync.Mutex
	launches := &[]orch.Options{}
	defer func(f func(orch.Options) (*orch.Orchestrator, error)) {
		t.Cleanup(func() { launchOrchestrator = f })
	}(launchOrchestrator)
	launchOrchestrator = func(o orch.Options) (*orch.Orchestrator, error) {
		mu.Lock()
		*launches = append(*launches, o)
		mu.Unlock()
		o.Command, o.HookCommand, o.Dir = []string{script}, []string{hook}, dir
		p, err := orch.Start(context.Background(), o)
		if p != nil {
			t.Cleanup(p.Close)
		}
		return p, err
	}
	defer func(f func(context.Context, []string) (string, error)) {
		t.Cleanup(func() { orchVersion = f })
	}(orchVersion)
	orchVersion = func(context.Context, []string) (string, error) { return "2.1.296", nil }
	m := testModel(140, 40)
	m.base = append(m.base, m.agents...)
	apiOn(t, &m)
	return &loop{t: t, m: &m, msgs: make(chan tea.Msg, 64)}, dir, launches
}

// pairApp pairs a client on the model's socket, answering the pairing card
// with ^Y, and proves the key on a fresh connection.
func pairApp(t *testing.T, l *loop) *apiConn {
	t.Helper()
	mm, _ := l.m.apiCommand([]string{"pair"})
	*l.m = mm.(Model)
	words := l.m.ctl.code
	if len(words) != 8 || !strings.Contains(l.m.codeLine(), "Type this only into Drey's pairing window") {
		t.Fatalf("code %v, line %q", words, l.m.codeLine())
	}
	for _, e := range l.m.orch.chat {
		if strings.Contains(e.text, words[0]+" "+words[1]) {
			t.Fatal("the pairing code went into the chat")
		}
	}
	c := dialAPI(t, l.m)
	got := make(chan map[string]any, 1)
	go func() { got <- c.callNoT("pair.claim", map[string]string{"code": strings.Join(words, " ")}) }()
	var reply map[string]any
	l.until("the claim's answer", func() bool {
		select {
		case reply = <-got:
			return true
		default:
			return false
		}
	}, func() { answerCard(t, l.m, inbox.Pair, "allow") })
	res, _ := reply["result"].(map[string]any)
	key, _ := hex.DecodeString(res["key"].(string))
	if l.m.codeLine() != "" {
		t.Error("the code stays on screen after its claim")
	}
	ch := c.call("pair.challenge", nil)["result"].(map[string]any)
	nonce, conn := ch["nonce"].(string), ch["conn"].(string)
	auth := c.call("pair.auth", map[string]string{"pairing": res["pairing"].(string), "mac": mac(key, "app-to-den", conn, nonce)})
	if r, _ := auth["result"].(map[string]any); r == nil || r["mac"] != mac(key, "den-to-app", conn, nonce) {
		t.Fatalf("auth %v", auth)
	}
	return c
}

// callNoT is call without failing the test from another goroutine.
func (c *apiConn) callNoT(op string, params any) map[string]any {
	c.id++
	id := c.id
	raw, _ := json.Marshal(map[string]any{"v": 1, "id": id, "op": op, "params": params})
	c.nc.Write(append(raw, '\n'))
	for {
		c.nc.SetReadDeadline(time.Now().Add(15 * time.Second))
		b, err := c.r.ReadBytes('\n')
		if err != nil {
			return map[string]any{"error": err.Error()}
		}
		var l map[string]any
		json.Unmarshal(b, &l)
		if n, ok := l["id"].(float64); ok && int(n) == id {
			return l
		}
	}
}

// chunks reads an app turn's chunks until done or error.
func chunks(c *apiConn) <-chan []map[string]any {
	out := make(chan []map[string]any, 1)
	go func() {
		var got []map[string]any
		for {
			c.nc.SetReadDeadline(time.Now().Add(15 * time.Second))
			b, err := c.r.ReadBytes('\n')
			if err != nil {
				out <- got
				return
			}
			var l map[string]any
			json.Unmarshal(b, &l)
			if l["event"] != "orch" {
				continue
			}
			ch := l["data"].(map[string]any)
			got = append(got, ch)
			if ch["type"] == "done" || ch["type"] == "error" {
				out <- got
				return
			}
		}
	}()
	return out
}

// The whole way, in Den: pair on ^Y, an app's turn that starts the
// orchestrator in control mode, after Den's preface; the tool it wants
// waits on a card the human answers, and the app hears only that
// something waits, then the reply and done.
func TestAppTurnInDen(t *testing.T) {
	l, dir, launches := controlModel(t)
	c := pairApp(t, l)
	reply := c.call("orch.say", map[string]string{"text": "who is busy? SPAWN-PLEASE"})
	if reply["ok"] != true {
		t.Fatalf("orch.say %v", reply)
	}
	stream := chunks(c)
	var got []map[string]any
	l.until("the app's turn", func() bool {
		select {
		case got = <-stream:
			return true
		default:
			return false
		}
	}, func() { answerCard(t, l.m, inbox.ControlAsk, "allow") })
	if len(*launches) == 0 || !(*launches)[0].Control {
		t.Fatalf("launches %+v", *launches)
	}
	var types []string
	item := ""
	for _, ch := range got {
		types = append(types, ch["type"].(string))
		if ch["type"] == "needs_approval" {
			item, _ = ch["item"].(string)
		}
	}
	if len(types) == 0 || types[len(types)-1] != "done" || item == "" {
		t.Fatalf("chunks %v", got)
	}
	turns, _ := os.ReadFile(filepath.Join(dir, "turns"))
	if !strings.Contains(string(turns), "Den: the human sends this turn through") || !strings.Contains(string(turns), "who is busy? SPAWN-PLEASE") {
		t.Errorf("turn %s", turns)
	}
	answers, _ := os.ReadFile(filepath.Join(dir, "answers"))
	if !strings.Contains(string(answers), `"behavior":"allow"`) {
		t.Errorf("answers %s", answers)
	}
	if l.m.ctl.turn != nil {
		t.Error("the app's turn outlived its result")
	}
}

// The human's own words in Den stop an app's turn and go first; a report
// or Den's own turn waits for the app's turn.
func TestHumanStopsAnAppTurn(t *testing.T) {
	l, dir, _ := controlModel(t)
	c := pairApp(t, l)
	c.call("orch.say", map[string]string{"text": "take your time SLOW-PLEASE"})
	stream := chunks(c)
	l.until("the app's turn to go out", func() bool { return l.m.ctl.sent }, nil)
	l.run(l.m.tell("", "stop that", "stop that"))
	var got []map[string]any
	l.until("the app's turn to end", func() bool {
		select {
		case got = <-stream:
			return true
		default:
			return false
		}
	}, nil)
	last := got[len(got)-1]
	if last["type"] != "error" || !strings.Contains(last["message"].(string), "the human wrote in Den") {
		t.Errorf("last chunk %v", last)
	}
	l.until("the human's message to go out", func() bool {
		raw, _ := os.ReadFile(filepath.Join(dir, "turns"))
		return strings.Contains(string(raw), "stop that")
	}, nil)
	controls, _ := os.ReadFile(filepath.Join(dir, "controls"))
	if !strings.Contains(string(controls), `"interrupt"`) {
		t.Errorf("controls %s", controls)
	}
}

// ^N on the pairing card pairs nothing, and control stays off.
func TestPairRefusedInDen(t *testing.T) {
	l, _, _ := controlModel(t)
	mm, _ := l.m.apiCommand([]string{"pair"})
	*l.m = mm.(Model)
	c := dialAPI(t, l.m)
	got := make(chan map[string]any, 1)
	go func() { got <- c.callNoT("pair.claim", map[string]string{"code": strings.Join(l.m.ctl.code, " ")}) }()
	var reply map[string]any
	l.until("the claim's answer", func() bool {
		select {
		case reply = <-got:
			return true
		default:
			return false
		}
	}, func() { answerCard(t, l.m, inbox.Pair, "deny") })
	if e, _ := reply["error"].(map[string]any); e == nil || e["code"] != "forbidden" {
		t.Errorf("claim %v", reply)
	}
	if l.m.controlWanted() {
		t.Error("control on after ^N")
	}
}

// The guard stopping the orchestrator in an app's turn ends the turn and
// revokes every pairing.
func TestBackstopUnpairs(t *testing.T) {
	l, _, _ := controlModel(t)
	c := pairApp(t, l)
	c.call("orch.say", map[string]string{"text": "hold on SLOW-PLEASE"})
	stream := chunks(c)
	l.until("the app's turn to go out", func() bool { return l.m.ctl.sent }, nil)
	l.feed(orchEventMsg{ev: orch.Event{Kind: orch.Backstop, Text: "in an app's turn x ran without Den allowing it"}, from: l.m.orch.proc})
	var got []map[string]any
	l.until("the app's turn to end", func() bool {
		select {
		case got = <-stream:
			return true
		default:
			return false
		}
	}, nil)
	if len(got) == 0 || got[len(got)-1]["type"] != "error" || !strings.Contains(got[len(got)-1]["message"].(string), "Den stopped the orchestrator") {
		t.Errorf("chunks %v", got)
	}
	if l.m.api.srv.Paired() || len(l.m.api.srv.Pairings()) != 0 {
		t.Errorf("pairings after the backstop: %+v", l.m.api.srv.Pairings())
	}
}

// An orchestrator silent for too long in an app's turn: the app is told,
// and Den stops the turn.
func TestAppTurnSilenceBound(t *testing.T) {
	defer func(d time.Duration) { appTurnSilence = d }(appTurnSilence)
	appTurnSilence = 100 * time.Millisecond
	l, dir, _ := controlModel(t)
	c := pairApp(t, l)
	c.call("orch.say", map[string]string{"text": "quiet one SLOW-PLEASE"})
	stream := chunks(c)
	l.until("the app's turn to go out", func() bool { return l.m.ctl.sent }, nil)
	time.Sleep(150 * time.Millisecond)
	l.run(l.m.controlTick(time.Now()))
	var got []map[string]any
	l.until("the app's turn to end", func() bool {
		select {
		case got = <-stream:
			return true
		default:
			return false
		}
	}, nil)
	if last := got[len(got)-1]; last["type"] != "error" || !strings.Contains(last["message"].(string), "said nothing for two minutes") {
		t.Errorf("chunks %v", got)
	}
	controls, _ := os.ReadFile(filepath.Join(dir, "controls"))
	if !strings.Contains(string(controls), `"interrupt"`) {
		t.Errorf("controls %s", controls)
	}
}

// On a Claude Code release control was not checked on, the app's turn ends
// with an error that says so, and the orchestrator keeps working for the
// human without control.
func TestControlNeedsACheckedRelease(t *testing.T) {
	l, dir, launches := controlModel(t)
	orchVersion = func(context.Context, []string) (string, error) { return "2.1.300", nil }
	c := pairApp(t, l)
	c.call("orch.say", map[string]string{"text": "who is busy?"})
	stream := chunks(c)
	var got []map[string]any
	l.until("the app's turn to end", func() bool {
		select {
		case got = <-stream:
			return true
		default:
			return false
		}
	}, nil)
	if last := got[len(got)-1]; last["type"] != "error" || !strings.Contains(last["message"].(string), "2.1.289, 2.1.296") {
		t.Errorf("chunks %v", got)
	}
	if len(*launches) != 1 || (*launches)[0].Control {
		t.Fatalf("launches %+v", *launches)
	}
	proc := l.m.orch.proc
	l.run(l.m.controlTick(time.Now()))
	if l.m.orch.proc != proc {
		t.Error("Den restarted the orchestrator for control it cannot have")
	}
	l.run(l.m.tell("", "hello", "hello"))
	l.until("the human's message", func() bool {
		raw, _ := os.ReadFile(filepath.Join(dir, "turns"))
		return strings.Contains(string(raw), "hello")
	}, nil)
}

// An agent's report that Den admits while an app's turn runs waits for
// that turn's end, then goes as its own turn, composed once (the security
// review's PoC: pair, orch.say, sendReport while the turn is open).
func TestReportHeldBehindAnAppTurn(t *testing.T) {
	l, dir, _ := controlModel(t)
	c := pairApp(t, l)
	c.call("orch.say", map[string]string{"text": "slow one SLOW-PLEASE"})
	l.until("the app's turn to go out", func() bool { return l.m.ctl.sent }, nil)
	l.run(l.m.sendReport(l.m.base[0], "REPORT-BODY tests pass"))
	time.Sleep(200 * time.Millisecond)
	turns := func() string { raw, _ := os.ReadFile(filepath.Join(dir, "turns")); return string(raw) }
	if strings.Contains(turns(), "REPORT-BODY") {
		t.Fatal("the report reached the orchestrator inside the app's turn")
	}
	if len(l.m.ctl.held) != 1 || len(l.m.orch.origins) != 1 || l.m.orch.origins[0] != originApp {
		t.Fatalf("held %d, origins %v", len(l.m.ctl.held), l.m.orch.origins)
	}
	c.nc.Close() // the app's turn ends: Den stops it
	l.until("the report to go out", func() bool { return strings.Contains(turns(), "REPORT-BODY") }, nil)
	if n := strings.Count(turns(), "agent_report"); n != 2 { // its open and close tags, once
		t.Errorf("the report was composed %d/2 times: %s", n, turns())
	}
	if l.m.ctl.turn != nil {
		t.Error("the app's turn outlived its result")
	}
}
