package sysd

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeManager serves the part of the systemd user manager Den reads, on
// a private socket under a test XDG_RUNTIME_DIR: GetUnit and the
// properties of one unit and of the manager, with the wire code Den reads
// it with. Tests change props (by name) or raw (a reply to send as is).
type fakeManager struct {
	t     *testing.T
	ln    net.Listener
	unit  string
	mu    sync.Mutex
	props map[string]Variant // the unit's, by name
	env   []string           // the manager's own environment
	raw   []byte             // when set, sent in place of any reply
	auth  string             // the authentication answer; "" for OK
}

func newFakeManager(t *testing.T, unit string) *fakeManager {
	t.Helper()
	run := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", run)
	os.MkdirAll(filepath.Join(run, "systemd"), 0o700)
	ln, err := net.Listen("unix", filepath.Join(run, "systemd", "private"))
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeManager{t: t, ln: ln, unit: unit, props: map[string]Variant{}}
	t.Cleanup(func() { ln.Close() })
	go f.serve()
	return f
}

func (f *fakeManager) socket() string { return f.ln.Addr().String() }

func (f *fakeManager) set(name string, sig Signature, v any) {
	f.mu.Lock()
	f.props[name] = Variant{Sig: sig, Value: v}
	f.mu.Unlock()
}

func (f *fakeManager) serve() {
	for {
		nc, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.handle(nc)
	}
}

func (f *fakeManager) handle(nc net.Conn) {
	defer nc.Close()
	r := bufio.NewReader(nc)
	line, err := r.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "\x00AUTH EXTERNAL ") {
		return
	}
	f.mu.Lock()
	ans := f.auth
	f.mu.Unlock()
	if ans == "" {
		ans = "OK 0123456789abcdef0123456789abcdef\r\n"
	}
	nc.Write([]byte(ans))
	if line, err := r.ReadString('\n'); err != nil || line != "BEGIN\r\n" {
		return
	}
	for {
		m, err := readMessage(r)
		if err != nil {
			return
		}
		f.mu.Lock()
		raw := f.raw
		f.mu.Unlock()
		if raw != nil {
			nc.Write(raw)
			continue
		}
		reply := f.answer(m)
		b, err := encodeMessage(reply)
		if err != nil {
			f.t.Errorf("fake manager: %v", err)
			return
		}
		nc.Write(b)
	}
}

func (f *fakeManager) answer(m *message) *message {
	ret := func(sig Signature, body ...any) *message {
		return &message{typ: msgReturn, serial: m.serial + 1000, sig: sig, body: body, fields: map[byte]Variant{fieldReplySerial: {Sig: "u", Value: m.serial}}}
	}
	fail := func(name string) *message {
		return &message{typ: msgError, serial: m.serial + 1000, sig: "s", body: []any{"no"}, fields: map[byte]Variant{fieldReplySerial: {Sig: "u", Value: m.serial}, fieldErrorName: {Sig: "s", Value: name}}}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch m.str(fieldMember) {
	case "GetUnit":
		if len(m.body) == 1 && m.body[0] == f.unit {
			return ret("o", ObjectPath("/org/freedesktop/systemd1/unit/drey_2dhost_2eservice"))
		}
		return fail("org.freedesktop.systemd1.NoSuchUnit")
	case "Get":
		if len(m.body) != 2 {
			return fail("org.freedesktop.DBus.Error.InvalidArgs")
		}
		iface, _ := m.body[0].(string)
		name, _ := m.body[1].(string)
		if iface == managerIface && name == "Environment" {
			return ret("v", Variant{Sig: "as", Value: append([]string{}, f.env...)})
		}
		if v, ok := f.props[name]; ok {
			return ret("v", v)
		}
		return fail("org.freedesktop.DBus.Error.UnknownProperty")
	}
	return fail("org.freedesktop.DBus.Error.UnknownMethod")
}
