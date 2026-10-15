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
)

// Event names.
const (
	EventFleet = "fleet"
	EventInbox = "inbox"
	EventChat  = "chat"
	EventGrant = "grant"
	EventCard  = "card"
)

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
}

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
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Role     string    `json:"role,omitempty"`
	State    string    `json:"state"`
	Waiting  string    `json:"waiting,omitempty"`
	Worktree string    `json:"worktree,omitempty"`
	Model    string    `json:"model,omitempty"`
	Since    time.Time `json:"since"`
	Window   string    `json:"window,omitempty"`
	Host     string    `json:"host,omitempty"`
	Remote   bool      `json:"remote,omitempty"`
	CostUSD  float64   `json:"costUsd"`
	Turns    int       `json:"turns"`
	LastSeen time.Time `json:"lastSeen"`
	// Color is the colour Den draws this agent in (its critter, an @Name),
	// as "#rrggbb"; only for agents on this machine.
	Color string `json:"color,omitempty"`
}

// Window is a terminal window Den matched to an agent.
type Window struct {
	Address string `json:"address"`
	Agent   string `json:"agent,omitempty"`
	Class   string `json:"class"`
	Title   string `json:"title"`
	PID     int    `json:"pid"`
}

// MissionRef is a mission as the fleet carries it.
type MissionRef struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	State string `json:"state"`
	Agent string `json:"agent,omitempty"`
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
	ID     string    `json:"id"`
	Kind   string    `json:"kind"`
	Agent  string    `json:"agent"`
	Since  time.Time `json:"since"`
	Title  string    `json:"title"`
	Digest Digest    `json:"digest"`
	Host   string    `json:"host,omitempty"`
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
