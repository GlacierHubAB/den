package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"

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
	dirSchema := map[string]any{"type": "string", "description": "Working directory, absolute or ~-relative. It must exist."}
	s := &mcp.Server{Name: orch.MCPServer, Version: version, Tools: []mcp.Tool{
		{
			Name:        strings.TrimPrefix(orch.SpawnAgentTool, "mcp__"+orch.MCPServer+"__"),
			Description: "Start a new Claude Code agent in a new terminal window on the workspace the human is looking at, in the given directory, with the brief as its first prompt. Returns the agent's critter name and peer name; the brief must be complete and self-contained, as if the human had typed it.",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"dir":   dirSchema,
					"brief": map[string]any{"type": "string", "description": "What the agent should do, as its first prompt."},
					"name":  map[string]any{"type": "string", "description": "Optional peer name for the session (lowercase, dashes). Generated from the directory when empty."},
				},
				"required": []string{"dir", "brief"},
			},
			Call: func(ctx context.Context, args json.RawMessage) (string, error) {
				in, ok := orch.ParseSpawn(args)
				if !ok {
					return "", errDir
				}
				res, err := spawn.Run(ctx, spawn.Request{Dir: in.Dir, Agent: true, Brief: in.Brief, Name: in.Name, Orchestrator: orcName})
				if err != nil {
					return "", err
				}
				return res.Describe(), nil
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
