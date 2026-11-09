package wlsec

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// pair is a conn on one end of a socketpair, and the other end to read
// what it wrote. No compositor is involved.
func pair(t *testing.T) (*conn, *net.UnixConn) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	mk := func(fd int, name string) *net.UnixConn {
		f := os.NewFile(uintptr(fd), name)
		defer f.Close()
		c, err := net.FileConn(f)
		if err != nil {
			t.Fatal(err)
		}
		return c.(*net.UnixConn)
	}
	a, b := mk(fds[0], "den"), mk(fds[1], "peer")
	t.Cleanup(func() { a.Close(); b.Close() })
	return newConn(a, time.Now().Add(5*time.Second)), b
}

func received(t *testing.T, peer *net.UnixConn) int {
	t.Helper()
	peer.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	buf := make([]byte, 4096)
	n, err := peer.Read(buf)
	if err != nil {
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			return 0
		}
		t.Fatalf("peer read: %v", err)
	}
	return n
}

func refuses(t *testing.T, iface string, op uint16) {
	t.Helper()
	w, peer := pair(t)
	id := w.newID(iface)
	func() {
		defer func() {
			if recover() == nil {
				t.Errorf("%s op %d: sent, want a panic", iface, op)
			}
		}()
		w.send(id, op, nil, u32(7), u32(8))
	}()
	if n := received(t, peer); n != 0 {
		t.Errorf("%s op %d: %d bytes reached the peer", iface, op, n)
	}
}

func TestInputRequestsAreRefused(t *testing.T) {
	refuses(t, "zwp_virtual_keyboard_manager_v1", 0)
	for op := uint16(0); op <= 2; op++ {
		refuses(t, "zwlr_virtual_pointer_manager_v1", op)
	}
	refuses(t, "wl_compositor", 0)
	refuses(t, "wl_seat", 0)
	refuses(t, manager, 0)
	refuses(t, manager, 2)
	refuses(t, "wp_security_context_v1", 0)
	for op := uint16(0); op <= 9; op++ {
		refuses(t, "zwp_virtual_keyboard_v1", op)
		refuses(t, "zwlr_virtual_pointer_v1", op)
	}
}

func TestAllowedIsExactly(t *testing.T) {
	want := map[request]string{
		{"wl_display", 0}:                     "sync",
		{"wl_display", 1}:                     "get_registry",
		{"wl_registry", 0}:                    "bind",
		{"wp_security_context_manager_v1", 1}: "create_listener",
		{"wp_security_context_v1", 1}:         "set_sandbox_engine",
		{"wp_security_context_v1", 2}:         "set_app_id",
		{"wp_security_context_v1", 3}:         "set_instance_id",
		{"wp_security_context_v1", 4}:         "commit",
	}
	if len(allowed) != len(want) {
		t.Fatalf("allowed has %d rows, want %d", len(allowed), len(want))
	}
	for k, v := range want {
		if allowed[k] != v {
			t.Errorf("allowed[%v] = %q, want %q", k, allowed[k], v)
		}
	}
}

// Only the security-context manager is ever bound.
func TestOnlyTheManagerIsBound(t *testing.T) {
	for _, iface := range []string{"zwp_virtual_keyboard_manager_v1", "zwlr_virtual_pointer_manager_v1", "wl_seat", "wl_compositor"} {
		w, peer := pair(t)
		reg := w.newID("wl_registry")
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("bound %s", iface)
				}
			}()
			w.bindManager(reg, global{name: 3, iface: iface, version: 1})
		}()
		if n := received(t, peer); n != 0 {
			t.Errorf("%s: %d bytes reached the peer", iface, n)
		}
	}
}

// fakeCompositor serves one client on a socket: it offers the globals
// named, answers every sync, and records each request and the fds that
// came with create_listener.
type fakeCompositor struct {
	path   string
	reqs   chan string
	fds    chan []int
	offers []string
}

func newFake(t *testing.T, offers ...string) *fakeCompositor {
	t.Helper()
	dir := t.TempDir()
	f := &fakeCompositor{path: filepath.Join(dir, "wayland-9"), reqs: make(chan string, 64), fds: make(chan []int, 1), offers: offers}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: f.path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go f.serve(ln)
	return f
}

func (f *fakeCompositor) serve(ln *net.UnixListener) {
	c, err := ln.AcceptUnix()
	if err != nil {
		return
	}
	defer c.Close()
	defer close(f.reqs)
	ifc := map[uint32]string{1: "wl_display"}
	var buf []byte
	for {
		b := make([]byte, 4096)
		oob := make([]byte, 256)
		n, oobn, _, _, err := c.ReadMsgUnix(b, oob)
		if err != nil || n == 0 {
			return
		}
		if oobn > 0 {
			msgs, _ := syscall.ParseSocketControlMessage(oob[:oobn])
			for _, m := range msgs {
				if fds, err := syscall.ParseUnixRights(&m); err == nil {
					f.fds <- fds
				}
			}
		}
		buf = append(buf, b[:n]...)
		for len(buf) >= 8 {
			size := int(le.Uint32(buf[4:]) >> 16)
			if len(buf) < size {
				break
			}
			obj, op, body := le.Uint32(buf), uint16(le.Uint32(buf[4:])), buf[8:size]
			buf = buf[size:]
			iface := ifc[obj]
			line := iface + "." + strconv.Itoa(int(op))
			switch {
			case iface == "wl_display" && op == 1: // get_registry
				reg := le.Uint32(body)
				ifc[reg] = "wl_registry"
				for i, name := range f.offers {
					ev := append(u32(uint32(10+i)), str(name)...)
					ev = append(ev, u32(1)...)
					c.Write(msg(reg, 0, ev))
				}
			case iface == "wl_display" && op == 0: // sync
				c.Write(msg(le.Uint32(body), 0, u32(0)))
			case iface == "wl_registry" && op == 0: // bind
				name, rest, _ := getStr(body[4:])
				ifc[le.Uint32(rest[4:])] = name
				line += " " + name
			case iface == manager && op == 1:
				ifc[le.Uint32(body)] = "wp_security_context_v1"
			case iface == "wp_security_context_v1" && op <= 3:
				s, _, _ := getStr(body)
				line += " " + s
			}
			f.reqs <- line
		}
	}
}

func msg(obj uint32, op uint16, body []byte) []byte {
	b := make([]byte, 8+len(body))
	le.PutUint32(b, obj)
	le.PutUint32(b[4:], uint32(len(b))<<16|uint32(op))
	copy(b[8:], body)
	return b
}

// The listener is made in Den's order, the compositor gets the listening
// socket and the pipe's read end, and Den's close ends the context.
func TestOpenOrderAndClose(t *testing.T) {
	f := newFake(t, "wl_compositor", manager, "zwp_virtual_keyboard_manager_v1")
	dir := filepath.Join(t.TempDir(), "listen")
	l, err := Open(f.path, dir, "io.glacierhub.den.desktop", "m1", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for r := range f.reqs {
		got = append(got, r)
	}
	want := []string{"wl_display.1", "wl_display.0", "wl_registry.0 " + manager, manager + ".1",
		"wp_security_context_v1.1 den", "wp_security_context_v1.2 io.glacierhub.den.desktop", "wp_security_context_v1.3 m1",
		"wp_security_context_v1.4", "wl_display.0"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("requests\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	fds := <-f.fds
	if len(fds) != 2 {
		t.Fatalf("create_listener carried %d fds", len(fds))
	}
	defer syscall.Close(fds[0])
	defer syscall.Close(fds[1])
	if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("listener dir: %v %v", st, err)
	}
	// [S4-4] The write end is close-on-exec: no launch inherits it.
	flags, _, e := syscall.Syscall(syscall.SYS_FCNTL, uintptr(l.closeFD()), syscall.F_GETFD, 0)
	if e != 0 || flags&syscall.FD_CLOEXEC == 0 {
		t.Fatalf("the close fd's flags %x (%v)", flags, e)
	}
	// The read end the compositor got: nothing yet, then a hangup.
	if n, _ := syscall.Read(setNonblock(t, fds[1]), make([]byte, 1)); n > 0 {
		t.Fatal("data on the close fd")
	}
	if !l.Live() {
		t.Fatal("not live")
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	syscall.SetNonblock(fds[1], false)
	if n, err := syscall.Read(fds[1], make([]byte, 1)); n != 0 || err != nil {
		t.Fatalf("after Close the read end gives %d, %v; want end of file", n, err)
	}
	if l.Live() {
		t.Fatal("live after Close")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("the listener dir outlived Close")
	}
}

func setNonblock(t *testing.T, fd int) int {
	t.Helper()
	if err := syscall.SetNonblock(fd, true); err != nil {
		t.Fatal(err)
	}
	return fd
}

// No manager offered: no listener, no folder, and the launch is refused
// by the caller.
func TestOpenNeedsTheManager(t *testing.T) {
	f := newFake(t, "wl_compositor", "zwp_virtual_keyboard_manager_v1")
	dir := filepath.Join(t.TempDir(), "listen")
	if _, err := Open(f.path, dir, "a", "m1", 2*time.Second); err == nil {
		t.Fatal("opened with no security-context manager")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("the folder was made")
	}
}

// An existing folder is refused: Den makes its own.
func TestOpenNeedsAFreshFolder(t *testing.T) {
	f := newFake(t, manager)
	if _, err := Open(f.path, t.TempDir(), "a", "m1", 2*time.Second); err == nil {
		t.Fatal("opened in an existing folder")
	}
}
