package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/config"
	"github.com/GlacierHubAB/den/internal/mcp"
	"github.com/GlacierHubAB/den/internal/orch"
	"github.com/GlacierHubAB/den/internal/spawn"
)

// runMCP is `den mcp`: the MCP server the orchestrator gets through
// --mcp-config, speaking over stdin/stdout. It offers two tools, and both
// open the user's default terminal on the workspace they are looking at.
func runMCP() {
	orcName := config.Load().Orchestrator.Name
	// An agent Den started carries its peer name; agents it starts in turn
	// report to it rather than to the orchestrator.
	lead := os.Getenv("DEN_PEER")
	exe, _ := os.Executable()
	dirSchema := map[string]any{"type": "string", "description": "Working directory, absolute or ~-relative. It must exist."}
	var when []string
	for _, m := range spawn.Models {
		when = append(when, m.Name+": "+m.When)
	}
	s := &mcp.Server{Name: orch.MCPServer, Version: version, Tools: []mcp.Tool{
		{
			Name:        strings.TrimPrefix(orch.SpawnAgentTool, "mcp__"+orch.MCPServer+"__"),
			Description: "Start a new Claude Code agent in a new terminal window on the workspace the human is looking at, in the given directory, with the brief as its first prompt. The agent reports to you when done and has this same tool, so a lead can staff its own work. Returns the agent's critter name and peer name; the brief must be complete and self-contained, as if the human had typed it.",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"dir":   dirSchema,
					"brief": map[string]any{"type": "string", "description": "What the agent should do, as its first prompt."},
					"name":  map[string]any{"type": "string", "description": "Optional peer name for the session (lowercase, dashes). Generated from the directory when empty."},
					"model": map[string]any{"type": "string", "enum": spawn.ModelNames(), "description": "The model for the agent, by how hard its task is. " + strings.Join(when, ". ") + ". Omit for the user's default."},
				},
				"required": []string{"dir", "brief"},
			},
			Call: func(ctx context.Context, args json.RawMessage) (string, error) {
				in, ok := orch.ParseSpawn(args)
				if !ok {
					return "", errDir
				}
				res, err := spawn.Run(ctx, spawn.Request{Dir: in.Dir, Agent: true, Brief: in.Brief, Name: in.Name, Model: in.Model, Orchestrator: orcName, Lead: lead, Spawner: exe})
				if err != nil {
					return "", err
				}
				return res.Describe(), nil
			},
		},
		{
			Name:        strings.TrimPrefix(orch.CloseAgentTool, "mcp__"+orch.MCPServer+"__"),
			Description: "Ask the human, in Den, to close agents: end their Claude Code sessions and close their terminal windows. Nothing closes until the human approves in Den, where they see exactly which agents and windows are affected; Den refuses agents that are working or waiting on the human, the orchestrator and itself. Give peer names from the roster. The answer says what was closed, or that the human declined.",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"peers":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Peer names of the agents to close, from the roster."},
					"reason": map[string]any{"type": "string", "description": "One line on why, shown to the human."},
				},
				"required": []string{"peers"},
			},
			Call: func(ctx context.Context, args json.RawMessage) (string, error) {
				in, ok := orch.ParseClose(args)
				if !ok {
					return "", errorString("peers is required: the peer names of the agents to close")
				}
				payload, _ := json.Marshal(bridge.CloseInput{Peers: in.Peers, Reason: in.Reason})
				env := bridge.Envelope{Seq: time.Now().UnixNano(), Event: bridge.CloseEvent, Payload: payload}
				d, ok := bridge.Ask(bridge.SocketPath(), env, 10*time.Minute)
				if !ok {
					return "", errorString("Den is not running or did not answer; nothing was closed")
				}
				switch d.Behavior {
				case "allow":
					return "Closed with the human's approval: " + d.Message, nil
				default:
					return "The human declined in Den; nothing was closed. " + d.Message, nil
				}
			},
		},
		{
			Name:        strings.TrimPrefix(orch.OpenTerminalTool, "mcp__"+orch.MCPServer+"__"),
			Description: "Open a plain terminal window (a shell, no agent) in the given directory on the workspace the human is looking at.",
			Schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"dir": dirSchema},
				"required":   []string{"dir"},
			},
			Call: func(ctx context.Context, args json.RawMessage) (string, error) {
				in, ok := orch.ParseSpawn(args)
				if !ok {
					return "", errDir
				}
				res, err := spawn.Run(ctx, spawn.Request{Dir: in.Dir})
				if err != nil {
					return "", err
				}
				return res.Describe(), nil
			},
		},
	}}
	if err := s.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		fail("den mcp: %v", err)
	}
}

var errDir = errorString("dir is required")

type errorString string

func (e errorString) Error() string { return string(e) }
