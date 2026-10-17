package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/bridge"
)

// A second Den says why it does not start, naming the first's pid, while
// one holds the socket; none, a stale socket file, or a plain file there
// starts as usual.
func TestAlreadyRunning(t *testing.T) {
	dir, err := os.MkdirTemp("", "dn") // a socket path may not pass 108 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "den.sock")
	if why := alreadyRunning(path); why != "" {
		t.Errorf("no socket: %q", why)
	}
	srv, err := bridge.Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	why := alreadyRunning(path)
	if !strings.HasPrefix(why, "den: Den is already running (pid "+strconv.Itoa(os.Getpid())) || !strings.Contains(why, "switch to it, or quit it first") {
		t.Errorf("a Den listening: %q", why)
	}
	srv.Close()
	if why := alreadyRunning(path); why != "" {
		t.Errorf("after it closed: %q", why)
	}
	os.WriteFile(path, []byte("not a socket"), 0o600)
	if why := alreadyRunning(path); why != "" {
		t.Errorf("a plain file: %q", why)
	}
}
