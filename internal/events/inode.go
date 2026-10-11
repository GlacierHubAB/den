package events

import (
	"os"
	"syscall"
)

// inode identifies the file at path, 0 when there is none, so Follow can
// tell when the stream was moved aside and started again.
func inode(path string) uint64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		return s.Ino
	}
	return 0
}
