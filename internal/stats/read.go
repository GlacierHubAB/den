package stats

import (
	"bufio"
	"errors"
	"io"
	"io/fs"
	"os"
	"syscall"
	"time"
)

// What is read from a state file is bounded, whatever the file holds: the
// newest part of it, as much as the limit of its source (stats.go) says, and
// no line longer than maxLineBytes.
const maxLineBytes = 4 << 20

// Source says how much of a state file was read and what it covered. It
// holds counts and times only, never a path or an error text.
type Source struct {
	File string `json:"file"`
	// Lines are the records read; Skipped the lines that were not records
	// (corrupt, cut off, or longer than maxLineBytes).
	Lines   int        `json:"lines"`
	Skipped int        `json:"skipped"`
	First   *time.Time `json:"first,omitempty"` // the earliest record time seen
	Last    *time.Time `json:"last,omitempty"`  // the latest
	// Truncated is set when a file was longer than its limit, in bytes, and
	// only its newest part was read.
	Truncated bool  `json:"truncated,omitempty"`
	Limit     int64 `json:"limit_bytes,omitempty"` // set with Truncated
	// Problem is "missing", "not a regular file" or "unreadable" when no
	// file behind the source could be read.
	Problem string `json:"problem,omitempty"`

	opened bool // some file behind the source was opened
}

func (s *Source) see(t time.Time) {
	if t.IsZero() {
		return
	}
	t = t.UTC()
	if s.First == nil || t.Before(*s.First) {
		s.First = &t
	}
	if s.Last == nil || t.After(*s.Last) {
		s.Last = &t
	}
}

// ok reports whether a file behind the source could be read.
func (s *Source) ok() bool { return s.opened }

// problem records why a file could not be read, unless another file of the
// same source (events.jsonl.1, say) was.
func (s *Source) problem(why string) {
	if !s.opened {
		s.Problem = why
	}
}

// read calls fn with each line of the file at path, without its newline;
// fn returns the line's record time and whether the line was a record. Only
// the newest limit bytes of the file are read. The file is opened read-only
// without following a symlink and must be a regular file, so a state file
// swapped for a pipe or a link cannot block the read or redirect it.
func (s *Source) read(path string, limit int64, fn func(line []byte) (time.Time, bool)) {
	if path == "" {
		s.problem("missing")
		return
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			s.problem("missing")
		} else {
			s.problem("unreadable")
		}
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		s.problem("unreadable")
		return
	}
	if !fi.Mode().IsRegular() {
		s.problem("not a regular file")
		return
	}
	cut := fi.Size() > limit
	if cut {
		if _, err := f.Seek(fi.Size()-limit, io.SeekStart); err != nil {
			s.problem("unreadable")
			return
		}
		s.Truncated, s.Limit = true, limit
	}
	s.opened, s.Problem = true, ""
	br := bufio.NewReaderSize(io.LimitReader(f, limit), 64<<10)
	if cut {
		// The newest part starts inside a line: drop what is left of it.
		for {
			if _, err := br.ReadSlice('\n'); err != bufio.ErrBufferFull {
				break
			}
		}
	}
	var line []byte
	skip := false
	for {
		chunk, err := br.ReadSlice('\n')
		if !skip {
			if len(line)+len(chunk) > maxLineBytes {
				line, skip = line[:0], true
			} else {
				line = append(line, chunk...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if skip {
			s.Skipped++
		} else if n := len(line); n > 0 {
			if line[n-1] == '\n' {
				line = line[:n-1]
			}
			if len(line) > 0 {
				if t, ok := fn(line); ok {
					s.Lines++
					s.see(t)
				} else {
					s.Skipped++
				}
			}
		}
		line, skip = line[:0], false
		if err != nil {
			return
		}
	}
}
