package sysd

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Peer is the process at the other end of the manager's socket, as the
// kernel says (SO_PEERCRED).
type Peer struct {
	PID int
	UID int
}

// conn is a connection to the manager: one call at a time, every one
// bounded by the connection's deadline.
type conn struct {
	nc     net.Conn
	r      *bufio.Reader
	serial uint32
	peer   Peer
}

// CallTimeout bounds a whole read of the manager: the dial, the
// authentication and every call on the connection.
var CallTimeout = 3 * time.Second

// managerSocket is the user manager's private socket.
func managerSocket() (string, error) {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" || !filepath.IsAbs(dir) {
		return "", errors.New("XDG_RUNTIME_DIR is not set")
	}
	return filepath.Join(dir, "systemd", "private"), nil
}

// dial connects to the manager's private socket, reads who is at the
// other end, and authenticates as the user (EXTERNAL). The deadline set
// here holds for every call on the connection.
func dial(path string) (*conn, error) {
	nc, err := net.DialTimeout("unix", path, CallTimeout)
	if err != nil {
		return nil, err
	}
	nc.SetDeadline(time.Now().Add(CallTimeout))
	peer, err := peerOf(nc)
	if err != nil {
		nc.Close()
		return nil, err
	}
	c := &conn{nc: nc, r: bufio.NewReaderSize(nc, 4096), peer: peer}
	if err := c.auth(); err != nil {
		nc.Close()
		return nil, err
	}
	return c, nil
}

func (c *conn) close() { c.nc.Close() }

// peerOf reads SO_PEERCRED.
func peerOf(nc net.Conn) (Peer, error) {
	uc, ok := nc.(*net.UnixConn)
	if !ok {
		return Peer{}, errors.New("not a unix socket")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return Peer{}, err
	}
	var cred *syscall.Ucred
	var cerr error
	if err := raw.Control(func(fd uintptr) {
		cred, cerr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return Peer{}, err
	}
	if cerr != nil {
		return Peer{}, cerr
	}
	return Peer{PID: int(cred.Pid), UID: int(cred.Uid)}, nil
}

// maxAuthLine caps a line of the authentication exchange.
const maxAuthLine = 512

// auth is the EXTERNAL authentication: the uid, in hex of its decimal
// digits, then BEGIN once the server says OK.
func (c *conn) auth() error {
	uid := strconv.Itoa(os.Getuid())
	hex := fmt.Sprintf("%x", uid)
	if _, err := c.nc.Write([]byte("\x00AUTH EXTERNAL " + hex + "\r\n")); err != nil {
		return err
	}
	line, err := c.authLine()
	if err != nil {
		return err
	}
	if !strings.HasPrefix(line, "OK ") {
		return errors.New("the manager refused the authentication")
	}
	_, err = c.nc.Write([]byte("BEGIN\r\n"))
	return err
}

func (c *conn) authLine() (string, error) {
	var b []byte
	for len(b) < maxAuthLine {
		ch, err := c.r.ReadByte()
		if err != nil {
			return "", err
		}
		if ch == '\n' {
			return strings.TrimSuffix(string(b), "\r"), nil
		}
		b = append(b, ch)
	}
	return "", errors.New("authentication line too long")
}

// maxSkipped caps the messages a call skips while it waits for its reply.
const maxSkipped = 16

// CallError is an error the manager answered with.
type CallError struct {
	Name    string
	Message string
}

func (e *CallError) Error() string { return e.Name + ": " + e.Message }

// call makes one method call and returns the reply's body, whose
// signature must be want exactly.
func (c *conn) call(path ObjectPath, iface, member, sig string, want Signature, args ...any) ([]any, error) {
	c.serial++
	m := &message{typ: msgCall, serial: c.serial, sig: Signature(sig), body: args, fields: map[byte]Variant{
		fieldPath:      {Sig: "o", Value: path},
		fieldInterface: {Sig: "s", Value: iface},
		fieldMember:    {Sig: "s", Value: member},
	}}
	raw, err := encodeMessage(m)
	if err != nil {
		return nil, err
	}
	if _, err := c.nc.Write(raw); err != nil {
		return nil, err
	}
	for range maxSkipped {
		r, err := readMessage(c.r)
		if err != nil {
			return nil, err
		}
		if (r.typ != msgReturn && r.typ != msgError) || r.replySerial() != c.serial {
			continue // a signal, or not ours
		}
		if r.typ == msgError {
			e := &CallError{Name: r.str(fieldErrorName)}
			if len(r.body) > 0 {
				e.Message, _ = r.body[0].(string)
			}
			return nil, e
		}
		if r.sig != want {
			return nil, fmt.Errorf("%s answered %q, not %q", member, r.sig, want)
		}
		return r.body, nil
	}
	return nil, errors.New("no reply among the manager's messages")
}

// The manager's names.
const managerPath ObjectPath = "/org/freedesktop/systemd1"

const (
	managerIface = "org.freedesktop.systemd1.Manager"
	unitIface    = "org.freedesktop.systemd1.Unit"
	serviceIface = "org.freedesktop.systemd1.Service"
	propsIface   = "org.freedesktop.DBus.Properties"
)

// getUnit is the unit's object path.
func (c *conn) getUnit(name string) (ObjectPath, error) {
	body, err := c.call(managerPath, managerIface, "GetUnit", "s", "o", name)
	if err != nil {
		return "", err
	}
	return body[0].(ObjectPath), nil
}

// get is one property, whose type must be want exactly.
func (c *conn) get(path ObjectPath, iface, name string, want Signature) (any, error) {
	body, err := c.call(path, propsIface, "Get", "ss", "v", iface, name)
	if err != nil {
		return nil, err
	}
	v := body[0].(Variant)
	if v.Sig != want {
		return nil, fmt.Errorf("property %s is %q, not %q", name, v.Sig, want)
	}
	return v.Value, nil
}
