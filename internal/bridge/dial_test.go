package bridge

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// shortDir is a temporary directory with a path short enough for a unix
// socket name (108 bytes), which t.TempDir does not promise.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "den-t")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// foreignPeers makes every peer look like another user's process, as a
// squatter's listener (or client) would be. A second uid is not available
// to tests, so the check is exercised through the peerUID seam.
func foreignPeers(t *testing.T) {
	t.Helper()
	old := peerUIDFunc.Load()
	f := func(net.Conn) (int, error) { return os.Getuid() + 1, nil }
	peerUIDFunc.Store(&f)
	t.Cleanup(func() { peerUIDFunc.Store(old) })
}

// squatter listens at path, answers anything with allow, and records
// whether anything was sent to it.
func squatter(t *testing.T, path string) (got chan []byte) {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got = make(chan []byte, 8)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				c.SetReadDeadline(time.Now().Add(time.Second))
				b := make([]byte, 4096)
				n, _ := c.Read(b)
				if n > 0 {
					got <- b[:n]
					c.Write([]byte(`{"behavior":"allow"}` + "\n"))
				}
			}()
		}
	}()
	return got
}

func TestPeerUIDIsThisUser(t *testing.T) {
	path := filepath.Join(shortDir(t), "den.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if uid, err := PeerUID(c); err != nil || uid != os.Getuid() {
		t.Errorf("PeerUID = %d, %v; want %d", uid, err, os.Getuid())
	}
}

func TestNoClientTalksToAnotherUsersSocket(t *testing.T) {
	dir := shortDir(t)
	local, remote := filepath.Join(dir, "den.sock"), filepath.Join(dir, "den-remote.sock")
	gotLocal, gotRemote := squatter(t, local), squatter(t, remote)
	foreignPeers(t)
	env := Envelope{Event: "PermissionRequest", SessionID: "s1", Payload: []byte(`{"prompt":"SECRET"}`)}
	if Send(local, env) {
		t.Error("Send delivered to another user's socket")
	}
	if _, err := Call(local, env, time.Second); !errors.Is(err, ErrNoDen) {
		t.Errorf("Call: %v", err)
	}
	if _, ok := Ask(local, env, time.Second); ok {
		t.Error("Ask took an answer from another user's socket")
	}
	if d, ok := AskFirst(local, remote, env, time.Second); ok || d.Behavior != "" {
		t.Errorf("AskFirst took %+v from a squatter", d)
	}
	if _, err := Dial(local, time.Second); !errors.Is(err, ErrForeign) {
		t.Errorf("Dial: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if len(gotLocal)+len(gotRemote) > 0 {
		t.Error("a squatter was sent a request")
	}
}

func TestListenRefusesAnotherUsersSocketAndClients(t *testing.T) {
	dir := shortDir(t)
	path := filepath.Join(dir, "den.sock")
	squatter(t, path)
	foreignPeers(t)
	if _, err := Listen(path); err == nil || errors.Is(err, ErrAlreadyRunning) || !strings.Contains(err.Error(), "another user") {
		t.Errorf("Listen over a squatter: %v", err)
	}

	// A server of this user drops a client of another before reading it.
	path2 := filepath.Join(dir, "den2.sock")
	srv, err := Listen(path2)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	c, err := net.Dial("unix", path2)
	if err != nil {
		t.Fatal(err)
	}
	c.Write([]byte(`{"event":"PermissionRequest","payload":{}}` + "\n"))
	select {
	case r := <-srv.Requests():
		t.Errorf("a foreign client's %s reached Den", r.Event)
	case <-time.After(300 * time.Millisecond):
	}
	c.Close()
}

func TestDenSocketFallbackIsPrivate(t *testing.T) {
	t.Setenv("DEN_SOCKET", "")
	t.Setenv("XDG_RUNTIME_DIR", "")
	tmp := shortDir(t)
	t.Setenv("TMPDIR", tmp)
	path := SocketPath()
	if path != filepath.Join(FallbackDir(), "den.sock") || filepath.Dir(filepath.Dir(path)) != tmp {
		t.Fatalf("fallback = %q, want den.sock in %s", path, FallbackDir())
	}

	// Listen makes the directory 0700; a client then finds Den there.
	srv, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(FallbackDir()); st.Mode().Perm() != 0o700 {
		t.Errorf("fallback dir mode %v", st.Mode().Perm())
	}
	go func() {
		for r := range srv.Requests() {
			r.Reply(Decision{Behavior: "allow"})
		}
	}()
	if d, ok := Ask(path, Envelope{Event: "PermissionRequest"}, time.Second); !ok || d.Behavior != "allow" {
		t.Errorf("own Den in the fallback dir: %+v %v", d, ok)
	}

	// Opened up to others: clients stop trusting it, Listen refuses it.
	os.Chmod(FallbackDir(), 0o777)
	if Send(path, Envelope{Event: "Stop"}) {
		t.Error("a client used a world-writable fallback dir")
	}
	srv.Close()
	if _, err := Listen(path); err == nil {
		t.Error("Listen used a world-writable fallback dir")
	}

	// A symlink in its place, say to a directory another user controls.
	os.RemoveAll(FallbackDir())
	elsewhere := filepath.Join(tmp, "elsewhere")
	os.Mkdir(elsewhere, 0o700)
	os.Symlink(elsewhere, FallbackDir())
	if _, err := Listen(path); err == nil {
		t.Error("Listen followed a symlinked fallback dir")
	}
	if _, err := Dial(path, time.Second); err == nil {
		t.Error("Dial followed a symlinked fallback dir")
	}
	// A directory of another user is refused by the same uid check
	// (CheckPrivateDir); without a second uid it is not testable here.
}

// A socket another user created in the fallback directory before Den
// started is not taken for a running Den, and nothing is sent to it.
func TestPrecreatedSquatterInFallbackDir(t *testing.T) {
	t.Setenv("DEN_SOCKET", "")
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("TMPDIR", shortDir(t))
	if err := EnsurePrivateDir(FallbackDir()); err != nil {
		t.Fatal(err)
	}
	path := SocketPath()
	got := squatter(t, path)
	foreignPeers(t)
	if _, err := Listen(path); err == nil || errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("Listen over a squatter: %v", err)
	}
	if _, ok := Ask(path, Envelope{Event: "PermissionRequest", Payload: []byte(`{}`)}, time.Second); ok {
		t.Error("Ask took the squatter's answer")
	}
	time.Sleep(100 * time.Millisecond)
	if len(got) > 0 {
		t.Error("the squatter was sent a request")
	}
}

// DEN_SOCKET is used exactly as given, with no directory checks.
func TestDenSocketAsGiven(t *testing.T) {
	t.Setenv("DEN_SOCKET", "/some/where/den.sock")
	if SocketPath() != "/some/where/den.sock" {
		t.Errorf("SocketPath = %q", SocketPath())
	}
}
