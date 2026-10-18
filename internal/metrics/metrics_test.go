package metrics

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Records append as JSON lines in a file only Den reads, the file rotates
// at MaxBytes, a link in its place is refused, and LastDay finds the latest
// daily record.
func TestAppendAndLastDay(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "den")
	path := filepath.Join(dir, "metrics.jsonl")
	if LastDay(path) != "" {
		t.Error("a day with no file")
	}
	now := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	if err := Append(path, Record{Kind: Daily, At: now, Day: "2026-10-16", Mission: "m1"}, Record{Kind: Ended, At: now, Day: "2026-10-17", Mission: "m2"}, Record{Kind: Daily, Day: "2026-10-15"}); err != nil {
		t.Fatal(err)
	}
	if d := LastDay(path); d != "2026-10-16" {
		t.Errorf("last day %q", d)
	}
	for p, want := range map[string]os.FileMode{dir: 0o700, path: 0o600} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != want {
			t.Errorf("%s: %v %v", p, fi.Mode(), err)
		}
	}
	body, _ := os.ReadFile(path)
	if n := strings.Count(string(body), "\n"); n != 3 {
		t.Errorf("%d lines", n)
	}
	// Full: the file moves to .1 and a new one starts.
	if err := os.WriteFile(path, make([]byte, MaxBytes), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Append(path, Record{Kind: Daily, Day: "2026-10-18"}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path + ".1"); err != nil || fi.Size() != MaxBytes {
		t.Errorf("rotated: %v", err)
	}
	if d := LastDay(path); d != "2026-10-18" {
		t.Errorf("after rotation %q", d)
	}
	// A link where the file goes is never written through.
	target := filepath.Join(t.TempDir(), "elsewhere")
	os.WriteFile(target, nil, 0o644)
	os.Remove(path)
	os.Symlink(target, path)
	if err := Append(path, Record{Kind: Daily}); err == nil {
		t.Error("wrote through a link")
	}
	if b, _ := os.ReadFile(target); len(b) != 0 {
		t.Error("the link's target was written")
	}
}
