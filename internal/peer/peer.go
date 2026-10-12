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

// Send posts text to the socket. It returns an error when the session has no
// socket, the socket is gone, or the write fails.
//
// The path comes from a session file, which only vouches for itself, so it
// is not trusted: it must be a socket itself (a symlink is refused, whatever
// it points at, so the text cannot be steered to the ssh agent or another
// service) owned by the caller, and the process that answers must run as the
// caller too. The errors never carry the path.
func Send(socketPath, text string) error {
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
	line, err := json.Marshal(map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": Compose(text)},
	})
	if err != nil {
		return err
	}
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}
