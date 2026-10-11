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
					"dir":      dirSchema,
					"brief":    map[string]any{"type": "string", "description": "What the agent should do, as its first prompt."},
					"name":     map[string]any{"type": "string", "description": "Optional peer name for the session (lowercase, dashes). Generated from the directory when empty."},
					"model":    map[string]any{"type": "string", "enum": spawn.ModelNames(), "description": "The model for the agent, by how hard its task is. " + strings.Join(when, ". ") + ". Omit for the user's default."},
					"role":     map[string]any{"type": "string", "description": "The agent's role in a few words: coding agent, ui designer, frontend developer, qa automation, security auditor. Den offers the human the skills from its vetted catalogue that fit the role, for this agent only, and waits for their answer before starting it; the agent starts either way, with the skills they approved."},
					"skills":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional: offer only these of the role's catalogue skills."},
					"worktree": map[string]any{"oneOf": []any{map[string]any{"type": "boolean"}, map[string]any{"type": "string"}}, "description": "Optional: start the agent in a git worktree of its own (true, or a name for it), made by Claude Code under <repo>/.claude/worktrees on branch worktree-<name>, so agents editing one repository do not collide. dir must be in a git repository that has a commit. The human reviews and merges the branch in Den's Review; the agent is told to commit there and not to merge."},
				},
				"required": []string{"dir", "brief"},
			},
			Call: func(ctx context.Context, args json.RawMessage) (string, error) {
				in, ok := orch.ParseSpawn(args)
				if !ok {
					return "", errDir
				}
				req := spawn.Request{Dir: in.Dir, Agent: true, Brief: in.Brief, Name: in.Name, Model: in.Model, Orchestrator: orcName, Lead: lead, Worktree: in.Worktree, Spawner: exe}
				note := ""
				if strings.TrimSpace(in.Role) != "" {
					var err error
					if note, err = offerAgentSkills(&req, in.Role, in.Skills, asker(lead)); err != nil {
						return "", err
					}
				}
				res, err := spawn.Run(ctx, req)
				if err != nil {
					return "", err
				}
				return strings.TrimSpace(res.Describe() + " " + note), nil
			},
		},
		{
			Name:        strings.TrimPrefix(orch.CloseAgentTool, "mcp__"+orch.MCPServer+"__"),
			Description: "Ask the human, in Den, to close agents: end their Claude Code sessions and close their terminal windows. Nothing closes until the human approves in Den, where they see exactly which agents and windows are affected; Den refuses agents that are working or waiting on the human, the orchestrator and itself, skips the agents the human protected, and lists flagged ones without selecting them. Give peer names from the roster, or idle_for to select every idle agent idle at least that long. The answer says what was closed, or that the human declined.",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"peers":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Peer names of the agents to close, from the roster. With idle_for, only these are considered."},
					"idle_for": map[string]any{"type": "string", "description": "Select every idle agent whose last activity is at least this long ago: 30m, 1h, 2h. Protected agents are skipped; flagged ones are listed but not selected."},
					"reason":   map[string]any{"type": "string", "description": "One line on why, shown to the human."},
				},
			},
			Call: func(ctx context.Context, args json.RawMessage) (string, error) {
				in, ok := orch.ParseClose(args)
				if !ok {
					return "", errorString("give peers (the peer names to close) or idle_for (a duration like 1h), or both")
				}
				payload, _ := json.Marshal(bridge.CloseInput{Peers: in.Peers, Reason: in.Reason, IdleFor: in.IdleFor})
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
			Name:        strings.TrimPrefix(orch.SkillsTool, "mcp__"+orch.MCPServer+"__"),
			Description: "Offer the human, in Den, the skills from Den's vetted catalogue that fit a role, for one project: they go into <dir>/.claude/skills, where every Claude Code session in that project finds them. Each skill is pinned to a commit of a named repository; Den lists each one with its repository, commit, license and why it fits, and installs nothing unless the human approves. Only catalogue skills can be offered. The answer says what was installed, or that the human declined, or that no catalogue skill fits the role.",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"role":   map[string]any{"type": "string", "description": "The role to match, in a few words, e.g. the agent's role from the roster."},
					"dir":    dirSchema,
					"skills": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional: offer only these of the role's catalogue skills."},
					"reason": map[string]any{"type": "string", "description": "One line on why, shown to the human."},
				},
				"required": []string{"role", "dir"},
			},
			Call: func(ctx context.Context, args json.RawMessage) (string, error) {
				in, ok := orch.ParseSkills(args)
				if !ok {
					return "", errorString("give role (a few words) and dir (the project folder)")
				}
				return offerProjectSkills(in, asker(lead))
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
