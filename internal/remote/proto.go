// Package remote connects Den to agents on other machines over the
// system ssh (docs/REMOTE.md).
//
// While Den runs, it keeps one ssh session per enabled host in
// remotes.toml. The session runs `den remote serve --proto 1` on the host,
// which streams the host's fleet up the pipe as metadata and exits when
// the pipe closes. Nothing listens on a network port, nothing outlives
// the session, and Den stores no credential: ssh uses the human's own
// config, keys and known_hosts, with strict host key checking and no
// prompts.
//
// # Protocol, version 1
//
// Each message is one JSON object on one line, ended by '\n', at most
// MaxLine bytes. Every object has a "type". The set of types is closed:
// a receiver drops a type it does not know and flags the sender.
//
// Den (local) to serve (remote), on serve's stdin:
//
//	{"type":"hello","proto":1,"den":"v0.2.0","policy":"observe","labels":true,
//	 "cursor":{"ino":12,"off":3456,"head":"…","head_len":64}}
//
// The request, sent once. policy is the host's policy in remotes.toml,
// labels whether badge role and task may travel, cursor where Den's copy
// of the host's events.jsonl ended (absent or zero: from now on).
// controller marks which Den asks: a random id each local machine keeps in
// its state dir. It is an identity marker, not a secret: it lets a Den that
// reconnects take its own held requests back from a serve whose link died
// half-open, and tells a second Den apart. After it, serve reads only
// decision, ping, send and close lines (below), within MaxLine and a rate
// cap; anything else gets an error (code request) and serve exits. It exits
// quietly on stdin's EOF.
//
//	{"type":"decision","id":"…","hash":"…","behavior":"allow","message":"…","answers":{"Which?":"A"}}
//
// The human's answer to a held request: behavior allow, deny, or "" for
// "no decision, show the terminal's dialog". hash is the SHA-256 (hex) of
// the held request's body exactly as Den received it, so a decision only
// counts for the body the human saw. answers go with an allow of an
// AskUserQuestion call, keyed by the question text as shown.
//
//	{"type":"ping"}
//
// Den is still there. Den sends one every PingEvery; a serve that relays
// held requests and hears nothing from Den for Silence lets them all go to
// their terminals and exits, so a half-open link does not hold them.
//
//	{"type":"send","id":"…","session":"…","text":"…"}
//	{"type":"close","id":"…","session":"…","force":false}
//
// The two actions, send and close, the whole of what Den may ask serve to
// do: there is no generic exec and no shell, and text travels in this
// message, never in an argument list. Both are allowed only when the policy
// serve applies is approve or full; under observe serve answers each with a
// failed result and does nothing, and Den does not send them at all. id is
// Den's own, echoed in the result. session is a Claude Code session id,
// matched exactly against the host's live sessions (~/.claude/sessions, pid
// alive) when the command arrives, not when Den last saw a snapshot. send
// posts text, at most MaxSendText bytes, to that session's own
// messagingSocketPath exactly as `den send` does on the host. close applies
// the closer's rules there: only a live session found by id, a working agent
// only with force, SIGTERM then a wait, SIGKILL only with force, no window to
// close (a host has no desktop). At most MaxActions run at once on one serve.
//
// serve to Den, on serve's stdout:
//
//	{"type":"hello","proto":1,"den":"v0.2.0","claude":"2.1.300","hostname":"devbox",
//	 "remote_now":"2026-10-12T12:00:00.123Z","policy":"observe","labels":true,
//	 "cursor":{…}}
//
// First, once. policy is what serve applies: the lower of the requested
// policy and Supported (approve in this build: full applies as approve
// until a later slice), so a host can be held to less than it is
// configured for but never given more. cursor is where its event stream
// starts. When another Den already relays this host's held requests,
// serve applies observe and says who holds them:
// "attached":{"pid":4242,"since":"…"}. note says why serve applies less
// than asked for any other reason (no private directory for its socket).
//
//	{"type":"snapshot","remote_now":"…","agents":[fleet.Meta, …]}
//
// The host's whole fleet as fleet.Meta, on start and after each change to
// the session files or the badge store (debounced), and every
// SnapshotEvery while nothing changes.
//
//	{"type":"event","cursor":{…},"event":{events.Event}}
//
// One line of the host's events.jsonl and the cursor just past it, in
// order, from the requested cursor on. The event has the known fields only;
// an event that already carries a host (it came from a third machine) is
// not sent.
//
//	{"type":"held","id":"…","session":"…","tool":"Bash","body":{"command":"go test ./..."}}
//
// Under approve or full only: a PermissionRequest (a permission, a plan
// through ExitPlanMode, or an AskUserQuestion call) that a hook on the host
// holds, waiting for the human. id is 128 random bits in hex, new for each
// request and kept when serve offers it again after a reconnect. body is
// the part of the tool input Den shows the human, filtered, capped and
// sanitised on the host (heldBody); the rest of the tool input never
// leaves the host. Under observe nothing is held: hooks get no decision at
// once and Claude Code shows its own dialog.
//
//	{"type":"gone","id":"…"}
//
// A held request is no longer waiting: its hook gave up (Claude Code
// answered it in the terminal, or its own wait ran out).
//
//	{"type":"result","id":"…","ok":true,"killed":false,"error":"…"}
//
// The outcome of a send or close, once, with the id Den gave it. error is
// short, sanitised and says why nothing happened; killed says a close
// needed SIGKILL. A result for an id Den has no request open for is
// dropped and the host flagged. send has no acknowledgement from Claude
// Code: ok means the write to the session's socket succeeded.
//
//	{"type":"error","code":"proto","message":"…"}
//
// Something serve cannot do, such as an unsupported proto; serve exits
// after it. Codes: proto (unsupported version), request (a bad hello, or
// anything sent after it), internal.
//
// # What Den does with it
//
// The host label always comes from remotes.toml, never from the payload.
// Den checks the policy again on its side and drops any field the host's
// policy does not allow, sanitises every string (internal/sanitize), and
// turns ages into local times from when the message arrived, so it never
// compares clocks across machines. A line over MaxLine, or more than
// MaxRate messages in a second, drops the host to offline as flooding.
package remote

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/GlacierHubAB/den/internal/events"
	"github.com/GlacierHubAB/den/internal/fleet"
)

// Proto is the protocol version this build speaks.
const Proto = 1

// MaxLine is the longest message line either side accepts.
const MaxLine = 256 << 10

// MaxRate is how many messages a second Den takes from a host before it
// treats the host as flooding. serve paces its event backfill well under it.
const MaxRate = 2000

// Message types.
const (
	TypeHello    = "hello"
	TypeSnapshot = "snapshot"
	TypeEvent    = "event"
	TypeError    = "error"
	TypeHeld     = "held"
	TypeGone     = "gone"
	TypeDecision = "decision" // Den to serve
	TypePing     = "ping"     // Den to serve
	TypeSend     = "send"     // Den to serve, approve or full only
	TypeClose    = "close"    // Den to serve, approve or full only
	TypeResult   = "result"   // serve to Den: the outcome of a send or close
)

// Limits of the actions.
const (
	// MaxSendText caps the text of one send, in bytes.
	MaxSendText = 64 << 10
	// MaxActions caps the sends and closes one serve runs at once.
	MaxActions = 8
)

// Request is Den's hello to serve.
type Request struct {
	Type   string        `json:"type"`
	Proto  int           `json:"proto"`
	Den    string        `json:"den,omitempty"`
	Policy Policy        `json:"policy"`
	Labels bool          `json:"labels"`
	Cursor events.Cursor `json:"cursor,omitzero"`
	// Controller is the asking Den's identity marker (not a secret).
	Controller string `json:"controller,omitempty"`
}

// Hello is serve's first message.
type Hello struct {
	Type      string        `json:"type"`
	Proto     int           `json:"proto"`
	Den       string        `json:"den"`
	Claude    string        `json:"claude,omitempty"`
	Hostname  string        `json:"hostname"`
	RemoteNow time.Time     `json:"remote_now"`
	Policy    Policy        `json:"policy"`
	Labels    bool          `json:"labels"`
	Cursor    events.Cursor `json:"cursor,omitzero"`
	Attached  *Attached     `json:"attached,omitempty"`
	Note      string        `json:"note,omitempty"`
}

// Attached names the serve of another Den that relays the host's held
// requests.
type Attached struct {
	Pid   int       `json:"pid"`
	Since time.Time `json:"since,omitzero"`
}

// Snapshot is the host's fleet.
type Snapshot struct {
	Type      string       `json:"type"`
	RemoteNow time.Time    `json:"remote_now"`
	Agents    []fleet.Meta `json:"agents"`
}

// Event is one line of the host's event stream.
type Event struct {
	Type   string        `json:"type"`
	Cursor events.Cursor `json:"cursor"`
	Event  events.Event  `json:"event"`
}

// Error is serve saying it cannot go on.
type Error struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Held is a permission request a hook on the host holds.
type Held struct {
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Session string          `json:"session"`
	Tool    string          `json:"tool"`
	Body    json.RawMessage `json:"body"`
}

// Gone says a held request stopped waiting.
type Gone struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// Decision is the human's answer to a held request, Den to serve.
type Decision struct {
	Type     string            `json:"type"`
	ID       string            `json:"id"`
	Hash     string            `json:"hash"`
	Behavior string            `json:"behavior"`
	Message  string            `json:"message,omitempty"`
	Answers  map[string]string `json:"answers,omitempty"`
}

// SendCmd asks serve to post text to a session's inbox socket.
type SendCmd struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Session string `json:"session"`
	Text    string `json:"text"`
}

// CloseCmd asks serve to end a session's process.
type CloseCmd struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Session string `json:"session"`
	Force   bool   `json:"force,omitempty"`
}

// Result is serve's answer to a send or close.
type Result struct {
	Type   string `json:"type"`
	ID     string `json:"id"`
	OK     bool   `json:"ok"`
	Killed bool   `json:"killed,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Ping is Den saying it is still there.
type Ping struct {
	Type string `json:"type"`
}

// PingEvery is how often Den pings serve.
const PingEvery = 15 * time.Second

// BodyHash is the hash a decision carries: SHA-256 of a held body's bytes,
// in hex.
func BodyHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// allowed lists, per message type, the top-level keys version 1 knows.
// Anything else in a message is dropped and the host flagged.
var allowed = map[string][]string{
	TypeHello:    {"type", "proto", "den", "claude", "hostname", "remote_now", "policy", "labels", "cursor", "attached", "note"},
	TypeSnapshot: {"type", "remote_now", "agents"},
	TypeEvent:    {"type", "cursor", "event"},
	TypeError:    {"type", "code", "message"},
	TypeHeld:     {"type", "id", "session", "tool", "body"},
	TypeGone:     {"type", "id"},
	TypeResult:   {"type", "id", "ok", "killed", "error"},
}

// allowedIn is the same for what serve reads after Den's hello request.
var allowedIn = map[string][]string{
	TypeDecision: {"type", "id", "hash", "behavior", "message", "answers"},
	TypePing:     {"type"},
	TypeSend:     {"type", "id", "session", "text"},
	TypeClose:    {"type", "id", "session", "force"},
}

// ErrLineTooLong is a message over MaxLine.
var ErrLineTooLong = errors.New("message line over 256 KB")

// lineReader reads newline-framed messages of at most max bytes.
type lineReader struct {
	r   *bufio.Reader
	max int
}

func newLineReader(r io.Reader, max int) *lineReader {
	return &lineReader{r: bufio.NewReaderSize(r, 64<<10), max: max}
}

// next returns one line without its newline. A line over max is an error
// and the stream is not read further.
func (l *lineReader) next() ([]byte, error) {
	var line []byte
	for {
		chunk, err := l.r.ReadSlice('\n')
		if len(line)+len(chunk) > l.max+1 {
			return nil, ErrLineTooLong
		}
		line = append(line, chunk...)
		switch {
		case err == nil:
			return line[:len(line)-1], nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(line) > 0:
			return nil, io.ErrUnexpectedEOF // a cut-off last line is not a message
		default:
			return nil, err
		}
	}
}

// writeMsg writes v as one line.
func writeMsg(w io.Writer, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(raw) > MaxLine {
		return ErrLineTooLong
	}
	_, err = w.Write(append(raw, '\n'))
	return err
}

// peek reads a message's type and reports the top-level keys it does not
// know for that type.
func peek(line []byte) (typ string, unknown []string, err error) {
	return peekIn(line, allowed)
}

// peekIn is peek against another allowlist.
func peekIn(line []byte, allowed map[string][]string) (typ string, unknown []string, err error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(line, &m); err != nil {
		return "", nil, err
	}
	if err := json.Unmarshal(m["type"], &typ); err != nil {
		return "", nil, errors.New("message without a type")
	}
	known := map[string]bool{}
	for _, k := range allowed[typ] {
		known[k] = true
	}
	for k := range m {
		if !known[k] {
			unknown = append(unknown, k)
		}
	}
	return typ, unknown, nil
}

// rateCap counts messages per one-second window.
type rateCap struct {
	max    int
	window time.Time
	n      int
}

// allow counts one message at now and reports whether it is within the cap.
func (r *rateCap) allow(now time.Time) bool {
	if now.Sub(r.window) >= time.Second {
		r.window, r.n = now, 0
	}
	r.n++
	return r.n <= r.max
}
