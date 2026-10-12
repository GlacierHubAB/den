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
// of the host's events.jsonl ended (absent or zero: from now on). Nothing
// else may follow in version 1: serve answers anything more with an error
// (code request) and exits, and exits quietly on stdin's EOF.
//
// serve to Den, on serve's stdout:
//
//	{"type":"hello","proto":1,"den":"v0.2.0","claude":"2.1.300","hostname":"devbox",
//	 "remote_now":"2026-10-12T12:00:00.123Z","policy":"observe","labels":true,
//	 "cursor":{…}}
//
// First, once. policy is what serve applies: the lower of the requested
// policy and Supported (observe in this build), so a host can be held to
// less than it is configured for but never given more. cursor is where its
// event stream starts.
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
)

// Request is Den's hello to serve.
type Request struct {
	Type   string        `json:"type"`
	Proto  int           `json:"proto"`
	Den    string        `json:"den,omitempty"`
	Policy Policy        `json:"policy"`
	Labels bool          `json:"labels"`
	Cursor events.Cursor `json:"cursor,omitzero"`
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

// allowed lists, per message type, the top-level keys version 1 knows.
// Anything else in a message is dropped and the host flagged.
var allowed = map[string][]string{
	TypeHello:    {"type", "proto", "den", "claude", "hostname", "remote_now", "policy", "labels", "cursor"},
	TypeSnapshot: {"type", "remote_now", "agents"},
	TypeEvent:    {"type", "cursor", "event"},
	TypeError:    {"type", "code", "message"},
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
