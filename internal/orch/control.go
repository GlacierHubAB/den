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

// ControlPrompt follows the system prompt with control on.
const ControlPrompt = "SendMessage is not available in this session: message sessions with send_message instead, wherever these instructions say SendMessage. It reaches them the same way, and their reports come back to you as before. Den may ask the human before any tool runs."

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

// humanTools are the tools Den allows outside an app's turn: the ones it
// allows without control (Argv's allow-list), with send_message for
// SendMessage.
func humanTools(o Options) map[string]bool {
	set := map[string]bool{SendMessageTool: true, MissionTool: true}
	if o.Spawn {
		for _, t := range []string{SpawnAgentTool, OpenTerminalTool, CloseAgentTool, SkillsTool} {
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
// human. id names the turn in events.
func (o *Orchestrator) SendApp(id, content string) error {
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
	o.ctl.uses, o.ctl.allowed = map[string]string{}, map[string]bool{}
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
	if o.ctl == nil {
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

// controlToolUse notes a tool call; in an app's turn Claude Code's own
// SendMessage (disallowed) trips the backstop.
func (o *Orchestrator) controlToolUse(id, tool string) {
	o.mu.Lock()
	c := o.ctl
	trip := c != nil && tool == "SendMessage"
	if c != nil && c.app.open {
		c.uses[id] = tool
	}
	o.mu.Unlock()
	if trip {
		o.trip("the orchestrator used SendMessage, which control disallows")
	}
}

// controlToolResult checks, in an app's turn, that a tool that ran was one
// Den allowed (ListAgents needs no asking); anything else trips the
// backstop.
func (o *Orchestrator) controlToolResult(id string, isError bool) {
	o.mu.Lock()
	c := o.ctl
	bad := ""
	if c != nil && c.app.open && !isError {
		if tool, ok := c.uses[id]; ok && tool != "ListAgents" && !c.allowed[id] {
			bad = tool
		}
	}
	o.mu.Unlock()
	if bad != "" {
		o.trip("in an app's turn " + bad + " ran without Den allowing it")
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
