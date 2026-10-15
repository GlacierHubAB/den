package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/config"
	"github.com/GlacierHubAB/den/internal/mcp"
	"github.com/GlacierHubAB/den/internal/orch"
	"github.com/GlacierHubAB/den/internal/permmode"
	"github.com/GlacierHubAB/den/internal/spawn"
	"github.com/GlacierHubAB/den/internal/usage"
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
					"skills":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional: offer only these catalogue skills. Each must fit the role; it may be one the role's default offer (at most four) leaves out."},
					"worktree": map[string]any{"oneOf": []any{map[string]any{"type": "boolean"}, map[string]any{"type": "string"}}, "description": "Optional: start the agent in a git worktree of its own (true, or a name for it), made by Claude Code under <repo>/.claude/worktrees on branch worktree-<name>, so agents editing one repository do not collide. dir must be in a git repository that has a commit. The human reviews and merges the branch in Den's Review; the agent is told to commit there and not to merge."},
				},
				"required": []string{"dir", "brief"},
			},
			Call: func(ctx context.Context, args json.RawMessage) (string, error) {
				if err := usageStop(); err != nil {
					return "", err
				}
				in, ok := orch.ParseSpawn(args)
				if !ok {
					return "", errDir
				}
				req := spawnRequest(in, orcName, lead, exe)
				note := ""
				if strings.TrimSpace(in.Role) != "" {
					var err error
					if note, err = offerAgentSkills(&req, in.Role, in.Skills, asker(lead)); err != nil {
						return "", err
					}
				}
				// Asked last: the skills card may have waited on the human,
				// and the mode is the one in force as the agent starts.
				req.PermissionMode = agentMode()
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
					"skills": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional: offer only these catalogue skills. Each must fit the role; it may be one the role's default offer (at most four) leaves out."},
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
				if err := usageStop(); err != nil {
					return "", err
				}
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
	// Missions are the orchestrator's alone: an agent Den started (which
	// runs this server too) never gets the tool, and one started with
	// --missions-only gets nothing else.
	missionsOnly, control := false, false
	for _, a := range os.Args[2:] {
		missionsOnly = missionsOnly || a == orch.MissionsOnly
		control = control || a == orch.ControlFlag
	}
	s.Tools = mcpTools(s.Tools, os.Getenv("DEN_ORCHESTRATOR") == "1", missionsOnly, control)
	if err := s.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		fail("den mcp: %v", err)
	}
}

// mcpTools is what `den mcp` offers: update_mission only to the
// orchestrator (DEN_ORCHESTRATOR=1, which Den strips from the agents it
// starts), and with missionsOnly nothing else.
func mcpTools(all []mcp.Tool, orchestrator, missionsOnly, control bool) []mcp.Tool {
	if !orchestrator {
		return all
	}
	if missionsOnly {
		all = nil
	}
	all = append(all, missionTool())
	if control {
		all = append(all, sendMessageTool())
	}
	return all
}

// sendMessageTool is send_message, the orchestrator's SendMessage with
// control on: Den delivers it, so that it can hold it for the human first.
func sendMessageTool() mcp.Tool {
	return mcp.Tool{
		Name:        strings.TrimPrefix(orch.SendMessageTool, "mcp__"+orch.MCPServer+"__"),
		Description: "Send a message to another Claude Code session on this machine, by its peer name from the roster: what SendMessage does, which you do not have here. Den delivers it, and the session sees it from you, as with SendMessage; its report comes back to you the same way. Den may first ask the human.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"to":      map[string]any{"type": "string", "description": "The session's peer name, as the roster gives it."},
				"message": map[string]any{"type": "string", "description": "The message, complete and self-contained."},
				"summary": map[string]any{"type": "string", "description": "A few words on what it is, shown to the human."},
			},
			"required": []string{"to", "message"},
		},
		Call: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in bridge.OrchSendInput
			if json.Unmarshal(args, &in) != nil || strings.TrimSpace(in.To) == "" || strings.TrimSpace(in.Message) == "" {
				return "", errorString("give to (a peer name) and message")
			}
			payload, _ := json.Marshal(in)
			env := bridge.Envelope{Seq: time.Now().UnixNano(), Event: bridge.OrchSendEvent, Payload: payload}
			d, err := bridge.Call(bridge.SocketPath(), env, 10*time.Second)
			if errors.Is(err, bridge.ErrNoDen) {
				return "", errorString("Not sent: Den is not running")
			}
			if err != nil {
				// Den may have been writing it as the wait ran out.
				return "", errorString("Den did not answer in time; the message may have been sent, so ask " + in.To + " before sending it again")
			}
			if d.Behavior != "allow" {
				return "", errorString("Not sent: " + d.Message)
			}
			return d.Message, nil
		},
	}
}

// missionTool is update_mission: the orchestrator's word on a mission,
// applied by Den or put to the human there.
func missionTool() mcp.Tool {
	return mcp.Tool{
		Name:        strings.TrimPrefix(orch.MissionTool, "mcp__"+orch.MCPServer+"__"),
		Description: "Tell Den where a mission stands after its run: done (the goal is met), continue (another pass is needed; dispatch it in the same reply), waiting (on something outside the agents) or failed. Den applies continue and waiting at once when the human asked you about the mission; anything else, and done or failed always, it puts to the human to confirm. The answer says which.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":      map[string]any{"type": "string", "description": "The mission's id, as Den gave it."},
				"state":   map[string]any{"type": "string", "enum": []string{"done", "continue", "waiting", "failed"}},
				"summary": map[string]any{"type": "string", "description": "One line on where it stands, shown to the human."},
			},
			"required": []string{"id", "state", "summary"},
		},
		Call: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in bridge.MissionInput
			if json.Unmarshal(args, &in) != nil || strings.TrimSpace(in.ID) == "" {
				return "", errorString("give id, state and summary")
			}
			switch in.State {
			case "done", "continue", "waiting", "failed":
			default:
				return "", errorString("state is done, continue, waiting or failed")
			}
			payload, _ := json.Marshal(in)
			env := bridge.Envelope{Seq: time.Now().UnixNano(), Event: bridge.MissionEvent, Payload: payload}
			d, ok := bridge.Ask(bridge.SocketPath(), env, 10*time.Second)
			if !ok {
				return "", errorString("Den is not running or did not answer; the mission was not updated")
			}
			return d.Message, nil
		},
	}
}

// usageStop refuses to start anything while Den has the fleet stopped for
// usage; the human lets it go on in Den. It asks the running Den, and reads
// Den's usage.json only when no Den answers.
func usageStop() error {
	stopped, why := false, ""
	env := bridge.Envelope{Seq: time.Now().UnixNano(), Event: bridge.UsageEvent}
	if d, err := askUsage(bridge.SocketPath(), env, 3*time.Second); err == nil {
		stopped, why = d.Behavior == "deny", d.Message
	} else {
		stopped, why = usage.Stopped()
	}
	if stopped {
		return errorString("Den has stopped the fleet for usage (" + why + "): nothing new starts until the human lets it go on in Den (/usage resume)")
	}
	return nil
}

// askUsage reaches Den; tests replace it.
var askUsage = bridge.Call

// spawnRequest is the agent spawn_agent's input asks for. Nothing in the
// input picks its permission mode: the caller sets agentMode()'s.
func spawnRequest(in orch.SpawnInput, orcName, lead, exe string) spawn.Request {
	return spawn.Request{Dir: in.Dir, Agent: true, Brief: in.Brief, Name: in.Name, Model: in.Model, Orchestrator: orcName, Lead: lead, Worktree: in.Worktree, Spawner: exe}
}

// agentMode is the permission mode a new agent starts in: the running
// Den's global mode. With no answer from Den, or one that is not a ranked
// mode, it is config's mode at most permmode.Shipped, as Den itself would
// start: never bypassPermissions, whatever answers on den.sock.
func agentMode() string {
	env := bridge.Envelope{Seq: time.Now().UnixNano(), Event: bridge.ModeEvent}
	if d, err := askMode(bridge.SocketPath(), env, 3*time.Second); err == nil && d.Behavior == "allow" && permmode.Valid(d.Message) {
		return d.Message
	}
	return permmode.Lower(config.Load().Agents.PermissionMode, permmode.Shipped)
}

// askMode reaches Den; tests replace it.
var askMode = bridge.Call

var errDir = errorString("dir is required")

type errorString string

func (e errorString) Error() string { return string(e) }
