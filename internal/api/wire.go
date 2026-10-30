// Package api serves den-api.sock: Den's local API for apps on this
// machine (docs/DREY-API.md). It is off unless the
// human turns it on (api.enabled, /api on in Den).
//
// The wire is JSON lines over a Unix socket that only Den's own user may
// use. Every connection starts with hello. A request is
// {"v":1,"id":n,"op":"…","params":{…}}; its reply carries the same id,
// {"id":n,"ok":true,"result":…} or {"id":n,"ok":false,"error":{…}}. A
// subscribing op is answered first; then Den pushes {"event":"…","data":…}
// lines on that connection until either side closes it.
//
// Nothing here approves, allows, denies, answers, spawns, closes or sends
// to an agent. Reads carry metadata only (no prompt text, no transcript, no
// request body outside inbox.body, which local items alone serve), and the
// only way to a model is ask, which goes to a read-only lane with no tools.
package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// V is the protocol version Den serves.
const V = 1

// MaxLine is the longest line Den reads, in bytes.
const MaxLine = 1 << 20

// Request is one line from a client.
type Request struct {
	V      int             `json:"v"`
	ID     int64           `json:"id"`
	Op     string          `json:"op"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Reply is Den's answer to a request.
type Reply struct {
	ID     int64      `json:"id"`
	OK     bool       `json:"ok"`
	Result any        `json:"result,omitempty"`
	Error  *WireError `json:"error,omitempty"`
}

// Event is a pushed line on a subscribed connection.
type Event struct {
	Event string `json:"event"`
	Data  any    `json:"data,omitempty"`
}

// WireError is a failed reply's error.
type WireError struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
	// Data says more about some refusals: below_level's {class}.
	Data map[string]string `json:"data,omitempty"`
}

// Error codes.
const (
	CodeUnsupported = "unsupported"
	CodeBadRequest  = "bad_request"
	CodeNotFound    = "not_found"
	CodeTooLarge    = "too_large"
	CodeHelloFirst  = "hello_first"
	CodeForbidden   = "forbidden"
	CodeUnavailable = "unavailable"
	CodeInternal    = "internal"
	// CodeBusy answers a view switch asked for while another switch, or a
	// close of an agent's window, is running.
	CodeBusy = "busy"
)

// Event names.
const (
	EventFleet = "fleet"
	EventInbox = "inbox"
	EventChat  = "chat"
	EventGrant = "grant"
	EventCard  = "card"
	EventMode  = "mode"
	// EventView carries a view switch's outcome (ViewChange).
	EventView = "view"
)

// ModeInfo is the permission mode Den starts agents in (Global) and
// whether any local agent runs in another or one Den does not know
// (Mixed): hello's mode, the fleet's, and a mode event's data.
type ModeInfo struct {
	Global string `json:"global"`
	Mixed  bool   `json:"mixed"`
}

// Info is hello's result.
type Info struct {
	Version  string `json:"version"`
	Versions []int  `json:"versions"`
	Host     string `json:"host,omitempty"`
	// Control is ControlOn when an app may pair for control (pair.claim,
	// orch.say), ControlOff when the human turned control off in Den.
	// A Den without control sends none.
	Control string `json:"control,omitempty"`
	// Features lists what this Den offers beyond D1: FeatureConsole when
	// console pairing (console.*) is on. A Den without it sends none.
	Features []string `json:"features,omitempty"`
	// Mode is the global permission mode, once Den has published one.
	Mode *ModeInfo `json:"mode,omitempty"`
	// ConsolePublicKey is Den's console key (64 hex), when it offers
	// console pairing: public, for the dock to show its fingerprint.
	ConsolePublicKey string `json:"console_public_key,omitempty"`
	// View is the view Den is in, ViewAgent or ViewUser, and
	// ViewWorkspaces the workspaces Den hides windows on in User view. A
	// Den that cannot switch views (no Hyprland to move windows with)
	// sends neither.
	View           string   `json:"view,omitempty"`
	ViewWorkspaces []string `json:"view_workspaces,omitempty"`
	// God is God mode's state (god.go).
	God *God `json:"god,omitempty"`
}

// The two views. In Agent view every session, each agent and Den's own,
// shows in a terminal window of its own, tiled by the window manager as
// always; in User view Den has moved those windows out of sight.
const (
	ViewAgent = "agent"
	ViewUser  = "user"
)

// ViewWorkspace is the one workspace Den hides windows on, a fixed name so a
// launcher can skip exactly it.
const ViewWorkspace = "special:den"

// ViewResult is view.show's and view.hide's result. View is the view asked
// for. Shown counts Den's own windows still visible once the switch ended,
// whichever verb it was: a show succeeded when Shown == Total, a hide when
// Shown == 0, and Total 0 means Den has no windows of its own. Reason is
// one plain line of at most 40 characters, absent when there is nothing to
// say ("1 of 4 not hidden").
type ViewResult struct {
	View   string `json:"view"`
	Shown  int    `json:"shown"`
	Total  int    `json:"total"`
	Reason string `json:"reason,omitempty"`
}

// ViewChange is a view event's data: ViewResult without the reason.
type ViewChange struct {
	View  string `json:"view"`
	Shown int    `json:"shown"`
	Total int    `json:"total"`
}

// FeatureConsole is Info.Features' entry for console pairing.
const FeatureConsole = "console"

// Info.Control's values.
const (
	ControlOn  = "on"
	ControlOff = "off"
)

// Fleet is fleet.snapshot's result and a fleet event's data.
type Fleet struct {
	Taken    time.Time    `json:"taken"`
	Agents   []Agent      `json:"agents"`
	Windows  []Window     `json:"windows"`
	Missions []MissionRef `json:"missions"`
	Cost     Cost         `json:"cost"`
	Asks     []AskStatus  `json:"asks,omitempty"`
	Mode     *ModeInfo    `json:"mode,omitempty"`
	// View is the view Den is in; absent when Den cannot switch views.
	View string `json:"view,omitempty"`
}

// Agent.Hooks values.
const (
	HooksOn      = "on"
	HooksOff     = "off"
	HooksUnknown = "unknown"
)

// Agent states and waits.
const (
	StateIdle    = "idle"
	StateWorking = "working"
	StateWaiting = "waiting"
	StateStalled = "stalled"
	StateDone    = "done"

	WaitingPermission = "permission"
	WaitingQuestion   = "question"
	WaitingPlan       = "plan"
	WaitingTTY        = "tty"
)

// Agent is one agent, metadata only.
type Agent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Alias is the critter name an agent had before Den named it by its
	// role, "" when Name is it. Name and Alias are labels: ID is the agent.
	Alias    string    `json:"alias,omitempty"`
	Role     string    `json:"role,omitempty"`
	State    string    `json:"state"`
	Waiting  string    `json:"waiting,omitempty"`
	Worktree string    `json:"worktree,omitempty"`
	Model    string    `json:"model,omitempty"`
	Since    time.Time `json:"since"`
	Window   string    `json:"window,omitempty"`
	Host     string    `json:"host,omitempty"`
	Remote   bool      `json:"remote,omitempty"`
	// Hooks says whether Den can see this agent's prompts: HooksOn when Den
	// has had a hook event from the session since Den started, HooksOff
	// when Den's hooks are not installed at all, HooksUnknown when they are
	// but this session has not reported yet (new, or started before the hooks
	// or before this Den). Agents on other machines carry none.
	Hooks    string    `json:"hooks,omitempty"`
	CostUSD  float64   `json:"costUsd"`
	Turns    int       `json:"turns"`
	LastSeen time.Time `json:"lastSeen"`
	// Color is the colour Den draws this agent in (its critter, an @Name),
	// as "#rrggbb"; only for agents on this machine.
	Color string `json:"color,omitempty"`
	// PermissionMode is the mode the agent's own hooks last reported, ""
	// when none did; a mode Den does not know is "unknown". Only for
	// agents on this machine.
	PermissionMode string `json:"permission_mode,omitempty"`
	// Doing is what the agent is doing now, in one short line from Den's
	// own facts (see Doing).
	Doing Doing `json:"doing"`
}

// Window is a terminal window Den matched to an agent.
type Window struct {
	Address string `json:"address"`
	Agent   string `json:"agent,omitempty"`
	AgentID string `json:"agent_id,omitempty"` // the agent's id: Agent is only its name
	Class   string `json:"class"`
	Title   string `json:"title"`
	PID     int    `json:"pid"`
}

// MissionRef is a mission as the fleet carries it.
type MissionRef struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	State   string `json:"state"`
	Agent   string `json:"agent,omitempty"`
	AgentID string `json:"agent_id,omitempty"` // the agent's id: Agent is only its name
}

// Cost is the fleet's estimated spend.
type Cost struct {
	TodayUSD float64 `json:"todayUsd"`
	WeekUSD  float64 `json:"weekUsd"`
	Currency string  `json:"currency"`
}

// Inbox item kinds.
const (
	KindPermission   = "permission"
	KindQuestion     = "question"
	KindPlan         = "plan"
	KindNotification = "notification"
	KindGrant        = "grant"
)

// Item is an inbox item with a digest of its body, never the body.
type Item struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Agent string `json:"agent"`
	// AgentID is the agent's id, "" for an item from a host or from Den:
	// Agent is only its name.
	AgentID string    `json:"agent_id,omitempty"`
	Since   time.Time `json:"since"`
	Title   string    `json:"title"`
	Digest  Digest    `json:"digest"`
	Host    string    `json:"host,omitempty"`
}

// Digest describes a body without carrying it.
type Digest struct {
	FirstLine string `json:"firstLine"`
	Bytes     int    `json:"bytes"`
	Hash      string `json:"hash"`
}

// Body is inbox.body's result.
type Body struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Text string `json:"text"`
	Hash string `json:"hash"`
}

// InboxChange is an inbox event's data.
type InboxChange struct {
	Change string `json:"change"` // added, answered, expired
	Item   Item   `json:"item"`
}

// Chunk is one line of an ask stream. Den sends text, status, done and
// error chunks; no card and no window.
type Chunk struct {
	Type    string `json:"type"`
	Delta   string `json:"delta,omitempty"`
	Session string `json:"session,omitempty"`
	Message string `json:"message,omitempty"`
	Ask     string `json:"ask,omitempty"`
	Seq     int    `json:"seq,omitempty"`
	Text    string `json:"text,omitempty"`
	// Turn is an orch.say turn's id; Card and Item a needs_approval
	// chunk's inbox card title and item id.
	Turn string `json:"turn,omitempty"`
	Card string `json:"card,omitempty"`
	Item string `json:"item,omitempty"`
}

// AskStatus is the latest status of a running ask, as the fleet carries
// it. Session is set only for the client that asked.
type AskStatus struct {
	Ask     string `json:"ask"`
	Session string `json:"session,omitempty"`
	Seq     int    `json:"seq"`
	Text    string `json:"text"`
}

// Session is one lane session as chat.sessions lists it.
type Session struct {
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Last    time.Time `json:"last"`
	Current bool      `json:"current"`
}

// Message is one turn of chat.history.
type Message struct {
	Role string    `json:"role"` // user, orchestrator
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// Mission is a mission with its lines, as mission.list shows it.
type Mission struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	State     string    `json:"state"`
	Agent     string    `json:"agent,omitempty"`
	Agents    []string  `json:"agents,omitempty"`
	AgentIDs  []string  `json:"agent_ids,omitempty"` // the ids of the agents its sub-goals name
	Lines     []string  `json:"lines"`
	Done      int       `json:"done,omitempty"`
	SpendUSD  float64   `json:"spendUsd,omitempty"`
	BudgetUSD float64   `json:"budgetUsd,omitempty"`
	Proposed  time.Time `json:"proposed"`
}

// HashOf is the digest and body hash: "sha256:" + hex of the text.
func HashOf(text string) string {
	sum := sha256.Sum256([]byte(text))
	return "sha256:" + hex.EncodeToString(sum[:])
}
