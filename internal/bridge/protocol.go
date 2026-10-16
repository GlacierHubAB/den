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
	// Data is an answer's structured part, for the asker that expects it:
	// a DesktopEvent's allow carries the input to forward and the scope.
	Data json.RawMessage `json:"data,omitempty"`
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
// and answers allow with a note, or with why nothing was sent: SendDeclined
// when the human said no, SendCancelled when the question ended before they
// answered, and deny for everything else.
//
// Like InstallEvent it is only ever read from the local den.sock: a remote
// host's messages never reach the bridge (its held requests enter as
// PermissionRequests of Den's own making), and a Request that did come
// from a host is refused. The message is plain text and approves nothing.
const SendEvent = "SendRequest"

// InboundEvent is not a hook event either: the orchestrator's own
// UserPromptSubmit hook (`den hook orchestrator-inbound`) sends it for every
// prompt and waits for Den. For a prompt without an envelope Den answers
// allow only when it wrote exactly those bytes itself, lately; for a peer's
// envelope it says whether it took the message as a report. The hook
// blocks every envelope either way.
const InboundEvent = "OrchestratorInbound"

// InboundInput is the payload of an InboundEvent.
type InboundInput struct {
	Kind     string `json:"kind"`             // "prompt" or "envelope"
	SHA256   string `json:"sha256,omitempty"` // prompt: peermsg.Hash of the prompt
	From     string `json:"from,omitempty"`   // envelope: the sender's socket
	FromName string `json:"from_name,omitempty"`
	FromMode string `json:"from_mode,omitempty"`
	PID      int    `json:"pid,omitempty"` // from From
	Body     string `json:"body,omitempty"`
}

// MissionEvent is not a hook event: `den mcp` sends it for the
// orchestrator's update_mission call. Den applies the update at once only
// when the turn began with the human's own words and the state is not
// final; otherwise it asks the human (^Y). Its answer is immediate.
const MissionEvent = "MissionUpdate"

// MissionInput is the payload of a MissionEvent. State is optional when
// the call changes sub-goals, claims criteria or asks to change their text.
type MissionInput struct {
	ID      string `json:"id"`
	State   string `json:"state,omitempty"` // done, continue, waiting, failed
	Summary string `json:"summary,omitempty"`
	// Goals adds a sub-goal (no id) or changes one; Claims says criteria
	// are met, with evidence; CriteriaText asks the human to change a
	// criterion's text.
	Goals        []GoalInput     `json:"goals,omitempty"`
	Claims       []ClaimInput    `json:"claims,omitempty"`
	CriteriaText []CriterionText `json:"criteria_text,omitempty"`
}

// GoalInput is one sub-goal in a mission call: owner is a critter name
// from the roster or a team role.
type GoalInput struct {
	ID    string `json:"id,omitempty"`
	Text  string `json:"text,omitempty"`
	Owner string `json:"owner,omitempty"`
	State string `json:"state,omitempty"` // todo, doing, blocked, done
	Note  string `json:"note,omitempty"`
}

// ClaimInput says a criterion is met, with what shows it.
type ClaimInput struct {
	ID       string `json:"id"`
	Evidence string `json:"evidence"`
}

// CriterionText is a criterion's new text, for the human to approve.
type CriterionText struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// ProposeEvent is not a hook event: `den mcp` sends it for the
// orchestrator's propose_mission call. Den checks it and shows the
// proposal; nothing starts until the human's ^Y. Its answer is immediate.
const ProposeEvent = "MissionPropose"

// ProposeInput is the payload of a ProposeEvent. With ID it refines that
// proposal.
type ProposeInput struct {
	ID          string      `json:"id,omitempty"`
	Project     string      `json:"project"`
	Name        string      `json:"name"`
	Goal        string      `json:"goal"`
	Criteria    []string    `json:"criteria"`
	Goals       []GoalInput `json:"goals"`
	Shape       string      `json:"shape"`
	Cadence     string      `json:"cadence,omitempty"`
	Reason      string      `json:"reason,omitempty"`
	Constraints []string    `json:"constraints,omitempty"`
	Team        []TeamInput `json:"team,omitempty"`
}

// TeamInput is a role to start for a mission.
type TeamInput struct {
	Role  string `json:"role"`
	Model string `json:"model,omitempty"`
	Under string `json:"under,omitempty"`
}

// UsageEvent is not a hook event: `den mcp` sends it before it starts an
// agent or a terminal, to ask whether Den has the fleet stopped for
// usage. Den answers deny, with why, while it does. Read-only.
const UsageEvent = "UsageQuery"

// ModeEvent is not a hook event: `den mcp` sends it before it starts an
// agent, to ask which permission mode Den starts agents in. Den answers
// allow with the mode as the message. Read-only.
const ModeEvent = "ModeQuery"

// OrchSendEvent is not a hook event: `den mcp --control` sends it for the
// orchestrator's send_message call, which control gives it in place of
// SendMessage. Den delivers the message the way SendMessage would and
// answers at once.
const OrchSendEvent = "OrchSend"

// ConsolePairEvent is not a hook event: `den pair` sends it to have Den
// show a console code. Den refuses it from a process under Claude Code,
// an agent, `den mcp` or `den hook`; the confirm is a card in Den.
const ConsolePairEvent = "ConsolePair"

// ConsoleUnpairEvent is not a hook event: `den unpair [all]` sends it.
// Its payload is {"all": bool}. It only lowers, so anyone may ask.
const ConsoleUnpairEvent = "ConsoleUnpair"

// DesktopInstallEvent is not a hook event: `den install-hypruse` sends it
// to ask the human, in Den, to install hypruse into Den's own venv, and
// waits for the outcome. Den makes the plan itself, checks the clone, and
// installs only on ^Y. Refused from a process under Claude Code, an agent,
// `den mcp` or `den hook`.
const DesktopInstallEvent = "DesktopInstallRequest"

// DesktopInstallInput is the payload of a DesktopInstallEvent.
type DesktopInstallInput struct {
	Clone   string `json:"clone"`            // the hypruse clone to copy from
	Wheels  string `json:"wheels,omitempty"` // a folder of wheels for an offline install
	AskedBy string `json:"asked_by"`
}

// DesktopEvent is not a hook event: Den's desktop proxy (`den
// desktop-mcp`, the only MCP server a desktop agent has) sends it to start
// and for every tool call, and forwards a call to hypruse only on Den's
// allow. Den answers only the proxy of a live desktop mission, known by
// its process: a child of that mission's agent, then the pid and start it
// reported.
const DesktopEvent = "DesktopRequest"

// DesktopGateEvent is not a hook event either: `den hook desktop-gate`,
// the desktop agent's PreToolUse hook, sends it for every tool call. Den
// answers at once, allow (the hook then makes no decision) only for a
// live desktop agent's allowed tools, else deny.
const DesktopGateEvent = "DesktopGate"

// DesktopStopEvent is not a hook event: `den desktop stop` sends it (a
// Hyprland bind the human adds runs it). Den stops every desktop mission
// at once and answers what it did. It only lowers, so it is never refused
// for who asks.
const DesktopStopEvent = "DesktopStop"

// DesktopMissionEvent is not a hook event: `den mcp` sends it for the
// desktop_mission tool, asking the human, on a card, to start an agent
// that drives the desktop in a scope Den derives (a free workspace, the
// apps the human configured). It waits for the outcome: the agent's name,
// or why not.
const DesktopMissionEvent = "DesktopMissionRequest"

// DesktopMissionInput is the payload of a DesktopMissionEvent. It names
// apps from the human's config and nothing more: no command, class,
// workspace or scope comes from the asker.
type DesktopMissionInput struct {
	Plan       string   `json:"plan"`
	Apps       []string `json:"apps"`
	HumanSteps []string `json:"human_steps,omitempty"`
	Dir        string   `json:"dir"`
	Model      string   `json:"model,omitempty"`
	AskedBy    string   `json:"asked_by"` // the asker's peer name, for the card and the report
}

// DesktopInput is the payload of a DesktopEvent.
type DesktopInput struct {
	Kind    string          `json:"kind"` // "start", "child" or "call"
	Mission string          `json:"mission"`
	Tool    string          `json:"tool,omitempty"`
	Args    json.RawMessage `json:"args,omitempty"`
	Token   string          `json:"token,omitempty"` // the next piece of an allowed call
	// child: hypruse's process, for the panic path to know it by.
	ChildPID   int    `json:"child_pid,omitempty"`
	ChildStart string `json:"child_start,omitempty"`
}

// AgentInboundEvent is not a hook event either: a guarded agent's own
// UserPromptSubmit hook (`den hook agent-inbound`) sends it for a prompt
// that holds a peer envelope, with the SHA-256 of the prompt as Payload
// {"sha256"}. Den allows it only if Den delivered exactly that to the
// session; anything else, and no answer, blocks it.
const AgentInboundEvent = "AgentInbound"

// GodOffEvent is not a hook event: `den god off` sends it. It only
// lowers, so anyone may ask; nothing on den.sock turns God mode on.
const GodOffEvent = "GodOff"

// OrchSendInput is the payload of an OrchSendEvent: SendMessage's input.
type OrchSendInput struct {
	To      string `json:"to"`
	Message string `json:"message"`
	Summary string `json:"summary,omitempty"`
}

// The behaviours of a SendEvent answer besides allow and deny.
const (
	SendDeclined  = "declined"  // the human answered ^N
	SendCancelled = "cancelled" // the question ended unanswered: the agent's session ended, or the link was lost
)

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
	// SessionEnd: why it ended. Claude Code 2.1.289 sends "reason"
	// (prompt_input_exit, logout, clear, other); end_reason is read too.
	Reason    string `json:"reason"`
	EndReason string `json:"end_reason"`
	// StopFailure: the documented error_type, and the error field Claude
	// Code 2.1.289 actually sends (a string such as "rate_limit"). Raw so
	// another event's error of a different shape cannot spoil the decode.
	ErrorType string          `json:"error_type"`
	Error     json.RawMessage `json:"error"`
	// PostToolUseFailure: true when the human interrupted the tool (Esc),
	// which is not the tool failing.
	IsInterrupt bool `json:"is_interrupt"`
}

// SessionEndReason is why a session ended, "" when Claude Code gave none.
func (in HookInput) SessionEndReason() string {
	if in.Reason != "" {
		return in.Reason
	}
	return in.EndReason
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
