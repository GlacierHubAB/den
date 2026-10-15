package orch

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/peermsg"
)

// stdinBuf records what Den writes to the process.
type stdinBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *stdinBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *stdinBuf) Close() error { return nil }

// answers are the can_use_tool answers written, by request id.
func (s *stdinBuf) answers() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for _, l := range strings.Split(s.b.String(), "\n") {
		var r struct {
			Type     string `json:"type"`
			Response struct {
				RequestID string `json:"request_id"`
				Response  struct {
					Behavior string `json:"behavior"`
				} `json:"response"`
			} `json:"response"`
		}
		if json.Unmarshal([]byte(l), &r) == nil && r.Type == "control_response" {
			out[r.Response.RequestID] = r.Response.Response.Behavior
		}
	}
	return out
}

func controlOrch(spawn bool) (*Orchestrator, *stdinBuf) {
	in := &stdinBuf{}
	o := &Orchestrator{stdin: in, events: make(chan Event, 64), done: make(chan struct{}), sent: map[string]time.Time{},
		ctl: newControl(Options{Spawn: spawn})}
	return o, in
}

func feed(o *Orchestrator, lines ...string) {
	o.readStdout(strings.NewReader(strings.Join(lines, "\n") + "\n"))
}

func canUse(id, tool, useID string) string {
	return `{"type":"control_request","request_id":"` + id + `","request":{"subtype":"can_use_tool","tool_name":"` + tool + `","tool_use_id":"` + useID + `","input":{"x":1}}}`
}

func replayOf(content string) string {
	b, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": content}, "isReplay": true})
	return string(b)
}

func toolUse(id, tool string) string {
	return `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"` + id + `","name":"` + tool + `","input":{}}]}}`
}

func toolResult(id string, isErr bool) string {
	e := "false"
	if isErr {
		e = "true"
	}
	return `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `","content":"ok","is_error":` + e + `}]}}`
}

const resultLine = `{"type":"result","subtype":"success","is_error":false,"result":"done","num_turns":2}`

func drain(o *Orchestrator) []Event {
	var out []Event
	for {
		select {
		case e := <-o.events:
			out = append(out, e)
		default:
			return out
		}
	}
}

// With control the orchestrator asks Den for every tool but ListAgents,
// has no SendMessage, and Den's settings leave its permission prompts to
// Den.
func TestControlArgv(t *testing.T) {
	o := Options{Command: []string{"claude"}, Executable: "/usr/bin/den", Spawn: true, Control: true, SettingsFile: "/x"}
	args := strings.Join(Argv(o), "\x00")
	for _, want := range []string{
		"--tools\x00ListAgents\x00", "--permission-mode\x00manual\x00", "--permission-prompt-tool\x00stdio\x00",
		"--disallowedTools\x00SendMessage\x00", "--allowedTools\x00ListAgents\x00", `"args":["mcp","--control"]`,
	} {
		if !strings.Contains(args, want) {
			t.Errorf("argv lacks %q: %q", want, args)
		}
	}
	if strings.Contains(args, "dontAsk") || strings.Contains(args, "\x00SendMessage,") {
		t.Errorf("argv %q", args)
	}
	o.Spawn = false
	if !strings.Contains(strings.Join(Argv(o), " "), `"args":["mcp","--missions-only","--control"]`) {
		t.Error("without spawn, control's den mcp offers missions only")
	}
	if _, ok := denSettings(o)["hooks"].(map[string]any)["PermissionRequest"]; ok {
		t.Error("a PermissionRequest hook would answer before Den")
	}
	if _, ok := denSettings(Options{})["hooks"].(map[string]any)["PermissionRequest"]; !ok {
		t.Error("without control the PermissionRequest hook stays")
	}
}

// Outside an app's turn Den answers at once: its own tools yes, others no.
func TestControlHumanTurnAnswersAtOnce(t *testing.T) {
	o, in := controlOrch(true)
	feed(o, canUse("r1", SpawnAgentTool, "u1"), canUse("r2", "Bash", "u2"), canUse("r3", SendMessageTool, "u3"), canUse("r4", "WebFetch", "u4"))
	got := in.answers()
	want := map[string]string{"r1": "allow", "r2": "deny", "r3": "allow", "r4": "deny"}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s: %q, want %q", id, got[id], w)
		}
	}
	for _, e := range drain(o) {
		if e.Kind == PermissionAsk {
			t.Errorf("a human turn's request reached Den's human: %+v", e)
		}
	}
	o2, in2 := controlOrch(false)
	feed(o2, canUse("r1", SpawnAgentTool, "u1"), canUse("r2", MissionTool, "u2"))
	if a := in2.answers(); a["r1"] != "deny" || a["r2"] != "allow" {
		t.Errorf("without spawn: %v", a)
	}
}

// In an app's turn every request waits for Den's human; the turn ends on
// its own result, after its prompt was replayed.
func TestControlAppTurnAsksTheHuman(t *testing.T) {
	o, in := controlOrch(true)
	if err := o.SendApp("t-1", "fixed preface\n\nwho is busy?"); err != nil {
		t.Fatal(err)
	}
	if err := o.SendApp("t-2", "again"); err != ErrAppTurn {
		t.Errorf("a second app turn: %v", err)
	}
	content := peermsg.Defuse("fixed preface\n\nwho is busy?")
	feed(o, replayOf(content), toolUse("u1", SpawnAgentTool), canUse("r1", SpawnAgentTool, "u1"))
	if a := in.answers(); a["r1"] != "" {
		t.Fatalf("answered before the human: %v", a)
	}
	var ask *Event
	for _, e := range drain(o) {
		if e.Kind == PermissionAsk {
			e := e
			ask = &e
		}
	}
	if ask == nil || ask.Tool != SpawnAgentTool || ask.Turn != "t-1" || ask.RequestID != "r1" {
		t.Fatalf("ask %+v", ask)
	}
	if !o.AnswerPermission("r1", true, "") || o.AnswerPermission("r1", false, "late") {
		t.Error("an answer counts once")
	}
	feed(o, toolResult("u1", false), resultLine)
	var res *Event
	for _, e := range drain(o) {
		if e.Kind == Backstop {
			t.Fatalf("an allowed tool tripped the backstop: %s", e.Text)
		}
		if e.Kind == Result {
			e := e
			res = &e
		}
	}
	if res == nil || res.Turn != "t-1" || o.AppTurn() != "" {
		t.Errorf("result %+v, open turn %q", res, o.AppTurn())
	}
	if a := in.answers(); a["r1"] != "allow" {
		t.Errorf("answers %v", a)
	}
}

// A result before the app's prompt was replayed, or a refused prompt's,
// does not end the app's turn: what cannot be placed counts as the app's.
func TestControlAppTurnEndsOnlyOnItsOwnResult(t *testing.T) {
	o, in := controlOrch(true)
	o.SendApp("t-1", "hello")
	feed(o, resultLine, `{"type":"result","subtype":"success","is_error":false,"result":"blocked","num_turns":0}`)
	if o.AppTurn() != "t-1" {
		t.Fatal("an earlier result closed the app's turn")
	}
	feed(o, canUse("r1", SkillsTool, "u1"))
	if a := in.answers(); a["r1"] != "" {
		t.Errorf("a request after a stray result was answered as a human turn's: %v", a)
	}
	feed(o, replayOf("other text"), resultLine)
	if o.AppTurn() != "t-1" {
		t.Error("another prompt's replay ended the app's turn")
	}
	feed(o, replayOf("hello"), resultLine)
	if o.AppTurn() != "" {
		t.Error("the app's own result did not end its turn")
	}
	if a := in.answers(); a["r1"] != "deny" {
		t.Errorf("a request left open at the turn's end: %v", a)
	}
}

// A request nobody answers is denied after the deadline.
func TestControlUnansweredIsDenied(t *testing.T) {
	defer func(d time.Duration) { PermissionDeadline = d }(PermissionDeadline)
	PermissionDeadline = 50 * time.Millisecond
	o, in := controlOrch(true)
	o.SendApp("t-1", "hello")
	feed(o, canUse("r1", CloseAgentTool, "u1"))
	time.Sleep(200 * time.Millisecond)
	if a := in.answers(); a["r1"] != "deny" {
		t.Errorf("answers %v", a)
	}
	expired := false
	for _, e := range drain(o) {
		expired = expired || (e.Kind == PermissionExpired && e.RequestID == "r1")
	}
	if !expired {
		t.Error("no PermissionExpired")
	}
}

// In an app's turn a tool that ran without Den allowing it, and in any
// turn Claude Code's own SendMessage that ran, trips the backstop;
// ListAgents, denied calls and a SendMessage Claude Code refused do not.
func TestControlBackstop(t *testing.T) {
	// What 2.1.289 and 2.1.296 answer a disallowed SendMessage with.
	refused := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"u1","is_error":true,"content":"<tool_use_error>Error: No such tool available: SendMessage</tool_use_error>"}]}}`
	for _, c := range []struct {
		name  string
		app   bool
		lines []string
		trip  bool
	}{
		{"unasked tool ran", true, []string{toolUse("u1", SpawnAgentTool), toolResult("u1", false)}, true},
		{"SendMessage ran in an app's turn", true, []string{toolUse("u1", "SendMessage"), toolResult("u1", false)}, true},
		{"SendMessage ran in a human turn", false, []string{toolUse("u1", "SendMessage"), toolResult("u1", false)}, true},
		{"SendMessage refused in an app's turn", true, []string{toolUse("u1", "SendMessage"), refused}, false},
		{"SendMessage refused in a human turn", false, []string{toolUse("u1", "SendMessage"), refused}, false},
		{"SendMessage failed some other way", false, []string{toolUse("u1", "SendMessage"), toolResult("u1", true)}, true},
		{"SendMessage asked for, no result yet", false, []string{toolUse("u1", "SendMessage")}, false},
		{"ListAgents and a denied call", true, []string{toolUse("u1", "ListAgents"), toolResult("u1", false), toolUse("u2", CloseAgentTool), toolResult("u2", true)}, false},
	} {
		o, _ := controlOrch(true)
		lines := c.lines
		if c.app {
			o.SendApp("t-1", "hello")
			lines = append([]string{replayOf("hello")}, lines...)
		}
		feed(o, lines...)
		if o.Tripped() != c.trip {
			t.Errorf("%s: tripped %v", c.name, o.Tripped())
			continue
		}
		for _, e := range drain(o) {
			if e.Kind == Backstop && e.Trip != TripTool {
				t.Errorf("%s: trip kind %q", c.name, e.Trip)
			}
		}
	}
}

// With control the rules name send_message, by its exact name, as the way
// to message an agent, and SendMessage only to say it is not available;
// the human's guidance still comes after every rule.
func TestControlPrompt(t *testing.T) {
	for i := 0; i < len(controlSends); i += 2 {
		if !strings.Contains(SystemPrompt, controlSends[i]) {
			t.Errorf("SystemPrompt no longer says %q: the control rules would miss it", controlSends[i])
		}
	}
	rules := PromptFor("", true)
	if strings.Count(rules, ControlNoSendMessage) != 1 {
		t.Fatalf("the control rules do not say SendMessage is unavailable exactly once")
	}
	if left := strings.ReplaceAll(rules, ControlNoSendMessage, ""); strings.Contains(left, "SendMessage") {
		i := strings.Index(left, "SendMessage")
		t.Errorf("the control rules name SendMessage as usable: …%s…", left[max(0, i-80):min(len(left), i+40)])
	}
	if !strings.Contains(rules, "send it with send_message ("+SendMessageTool+")") || !strings.Contains(rules, "ListAgents and Den's send_message ("+SendMessageTool+")") {
		t.Error("the control rules do not name send_message by its exact name")
	}
	if PromptFor("", false) != SystemPrompt || Prompt("") != SystemPrompt {
		t.Error("without control the rules changed")
	}
	g := PromptFor("be brief", true)
	if !strings.HasPrefix(g, ControlSystemPrompt+"\n\n"+ControlPrompt+"\n\n<human_guidance>\n") || !strings.HasSuffix(g, "be brief\n</human_guidance>") {
		t.Errorf("guidance is not after the control rules: %q", g[len(g)-200:])
	}
	args := Argv(Options{Command: []string{"claude"}, Control: true, Guidance: "be brief", SettingsFile: "/x"})
	for i, a := range args {
		if a == "--append-system-prompt" && args[i+1] != g {
			t.Error("Argv does not pass the control rules")
		}
	}
}

// A standing allowance the human gave the app is answered at once in its
// turn; send_message's only for the peer it names, and a tool that cannot
// have one still asks.
func TestControlStandingAllowance(t *testing.T) {
	o, in := controlOrch(true)
	o.SendApp("t-1", "hello", AllowRule(MissionTool, ""), AllowRule(SendMessageTool, "worker-otter"), "Bash", AllowRule(SpawnAgentTool, ""))
	feed(o,
		canUse("r1", MissionTool, "u1"),
		`{"type":"control_request","request_id":"r2","request":{"subtype":"can_use_tool","tool_name":"`+SendMessageTool+`","tool_use_id":"u2","input":{"to":"@worker-otter [Busy Otter]","message":"x"}}}`,
		`{"type":"control_request","request_id":"r3","request":{"subtype":"can_use_tool","tool_name":"`+SendMessageTool+`","tool_use_id":"u3","input":{"to":"worker-fox","message":"x"}}}`,
		canUse("r4", "Bash", "u4"),
		canUse("r5", SpawnAgentTool, "u5"))
	a := in.answers()
	if a["r1"] != "allow" || a["r2"] != "allow" || a["r3"] != "" || a["r4"] != "" || a["r5"] != "" {
		t.Errorf("answers %v", a)
	}
	k := map[Kind]int{}
	for _, e := range drain(o) {
		k[e.Kind]++
	}
	if k[PermissionStanding] != 2 || k[PermissionAsk] != 3 {
		t.Errorf("events %v", k)
	}
	o2, in2 := controlOrch(true)
	o2.SendApp("t-2", "again")
	feed(o2, canUse("r1", MissionTool, "u1"))
	if in2.answers()["r1"] != "" {
		t.Error("an allowance outlived the turn it was given for")
	}
}

// Taking an allowance back reaches the turn that is running: its next
// request asks.
func TestControlStandingTakenBackMidTurn(t *testing.T) {
	o, in := controlOrch(true)
	o.SendApp("t-1", "hello", AllowRule(MissionTool, ""))
	feed(o, canUse("r1", MissionTool, "u1"))
	o.SetStanding()
	feed(o, canUse("r2", MissionTool, "u2"))
	if a := in.answers(); a["r1"] != "allow" || a["r2"] != "" {
		t.Errorf("answers %v", a)
	}
}

// Standing allowances answer at most StandingMax requests in one turn.
func TestControlStandingCap(t *testing.T) {
	o, in := controlOrch(true)
	o.SendApp("t-1", "hello", AllowRule(MissionTool, ""))
	var lines []string
	for i := 0; i < StandingMax+2; i++ {
		lines = append(lines, canUse("r"+string(rune('a'+i)), MissionTool, "u"+string(rune('a'+i))))
	}
	feed(o, lines...)
	allowed := 0
	for _, b := range in.answers() {
		if b == "allow" {
			allowed++
		}
	}
	if allowed != StandingMax {
		t.Errorf("%d answered by standing allowances, want %d", allowed, StandingMax)
	}
}

// A request still waiting for the human when the process dies goes with
// it: its deadline never fires into the closed events channel (a send on
// it would crash Den), and a late answer finds nothing.
func TestControlPendingPermissionOutlivesNoProcess(t *testing.T) {
	defer func(d time.Duration) { PermissionDeadline = d }(PermissionDeadline)
	PermissionDeadline = 100 * time.Millisecond
	o, _ := fakeWith(t, canUse("r1", SpawnAgentTool, "u1"))
	o.Control = true
	proc, err := Start(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if err := proc.SendApp("t-1", "hello"); err != nil {
		t.Fatal(err)
	}
	asked := false
	for e := range proc.Events() {
		if e.Kind == PermissionAsk {
			asked = true
			break
		}
	}
	if !asked {
		t.Fatal("no PermissionAsk")
	}
	proc.Kill()
	for range proc.Events() {
	}
	time.Sleep(3 * PermissionDeadline) // past the deadline: no panic
	if proc.AnswerPermission("r1", true, "") {
		t.Error("a request answered after its process was gone")
	}
}
