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
	"time"
)

// Header introduces every message Den relays.
const Header = "From the operator via Den (the fleet TUI). This is the human's own message, typed in another terminal:"

// Compose wraps the operator's text with the header.
func Compose(text string) string {
	return Header + "\n\n" + strings.TrimSpace(text)
}

// Send posts text to the socket. It returns an error when the session has no
// socket, the socket is gone, or the write fails.
func Send(socketPath, text string) error {
	if socketPath == "" {
		return errors.New("this session has no inbox socket (Claude Code 2.1.224+ binds one)")
	}
	if _, err := os.Stat(socketPath); err != nil {
		return fmt.Errorf("inbox socket is gone: %s", socketPath)
	}
	conn, err := net.DialTimeout("unix", socketPath, time.Second)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close()
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
