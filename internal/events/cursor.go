package events

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

// headLen is how much of a file's start identifies it in a Cursor.
const headLen = 64

// Cursor is a position in the stream that survives the move to
// events.jsonl.1: the file is named by its inode and a hash of its first
// bytes (an inode alone can be reused once the old file is deleted), the
// position by the offset just past the last line read. The file only
// ever grows, so its first bytes never change while it lives.
//
// The zero Cursor means "from the end of the stream as it is now".
type Cursor struct {
	Ino     uint64 `json:"ino"`
	Off     int64  `json:"off"`
	Head    string `json:"head"`     // hex sha256 of the first HeadLen bytes
	HeadLen int    `json:"head_len"` // up to 64; fewer while the file was shorter
}

// IsZero reports whether c is the zero Cursor.
func (c Cursor) IsZero() bool { return c == Cursor{} }

// fileHead hashes the first n bytes of f, or fewer when f is shorter, and
// returns how many it hashed.
func fileHead(f *os.File, n int) (string, int) {
	buf := make([]byte, n)
	got, _ := f.ReadAt(buf, 0)
	sum := sha256.Sum256(buf[:got])
	return hex.EncodeToString(sum[:]), got
}

// matches reports whether the file at path is the one c was taken in.
func (c Cursor) matches(path string) (*os.File, bool) {
	if inode(path) != c.Ino || c.Ino == 0 {
		return nil, false
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	if h, n := fileHead(f, c.HeadLen); n != c.HeadLen || h != c.Head {
		f.Close()
		return nil, false
	}
	return f, true
}

// Tail reads the stream line by line from a Cursor, across moves to
// events.jsonl.1, so a reader that comes back after a while gets every
// line written meanwhile exactly once. It does not wait: Poll returns what
// is there and is called again later.
type Tail struct {
	path    string
	f       *os.File
	ino     uint64
	off     int64 // just past the last complete line read from f
	partial []byte
	next    []string // files to read from the start once f is drained
	waiting bool     // the stream did not exist at the start: read it from 0 when it appears
}

// Follow starts reading the stream at path from cur. With the zero Cursor
// it starts at the current end. When cur names a file that is gone (moved
// aside twice since), everything still on disk is newer than cur and is
// read from the start; what was in the deleted file is lost.
func Follow(path string, cur Cursor) *Tail {
	t := &Tail{path: path}
	old := path + ".1"
	switch {
	case cur.IsZero():
		if f, err := os.Open(path); err == nil {
			end, _ := f.Seek(0, io.SeekEnd)
			t.use(f, end)
		} else {
			t.waiting = true
		}
	default:
		if f, ok := cur.matches(path); ok {
			t.use(f, cur.Off)
		} else if f, ok := cur.matches(old); ok {
			t.use(f, cur.Off)
			t.next = []string{path}
		} else {
			t.next, t.waiting = []string{old, path}, true
		}
	}
	return t
}

func (t *Tail) use(f *os.File, off int64) {
	if t.f != nil {
		t.f.Close()
	}
	if st, err := f.Stat(); err == nil && st.Size() < off {
		off = 0 // truncated in place: what is there now is new
	}
	f.Seek(off, io.SeekStart)
	t.f, t.off, t.partial = f, off, nil
	t.ino = inodeOf(f)
}

// Close releases the open file.
func (t *Tail) Close() {
	if t.f != nil {
		t.f.Close()
		t.f = nil
	}
}

// Cursor is the position after the last line Poll delivered.
func (t *Tail) Cursor() Cursor {
	if t.f == nil {
		return Cursor{}
	}
	h, n := fileHead(t.f, headLen)
	return Cursor{Ino: t.ino, Off: t.off, Head: h, HeadLen: n}
}

// Poll calls fn with each complete line now in the stream, without its
// newline, and the Cursor just past it. line is only valid during the call.
func (t *Tail) Poll(fn func(line []byte, at Cursor)) {
	for {
		if t.f == nil {
			if !t.openNext() {
				return
			}
		}
		t.drain(fn)
		// Drained. Move on when the file was moved aside (or a queued one
		// follows); otherwise wait for more.
		if len(t.next) > 0 {
			t.f.Close()
			t.f = nil
			continue
		}
		if n := inode(t.path); n != 0 && n != t.ino {
			t.f.Close()
			t.f = nil
			t.next = []string{t.path}
			continue
		}
		return
	}
}

func (t *Tail) openNext() bool {
	for len(t.next) > 0 {
		p := t.next[0]
		t.next = t.next[1:]
		if f, err := os.Open(p); err == nil {
			if p == t.path {
				t.waiting = false
			}
			t.use(f, 0)
			return true
		}
	}
	if t.waiting {
		if f, err := os.Open(t.path); err == nil {
			t.waiting = false
			t.use(f, 0)
			return true
		}
	}
	return false
}

func (t *Tail) drain(fn func(line []byte, at Cursor)) {
	buf := make([]byte, 32<<10)
	for {
		n, err := t.f.Read(buf)
		data := buf[:n]
		for len(data) > 0 {
			i := bytes.IndexByte(data, '\n')
			if i < 0 {
				t.partial = append(t.partial, data...)
				break
			}
			line := append(t.partial, data[:i]...)
			t.off += int64(len(t.partial) + i + 1)
			t.partial = nil
			data = data[i+1:]
			if len(line) > 0 {
				fn(line, t.Cursor())
			}
		}
		if err != nil || n == 0 {
			return
		}
	}
}
