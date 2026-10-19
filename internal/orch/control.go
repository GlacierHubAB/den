package orch

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/GlacierHubAB/den/internal/peermsg"
)

// Control: a paired app (den-api.sock) speaks to the orchestrator. With
// Options.Control the orchestrator runs in manual mode with its permission
// prompts sent to Den (--permission-prompt-tool stdio), Claude Code's own
// SendMessage disallowed (it never prompts, so it could not be held) and
// Den's send_message in its place. Every tool but ListAgents then asks Den
// first, and Den answers here, on the reader's goroutine, never waiting on
// its screen:
//
//   - in a turn an app sent (SendApp), until that turn's own result, every
//     request goes to Den's human as a PermissionAsk event, and is denied if
//     nobody answers within PermissionDeadline;
//   - in any other turn, Den's named tools are allowed and every other tool
//     is denied at once.
//
// A tool that runs in an app's turn without Den having allowed it trips
// the backstop.

// SendMessageTool is Den's send_message: the orchestrator's messages to
// agents with control on, delivered by Den the way SendMessage would.
const SendMessageTool = "mcp__" + MCPServer + "__send_message"

// ControlPrompt follows the control rules (ControlSystemPrompt).
const ControlPrompt = "Den may ask the human before any tool runs in this session. While it asks, wait for the answer; if the human declines, say so, and never try another way to do the same thing."

// controlSends are the places SystemPrompt names SendMessage as a tool to
// use, each with what ControlSystemPrompt says there instead. Every one
// must be found in SystemPrompt (a test checks), so that an edit to it
// cannot leave the control rules pointing at SendMessage.
var controlSends = []string{
	"(what ListAgents and SendMessage use)",
	"(what ListAgents and send_message use)",
	"You have exactly two tools, ListAgents and SendMessage, and need nothing else.",
	"You have exactly two tools for the fleet, ListAgents and Den's send_message (" + SendMessageTool + "), and need nothing else. " + ControlNoSendMessage + " send_message is the only way to message an agent: it reaches the agent the same way, and its report comes back to you as before.",
	"send it with SendMessage to that agent's peer name",
	"send it with send_message (" + SendMessageTool + ") to that agent's peer name",
	"dispatch per its shape: SendMessage to the running agents that own sub-goals",
	"dispatch per its shape: send_message to the running agents that own sub-goals",
}

// ControlNoSendMessage is the one place the control rules name
// SendMessage.
const ControlNoSendMessage = "SendMessage is not available in this session: never call it."

// ControlSystemPrompt is SystemPrompt with control on: send_message, by
// its exact name, wherever SendMessage was the tool to use.
var ControlSystemPrompt = strings.NewReplacer(controlSends...).Replace(SystemPrompt)

// SendVerified are the Claude Code releases on which Den's send_message
// was checked to reach a session exactly as SendMessage does: the inbox
// socket's line format and how the receiver shows it. Control runs, and
// Den delivers, only when the orchestrator and the receiving session run
// one of them.
var SendVerified = []string{"2.1.289", "2.1.296"}

// SendVerifiedVersion reports whether send_message may run on a release.
func SendVerifiedVersion(v string) bool {
	for _, ok := range SendVerified {
		if v == ok {
			return true
		}
	}
	return false
}

// ControlFlag is the `den mcp` argument that adds send_message.
const ControlFlag = "--control"

// PermissionDeadline is how long Den waits for its human on a request
// before it denies it; tests shorten it.
var PermissionDeadline = 60 * time.Second

// control is the state of an orchestrator started with Options.Control.
type control struct {
	human map[string]bool // the tools Den allows in turns that are not an app's
	app   appTurn
	perms map[string]*permission // asked, not yet answered, by request id
	// uses are the tool calls of the app's turn by tool_use id, and
	// allowed the ones Den allowed.
	uses    map[string]string
	allowed map[string]bool
	// sends are Claude Code's own SendMessage calls, in any turn, by
	// tool_use id, until their result.
	sends map[string]bool
	// standing are the human's standing allowances for the app whose turn
	// is open (AllowRule keys), set with SendApp and SetStanding;
	// standingUsed counts the requests they answered in that turn.
	standing     map[string]bool
	standingUsed int
}

// newControl is the control state of an orchestrator started with o, every
// map made: the tests build it the same way.
func newControl(o Options) *control {
	return &control{human: humanTools(o), perms: map[string]*permission{}, uses: map[string]string{}, allowed: map[string]bool{}, sends: map[string]bool{}}
}

// StandingMax is how many requests standing allowances answer in one app
// turn; after that every request asks.
const StandingMax = 5

// SetStanding replaces the open app turn's standing allowances at once:
// when the human takes one back, or control off, the turn running is
// asked again from its next request.
func (o *Orchestrator) SetStanding(rules ...string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.ctl == nil || !o.ctl.app.open {
		return
	}
	o.ctl.standing = map[string]bool{}
	for _, r := range rules {
		o.ctl.standing[r] = true
	}
}

// AllowRule is the key of a standing allowance for a paired app: a tool
// Den allows in its turns without asking, or for send_message one peer it
// may message. Only Den's own tools can be allowed this way.
func AllowRule(tool, peer string) string {
	if tool == SendMessageTool {
		return tool + " " + peer
	}
	return tool
}

// Allowable reports whether a tool may have a standing allowance: Den's
// own tools, never anything else.
func Allowable(tool string) bool {
	// Never one to start an agent (its brief is a whole prompt), open a
	// terminal, close agents or install skills: those always ask.
	return tool == SendMessageTool || tool == MissionTool
}

// PeerName is a send_message "to" as Den matches it: no leading "@", no
// " [...]" suffix, trimmed. Den's delivery (ui's sendTarget) and the
// standing allowances use the same name.
func PeerName(to string) string {
	p := strings.TrimPrefix(strings.TrimSpace(to), "@")
	if i := strings.Index(p, " ["); i > 0 {
		p = p[:i]
	}
	return p
}

// RuleOf is the AllowRule a request would need: send_message's by its
// "to".
func RuleOf(tool string, input json.RawMessage) string {
	if tool == SendMessageTool {
		var in struct {
			To string `json:"to"`
		}
		json.Unmarshal(input, &in)
		return AllowRule(tool, PeerName(in.To))
	}
	return AllowRule(tool, "")
}

// appTurn is the turn an app sent, from SendApp until its own result.
type appTurn struct {
	open     bool
	id       string
	hash     string // of the prompt as written
	replayed bool   // Claude Code replayed that prompt: its result ends the turn
}

type permission struct {
	tool, toolUseID string
	input           json.RawMessage
	timer           *time.Timer
}

// humanTools are the tools Den allows outside an app's turn (HumanTurnTools).
func humanTools(o Options) map[string]bool { return HumanTurnTools(o.Spawn) }

// HumanTurnTools are the tools Den allows the orchestrator in a turn that
// is not an app's, with control on: send_message, DenTools, and SpawnTools
// with spawn. Without control the same lists are its --allowedTools.
func HumanTurnTools(spawn bool) map[string]bool {
	set := map[string]bool{SendMessageTool: true}
	for _, t := range DenTools {
		set[t] = true
	}
	if spawn {
		for _, t := range SpawnTools {
			set[t] = true
		}
	}
	return set
}

// controlMCPConfig is the --mcp-config with control on: `den mcp`, with
// missions only unless spawn, and send_message.
func controlMCPConfig(exe string, spawn bool) string {
	args := []string{"mcp", ControlFlag}
	if !spawn {
		args = []string{"mcp", MissionsOnly, ControlFlag}
	}
	b, _ := json.Marshal(map[string]any{
		"mcpServers": map[string]any{MCPServer: map[string]any{"command": exe, "args": args}},
	})
	return string(b)
}

// ErrAppTurn is SendApp's refusal while an app's turn is open.
var ErrAppTurn = errors.New("an app's turn is already running")

// SendApp writes an app's turn, as Send does, and opens its window: every
// permission request from now until this turn's own result asks Den's
// human, unless it matches one of standing (AllowRule keys), the human's
// standing allowances for that app. id names the turn in events.
func (o *Orchestrator) SendApp(id, content string, standing ...string) error {
	content = peermsg.Defuse(content)
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.ctl == nil {
		return errors.New("the orchestrator was not started with control")
	}
	if o.closed {
		return errors.New("orchestrator is closed")
	}
	if o.ctl.app.open {
		return ErrAppTurn
	}
	h := peermsg.Hash(content)
	o.ctl.app = appTurn{open: true, id: id, hash: h}
	o.ctl.uses, o.ctl.allowed, o.ctl.standing, o.ctl.standingUsed = map[string]string{}, map[string]bool{}, map[string]bool{}, 0
	for _, r := range standing {
		o.ctl.standing[r] = true
	}
	o.sent[h] = time.Now()
	return o.writeUser(content)
}

// AppTurn is the open app turn's id, "" when none is open.
func (o *Orchestrator) AppTurn() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.ctl == nil || !o.ctl.app.open {
		return ""
	}
	return o.ctl.app.id
}

// CloseAppTurn closes the open app turn whose result Den missed, and
// denies the permissions it still holds; it returns the turn's id, "" when
// none was open. Den's watchdog calls it when Claude Code has finished its
// turns and the app turn is still open.
func (o *Orchestrator) CloseAppTurn() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	c := o.ctl
	if c == nil || !c.app.open {
		return ""
	}
	id := c.app.id
	c.app = appTurn{}
	for rid := range c.perms {
		o.answerLocked(rid, false, "the turn ended")
	}
	return id
}

// Interrupt asks Claude Code to stop the turn it is on; its result follows.
func (o *Orchestrator) Interrupt() error {
	return o.ControlRequest("den-interrupt-"+time.Now().Format("150405.000000000"), "interrupt")
}

// AnswerPermission answers a PermissionAsk; false when it was answered
// already (by Den's deadline, say) or never asked.
func (o *Orchestrator) AnswerPermission(id string, allow bool, why string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.answerLocked(id, allow, why)
}

func (o *Orchestrator) answerLocked(id string, allow bool, why string) bool {
	if o.ctl == nil || o.exited {
		return false
	}
	p := o.ctl.perms[id]
	if p == nil {
		return false
	}
	delete(o.ctl.perms, id)
	if p.timer != nil {
		p.timer.Stop()
	}
	if allow && p.toolUseID != "" {
		o.ctl.allowed[p.toolUseID] = true
	}
	o.writePermission(id, allow, p.input, why)
	return true
}

// writePermission sends a can_use_tool answer; o.mu is held.
func (o *Orchestrator) writePermission(id string, allow bool, input json.RawMessage, why string) {
	resp := map[string]any{"behavior": "deny", "message": why}
	if allow {
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}
		resp = map[string]any{"behavior": "allow", "updatedInput": input}
	}
	line, err := json.Marshal(map[string]any{
		"type":     "control_response",
		"response": map[string]any{"subtype": "success", "request_id": id, "response": resp},
	})
	if err != nil || o.closed || o.stdin == nil {
		return
	}
	o.stdin.Write(append(line, '\n'))
}

// canUseTool answers a permission request: at once outside an app's turn,
// by Den's human inside one.
func (o *Orchestrator) canUseTool(id, tool, toolUseID string, input json.RawMessage) {
	o.mu.Lock()
	c := o.ctl
	if c == nil {
		// Never asked without control (dontAsk); deny whatever it is.
		o.writePermission(id, false, nil, "Den allows nothing it is asked about")
		o.mu.Unlock()
		return
	}
	if !c.app.open {
		allow := c.human[tool]
		if allow && toolUseID != "" {
			c.allowed[toolUseID] = true
		}
		o.writePermission(id, allow, input, "Den does not allow "+tool)
		o.mu.Unlock()
		return
	}
	turn := c.app.id
	if Allowable(tool) && c.standing[RuleOf(tool, input)] && c.standingUsed < StandingMax {
		c.standingUsed++
		if toolUseID != "" {
			c.allowed[toolUseID] = true
		}
		o.writePermission(id, true, input, "")
		o.mu.Unlock()
		o.emit(Event{Kind: PermissionStanding, RequestID: id, Tool: tool, ToolUseID: toolUseID, Input: input, Turn: turn})
		return
	}
	p := &permission{tool: tool, toolUseID: toolUseID, input: append(json.RawMessage(nil), input...)}
	c.perms[id] = p
	p.timer = time.AfterFunc(PermissionDeadline, func() {
		if o.AnswerPermission(id, false, "nobody answered in Den within a minute") {
			o.emit(Event{Kind: PermissionExpired, RequestID: id, Turn: turn})
		}
	})
	o.mu.Unlock()
	o.emit(Event{Kind: PermissionAsk, RequestID: id, Tool: tool, ToolUseID: toolUseID, Input: input, Turn: turn})
}

// controlToolUse notes a tool call: in an app's turn every one, and in any
// turn Claude Code's own SendMessage, for controlToolResult to check.
func (o *Orchestrator) controlToolUse(id, tool string) {
	o.mu.Lock()
	if c := o.ctl; c != nil {
		if tool == "SendMessage" {
			c.sends[id] = true
		}
		if c.app.open {
			c.uses[id] = tool
		}
	}
	o.mu.Unlock()
}

// sendRefused is what 2.1.289 and 2.1.296 answer a call to a disallowed
// SendMessage with, in an error result, having run nothing.
const sendRefused = "No such tool available: SendMessage"

// controlToolResult checks that a tool that ran was one Den allowed: a
// SendMessage, in any turn, trips the backstop; in an app's turn so does
// any tool but ListAgents Den did not allow. Only Claude Code's refusal of
// the disallowed SendMessage (an error result saying sendRefused) shows it
// ran nothing; any other result for it, an error too, trips.
func (o *Orchestrator) controlToolResult(id string, isError bool, text string) {
	o.mu.Lock()
	c := o.ctl
	why := ""
	if c != nil {
		if c.sends[id] && !(isError && strings.Contains(text, sendRefused)) {
			why = "Claude Code's own SendMessage ran, which control disallows"
		}
		delete(c.sends, id)
	}
	if c != nil && why == "" && c.app.open && !isError {
		if tool, ok := c.uses[id]; ok && tool != "ListAgents" && !c.allowed[id] {
			why = "in an app's turn " + tool + " ran without Den allowing it"
		}
	}
	o.mu.Unlock()
	if why != "" {
		o.trip(why, TripTool)
	}
}

// controlReplay marks the app's turn replayed when Claude Code echoes the
// prompt Den wrote for it.
func (o *Orchestrator) controlReplay(raw json.RawMessage) {
	var parts []string
	for _, b := range blocks(raw) {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	h := peermsg.Hash(strings.Join(parts, ""))
	o.mu.Lock()
	if c := o.ctl; c != nil && c.app.open && !c.app.replayed && h == c.app.hash {
		c.app.replayed = true
	}
	o.mu.Unlock()
}

// controlResult ends the app's turn on its own result: one that comes
// after its prompt was replayed. It returns the turn's id, "" when the
// result was another turn's.
func (o *Orchestrator) controlResult() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	c := o.ctl
	if c != nil {
		clear(c.sends) // a turn's calls have their results before its result
	}
	if c == nil || !c.app.open || !c.app.replayed {
		return ""
	}
	id := c.app.id
	c.app = appTurn{}
	for rid := range c.perms {
		o.answerLocked(rid, false, "the turn ended")
	}
	return id
}
