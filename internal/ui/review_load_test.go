package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x",
		"GIT_COMMITTER_DATE="+time.Now().Add(-2*time.Hour).Format(time.RFC1123Z))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// An agent started in ~ (no repository) that edits a file in a checkout
// elsewhere still shows up, found through its transcript.
func TestLoadWorksFollowsTheFilesAnAgentEdited(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init", "-q", "-b", "main")
	file := filepath.Join(repo, "a.go")
	os.WriteFile(file, []byte("package a\n\nfunc A() {}\n"), 0o644)
	gitRun(t, repo, "add", ".")
	// Committed before the session started, so the review base is this
	// commit rather than the empty tree.
	past := time.Now().Add(-2 * time.Hour).Format(time.RFC1123Z)
	gitRun(t, repo, "-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "-m", "init", "--date="+past)
	os.WriteFile(file, []byte("package a\n\nfunc A() { B() }\n"), 0o644)

	home := t.TempDir() // not a repository
	transcript := filepath.Join(t.TempDir(), "s.jsonl")
	at := time.Now().UTC().Format(time.RFC3339)
	os.WriteFile(transcript, []byte(strings.Join([]string{
		`{"type":"assistant","timestamp":"` + at + `","message":{"content":[{"type":"tool_use","id":"t1","name":"Edit","input":{"file_path":"` + file + `"}}]}}`,
		`{"type":"user","timestamp":"` + at + `","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]},"toolUseResult":{"filePath":"` + file + `","structuredPatch":[{"oldStart":3,"newStart":3,"lines":["-func A() {}","+func A() { B() }"]}]}}`,
	}, "\n")+"\n"), 0o644)

	msg := loadWorks([]reviewInput{{SessionID: "s1", Name: "Snug", CWD: home, HasGit: false, Path: transcript, Started: time.Now().Add(-time.Hour)}})
	if len(msg.errs) > 0 {
		t.Fatalf("errors: %v", msg.errs)
	}
	if len(msg.works) != 1 || msg.works[0].SessionID != "s1" || len(msg.works[0].Files) != 1 || msg.works[0].Added != 1 {
		t.Fatalf("works = %+v (note %q)", msg.works, msg.note)
	}

	// The same agent with nothing edited inside a repository explains itself.
	other := filepath.Join(t.TempDir(), "s2.jsonl")
	notes := filepath.Join(home, "notes.md")
	os.WriteFile(other, []byte(strings.Join([]string{
		`{"type":"assistant","timestamp":"` + at + `","message":{"content":[{"type":"tool_use","id":"t2","name":"Edit","input":{"file_path":"` + notes + `"}}]}}`,
		`{"type":"user","timestamp":"` + at + `","message":{"content":[{"type":"tool_result","tool_use_id":"t2","content":"ok"}]},"toolUseResult":{"filePath":"` + notes + `","structuredPatch":[{"oldStart":1,"newStart":1,"lines":["+hi"]}]}}`,
	}, "\n")+"\n"), 0o644)
	msg = loadWorks([]reviewInput{{SessionID: "s2", Path: other, Started: time.Now().Add(-time.Hour)}})
	if len(msg.works) != 0 || !strings.Contains(msg.note, "none inside a git repository") {
		t.Errorf("note = %q works = %d", msg.note, len(msg.works))
	}
	if msg := loadWorks(nil); !strings.Contains(msg.note, "transcript") {
		t.Errorf("empty note = %q", msg.note)
	}
}
