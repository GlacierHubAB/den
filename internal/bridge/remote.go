package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// RemoteSocketName is the socket `den remote serve` listens on, beside
// den.sock, while a Den on another machine is attached to this host. Hooks
// dial it only when no Den runs here.
const RemoteSocketName = "den-remote.sock"

// RemoteSocketPath is where `den remote serve` listens for held requests:
// $DEN_REMOTE_SOCKET, else $XDG_RUNTIME_DIR/den-remote.sock, else
// den-remote.sock in $TMPDIR/den-<uid>, a directory that must be this
// user's own with mode 0700 (serve creates it so; nobody else may have made
// it first). It fails when that directory is not safe to use.
func RemoteSocketPath() (string, error) {
	if p := os.Getenv("DEN_REMOTE_SOCKET"); p != "" {
		return p, nil
	}
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, RemoteSocketName), nil
	}
	dir := FallbackDir()
	if err := CheckPrivateDir(dir); err != nil {
		return "", err
	}
	return filepath.Join(dir, RemoteSocketName), nil
}

// AskFirst is Ask over the first of paths that accepts a connection: a
// Den on this machine (den.sock) before a Den attached from elsewhere
// (den-remote.sock). Only the first that answers the dial is asked, so a
// Den here that holds the request and lets it time out is never followed
// by a second ask. Either must be served by a process of this user
// (SO_PEERCRED, through Dial); otherwise it counts as not there.
func AskFirst(local string, remote string, env Envelope, wait time.Duration) (d Decision, ok bool) {
	if conn, err := Dial(local, 500*time.Millisecond); err == nil {
		d, err := ask(conn, env, wait)
		return d, err == nil
	}
	if remote == "" {
		return Decision{}, false
	}
	conn, err := Dial(remote, 500*time.Millisecond)
	if err != nil {
		return Decision{}, false
	}
	d, err = ask(conn, env, wait)
	return d, err == nil
}

// NewRemoteRequest is a PermissionRequest held on another machine and
// relayed by `den remote serve`. host is the label of the connection it came
// on, id the remote's request id. send carries the decision back down that
// connection; it is called at most once, from Reply. LetGo marks it no
// longer answerable (the remote hook gave up, or the link was lost).
func NewRemoteRequest(host, id string, env Envelope, send func(Decision)) *Request {
	r := &Request{Envelope: env, Host: host, RemoteID: id, reply: make(chan Decision, 1), gone: make(chan struct{}), send: send}
	_ = json.Unmarshal(env.Payload, &r.Input)
	return r
}

// LetGo marks a remote request as no longer held, with the reason the
// human sees ("released to the terminal on devbox (link lost)"). Later
// replies are not sent. The first reason counts.
func (r *Request) LetGo(why string) {
	r.bye.Do(func() {
		r.why = why
		if r.gone != nil {
			close(r.gone)
		}
	})
}

// Why is the reason given to LetGo, "" when none was.
func (r *Request) Why() string {
	if !r.HungUp() {
		return ""
	}
	return r.why
}
