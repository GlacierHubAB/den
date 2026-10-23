// Package events is Den's stable event stream for scripts: one JSON object
// per line in events.jsonl, written by `den hook` as Claude Code's hooks
// fire and by the running Den for what only it can tell (stalled, still
// waiting). It works without Den running.
//
// An event carries metadata only: kind, session, agent name, time and a
// few kind-specific fields. Never a command, tool input, prompt, message
// or file text; a turn_start names where its prompt came from (source) and
// keeps nothing of the prompt. The schema is versioned by the v field; see
// docs/EVENTS.md.
package events

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/GlacierHubAB/den/internal/badge"
	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/peermsg"
)

// Version is the schema version every event carries in v.
const Version = 1

// Event kinds.
const (
	SessionStart = "session_start" // reason: startup, resume, clear, compact
	SessionEnd   = "session_end"   // reason: why it ended, when Claude Code says
	TurnStart    = "turn_start"    // a prompt was submitted
	Done         = "done"          // a turn finished
	NeedsYou     = "needs_you"     // request: permission, plan, question; tool for permission
	Idle         = "idle"          // Claude Code's idle notice: waiting for input
	TurnFailed   = "turn_failed"   // error: the API error type
	ToolFailed   = "tool_failed"   // tool: the tool whose call failed
	Stalled      = "stalled"       // minutes: working with no hook event that long (from Den)
	StillWaiting = "still_waiting" // request, minutes: a needs_you unanswered that long (from Den)
)

// Sources of a turn_start: where the prompt came from.
const (
	SourcePeer     = "peer"     // a message from another session, Den's own included
	SourceTerminal = "terminal" // any other prompt: typed in the agent's terminal, or Den's brief for a spawned agent
)

// Kinds lists every kind, in the order the docs give them.
var Kinds = []string{SessionStart, SessionEnd, TurnStart, Done, NeedsYou, Idle, TurnFailed, ToolFailed, Stalled, StillWaiting}

// Event is one line of the stream.
type Event struct {
	V       int       `json:"v"`
	Time    time.Time `json:"time"`
	Kind    string    `json:"kind"`
	Session string    `json:"session"`
	Agent   string    `json:"agent"`
	Request string    `json:"request,omitempty"`
	Tool    string    `json:"tool,omitempty"`
	Error   string    `json:"error,omitempty"`
	Minutes int       `json:"minutes,omitempty"`
	Reason  string    `json:"reason,omitempty"`
	// Source, on a turn_start, is where the prompt came from: SourcePeer or
	// SourceTerminal. Absent in a stream written before the field, and when
	// Claude Code sent no prompt. Additive: v stays 1.
	Source string `json:"source,omitempty"`
	// Host is the remote host the event happened on, as the local Den
	// labels it (remotes.toml); left out for this machine. Additive: v
	// stays 1.
	Host string `json:"host,omitempty"`
}

// New stamps an event of kind for a session with the version, the time and
// the agent's name.
func New(kind, sessionID string) Event {
	return Event{V: Version, Time: time.Now().UTC().Truncate(time.Millisecond), Kind: kind, Session: sessionID, Agent: badge.Name(sessionID)}
}

// FromHook maps a hook event to a stream event, picking metadata fields by
// name. ok is false for hooks the stream leaves out (PostToolUse, a
// permission prompt or an AskUserQuestion permission request whose wait
// was already reported, and so on). One wait is one needs_you.
func FromHook(in bridge.HookInput) (ev Event, ok bool) {
	if in.SessionID == "" {
		return Event{}, false
	}
	ev = New("", in.SessionID)
	switch in.HookEventName {
	case "SessionStart":
		ev.Kind, ev.Reason = SessionStart, in.Source
	case "SessionEnd":
		ev.Kind, ev.Reason = SessionEnd, in.SessionEndReason()
	case "UserPromptSubmit":
		ev.Kind, ev.Source = TurnStart, promptSource(in.Prompt)
	case "Stop":
		ev.Kind = Done
	case "StopFailure":
		ev.Kind, ev.Error = TurnFailed, in.FailureType()
	case "PostToolUseFailure":
		ev.Kind, ev.Tool = ToolFailed, in.ToolName
	case "PermissionRequest":
		if in.ToolName == "AskUserQuestion" {
			// Its PreToolUse reported this wait as a question about 60 ms
			// earlier; a second needs_you would show the question twice.
			return Event{}, false
		}
		ev.Kind, ev.Request, ev.Tool = NeedsYou, "permission", in.ToolName
		if in.ToolName == "ExitPlanMode" {
			ev.Request, ev.Tool = "plan", ""
		}
	case "PreToolUse":
		if in.ToolName != "AskUserQuestion" {
			return Event{}, false
		}
		ev.Kind, ev.Request = NeedsYou, "question"
	case "Notification":
		switch in.NotificationType {
		case "idle_prompt":
			ev.Kind = Idle
		case "agent_needs_input", "elicitation_dialog", "elicitation_url_dialog":
			ev.Kind, ev.Request = NeedsYou, "question"
		default:
			// permission_prompt follows a PermissionRequest that already
			// reported the same wait.
			return Event{}, false
		}
	default:
		return Event{}, false
	}
	return ev, true
}

// promptSource names where a prompt came from, "" when there is none to
// look at. Only the name is kept, never the prompt. A prompt that holds
// anything like a peer envelope is a peer's, as Den's inbound hook takes it
// (peermsg.HasEnvelope); one without is not delivered by a peer. The name is
// a label for counting, not a check: a prompt can be written to look like
// either.
func promptSource(prompt string) string {
	switch {
	case prompt == "":
		return ""
	case peermsg.HasEnvelope(prompt):
		return SourcePeer
	}
	return SourceTerminal
}

// Path is the stream: $XDG_STATE_HOME/den/events.jsonl.
func Path() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "den", "events.jsonl")
}

// MaxSize is how large the stream grows before it moves to events.jsonl.1
// and starts again.
const MaxSize = 4 << 20

// Append writes one event as one line. Best effort, like every state file.
func Append(path string, ev Event) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if st, err := os.Stat(path); err == nil && st.Size() > MaxSize {
		os.Rename(path, path+".1")
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(raw, '\n'))
	return err
}

// Reader follows the stream from where it ended when it was opened.
type Reader struct {
	path    string
	f       *os.File
	r       *bufio.Reader
	ino     uint64
	partial []byte
	// atEOF, when set, runs each time the file was read to its end, before
	// the move check: tests write and move the file there.
	atEOF func()
}

// Open starts following the stream at its current end, so only events
// appended from now on are read. A file that does not exist yet is
// waited for.
func Open(path string) *Reader {
	rd := &Reader{path: path}
	rd.open(true)
	return rd
}

func (rd *Reader) open(fromEnd bool) {
	f, err := os.Open(rd.path)
	if err != nil {
		return
	}
	if fromEnd {
		f.Seek(0, io.SeekEnd)
	}
	if rd.f != nil {
		rd.f.Close()
	}
	rd.f, rd.r, rd.ino, rd.partial = f, bufio.NewReader(f), inode(rd.path), nil
}

// drain reads the open file to its end, calling fn for each whole line;
// a line without its end yet is kept for the next read.
func (rd *Reader) drain(fn func(raw []byte, ev Event)) {
	for rd.r != nil {
		line, err := rd.r.ReadBytes('\n')
		rd.partial = append(rd.partial, line...)
		if err != nil {
			return // no full line yet: keep what came and wait for the rest
		}
		var ev Event
		if json.Unmarshal(rd.partial, &ev) == nil {
			fn(rd.partial[:len(rd.partial)-1], ev)
		}
		rd.partial = nil
	}
}

// Run calls fn for each event as it is appended, until stop is closed. It
// follows the stream across a move to events.jsonl.1.
func (rd *Reader) Run(stop <-chan struct{}, fn func(raw []byte, ev Event)) {
	defer func() {
		if rd.f != nil {
			rd.f.Close()
		}
	}()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		if rd.f == nil {
			rd.open(false)
		}
		rd.drain(fn)
		if rd.atEOF != nil {
			rd.atEOF()
		}
		if n := inode(rd.path); rd.f != nil && n != 0 && n != rd.ino {
			// Moved aside. What was appended to the old file after the read
			// above, before the move, is read now: the old file still open
			// here is the moved one. A writer that opened the old path just
			// before the move can still append after this read, and that
			// event is lost: the stream is best effort.
			rd.drain(fn)
			rd.open(false)
			continue
		}
		select {
		case <-stop:
			return
		case <-tick.C:
		}
	}
}
