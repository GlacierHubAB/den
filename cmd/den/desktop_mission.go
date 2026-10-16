package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/desktop"
	"github.com/GlacierHubAB/den/internal/mcp"
	"github.com/GlacierHubAB/den/internal/orch"
	"github.com/GlacierHubAB/den/internal/sanitize"
	"github.com/GlacierHubAB/den/internal/spawn"
)

// desktopMissionTool is desktop_mission: ask the human, on a Den card, to
// start an agent that drives the desktop. Either a task: the whole
// desktop for that one task, no app config needed, behind Den's fixed
// rules and the human's limits; or a plan and apps from the human's own
// [desktop.apps] config, which Den launches on a workspace it frees. The
// agent has only desktop tools, all behind Den's gate. It returns once the
// human answered and the agent started, or why not. Nothing here sets a
// limit: those come from the human's config alone.
func desktopMissionTool(asker string) mcp.Tool {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	list := func(desc string) map[string]any {
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
	}
	return mcp.Tool{
		Name:        strings.TrimPrefix(orch.DesktopMissionTool, "mcp__"+orch.MCPServer+"__"),
		Description: "Ask the human, in Den, to start a desktop agent that moves the mouse and types on their Hyprland desktop. Either give task: one sentence of what to do; the human's card reads \"Control your mouse and keyboard to: <task>\" and, on yes, the agent may use the whole desktop (any window, keybinds, launching apps) for that task only, until it reports or the human's time and action limits end it. Or give plan and apps: the agent works only in apps from the human's [desktop.apps] config, on a workspace Den frees, and every outward step asks the human. Never ask it to type credentials: list steps only the human may do (signing in, passwords, codes) in human_steps. Nothing starts until the human approves the card in Den. Returns the agent's name, or why not. The agent reports to Den when done, and Den passes its report to you.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task":        str(fmt.Sprintf("The whole desktop for one task: what to do, in one sentence of plain text (at most %d characters), shown whole on the human's card. Not with plan or apps.", desktop.TaskMaxRunes)),
				"plan":        str("With apps: what the agent does, step by step, as its brief. Never ask it to type credentials."),
				"apps":        list("With plan: names of apps from the human's [desktop.apps] config that the agent may use."),
				"human_steps": list("Steps only the human does, such as \"sign in to Gmail\"."),
				"dir":         str("Working directory for the agent's session, absolute or ~-relative. It must exist."),
				"model":       map[string]any{"type": "string", "enum": spawn.ModelNames(), "description": "The agent's model; sonnet when omitted."},
			},
			"required": []string{"dir"},
		},
		Call: func(ctx context.Context, args json.RawMessage) (string, error) {
			if err := usageStop(); err != nil {
				return "", err
			}
			var in bridge.DesktopMissionInput
			if json.Unmarshal(args, &in) != nil || strings.TrimSpace(in.Dir) == "" {
				return "", errorString("give dir, and either task or plan and apps (names from the human's config)")
			}
			switch task := strings.TrimSpace(in.Task) != ""; {
			case task && (strings.TrimSpace(in.Plan) != "" || len(in.Apps) > 0):
				return "", errorString("give either task, or plan and apps; not both")
			case !task && (strings.TrimSpace(in.Plan) == "" || len(in.Apps) == 0):
				return "", errorString("give either task, or plan and apps (names from the human's config)")
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
