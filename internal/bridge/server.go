package bridge

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// Request is one delivered envelope. For PermissionRequest events the handler
// must call Reply exactly once; for every other event Reply is a no-op.
type Request struct {
	Envelope
	Input HookInput
	// Host is the remote host label the request came from, "" for this
	// machine. It is set by whoever builds the request from the connection,
	// never read from the payload.
	Host string
	// RemoteID is the remote's id for a request relayed from Host.
	RemoteID string
	reply    chan Decision
	once     sync.Once
	gone     chan struct{} // closed when the waiting hook hangs up
	bye      sync.Once
	send     func(Decision) // a remote request's way back; nil for a local one
	why      string         // why a remote request was let go
}

// WantsReply reports whether the sender is waiting for a decision.
func (r *Request) WantsReply() bool { return r.reply != nil }

// HungUp reports whether the hook stopped waiting before a reply: Claude
// Code killed it (its timeout, or the dialog answered or dismissed in the
// terminal) or it gave up itself.
func (r *Request) HungUp() bool {
	if r.gone == nil {
		return false
	}
	select {
	case <-r.gone:
		return true
	default:
		return false
	}
}

func (r *Request) hangUp() {
	if r.gone != nil {
		r.bye.Do(func() { close(r.gone) })
	}
}

// Reply sends the decision to the waiting hook. Safe to call more than once;
// only the first call counts.
func (r *Request) Reply(d Decision) {
	if r.reply == nil {
		return
	}
	r.once.Do(func() {
		r.reply <- d
		if r.send != nil && !r.HungUp() {
			r.send(d)
		}
	})
}

// Server accepts hook connections.
type Server struct {
	path string
	ln   net.Listener
	out  chan *Request
	held atomic.Int64 // connections waiting for a decision now
	// A connection hands its request over holding sending; accept closes
	// out only after done has released every one of them.
	sending sync.RWMutex
	done    chan struct{}
}

// MaxLine bounds the one line a connection sends. A hook payload carries a
// tool call's input, which Claude Code itself keeps far below this.
const MaxLine = 1 << 20

// MaxHeld bounds the connections waiting for a decision at once. Past it a
// request gets no decision, as when Den is not running: Claude Code's own
// dialog answers it. A var for tests.
var MaxHeld int64 = 256

// ErrAlreadyRunning means another Den owns the socket.
var ErrAlreadyRunning = errors.New("another den is already listening")

// Listen binds the socket. A stale socket file with no listener is removed.
// In the fallback directory it makes the directory private first, and
// refuses one that is not. A listener there of another user is an error,
// not a running Den.
func Listen(path string) (*Server, error) {
	if filepath.Dir(path) == FallbackDir() {
		if err := EnsurePrivateDir(FallbackDir()); err != nil {
			return nil, err
		}
	}
	if _, err := os.Lstat(path); err == nil {
		c, err := net.DialTimeout("unix", path, 300*time.Millisecond)
		if err == nil {
			uid, perr := peerUID(c)
			c.Close()
			if perr == nil && uid == os.Getuid() {
				return nil, ErrAlreadyRunning
			}
			return nil, fmt.Errorf("%s is held by another user's process", path)
		}
		os.Remove(path)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	os.Chmod(path, 0o600)
	s := &Server{path: path, ln: ln, out: make(chan *Request, 64), done: make(chan struct{})}
	go s.accept()
	return s, nil
}

// Requests delivers envelopes as they arrive.
func (s *Server) Requests() <-chan *Request { return s.out }

// Close stops listening and removes the socket file.
func (s *Server) Close() error {
	err := s.ln.Close()
	os.Remove(s.path)
	return err
}

func (s *Server) accept() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			close(s.done)
			s.sending.Lock()
			close(s.out)
			s.sending.Unlock()
			return
		}
		go s.serve(conn)
	}
}

func (s *Server) serve(conn net.Conn) {
	defer conn.Close()
	// The socket is 0600, but a connection is also checked: only this
	// user's processes may hand Den an event, a close or an install.
	if uid, err := peerUID(conn); err != nil || uid != os.Getuid() {
		return
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReader(io.LimitReader(conn, MaxLine+1)).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return
	}
	if len(line) > MaxLine {
		return // too long to be a hook's: no decision
	}
	var env Envelope
	if err := json.Unmarshal(line, &env); err != nil {
		fmt.Fprintf(conn, `{"error":%q}`+"\n", "bad envelope: "+err.Error())
		return
	}
	req := &Request{Envelope: env}
	_ = json.Unmarshal(env.Payload, &req.Input)
	if env.Event == "PermissionRequest" || env.Event == CloseEvent || env.Event == SkillsEvent || env.Event == InstallEvent || env.Event == SendEvent || env.Event == InboundEvent || env.Event == MissionEvent {
		if s.held.Add(1) > MaxHeld {
			s.held.Add(-1)
			return // too many waiting: no decision, fail open
		}
		defer s.held.Add(-1)
		req.reply = make(chan Decision, 1)
		req.gone = make(chan struct{})
	}
	if !s.hand(req) {
		return // Den is closing
	}
	if req.reply == nil {
		conn.Write([]byte("{\"ok\":true}\n"))
		return
	}
	conn.SetReadDeadline(time.Time{})
	// The hook writes nothing more, so a read returns only when it hangs up.
	go func() {
		var b [1]byte
		conn.Read(b[:])
		req.hangUp()
	}()
	// Hold the connection until Den decides. If the hook goes away first
	// (its own timeout), the write fails and the decision is dropped.
	d := <-req.reply
	b, _ := json.Marshal(d)
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	conn.Write(append(b, '\n'))
}

// hand passes req to Den, or reports false when the server is closing.
func (s *Server) hand(req *Request) bool {
	s.sending.RLock()
	defer s.sending.RUnlock()
	select {
	case <-s.done:
		return false
	default:
	}
	select {
	case s.out <- req:
		return true
	case <-s.done:
		return false
	}
}

// TestRequest builds a request that wants a reply, for tests of whoever
// answers; Decided reads the reply without blocking.
func TestRequest(env Envelope) *Request {
	req := &Request{Envelope: env, reply: make(chan Decision, 1), gone: make(chan struct{})}
	_ = json.Unmarshal(env.Payload, &req.Input)
	return req
}

// TestHangUp makes a TestRequest's hook hang up.
func TestHangUp(r *Request) { r.hangUp() }

// Decided returns the reply if one was sent.
func (r *Request) Decided() (Decision, bool) {
	select {
	case d := <-r.reply:
		r.reply <- d
		return d, true
	default:
		return Decision{}, false
	}
}
