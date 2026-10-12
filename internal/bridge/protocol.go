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
)

// Envelope is what a hook sends. Payload is the raw hook input JSON.
type Envelope struct {
	Seq       int64           `json:"seq"`
	Event     string          `json:"event"`
	SessionID string          `json:"session_id"`
	Payload   json.RawMessage `json:"payload"`
}

// Decision is Den's answer to a PermissionRequest. Behavior is "allow",
// "deny" or "" for "no decision, let the terminal dialog show". Answers
// goes with an allow of an AskUserQuestion call answered in Den: question
// text to answer, as Claude Code keys them.
type Decision struct {
	Behavior string            `json:"behavior"`
	Message  string            `json:"message,omitempty"`
	Answers  map[string]string `json:"answers,omitempty"`
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

// InstallEvent is not a hook either: `den remote install` (and `den
// remote add`) send it to ask the human, in Den's inbox, to copy this den
// to a remote host (docs/REMOTE.md), and wait for
// the outcome. Den makes the plan itself and carries it out only on ^Y.
//
// It is only ever read from the local den.sock. A remote host's messages
// travel its ssh pipe to remote.Manager, whose closed set of types has no
// way to reach the bridge, so no host can ask for an install, and an agent
// on this machine can only ask: the human answers.
const InstallEvent = "InstallRequest"

// InstallInput is the payload of an InstallEvent.
type InstallInput struct {
	Host    string `json:"host"`     // the host's label in remotes.toml
	AskedBy string `json:"asked_by"` // who ran the command, for the question
}

// SendEvent is not a hook either: `den send name@host …` sends it to the
// running Den to message an agent on a remote host, because only Den holds the
// ssh session to that host. Den resolves the agent itself, in its own fleet,
// and answers allow with a note, or deny with why nothing was sent.
//
// Like InstallEvent it is only ever read from the local den.sock: a remote
// host's messages never reach the bridge (its held requests enter as
// PermissionRequests of Den's own making), and a Request that did come
// from a host is refused. The message is plain text and approves nothing.
const SendEvent = "SendRequest"

// SendInput is the payload of a SendEvent.
type SendInput struct {
	Who     string `json:"who"`      // name@host, session-id@host or pid@host as typed
	Text    string `json:"text"`     // the message
	AskedBy string `json:"asked_by"` // who ran the command, for the notice
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
	// PostToolUseFailure: true when the human interrupted the tool (Esc),
	// which is not the tool failing.
	IsInterrupt bool `json:"is_interrupt"`
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

// SocketPath is where Den listens: $DEN_SOCKET as given, else
// $XDG_RUNTIME_DIR/den.sock, else den.sock in the private fallback
// directory $TMPDIR/den-<uid> (FallbackDir), never directly in a shared
// /tmp where another user could take the name first. Listen makes that
// directory; clients dial through Dial, which checks it and the peer.
func SocketPath() string {
	if p := os.Getenv("DEN_SOCKET"); p != "" {
		return p
	}
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "den.sock")
	}
	return filepath.Join(FallbackDir(), "den.sock")
}

// PlanTool is the tool Claude Code asks with when a plan is ready
// (permission mode plan).
const PlanTool = "ExitPlanMode"

// PlanApproval is the updatedInput a PermissionRequest hook's allow must
// carry to approve a plan: the call's own tool_input, unchanged. ok is
// false when the input is not a JSON object with a plan in it; Den then
// cannot approve the plan and leaves it to the terminal.
//
// Claude Code 2.1.289 ignores a bare allow for a tool that needs the
// user's interaction (ExitPlanMode, AskUserQuestion) and keeps its dialog
// open; an allow with updatedInput is taken, and ExitPlanMode then leaves
// plan mode itself. A deny needs nothing more than its message.
func PlanApproval(input json.RawMessage) (updated map[string]json.RawMessage, ok bool) {
	if json.Unmarshal(input, &updated) != nil || updated == nil {
		return nil, false
	}
	var plan string
	if json.Unmarshal(updated["plan"], &plan) != nil || plan == "" {
		return nil, false
	}
	return updated, true
}
