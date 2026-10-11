package bridge

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"
)

// Request is one delivered envelope. For PermissionRequest events the handler
// must call Reply exactly once; for every other event Reply is a no-op.
type Request struct {
	Envelope
	Input HookInput
	reply chan Decision
	once  sync.Once
}

// WantsReply reports whether the sender is waiting for a decision.
func (r *Request) WantsReply() bool { return r.reply != nil }

// Reply sends the decision to the waiting hook. Safe to call more than once;
// only the first call counts.
func (r *Request) Reply(d Decision) {
	if r.reply == nil {
		return
	}
	r.once.Do(func() { r.reply <- d })
}

// Server accepts hook connections.
type Server struct {
	path string
	ln   net.Listener
	out  chan *Request
}

// ErrAlreadyRunning means another Den owns the socket.
var ErrAlreadyRunning = errors.New("another den is already listening")

// Listen binds the socket. A stale socket file with no listener is removed.
func Listen(path string) (*Server, error) {
	if _, err := os.Stat(path); err == nil {
		c, err := net.DialTimeout("unix", path, 300*time.Millisecond)
		if err == nil {
			c.Close()
			return nil, ErrAlreadyRunning
		}
		os.Remove(path)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	os.Chmod(path, 0o600)
	s := &Server{path: path, ln: ln, out: make(chan *Request, 64)}
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
			close(s.out)
			return
		}
		go s.serve(conn)
	}
}

func (s *Server) serve(conn net.Conn) {
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return
	}
	var env Envelope
	if err := json.Unmarshal(line, &env); err != nil {
		fmt.Fprintf(conn, `{"error":%q}`+"\n", "bad envelope: "+err.Error())
		return
	}
	req := &Request{Envelope: env}
	_ = json.Unmarshal(env.Payload, &req.Input)
	if env.Event == "PermissionRequest" || env.Event == CloseEvent || env.Event == SkillsEvent {
		req.reply = make(chan Decision, 1)
	}
	s.out <- req
	if req.reply == nil {
		conn.Write([]byte("{\"ok\":true}\n"))
		return
	}
	conn.SetReadDeadline(time.Time{})
	// Hold the connection until Den decides. If the hook goes away first
	// (its own timeout), the write fails and the decision is dropped.
	d := <-req.reply
	b, _ := json.Marshal(d)
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	conn.Write(append(b, '\n'))
}

// TestRequest builds a request that wants a reply, for tests of whoever
// answers; Decided reads the reply without blocking.
func TestRequest(env Envelope) *Request {
	req := &Request{Envelope: env, reply: make(chan Decision, 1)}
	_ = json.Unmarshal(env.Payload, &req.Input)
	return req
}

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
