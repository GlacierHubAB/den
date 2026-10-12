package events

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func appendLine(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(s)
	f.Close()
}

func poll(tl *Tail) (lines []string, last Cursor) {
	tl.Poll(func(line []byte, at Cursor) {
		lines = append(lines, string(line))
		last = at
	})
	return
}

func TestTailFromZeroCursorStartsAtTheEnd(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.jsonl")
	appendLine(t, p, "old\n")
	tl := Follow(p, Cursor{})
	defer tl.Close()
	if got, _ := poll(tl); len(got) != 0 {
		t.Errorf("read old lines: %v", got)
	}
	appendLine(t, p, "new1\nnew2\npart")
	if got, _ := poll(tl); fmt.Sprint(got) != "[new1 new2]" {
		t.Errorf("got %v", got)
	}
	appendLine(t, p, "ial\n")
	if got, _ := poll(tl); fmt.Sprint(got) != "[partial]" {
		t.Errorf("partial line: %v", got)
	}
}

func TestTailMissingStreamIsReadFromItsStart(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.jsonl")
	tl := Follow(p, Cursor{})
	defer tl.Close()
	poll(tl)
	appendLine(t, p, "a\n")
	if got, _ := poll(tl); fmt.Sprint(got) != "[a]" {
		t.Errorf("got %v", got)
	}
}

// TestTailResumesExactlyOnce is the backfill contract: a reader that stops,
// misses lines (some across a move to .1), and resumes from its cursor
// sees each line once.
func TestTailResumesExactlyOnce(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "events.jsonl")
	appendLine(t, p, "1\n2\n")
	tl := Follow(p, Cursor{})
	appendLine(t, p, "3\n")
	got, cur := poll(tl)
	tl.Close()
	if fmt.Sprint(got) != "[3]" {
		t.Fatalf("got %v", got)
	}

	// Same file: resume where it stopped.
	appendLine(t, p, "4\n")
	tl = Follow(p, cur)
	got, cur = poll(tl)
	tl.Close()
	if fmt.Sprint(got) != "[4]" {
		t.Fatalf("same file: %v", got)
	}

	// Moved aside once while away: the rest of .1, then the new file.
	appendLine(t, p, "5\n")
	os.Rename(p, p+".1")
	appendLine(t, p, "6\n7\n")
	tl = Follow(p, cur)
	got, cur = poll(tl)
	if fmt.Sprint(got) != "[5 6 7]" {
		t.Fatalf("after one move: %v", got)
	}
	// Moved aside while following.
	appendLine(t, p, "8\n")
	os.Rename(p, p+".1")
	appendLine(t, p, "9\n")
	got, cur = poll(tl)
	tl.Close()
	if fmt.Sprint(got) != "[8 9]" {
		t.Fatalf("move while following: %v", got)
	}

	// Moved aside twice while away: the cursor's file is gone, so all of .1
	// and the new file are newer; nothing is repeated.
	appendLine(t, p, "10\n")
	os.Rename(p, p+".1")
	appendLine(t, p, "11\n")
	os.Rename(p, p+".1")
	appendLine(t, p, "12\n")
	tl = Follow(p, cur)
	got, _ = poll(tl)
	tl.Close()
	if fmt.Sprint(got) != "[11 12]" {
		t.Fatalf("after two moves: %v (10 was in the deleted file)", got)
	}
}

func TestTailTruncatedFileStartsOver(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.jsonl")
	appendLine(t, p, "aaaaaaaaaa\nbbbbbbbbbb\n")
	tl := Follow(p, Cursor{})
	appendLine(t, p, "c\n")
	_, cur := poll(tl)
	tl.Close()
	os.Truncate(p, 0)
	appendLine(t, p, "d\n")
	tl = Follow(p, cur)
	got, _ := poll(tl)
	tl.Close()
	if fmt.Sprint(got) != "[d]" {
		t.Errorf("after truncation: %v", got)
	}
}

// A new file that reuses the old inode is told apart by its first bytes.
func TestTailCursorChecksTheFilesHead(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.jsonl")
	appendLine(t, p, "first line of the old file\n")
	f, _ := os.Open(p)
	h, n := fileHead(f, headLen)
	f.Close()
	cur := Cursor{Ino: inode(p), Off: 27, Head: h, HeadLen: n}
	os.WriteFile(p, []byte("another file entirely, longer\nnext\n"), 0o600) // same inode, other content
	tl := Follow(p, cur)
	got, _ := poll(tl)
	tl.Close()
	if fmt.Sprint(got) != "[another file entirely, longer next]" {
		t.Errorf("got %v", got)
	}
}
