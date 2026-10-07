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
