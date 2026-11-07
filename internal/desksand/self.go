package desksand

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Self is Den's own binary, which runs the wrapper's stages. It is taken
// once at start, and a launch is refused if it has changed since.
type Self struct {
	Path  string
	dev   uint64
	ino   uint64
	size  int64
	mtime time.Time
}

// TakeSelf resolves the running executable: a regular file of the user's
// or root's, in a directory of the user's or root's, neither writable by
// group or others.
func TakeSelf() (Self, error) {
	p, err := os.Executable()
	if err != nil {
		return Self{}, err
	}
	if p, err = filepath.EvalSymlinks(p); err != nil {
		return Self{}, err
	}
	return takeSelf(p, os.Getuid())
}

func takeSelf(p string, uid int) (Self, error) {
	for _, q := range []string{p, filepath.Dir(p)} {
		if err := ownOrRoot(q, uid); err != nil {
			return Self{}, fmt.Errorf("%s: %v", q, err)
		}
	}
	fi, err := os.Stat(p)
	if err != nil {
		return Self{}, err
	}
	if !fi.Mode().IsRegular() {
		return Self{}, fmt.Errorf("%s: not a regular file", p)
	}
	st := fi.Sys().(*syscall.Stat_t)
	return Self{Path: p, dev: uint64(st.Dev), ino: st.Ino, size: fi.Size(), mtime: fi.ModTime()}, nil
}

// Check fails if the binary is not the one taken at start.
func (s Self) Check() error {
	if s.Path == "" {
		return errors.New("Den's own binary was not taken at start")
	}
	now, err := takeSelf(s.Path, os.Getuid())
	if err != nil {
		return err
	}
	if now.dev != s.dev || now.ino != s.ino || now.size != s.size || !now.mtime.Equal(s.mtime) {
		return fmt.Errorf("%s changed since Den started; restart Den to launch apps", s.Path)
	}
	return nil
}

func ownOrRoot(p string, uid int) error {
	fi, err := os.Stat(p)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || (st.Uid != 0 && int(st.Uid) != uid) {
		return errors.New("owned by neither you nor root")
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return errors.New("writable by group or others")
	}
	return nil
}
