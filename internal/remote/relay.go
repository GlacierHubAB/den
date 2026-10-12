package remote

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// Limits of the relay on the host.
const (
	// MaxHookLine caps the envelope a hook sends to den-remote.sock. A
	// larger request (a Write of a big file) is not relayed: its hook gets
	// no decision and the terminal asks.
	MaxHookLine = 1 << 20
	// MaxHeld caps how many requests one serve holds at once; more fail
	// open at once.
	MaxHeld = 32
)

// claimEvent is the envelope event a starting serve sends to the socket of
// a serve that already holds it, to ask who it is or, from the same Den, to
// take its held requests over. Hooks never send it.
const claimEvent = "DenRemoteServe"

// claim is the payload of a claimEvent.
type claim struct {
	Controller string `json:"controller"`
	Pid        int    `json:"pid"`
}

// claimReply is a holding serve's answer: who holds the socket, or, before
// the descriptors it passed, the requests it hands over.
type claimReply struct {
	Owner    *Attached `json:"owner,omitempty"`
	Handover *handover `json:"handover,omitempty"`
}

type handover struct {
	Pid        int           `json:"pid"`
	Controller string        `json:"controller"`
	Held       []handoverReq `json:"held"`
}

// handoverReq is one held request passed to the next serve: its id and the
// hook's envelope as it came (it stays on the host).
type handoverReq struct {
	ID       string          `json:"id"`
	Envelope bridge.Envelope `json:"envelope"`
}

// relay is serve's half of answering held requests. It owns
// den-remote.sock on the host: hooks dial it when no Den runs there, it
// sends each held PermissionRequest up the pipe with a random id and the
// filtered body, and it writes the human's decision back to that hook only
// when the id is open, was offered on this connection, and the body hash
// matches. Nothing it relays is a command: a hook gets a decision, or
// nothing.
type relay struct {
	path       string
	controller string
	questions  bool // the host's hooks.answer_questions: relay AskUserQuestion
	since      time.Time
	up         func(v any) error // writes one message up the pipe
	log        func(string)

	mu      sync.Mutex
	ln      *net.UnixListener
	held    map[string]*held
	order   []string // ids in arrival order, for offering again
	started bool     // hello went up: held requests may follow
	handed  bool     // handed over to a newer serve of the same Den: answer nothing
	closed  bool
	done    chan struct{} // closed once handed over
}

type held struct {
	id      string
	conn    *net.UnixConn
	env     bridge.Envelope
	in      bridge.HookInput
	body    json.RawMessage
	hash    string
	keys    map[string]string // AskUserQuestion: question as shown → as asked
	offered bool              // sent up on this serve's connection
}

// openRelay takes den-remote.sock at path for controller. A live serve
// already there is asked who it is: another Den's serve leaves this one
// read-only (attached names it); the same Den's serve, half-open after its
// link died, hands its listener and held hooks over, and they are offered
// again with the same ids once start runs. A stale file is removed.
func openRelay(path, controller string, questions bool, up func(any) error, log func(string)) (*relay, *Attached, error) {
	r := &relay{path: path, controller: controller, questions: questions, since: time.Now(), up: up, log: log,
		held: map[string]*held{}, done: make(chan struct{})}
	if _, err := os.Lstat(path); err == nil {
		c, err := net.DialTimeout("unix", path, 300*time.Millisecond)
		if err == nil {
			att, ok := r.claimFrom(c.(*net.UnixConn))
			if att != nil {
				return nil, att, nil
			}
			if ok {
				go r.accept()
				return r, nil, nil
			}
			// The handover failed: its hooks were let go; start clean.
		}
		os.Remove(path)
	}
	old := syscall.Umask(0o177)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	syscall.Umask(old)
	if err != nil {
		return nil, nil, err
	}
	os.Chmod(path, 0o600)
	ln.SetUnlinkOnClose(false) // close removes it, and only while it is still this serve's
	r.ln = ln
	go r.accept()
	return r, nil, nil
}

// start offers what is held (handed over, or arrived before hello) and
// lets later requests go up as they come.
func (r *relay) start() {
	r.mu.Lock()
	r.started = true
	var offer []*held
	for _, id := range r.order {
		if h := r.held[id]; h != nil {
			offer = append(offer, h)
		}
	}
	r.mu.Unlock()
	for _, h := range offer {
		r.offer(h)
	}
}

// Done is closed when the relay handed over to a newer serve.
func (r *relay) Done() <-chan struct{} { return r.done }

// close lets every held request go to its terminal (its hook sees the
// connection close, with no decision) and removes the socket. After a
// handover it does nothing: the hooks and the socket are the next serve's.
func (r *relay) close() {
	r.mu.Lock()
	if r.handed || r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	hs := r.held
	r.held = map[string]*held{}
	ln := r.ln
	r.mu.Unlock()
	if ln != nil {
		ln.Close()
		os.Remove(r.path)
	}
	for _, h := range hs {
		h.conn.Close()
	}
}

func (r *relay) accept() {
	r.mu.Lock()
	ln := r.ln
	r.mu.Unlock()
	for {
		c, err := ln.AcceptUnix()
		if err != nil {
			return
		}
		go r.serveConn(c)
	}
}

// serveConn reads one envelope from a connection to den-remote.sock: a
// held PermissionRequest from `den hook`, or a starting serve's claim.
// Anything else is refused.
func (r *relay) serveConn(c *net.UnixConn) {
	if uid, err := bridge.PeerUID(c); err != nil || uid != os.Getuid() {
		r.log("refused a connection from another user")
		c.Close()
		return
	}
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := newLineReader(c, MaxHookLine).next()
	if err != nil {
		if errors.Is(err, ErrLineTooLong) {
			r.log("let a request over 1 MB go to its terminal")
		}
		c.Close()
		return
	}
	var env bridge.Envelope
	if json.Unmarshal(line, &env) != nil {
		c.Close()
		return
	}
	if env.Event == claimEvent {
		r.claimed(c, env.Payload)
		return
	}
	if env.Event != "PermissionRequest" {
		fmt.Fprintf(c, `{"error":%q}`+"\n", "den remote serve takes permission requests only")
		c.Close()
		r.log("refused a " + sanitize.Label(env.Event, 40) + " envelope")
		return
	}
	c.SetReadDeadline(time.Time{})
	r.hold(c, env, "")
}

// hold keeps a hook's request open and offers it up, or lets it go to its
// terminal at once when it cannot be relayed. id is "" for a new request,
// or the id it had before a handover.
func (r *relay) hold(c *net.UnixConn, env bridge.Envelope, id string) {
	var in bridge.HookInput
	json.Unmarshal(env.Payload, &in)
	if in.HookEventName != "PermissionRequest" || in.SessionID == "" || in.SessionID != env.SessionID || in.AgentID != "" {
		release(c)
		return
	}
	if in.ToolName == "AskUserQuestion" && !r.questions {
		release(c) // the hook here would ignore the answers (hooks.answer_questions is off)
		return
	}
	body, keys, ok := heldBody(in)
	if !ok {
		r.log("let a request go to its terminal: it cannot be shown in full (" + heldSummary(id, in) + ")")
		release(c)
		return
	}
	if id == "" {
		id = newID()
	}
	h := &held{id: id, conn: c, env: env, in: in, body: body, hash: BodyHash(body), keys: keys}
	r.mu.Lock()
	if r.handed || r.closed || len(r.held) >= MaxHeld || r.held[id] != nil {
		r.mu.Unlock()
		release(c)
		return
	}
	r.held[id] = h
	r.order = append(r.order, id)
	started := r.started
	r.mu.Unlock()
	go r.watch(h)
	if started {
		r.offer(h)
	}
}

// offer sends a held request up and marks it offered on this connection.
func (r *relay) offer(h *held) {
	r.mu.Lock()
	if r.held[h.id] != h || r.handed {
		r.mu.Unlock()
		return
	}
	h.offered = true
	r.mu.Unlock()
	msg := Held{Type: TypeHeld, ID: h.id, Session: h.in.SessionID, Tool: h.in.ToolName, Body: h.body}
	if err := r.up(msg); err != nil {
		if ok, _ := r.forget(h); ok {
			release(h.conn)
		}
	}
}

// watch waits for the hook to hang up (it writes nothing more), then
// forgets the request and tells Den it is gone.
func (r *relay) watch(h *held) {
	var b [1]byte
	h.conn.Read(b[:])
	if ok, offered := r.forget(h); ok {
		h.conn.Close()
		if offered {
			r.up(Gone{Type: TypeGone, ID: h.id})
		}
	}
}

// forget drops a held request; ok is false when it was no longer held
// (answered, let go, or handed over). offered says whether Den knew of it.
func (r *relay) forget(h *held) (ok, offered bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.handed || r.held[h.id] != h {
		return false, false
	}
	delete(r.held, h.id)
	for i, id := range r.order {
		if id == h.id {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
	return true, h.offered
}

// decide applies a decision from Den: once, only for a request still open
// and offered on this connection, and only when the hash matches the body
// sent. Anything else is dropped and logged (metadata only).
func (r *relay) decide(d Decision) {
	r.mu.Lock()
	h := r.held[d.ID]
	reason := ""
	switch {
	case r.handed || h == nil:
		reason = "an id that is not open"
	case !h.offered:
		reason = "a request not offered on this connection"
	case d.Hash != h.hash:
		reason = "a body hash that does not match"
	case d.Behavior != "allow" && d.Behavior != "deny" && d.Behavior != "":
		reason = "an unknown behavior"
	case len(d.Answers) > 0 && (d.Behavior != "allow" || h.keys == nil):
		reason = "answers for a request that is not an allowed question"
	}
	if reason != "" {
		r.mu.Unlock()
		r.log("dropped a decision for " + reason + " (" + shortID(d.ID) + ")")
		return
	}
	r.mu.Unlock()
	out := bridge.Decision{Behavior: d.Behavior, Message: sanitize.Label(d.Message, 300)}
	if len(d.Answers) > 0 {
		answers, ok := answersBack(d.Answers, h.keys)
		if !ok {
			r.log("dropped a decision whose answers name no question (" + shortID(d.ID) + ")")
			return
		}
		out.Answers = answers
	}
	if ok, _ := r.forget(h); !ok {
		return // the hook hung up meanwhile
	}
	b, _ := json.Marshal(out)
	h.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	h.conn.Write(append(b, '\n'))
	h.conn.Close()
}

// release ends a hook's wait with no decision, so Claude Code shows its
// own dialog.
func release(c *net.UnixConn) {
	c.SetWriteDeadline(time.Now().Add(time.Second))
	c.Write([]byte("{\"behavior\":\"\"}\n"))
	c.Close()
}

// claimed answers a starting serve. Another Den's serve hears who holds
// the host. The same Den's serve gets everything: the listener and every
// held hook, passed as descriptors, then the requests' ids and envelopes.
// From the moment it decides to hand over, this serve answers nothing
// again; if passing fails, the hooks lose their last holder and fail open.
func (r *relay) claimed(c *net.UnixConn, payload json.RawMessage) {
	defer c.Close()
	var cl claim
	json.Unmarshal(payload, &cl)
	r.mu.Lock()
	if cl.Controller == "" || r.controller == "" || cl.Controller != r.controller || r.handed || r.closed {
		r.mu.Unlock()
		b, _ := json.Marshal(claimReply{Owner: &Attached{Pid: os.Getpid(), Since: r.since.UTC()}})
		c.SetWriteDeadline(time.Now().Add(2 * time.Second))
		c.Write(append(b, '\n'))
		r.log(fmt.Sprintf("another Den's serve (pid %d) asked for this host; it stays read-only", cl.Pid))
		return
	}
	r.handed = true
	var hs []*held
	for _, id := range r.order {
		if h := r.held[id]; h != nil {
			hs = append(hs, h)
		}
	}
	r.held, r.order = map[string]*held{}, nil
	ln := r.ln
	r.mu.Unlock()
	defer close(r.done)

	var files []*os.File
	defer func() {
		for _, f := range files {
			f.Close()
		}
		for _, h := range hs {
			h.conn.Close() // this serve's copies; the next serve holds its own
		}
	}()
	lf, err := ln.File()
	ln.Close() // stops accepting here; the socket lives on in lf
	if err != nil {
		r.log("handover failed: " + err.Error())
		return
	}
	files = append(files, lf)
	ho := handover{Pid: os.Getpid(), Controller: r.controller}
	for _, h := range hs {
		f, err := h.conn.File()
		if err != nil {
			continue // that hook loses its holder and fails open
		}
		files = append(files, f)
		ho.Held = append(ho.Held, handoverReq{ID: h.id, Envelope: h.env})
	}
	fds := make([]int, len(files))
	for i, f := range files {
		fds[i] = int(f.Fd())
	}
	c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, _, err := c.WriteMsgUnix([]byte{'H'}, syscall.UnixRights(fds...), nil); err != nil {
		r.log("handover failed: " + err.Error())
		return
	}
	b, _ := json.Marshal(claimReply{Handover: &ho})
	if _, err := c.Write(append(b, '\n')); err != nil {
		r.log("handover failed: " + err.Error())
		return
	}
	r.log(fmt.Sprintf("handed %d held requests over to a new session of the same Den (pid %d)", len(ho.Held), cl.Pid))
}

// claimFrom asks the serve holding the socket on c. att is set when
// another Den holds it (or something there does not answer as a serve);
// ok is true when the same Den's serve handed over and r now holds its
// listener and requests. Both unset: the handover failed, every descriptor
// that came was closed, so its hooks fail open, and the caller starts
// clean.
func (r *relay) claimFrom(c *net.UnixConn) (att *Attached, ok bool) {
	defer c.Close()
	if uid, err := bridge.PeerUID(c); err != nil || uid != os.Getuid() {
		return &Attached{}, false
	}
	payload, _ := json.Marshal(claim{Controller: r.controller, Pid: os.Getpid()})
	line, _ := json.Marshal(bridge.Envelope{Event: claimEvent, Payload: payload})
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write(append(line, '\n')); err != nil {
		return &Attached{}, false
	}
	first := make([]byte, 1)
	oob := make([]byte, syscall.CmsgSpace(4*(MaxHeld+1)))
	n, oobn, _, _, err := c.ReadMsgUnix(first, oob)
	files := receivedFiles(oob[:oobn])
	failOpen := func(why string) (*Attached, bool) {
		for _, f := range files {
			f.Close()
		}
		r.log("handover failed, its held requests went to their terminals: " + why)
		return nil, false
	}
	if err != nil || n != 1 {
		if len(files) > 0 {
			return failOpen("a short read")
		}
		return &Attached{}, false
	}
	rest := bufio.NewReader(c)
	if first[0] == '{' {
		for _, f := range files {
			f.Close()
		}
		var rep claimReply
		tail, _ := rest.ReadBytes('\n')
		if json.Unmarshal(append(first, tail...), &rep) != nil || rep.Owner == nil {
			return &Attached{}, false
		}
		return rep.Owner, false
	}
	if first[0] != 'H' || len(files) == 0 {
		return failOpen("an unexpected reply")
	}
	lr := newLineReader(rest, MaxHeld*(MaxHookLine+256)+4096)
	tail, err := lr.next()
	var rep claimReply
	if err != nil || json.Unmarshal(tail, &rep) != nil || rep.Handover == nil {
		return failOpen("no list of requests")
	}
	ho := rep.Handover
	if ho.Controller != r.controller || len(ho.Held)+1 != len(files) {
		return failOpen("the list does not match what was passed")
	}
	fl, err := net.FileListener(files[0])
	ln, isUnix := fl.(*net.UnixListener)
	if err != nil || !isUnix || ln.Addr().String() != r.path {
		if fl != nil {
			fl.Close()
		}
		return failOpen("the listener is not " + r.path)
	}
	files[0].Close()
	ln.SetUnlinkOnClose(false)
	r.ln = ln
	conns := files[1:]
	for i, f := range conns {
		fc, err := net.FileConn(f)
		f.Close()
		uc, isUnix := fc.(*net.UnixConn)
		if err != nil || !isUnix {
			if fc != nil {
				fc.Close()
			}
			continue
		}
		if uid, err := bridge.PeerUID(uc); err != nil || uid != os.Getuid() {
			uc.Close()
			continue
		}
		r.hold(uc, ho.Held[i].Envelope, validID(ho.Held[i].ID))
	}
	r.log(fmt.Sprintf("took over %d held requests from the previous session's serve (pid %d)", len(conns), ho.Pid))
	return nil, true
}

// receivedFiles are the descriptors passed in a control message.
func receivedFiles(oob []byte) []*os.File {
	msgs, err := syscall.ParseSocketControlMessage(oob)
	if err != nil {
		return nil
	}
	var out []*os.File
	for _, m := range msgs {
		fds, err := syscall.ParseUnixRights(&m)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			out = append(out, os.NewFile(uintptr(fd), "passed"))
		}
	}
	return out
}

// newID is a request id: 128 random bits in hex.
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand does not fail on Linux
	}
	return hex.EncodeToString(b[:])
}

// validID is id when it has the shape newID gives, else a fresh one.
func validID(id string) string {
	if isID(id) {
		return id
	}
	return newID()
}

// isID reports whether s is 32 lower-case hex digits.
func isID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func shortID(id string) string {
	id = sanitize.Line(id)
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
