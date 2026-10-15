package main

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/mcp"
	"github.com/GlacierHubAB/den/internal/orch"
	"github.com/GlacierHubAB/den/internal/sanitize"
	"github.com/GlacierHubAB/den/internal/spawn"
)

// desktopMissionTool is desktop_mission: ask the human, on a Den card, to
// start an agent that drives the desktop. The asker names the plan and
// apps from the human's own [desktop.apps] config; Den picks the
// workspace, launches the apps, and starts the agent with only desktop
// tools, all behind its gate. It returns once the human answered and the
// agent started, or why not.
func desktopMissionTool(asker string) mcp.Tool {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	list := func(desc string) map[string]any {
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
	}
	return mcp.Tool{
		Name:        strings.TrimPrefix(orch.DesktopMissionTool, "mcp__"+orch.MCPServer+"__"),
		Description: "Ask the human, in Den, to start a desktop agent: one that moves the mouse and types on their Hyprland desktop, in apps they allowed in Den's config, on a workspace Den frees for it, behind Den's approvals (every outward step asks the human). Give the plan step by step, the app names, and every step only the human may do (signing in, passwords, codes): the agent hands those to the human and waits. Nothing starts until the human approves the card. Returns the agent's name, or why not. The agent reports to Den when done, and Den passes its report to you.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"plan":        str("What the agent does, step by step, as its brief. Never ask it to type credentials."),
				"apps":        list("Names of apps from the human's [desktop.apps] config that the agent may use."),
				"human_steps": list("Steps only the human does, such as \"sign in to Gmail\"."),
				"dir":         str("Working directory for the agent's session, absolute or ~-relative. It must exist."),
				"model":       map[string]any{"type": "string", "enum": spawn.ModelNames(), "description": "The agent's model; sonnet when omitted."},
			},
			"required": []string{"plan", "apps", "dir"},
		},
		Call: func(ctx context.Context, args json.RawMessage) (string, error) {
			if err := usageStop(); err != nil {
				return "", err
			}
			var in bridge.DesktopMissionInput
			if json.Unmarshal(args, &in) != nil || strings.TrimSpace(in.Plan) == "" || len(in.Apps) == 0 || strings.TrimSpace(in.Dir) == "" {
				return "", errorString("give plan, apps (names from the human's config) and dir")
			}
			dir, err := spawn.ExpandDir(in.Dir)
			if err != nil {
				return "", err
			}
			in.Dir, in.AskedBy = dir, asker
			payload, _ := json.Marshal(in)
			env := bridge.Envelope{Seq: time.Now().UnixNano(), Event: bridge.DesktopMissionEvent, Payload: payload}
			d, err := bridge.Call(bridge.SocketPath(), env, 30*time.Minute)
			if err != nil {
				return "", errorString("Den did not answer (" + err.Error() + "); nothing started")
			}
			if d.Behavior != "allow" {
				return "", errorString(sanitize.Text(d.Message))
			}
			return sanitize.Text(d.Message), nil
		},
	}
}
