// Package peer delivers a message into a running Claude Code session through
// the session's inbox socket, the same channel other Claude sessions use.
//
// Wire format (documented under "cross-session messaging"): one JSON line,
// {"type":"user","message":{"role":"user","content":"…"}}. The session sends
// no acknowledgement; a successful write is the receipt. The receiving
// Claude sees the text framed as a message from another session, so Den
// says in the text who really wrote it.
package peer

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
)

// Header introduces every message Den relays.
const Header = "Operator message via Den (typed by your human in another terminal):"

// Compose wraps the operator's text with the header.
func Compose(text string) string {
	return Header + "\n\n" + strings.TrimSpace(text)
}

// DenHeader introduces a message Den sends on its own, from a check it
// runs: nobody typed it, so it never carries the human's weight.
const DenHeader = "Automatic message from Den (sent by Den itself; your human did not type it):"

// ComposeDen wraps Den's own text with DenHeader.
func ComposeDen(text string) string {
	return DenHeader + "\n\n" + strings.TrimSpace(text)
}

// SendFromDen posts Den's own message (an automatic check's) to the
// socket, framed with DenHeader, never Header; the checks are Send's.
func SendFromDen(socketPath, text string) error {
	return post(socketPath, map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": ComposeDen(text)},
	})
}

// UsageHeader introduces the usage stop: Den sends it on its own, but it
// enforces a limit the human set, so it is binding; it never claims the
// human typed it.
const UsageHeader = "Automatic message from Den, enforcing a usage stop your human set (your human did not type this; the stop is theirs and binding):"

// ComposeUsage wraps the usage stop's text with UsageHeader.
func ComposeUsage(text string) string {
	return UsageHeader + "\n\n" + strings.TrimSpace(text)
}

// SendUsage posts the usage stop to the socket, framed with UsageHeader;
// the checks are Send's.
func SendUsage(socketPath, text string) error {
	return post(socketPath, map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": ComposeUsage(text)},
	})
}

// Envelope is how Claude Code's SendMessage frames a message: the
// receiving session shows it from the session at from (its inbox socket,
// "uds:<path>"), named fromName, and a reply goes back there.
func Envelope(from, fromName, body string) string {
	return fmt.Sprintf("<cross-session-message from=%q from-name=%q from-mode=\"prompting\">\n%s\n</cross-session-message>", from, fromName, strings.TrimSpace(body))
}

// SendAs posts a message to the socket as SendMessage from another session
// would: the envelope (Envelope) and that session's address, so that the
// receiver sees it from that session and answers it there. Den uses it for
// the orchestrator's send_message; the checks are Send's.
func SendAs(socketPath, from, fromName, body string) error {
	return post(socketPath, map[string]any{
		"type":     "user",
		"message":  map[string]any{"role": "user", "content": Envelope(from, fromName, body)},
		"priority": "next",
		"from":     from,
	})
}

// Send posts text to the socket. It returns an error when the session has no
// socket, the socket is gone, or the write fails.
//
// The path comes from a session file, which only vouches for itself, so it
// is not trusted: it must be a socket itself (a symlink is refused, whatever
// it points at, so the text cannot be steered to the ssh agent or another
// service) owned by the caller, and the process that answers must run as the
// caller too. The errors never carry the path.
func Send(socketPath, text string) error {
	return post(socketPath, map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": Compose(text)},
	})
}

// post writes one line to a session's inbox socket, after Send's checks.
func post(socketPath string, msg map[string]any) error {
	if socketPath == "" {
		return errors.New("this session has no inbox socket (Claude Code 2.1.224+ binds one)")
	}
	fi, err := os.Lstat(socketPath)
	if err != nil {
		return errors.New("inbox socket is gone")
	}
	if fi.Mode()&os.ModeType != os.ModeSocket {
		return errors.New("inbox socket is not a plain socket (a link or another kind of file); refusing")
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Getuid() {
		return errors.New("inbox socket is not owned by you; refusing")
	}
	conn, err := net.DialTimeout("unix", socketPath, time.Second)
	if err != nil {
		var op *net.OpError
		if errors.As(err, &op) && op.Err != nil {
			err = op.Err // the net error names the path
		}
		return fmt.Errorf("connect: %v", err)
	}
	defer conn.Close()
	if uid, err := bridge.PeerUID(conn); err != nil || uid != os.Getuid() {
		return errors.New("inbox socket is served by another user; refusing")
	}
	line, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}
