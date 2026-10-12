package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// unsafeName is a file name with an escape sequence and a bidi override.
const unsafeName = "evil\x1b[31mname\u202e.txt"

// unsafeRepo is a repository where a session wrote two new files, one of
// them with unsafeName, and the transcript that says so.
func unsafeRepo(t *testing.T) (root, transcriptPath string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root = t.TempDir()
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	gitRun(t, root, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(root, "f.txt"), []byte("a\n"), 0o644)
	gitRun(t, root, "add", ".")
	gitRun(t, root, "-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "-m", "init")
	at := time.Now().UTC().Format(time.RFC3339Nano)
	var tr strings.Builder
	for i, name := range []string{unsafeName, "plain-new.txt"} {
		p := filepath.Join(root, name)
		content := "x\x1b]8;;http://example.invalid/\x07click\x1b]8;;\x07\n"
		os.WriteFile(p, []byte(content), 0o644)
		q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
		fmt.Fprintf(&tr, `{"type":"assistant","timestamp":%q,"message":{"content":[{"type":"tool_use","id":"w%d","name":"Write","input":{"file_path":%s}}]}}`+"\n", at, i, q(p))
		fmt.Fprintf(&tr, `{"type":"user","timestamp":%q,"message":{"content":[{"type":"tool_result","tool_use_id":"w%d","content":"ok"}]},"toolUseResult":{"type":"create","filePath":%s,"content":%s,"structuredPatch":[]}}`+"\n", at, i, q(p), q(content))
	}
	transcriptPath = filepath.Join(t.TempDir(), "s.jsonl")
	os.WriteFile(transcriptPath, []byte(tr.String()), 0o644)
	return root, transcriptPath
}

// A file the agent wrote whose name has control characters is listed by
// its cleaned name and says its content is not shown, rather than being
// left out.
func TestReviewListsAFileWithAnUnsafeNameWithoutItsContent(t *testing.T) {
	root, tp := unsafeRepo(t)
	msg := loadWorks([]reviewInput{{SessionID: "s1", Name: "Mossy", CWD: root, HasGit: true, Path: tp, Started: time.Now().Add(-time.Hour)}})
	if len(msg.errs) > 0 || len(msg.works) != 1 {
		t.Fatalf("works = %+v errs=%v note=%q", msg.works, msg.errs, msg.note)
	}
	var names []string
	for _, f := range msg.works[0].Files {
		names = append(names, fmt.Sprintf("%q unsafe=%v", f.Path, f.Unsafe))
	}
	if got := strings.Join(names, ", "); got != fmt.Sprintf("%q unsafe=true, \"plain-new.txt\" unsafe=false", unsafeName) {
		t.Fatalf("files = %s", got)
	}

	m := missionModel()
	m.tab = tabReview
	m.base[0].Session.SessionID = "s1"
	m.refresh()
	m.proj.cur = root
	m.applyReview(msg)
	if len(m.rev.works) != 1 {
		t.Fatalf("Review lists %d works", len(m.rev.works))
	}
	for i, f := range m.rev.works[0].Files {
		if f.Unsafe {
			m.rev.fileCur = i
		}
	}
	m.rev.focus = revFiles
	out := m.View().Content
	if strings.Contains(out, "\x1b]8") || strings.Contains(out, "\u202e") || strings.Contains(out, "\x1b[31m") {
		t.Errorf("the name or content reached the terminal: %q", out)
	}
	plain := strings.Join(strings.Fields(ansi.Strip(out)), " ")
	for _, want := range []string{"evilname.txt", "not shown", "Content not shown (name contains control characters)."} {
		if !strings.Contains(plain, want) {
			t.Errorf("Review lacks %q:\n%s", want, ansi.Strip(out))
		}
	}
	if strings.Contains(plain, "click") {
		t.Errorf("the content of the file with the unsafe name is shown:\n%s", ansi.Strip(out))
	}
}
