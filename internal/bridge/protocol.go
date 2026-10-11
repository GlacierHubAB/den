// Package bridge carries Claude Code hook events from `den hook` to the
// running Den over a Unix socket, one JSON line per message, and carries a
// permission decision back on the same connection.
//
// Hooks fail open: when Den is not running the hook exits quietly and Claude
// Code behaves as if Den did not exist.
package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
)

// Envelope is what a hook sends. Payload is the raw hook input JSON.
type Envelope struct {
	Seq       int64           `json:"seq"`
	Event     string          `json:"event"`
	SessionID string          `json:"session_id"`
	Payload   json.RawMessage `json:"payload"`
}

// Decision is Den's answer to a PermissionRequest. Behavior is "allow",
// "deny" or "" for "no decision, let the terminal dialog show".
type Decision struct {
	Behavior string `json:"behavior"`
	Message  string `json:"message,omitempty"`
}

// CloseEvent is not a hook: `den mcp` sends it when the orchestrator asks
// to close agents, and waits for the human's decision from Den.
const CloseEvent = "CloseRequest"

// CloseInput is the payload of a CloseEvent.
type CloseInput struct {
	Peers   []string `json:"peers"`    // session names; with IdleFor, a filter
	Reason  string   `json:"reason"`   // the orchestrator's words
	IdleFor string   `json:"idle_for"` // "1h": every idle agent idle that long, protected ones aside
}

// SkillsEvent is not a hook either: `den mcp` sends it when the
// orchestrator or a lead proposes catalogue skills for a project or for an
// agent it is starting, and waits for the human's ^Y or ^N in Den.
const SkillsEvent = "SkillsRequest"

// SkillsInput is the payload of a SkillsEvent. Den looks the skills up in
// its own catalogue again; the names only select among the role's matches.
type SkillsInput struct {
	Role    string   `json:"role"`
	Skills  []string `json:"skills,omitempty"`
	Scope   string   `json:"scope"` // "project" or "agent"
	Dir     string   `json:"dir"`   // the project, or the new agent's working directory
	Peer    string   `json:"peer,omitempty"`
	Root    string   `json:"root,omitempty"` // agent scope: the agent's private skills folder
	AskedBy string   `json:"asked_by"`
	Reason  string   `json:"reason,omitempty"`
}

// HookInput is the subset of Claude Code's hook JSON that Den reads.
type HookInput struct {
	SessionID        string          `json:"session_id"`
	CWD              string          `json:"cwd"`
	HookEventName    string          `json:"hook_event_name"`
	PermissionMode   string          `json:"permission_mode"`
	TranscriptPath   string          `json:"transcript_path"`
	AgentID          string          `json:"agent_id"`
	ToolName         string          `json:"tool_name"`
	ToolInput        json.RawMessage `json:"tool_input"`
	ToolResponse     json.RawMessage `json:"tool_response"`
	ToolUseID        string          `json:"tool_use_id"`
	PermissionReason string          `json:"permission_reason"`
	NotificationType string          `json:"notification_type"`
	Message          string          `json:"message"`
	Title            string          `json:"title"`
	LastAssistant    string          `json:"last_assistant_message"`
	StopReason       string          `json:"stop_reason"`
	Prompt           string          `json:"prompt"`
	Source           string          `json:"source"`
	EndReason        string          `json:"end_reason"`
	// StopFailure: the documented error_type, and the error field Claude
	// Code 2.1.289 actually sends (a string such as "rate_limit"). Raw so
	// another event's error of a different shape cannot spoil the decode.
	ErrorType string          `json:"error_type"`
	Error     json.RawMessage `json:"error"`
}

// FailureType is a StopFailure's error type: "rate_limit", "server_error",
// "authentication_failed" and so on, "unknown" when none came.
func (in HookInput) FailureType() string {
	if in.ErrorType != "" {
		return in.ErrorType
	}
	var s string
	if json.Unmarshal(in.Error, &s) == nil && s != "" {
		return s
	}
	return "unknown"
}

// SocketPath is where Den listens: $DEN_SOCKET, else $XDG_RUNTIME_DIR/den.sock,
// else /tmp/den-<uid>.sock.
func SocketPath() string {
	if p := os.Getenv("DEN_SOCKET"); p != "" {
		return p
	}
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "den.sock")
	}
	return filepath.Join(os.TempDir(), "den-"+strconv.Itoa(os.Getuid())+".sock")
}
