package bridge

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"
)

// peerUIDFunc reads the uid at the other end of a connection; tests swap
// it (atomically: servers read it from their own goroutines), since a
// second user is not available to them.
var peerUIDFunc atomic.Pointer[func(net.Conn) (int, error)]

func init() {
	f := PeerUID
	peerUIDFunc.Store(&f)
}

func peerUID(c net.Conn) (int, error) { return (*peerUIDFunc.Load())(c) }

// PeerUID is the uid of the process at the other end of a unix socket
// connection (SO_PEERCRED).
func PeerUID(c net.Conn) (int, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return -1, errors.New("not a unix socket")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return -1, err
	}
	uid := -1
	var cerr error
	err = raw.Control(func(fd uintptr) {
		var cred *syscall.Ucred
		cred, cerr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
		if cerr == nil {
			uid = int(cred.Uid)
		}
	})
	if err != nil {
		return -1, err
	}
	return uid, cerr
}

// ErrForeign means the process at the other end of den.sock is not this
// user's.
var ErrForeign = errors.New("the socket belongs to another user")

// Dial connects to den.sock and keeps the connection only when the process
// at the other end is this user's. A listener another user put in place is
// treated as no Den at all: nothing is sent to it, and a hook fails open
// instead of handing it a request to answer. A path in the private fallback
// directory is dialled only while that directory checks out.
func Dial(path string, timeout time.Duration) (net.Conn, error) {
	if filepath.Dir(path) == FallbackDir() {
		if err := CheckPrivateDir(FallbackDir()); err != nil {
			return nil, err
		}
	}
	conn, err := net.DialTimeout("unix", path, timeout)
	if err != nil {
		return nil, err
	}
	if uid, err := peerUID(conn); err != nil || uid != os.Getuid() {
		conn.Close()
		return nil, ErrForeign
	}
	return conn, nil
}

// FallbackDir is the private directory den.sock lives in when
// $XDG_RUNTIME_DIR is unset (a server reached over ssh without
// systemd-logind, for one): $TMPDIR/den-<uid>.
func FallbackDir() string {
	return filepath.Join(os.TempDir(), "den-"+strconv.Itoa(os.Getuid()))
}

// CheckPrivateDir fails unless dir is a real directory (not a symlink)
// owned by this user and closed to everyone else.
func CheckPrivateDir(dir string) error {
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	switch {
	case !st.IsDir():
		return fmt.Errorf("%s is not a directory", dir)
	case !ok || int(sys.Uid) != os.Getuid():
		return fmt.Errorf("%s is not owned by this user", dir)
	case st.Mode().Perm()&0o077 != 0:
		return fmt.Errorf("%s is open to other users (mode %o)", dir, st.Mode().Perm())
	}
	return nil
}

// EnsurePrivateDir makes dir (mode 0700) when it is missing and checks it
// as CheckPrivateDir does: a directory somebody else made or opened up, or
// a symlink, is refused rather than repaired.
func EnsurePrivateDir(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return CheckPrivateDir(dir)
}
