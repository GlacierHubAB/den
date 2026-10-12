package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/fleet"
)

// A file the session wrote whose name has control characters is sent by
// its cleaned name, marked, with no content, rather than left out.
func TestHostDiffSendsAFileWithAnUnsafeNameWithoutItsContent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root, "-c", "user.name=user", "-c", "user.email=user@example.com"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(root, "f.txt"), []byte("a\n"), 0o644)
	git("add", ".")
	git("commit", "-qm", "init")
	evil := "evil\x1b[31mname\u202e.txt"
	content := "x\x1b]8;;http://example.invalid/\x07click\x1b]8;;\x07\n"
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	at := time.Now().UTC().Format(time.RFC3339Nano)
	var tr strings.Builder
	for i, name := range []string{evil, "plain-new.txt"} {
		p := filepath.Join(root, name)
		os.WriteFile(p, []byte(content), 0o644)
		fmt.Fprintf(&tr, `{"type":"assistant","timestamp":%q,"message":{"content":[{"type":"tool_use","id":"w%d","name":"Write","input":{"file_path":%s}}]}}`+"\n", at, i, q(p))
		fmt.Fprintf(&tr, `{"type":"user","timestamp":%q,"message":{"content":[{"type":"tool_result","tool_use_id":"w%d","content":"ok"}]},"toolUseResult":{"type":"create","filePath":%s,"content":%s,"structuredPatch":[]}}`+"\n", at, i, q(p), q(content))
	}
	tp := filepath.Join(t.TempDir(), "s.jsonl")
	os.WriteFile(tp, []byte(tr.String()), 0o644)
	ag := fleet.Agent{Path: tp, Session: discover.Session{SessionID: "11111111-2222-4333-8444-555555555555", CWD: root, StartedAt: time.Now().Add(-time.Hour).UnixMilli()}}

	d, err := hostDiff(context.Background(), ag)
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]DiffFile{}
	for _, f := range d.Files {
		byPath[f.Path] = f
	}
	if f, ok := byPath["evilname.txt"]; !ok || !f.Unsafe || len(f.Hunks) > 0 || !f.Created || f.Added != 1 || f.Removed != 0 {
		t.Errorf("the file with the unsafe name: %+v (files %+v)", f, d.Files)
	}
	if f := byPath["plain-new.txt"]; f.Unsafe || len(f.Hunks) != 1 {
		t.Errorf("plain-new.txt: %+v", f)
	}
	raw, _ := json.Marshal(d)
	if strings.Contains(string(raw), `\u001b`) || strings.Contains(string(raw), "\u202e") || strings.Contains(string(raw), "example.invalid") {
		t.Errorf("escapes crossed: %s", raw)
	}

	// Den shows no content for it, whatever a host sends.
	lying := &Diff{Files: []DiffFile{{Path: "evilname.txt", Unsafe: true, Hunks: []DiffHunk{{NewStart: 1, Lines: []string{"+secret"}, Mine: []int{0}}}}}}
	if got := cleanDiff(lying); len(got.Files) != 1 || !got.Files[0].Unsafe || len(got.Files[0].Hunks) > 0 {
		t.Errorf("cleanDiff kept content: %+v", got.Files)
	}
	// Its counts come through, held to bounds; a safe file's come from its
	// lines alone.
	counted := &Diff{Files: []DiffFile{
		{Path: "a.txt", Unsafe: true, Added: 3, Removed: -4},
		{Path: "b.txt", Unsafe: true, Added: 1 << 40},
		{Path: "c.txt", Added: 9, Hunks: []DiffHunk{{NewStart: 1, Lines: []string{"+x"}, Mine: []int{0}}}},
	}}
	got := cleanDiff(counted).Files
	if got[0].Added != 3 || got[0].Removed != 0 || got[1].Added != maxUnsafeCount || got[2].Added != 0 {
		t.Errorf("counts = %+v", got)
	}
}
