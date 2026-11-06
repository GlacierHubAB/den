// Package wlsec makes a mission's Wayland listener: a socket whose clients
// the compositor treats as sandboxed (wp_security_context_v1), so the apps
// a held task opens see only the ordinary globals.
//
// It speaks the Wayland wire itself, with the probe client's code.
// Every request goes through send, the one function that writes to the
// socket. send looks up (interface, opcode) in allowed, a fixed literal,
// and panics without writing when the pair isn't there. An object's
// interface is recorded when this package creates it, never taken from
// the compositor, and the only global it binds is the security-context
// manager.
package wlsec

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

var le = binary.LittleEndian

// request is one request a client may send: an interface and an opcode.
type request struct {
	iface string
	op    uint16
}

// allowed is every request this package can send, and the whole of it.
var allowed = map[request]string{
	{"wl_display", 0}:                     "sync",
	{"wl_display", 1}:                     "get_registry",
	{"wl_registry", 0}:                    "bind",
	{"wp_security_context_manager_v1", 1}: "create_listener",
	{"wp_security_context_v1", 1}:         "set_sandbox_engine",
	{"wp_security_context_v1", 2}:         "set_app_id",
	{"wp_security_context_v1", 3}:         "set_instance_id",
	{"wp_security_context_v1", 4}:         "commit",
}

// wire, when set (only by this package's real test), gets one SEND line
// per request, written just before it goes out: the run's audit.
var wire io.Writer

// manager is the one global bound.
const manager = "wp_security_context_manager_v1"

// Engine is the sandbox engine the compositor is told.
const Engine = "den"

type conn struct {
	c     *net.UnixConn
	next  uint32
	ifc   map[uint32]string // each object's interface, set when this package creates it
	buf   []byte
	until time.Time
}

func newConn(c *net.UnixConn, until time.Time) *conn {
	return &conn{c: c, next: 2, ifc: map[uint32]string{1: "wl_display"}, until: until}
}

func (w *conn) newID(iface string) uint32 {
	id := w.next
	w.next++
	w.ifc[id] = iface
	return id
}

func str(s string) []byte {
	n := len(s) + 1
	b := make([]byte, 4+(n+3)&^3)
	le.PutUint32(b, uint32(n))
	copy(b[4:], s)
	return b
}

func u32(v uint32) []byte { b := make([]byte, 4); le.PutUint32(b, v); return b }

// send writes one request, after checking it against allowed. A request
// that isn't there is a bug in this package: it panics before a byte goes
// out.
func (w *conn) send(obj uint32, op uint16, fds []int, args ...[]byte) error {
	iface, ok := w.ifc[obj]
	if !ok {
		panic(fmt.Sprintf("wlsec: request on unknown object %d", obj))
	}
	if _, ok := allowed[request{iface, op}]; !ok {
		panic(fmt.Sprintf("wlsec: request %s op %d is not allowed", iface, op))
	}
	var body []byte
	for _, a := range args {
		body = append(body, a...)
	}
	msg := make([]byte, 8+len(body))
	le.PutUint32(msg, obj)
	le.PutUint32(msg[4:], uint32(len(msg))<<16|uint32(op))
	copy(msg[8:], body)
	var oob []byte
	if len(fds) > 0 {
		oob = syscall.UnixRights(fds...)
	}
	if wire != nil {
		fmt.Fprintf(wire, "SEND obj=%d iface=%s op=%d len=%d fds=%d\n", obj, iface, op, len(msg), len(fds))
	}
	w.c.SetWriteDeadline(w.until)
	_, _, err := w.c.WriteMsgUnix(msg, oob, nil)
	return err
}

type event struct {
	obj  uint32
	op   uint16
	body []byte
}

func (w *conn) read() (event, error) {
	for {
		if len(w.buf) >= 8 {
			size := int(le.Uint32(w.buf[4:]) >> 16)
			if size < 8 {
				return event{}, errors.New("malformed event")
			}
			if len(w.buf) >= size {
				e := event{obj: le.Uint32(w.buf), op: uint16(le.Uint32(w.buf[4:])), body: append([]byte(nil), w.buf[8:size]...)}
				w.buf = w.buf[size:]
				return e, nil
			}
		}
		b := make([]byte, 65536)
		oob := make([]byte, 1024)
		w.c.SetReadDeadline(w.until)
		n, oobn, _, _, err := w.c.ReadMsgUnix(b, oob)
		if err != nil {
			return event{}, err
		}
		closeFDs(oob[:oobn]) // no event read here carries an fd Den wants
		if n == 0 {
			return event{}, errors.New("connection closed")
		}
		w.buf = append(w.buf, b[:n]...)
	}
}

func closeFDs(oob []byte) {
	msgs, err := syscall.ParseSocketControlMessage(oob)
	if err != nil {
		return
	}
	for _, m := range msgs {
		if fds, err := syscall.ParseUnixRights(&m); err == nil {
			for _, fd := range fds {
				syscall.Close(fd)
			}
		}
	}
}

func getStr(b []byte) (string, []byte, bool) {
	if len(b) < 4 {
		return "", nil, false
	}
	n := int(le.Uint32(b))
	pad := (n + 3) &^ 3
	if n < 1 || len(b) < 4+pad {
		return "", nil, false
	}
	return string(b[4 : 4+n-1]), b[4+pad:], true
}

type global struct {
	name    uint32
	iface   string
	version uint32
}

// errRefused is a wl_display.error: the compositor refused a request.
type errRefused struct{ msg string }

func (e errRefused) Error() string { return e.msg }

// roundtrip sends a sync and reads until its done, collecting the
// registry's globals when reg is set.
func (w *conn) roundtrip(reg uint32, globals map[string]global) error {
	cb := w.newID("wl_callback")
	if err := w.send(1, 0, nil, u32(cb)); err != nil {
		return err
	}
	for {
		e, err := w.read()
		if err != nil {
			return err
		}
		switch {
		case e.obj == 1 && e.op == 0 && len(e.body) >= 8: // wl_display.error
			obj, code := le.Uint32(e.body), le.Uint32(e.body[4:])
			msg, _, _ := getStr(e.body[8:])
			return errRefused{fmt.Sprintf("protocol error on object %d (%s), code %d: %s", obj, w.ifc[obj], code, msg)}
		case reg != 0 && e.obj == reg && e.op == 0 && globals != nil && len(e.body) >= 4: // wl_registry.global
			name := le.Uint32(e.body)
			iface, rest, ok := getStr(e.body[4:])
			if ok && len(rest) >= 4 {
				globals[iface] = global{name, iface, le.Uint32(rest)}
			}
		case e.obj == cb && e.op == 0: // wl_callback.done
			return nil
		}
	}
}

// bindManager binds the security-context manager at version 1; no other
// global is ever bound.
func (w *conn) bindManager(reg uint32, g global) (uint32, error) {
	if g.iface != manager {
		panic("wlsec: bind of " + g.iface)
	}
	id := w.newID(g.iface)
	return id, w.send(reg, 0, nil, u32(g.name), str(g.iface), u32(1), u32(id))
}

// Listener is a mission's listening socket, which the compositor serves
// as sandboxed for as long as Den holds the close fd's write end.
type Listener struct {
	Path  string // the socket, for the sandbox to bind at WAYLAND_DISPLAY
	dir   string
	close *os.File // the write end; closing it ends the context
}

// Open makes a listener in dir, which must not exist: Den makes it 0700.
// way is the compositor's own socket (Den's side), appID and instance name
// the mission. It takes at most bound.
func Open(way, dir, appID, instance string, bound time.Duration) (l *Listener, err error) {
	until := time.Now().Add(bound)
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: way, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("the compositor's socket: %v", err)
	}
	defer c.Close()
	w := newConn(c, until)
	reg := w.newID("wl_registry")
	if err := w.send(1, 1, nil, u32(reg)); err != nil {
		return nil, err
	}
	g := map[string]global{}
	if err := w.roundtrip(reg, g); err != nil {
		return nil, err
	}
	mgr, ok := g[manager]
	if !ok {
		return nil, errors.New("the compositor offers no " + manager)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "wayland")
	defer func() {
		if err != nil {
			os.Remove(path)
			os.Remove(dir)
		}
	}()
	lfd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer syscall.Close(lfd) // the compositor holds its own copy once sent
	if err := syscall.Bind(lfd, &syscall.SockaddrUnix{Name: path}); err != nil {
		return nil, err
	}
	if err := syscall.Listen(lfd, 16); err != nil {
		return nil, err
	}
	var pipe [2]int
	if err := syscall.Pipe2(pipe[:], syscall.O_CLOEXEC); err != nil {
		return nil, err
	}
	defer syscall.Close(pipe[0]) // the compositor's end, sent
	wend := os.NewFile(uintptr(pipe[1]), "wlsec-close")
	defer func() {
		if err != nil {
			wend.Close()
		}
	}()
	mid, err := w.bindManager(reg, mgr)
	if err != nil {
		return nil, err
	}
	ctx := w.newID("wp_security_context_v1")
	if err := w.send(mid, 1, []int{lfd, pipe[0]}, u32(ctx)); err != nil {
		return nil, err
	}
	for _, step := range []struct {
		op  uint16
		arg string
	}{{1, Engine}, {2, appID}, {3, instance}} {
		if err := w.send(ctx, step.op, nil, str(step.arg)); err != nil {
			return nil, err
		}
	}
	if err := w.send(ctx, 4, nil); err != nil {
		return nil, err
	}
	if err := w.roundtrip(0, nil); err != nil {
		return nil, fmt.Errorf("commit: %v", err)
	}
	return &Listener{Path: path, dir: dir, close: wend}, nil
}

// Close ends the context: the compositor stops listening, so no new
// client connects. Clients already connected stay sandboxed; the caller
// stops them first.
func (l *Listener) Close() error {
	if l == nil || l.close == nil {
		return nil
	}
	err := l.close.Close()
	l.close = nil
	os.Remove(l.Path)
	os.Remove(l.dir)
	return err
}

// Socket is the listening socket's path.
func (l *Listener) Socket() string { return l.Path }

// Live reports whether Den still holds the context open.
func (l *Listener) Live() bool { return l != nil && l.close != nil }

// closeFD is the write end's fd, for tests.
func (l *Listener) closeFD() int { return int(l.close.Fd()) }
