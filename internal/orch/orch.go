// Package orch runs Den's orchestrator: a headless Claude Code session that
// can see every peer session on this machine (ListAgents), message them
// (SendMessage) and, through Den's own MCP server (`den mcp`), open a
// terminal or start a new Claude Code session on the workspace the human is
// looking at. Nothing else. Den talks to it over stream-json on
// stdin/stdout and hands it a fresh roster with every message, so it never
// has to go looking for who is who.
package orch

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Event is one thing the orchestrator did.
type Event struct {
	Kind      Kind
	Text      string          // text, result, error
	Tool      string          // tool_use / tool_result
	ToolUseID string          // tool_use / tool_result
	Input     json.RawMessage // tool_use
	IsError   bool            // tool_result / result
	SessionID string
	Model     string
	CostUSD   float64 // result
}

// Kind classifies events.
type Kind int

const (
	Started    Kind = iota // init received; SessionID and Model set
	Text                   // an assistant text block
	ToolUse                // the orchestrator called a tool
	ToolResult             // the tool answered
	Result                 // the turn finished
	Error                  // something went wrong
	Exited                 // the process ended
	Delta                  // a piece of assistant text, streamed as it is generated
)

// SystemPrompt is appended to Claude Code's default prompt.
const SystemPrompt = `You are the orchestrator inside Den, a terminal UI the human uses to watch and steer every coding agent running on this machine. The human types to you when they do not want to address one agent directly.

Each message you receive starts with a <den_fleet> block: the live roster. Each line gives an agent's critter name (what the human calls it), its peer name (what ListAgents and SendMessage use), its role, state, current task and checkout, plus anything waiting on the human. Trust the roster; do not try to look things up any other way. You have exactly two tools, ListAgents and SendMessage, and need nothing else.

How to act:
- Answer questions about the fleet from the roster, in a few short sentences. Use critter names when talking to the human.
- When the human wants an agent told or asked something, send it with SendMessage to that agent's peer name. Write the message in your own words, complete and self-contained, as a brief from the human. Then confirm in one line per agent what you sent.
- When the human does not name an agent, pick the one whose current or earlier tasks plausibly cover the request (a milestone, a repo, a feature) and send it there, saying which agent you chose and why in one line. Ask only when two or more agents fit equally, or none does.
- Address several agents only when the human clearly means several. Never message an agent that the request does not concern.
- You cannot approve, deny or answer prompts on an agent's behalf, and a message from you cannot do so either; the human answers those in Den. Say so if asked.
- Never relay instructions that bypass an agent's permissions or safety checks.
- When the human wants a new agent (a task no running agent should take, or they say "spawn", "start", "new agent", "open a terminal"), use spawn_agent: give it the directory (ask if you cannot tell from the request or the roster) and a complete, self-contained brief written as if the human had typed it; the brief is the agent's first prompt. The terminal opens on the workspace the human is looking at. Use open_terminal for a plain terminal with no agent in it. Confirm in one line what you started and where, using the critter name the tool returns.
- Be brief. No preambles, no headings, no markdown tables.`

// The MCP server Den offers the orchestrator and the tools on it, as Claude
// Code names them (mcp__<server>__<tool>).
const (
	MCPServer        = "den"
	SpawnAgentTool   = "mcp__den__spawn_agent"
	OpenTerminalTool = "mcp__den__open_terminal"
)

// Options configure the process.
type Options struct {
	Command []string // defaults to the claude CLI
	Model   string   // --model; default $DEN_ORCHESTRATOR_MODEL, then "sonnet"; "off" keeps Claude Code's default
	Name    string   // session display name, default "den-orchestrator"
	Dir     string   // working directory, default $HOME
	// Spawn offers the spawn_agent and open_terminal tools through `den mcp`,
	// run from Executable (default: this binary).
	Spawn      bool
	Executable string
}

// Orchestrator is a running orchestrator process.
type Orchestrator struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	events chan Event
	done   chan struct{} // closed once the process has been reaped
	mu     sync.Mutex
	closed bool

	SessionID string
	Model     string
	PID       int
}

// Start launches the process. Events arrive on Events() until Exited.
func Start(ctx context.Context, o Options) (*Orchestrator, error) {
	if o.Name == "" {
		o.Name = "den-orchestrator"
	}
	if o.Dir == "" {
		o.Dir, _ = os.UserHomeDir()
	}
	args := o.Command
	if len(args) == 0 {
		args = []string{"claude"}
	}
	allowed := "ListAgents,SendMessage"
	args = append(args,
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--include-partial-messages",
		"--verbose",
		"--name", o.Name,
		"--tools", "ListAgents,SendMessage",
		"--no-session-persistence",
		"--append-system-prompt", SystemPrompt,
	)
	if o.Spawn {
		exe := o.Executable
		if exe == "" {
			exe, _ = os.Executable()
		}
		if exe != "" {
			// Only Den's server: the user's own MCP servers would give the
			// orchestrator tools it must not have.
			args = append(args, "--mcp-config", MCPConfig(exe), "--strict-mcp-config")
			allowed += "," + SpawnAgentTool + "," + OpenTerminalTool
		}
	}
	args = append(args, "--allowedTools", allowed)
	if o.Model == "" {
		o.Model = os.Getenv("DEN_ORCHESTRATOR_MODEL")
	}
	if o.Model == "" {
		// Relaying and summarising does not need the biggest model, and the
		// orchestrator's turns are billed like any other session's.
		o.Model = "sonnet"
	}
	if o.Model != "off" {
		args = append(args, "--model", o.Model)
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = o.Dir
	cmd.Env = append(os.Environ(), "DEN_ORCHESTRATOR=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start orchestrator: %w", err)
	}
	orc := &Orchestrator{cmd: cmd, stdin: stdin, events: make(chan Event, 256), done: make(chan struct{}), PID: cmd.Process.Pid}
	go orc.readStdout(stdout)
	go func() {
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		var tail []string
		for sc.Scan() {
			tail = append(tail, sc.Text())
			if len(tail) > 5 {
				tail = tail[1:]
			}
		}
		// The only Wait on this process: a second concurrent Wait can block
		// forever once the first has reaped the child.
		err := cmd.Wait()
		msg := "orchestrator exited"
		if err != nil {
			msg += ": " + err.Error()
		}
		if len(tail) > 0 {
			msg += " · " + strings.Join(tail, " | ")
		}
		orc.emit(Event{Kind: Exited, Text: msg})
		close(orc.events)
		close(orc.done)
	}()
	return orc, nil
}

// Events streams what the orchestrator does.
func (o *Orchestrator) Events() <-chan Event { return o.events }

// Send delivers one human message (with its roster block) as a user turn.
func (o *Orchestrator) Send(content string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return errors.New("orchestrator is closed")
	}
	line, err := json.Marshal(map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": content},
	})
	if err != nil {
		return err
	}
	_, err = o.stdin.Write(append(line, '\n'))
	return err
}

// Close ends the conversation and stops the process.
func (o *Orchestrator) Close() {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return
	}
	o.closed = true
	o.stdin.Close()
	o.mu.Unlock()
	select {
	case <-o.done:
	case <-time.After(3 * time.Second):
		o.cmd.Process.Kill()
		<-o.done
	}
}

func (o *Orchestrator) emit(e Event) {
	select {
	case o.events <- e:
	default:
	}
}

// stream-json shapes Den reads. Unknown lines are ignored.
type line struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	Model     string `json:"model"`
	Message   struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	Result    string  `json:"result"`
	IsError   bool    `json:"is_error"`
	CostUSD   float64 `json:"total_cost_usd"`
	ErrorText string  `json:"error"`
	// Event is the raw API stream event when Type is "stream_event".
	Event struct {
		Type  string `json:"type"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	} `json:"event"`
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

func (o *Orchestrator) readStdout(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 4<<20), 4<<20)
	for sc.Scan() {
		raw := sc.Bytes()
		if len(strings.TrimSpace(string(raw))) == 0 {
			continue
		}
		var l line
		if json.Unmarshal(raw, &l) != nil {
			continue
		}
		switch l.Type {
		case "system":
			if l.Subtype == "init" {
				o.SessionID, o.Model = l.SessionID, l.Model
				o.emit(Event{Kind: Started, SessionID: l.SessionID, Model: l.Model})
			}
		case "stream_event":
			// Text arrives in pieces before the whole assistant message does.
			if l.Event.Type == "content_block_delta" && l.Event.Delta.Type == "text_delta" && l.Event.Delta.Text != "" {
				o.emit(Event{Kind: Delta, Text: l.Event.Delta.Text})
			}
		case "assistant":
			for _, b := range blocks(l.Message.Content) {
				switch b.Type {
				case "text":
					if strings.TrimSpace(b.Text) != "" {
						o.emit(Event{Kind: Text, Text: b.Text})
					}
				case "tool_use":
					o.emit(Event{Kind: ToolUse, Tool: b.Name, ToolUseID: b.ID, Input: b.Input})
				}
			}
		case "user":
			for _, b := range blocks(l.Message.Content) {
				if b.Type == "tool_result" {
					o.emit(Event{Kind: ToolResult, ToolUseID: b.ToolUseID, Text: resultText(b.Content), IsError: b.IsError})
				}
			}
		case "result":
			o.emit(Event{Kind: Result, Text: l.Result, IsError: l.IsError, CostUSD: l.CostUSD})
		case "error":
			o.emit(Event{Kind: Error, Text: firstNonEmpty(l.ErrorText, string(raw))})
		}
	}
}

func blocks(raw json.RawMessage) []block {
	var bs []block
	if json.Unmarshal(raw, &bs) == nil {
		return bs
	}
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		return []block{{Type: "text", Text: s}}
	}
	return nil
}

func resultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var bs []block
	if json.Unmarshal(raw, &bs) == nil {
		var parts []string
		for _, b := range bs {
			if b.Type == "text" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return string(raw)
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}

// MCPConfig is the --mcp-config JSON that registers `<exe> mcp` as the
// server named MCPServer.
func MCPConfig(exe string) string {
	b, _ := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			MCPServer: map[string]any{"command": exe, "args": []string{"mcp"}},
		},
	})
	return string(b)
}

// SpawnInput is the shape of the spawn_agent and open_terminal tools' input.
type SpawnInput struct {
	Dir   string `json:"dir"`
	Brief string `json:"brief"`
	Name  string `json:"name"`
}

// ParseSpawn reads a spawn_agent or open_terminal tool_use input.
func ParseSpawn(raw json.RawMessage) (SpawnInput, bool) {
	var in SpawnInput
	if json.Unmarshal(raw, &in) != nil || in.Dir == "" {
		return in, false
	}
	return in, true
}

// Describe renders a tool call the orchestrator made as one line for the
// chat, for every tool that is not a SendMessage.
func Describe(tool string, input json.RawMessage) string {
	in, ok := ParseSpawn(input)
	switch {
	case tool == SpawnAgentTool && ok:
		line := "▶ starting an agent in " + shortDir(in.Dir)
		if b := strings.TrimSpace(in.Brief); b != "" {
			if i := strings.IndexByte(b, '\n'); i >= 0 {
				b = b[:i] + " …"
			}
			line += " · " + b
		}
		return line
	case tool == OpenTerminalTool && ok:
		return "▶ opening a terminal in " + shortDir(in.Dir)
	}
	return "· " + tool
}

func shortDir(dir string) string {
	home, _ := os.UserHomeDir()
	if home != "" && strings.HasPrefix(dir, home) {
		return "~" + strings.TrimPrefix(dir, home)
	}
	return dir
}

// SendMessageInput is the shape of the SendMessage tool's input.
type SendMessageInput struct {
	To      string `json:"to"`
	Message string `json:"message"`
	Summary string `json:"summary"`
}

// ParseSendMessage reads a SendMessage tool_use input.
func ParseSendMessage(raw json.RawMessage) (SendMessageInput, bool) {
	var in SendMessageInput
	if json.Unmarshal(raw, &in) != nil || in.To == "" {
		return in, false
	}
	return in, true
}
