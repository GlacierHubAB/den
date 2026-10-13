package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
)

// SocketPath is den-api.sock in $XDG_RUNTIME_DIR, "" when that is unset:
// then there is no API, as Drey's client finds none either.
func SocketPath() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "den-api.sock")
}

// ErrOtherDen is another Den already serving the path.
var ErrOtherDen = errors.New("another Den serves den-api.sock")

// bound is a socket Den bound and the file it put at the path.
type bound struct {
	ln   *net.UnixListener
	path string
	dev  uint64
	ino  uint64
}

// bind puts a listening socket at path without a umask: in a private
// folder, at a temporary name, chmod 0600, then renamed into place, so the
// path never has a wrong mode and is never empty while Den runs. It leaves
// alone anything at the path that is not a dead socket of Den's own user.
func bind(path string) (*bound, error) {
	dir := filepath.Dir(path)
	if err := bridge.CheckPrivateDir(dir); err != nil {
		return nil, fmt.Errorf("den-api.sock needs a private runtime folder: %w", err)
	}
	if fi, err := os.Lstat(path); err == nil {
		st, _ := fi.Sys().(*syscall.Stat_t)
		switch {
		case fi.Mode()&os.ModeSocket == 0:
			return nil, fmt.Errorf("%s is a %s, not a socket; Den leaves it alone", path, kindOf(fi))
		case st == nil || int(st.Uid) != os.Getuid():
			return nil, fmt.Errorf("%s belongs to another user; Den leaves it alone", path)
		}
		if c, err := net.DialTimeout("unix", path, 300*time.Millisecond); err == nil {
			c.Close()
			return nil, ErrOtherDen
		}
		// A dead socket of ours: replaced by the rename below.
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	var b [8]byte
	rand.Read(b[:])
	tmp := filepath.Join(dir, fmt.Sprintf(".den-api.%d.%s.sock", os.Getpid(), hex.EncodeToString(b[:])))
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: tmp, Net: "unix"})
	if err != nil {
		return nil, err
	}
	ln.SetUnlinkOnClose(false)
	if err := os.Chmod(tmp, 0o600); err != nil {
		ln.Close()
		os.Remove(tmp)
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		ln.Close()
		os.Remove(tmp)
		return nil, err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		ln.Close()
		return nil, err
	}
	st := fi.Sys().(*syscall.Stat_t)
	return &bound{ln: ln, path: path, dev: uint64(st.Dev), ino: st.Ino}, nil
}

// ours reports whether the path still holds the socket Den bound.
func (b *bound) ours() bool {
	fi, err := os.Lstat(b.path)
	if err != nil {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && uint64(st.Dev) == b.dev && st.Ino == b.ino
}

// close stops listening and removes the path only if it is still Den's.
func (b *bound) close() {
	b.ln.Close()
	if b.ours() {
		os.Remove(b.path)
	}
}

func kindOf(fi os.FileInfo) string {
	switch m := fi.Mode(); {
	case m&os.ModeSymlink != 0:
		return "symlink"
	case m.IsDir():
		return "directory"
	case m.IsRegular():
		return "regular file"
	}
	return "special file"
}
