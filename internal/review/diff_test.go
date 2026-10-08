package review

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const unified = `diff --git a/cmd/list.go b/cmd/list.go
index 1111111..2222222 100644
--- a/cmd/list.go
+++ b/cmd/list.go
@@ -18,3 +18,4 @@ func newListCmd() *cobra.Command {
 	cmd := &cobra.Command{
-		RunE:  runList,
+		RunE: run,
+		Args: cobra.NoArgs,
@@ -40,2 +41,2 @@ func runList() {
--- a decremented dashed line
+++ an added line that looks like a header
diff --git a/old name.txt b/new name.txt
similarity 90%
rename from old name.txt
rename to new name.txt
index 3333333..4444444 100644
--- a/old name.txt
+++ b/new name.txt
@@ -1 +1 @@
-hello
+hello there
\ No newline at end of file
diff --git a/gone.go b/gone.go
deleted file mode 100644
index 5555555..0000000
--- a/gone.go
+++ /dev/null
@@ -1,2 +0,0 @@
-package gone
-
diff --git a/logo.png b/logo.png
new file mode 100644
index 0000000..6666666
Binary files /dev/null and b/logo.png differ
diff --git "a/sp\303\244ce.go" "b/sp\303\244ce.go"
new file mode 100644
--- /dev/null
+++ "b/sp\303\244ce.go"
@@ -0,0 +1 @@
+package x
`

func TestParseUnified(t *testing.T) {
	files := ParseUnified(unified)
	if len(files) != 5 {
		t.Fatalf("got %d files: %+v", len(files), files)
	}
	list := files[0]
	if list.Path != "cmd/list.go" || list.OldPath != "" || len(list.Hunks) != 2 {
		t.Fatalf("list.go = %+v", list)
	}
	if h := list.Hunks[0]; h.OldStart != 18 || h.NewStart != 18 || len(h.Lines) != 4 || h.Lines[3] != "+\t\tArgs: cobra.NoArgs," {
		t.Errorf("hunk 0 = %+v", h)
	}
	if h := list.Hunks[1]; h.NewStart != 41 || len(h.Lines) != 2 || h.Lines[0] != "--- a decremented dashed line" {
		t.Errorf("header-looking lines inside a hunk must stay content: %+v", h)
	}
	if a, r := list.Counts(); a != 3 || r != 2 {
		t.Errorf("counts = +%d -%d", a, r)
	}
	if f := files[1]; f.Path != "new name.txt" || f.OldPath != "old name.txt" || len(f.Hunks[0].Lines) != 2 {
		t.Errorf("rename = %+v", f)
	}
	if f := files[2]; f.Path != "gone.go" || !f.Deleted || f.OldPath != "" {
		t.Errorf("delete = %+v", f)
	}
	if f := files[3]; f.Path != "logo.png" || !f.Binary || !f.Created {
		t.Errorf("binary = %+v", f)
	}
	if f := files[4]; f.Path != "späce.go" || !f.Created || f.Hunks[0].Lines[0] != "+package x" {
		t.Errorf("quoted path = %+v", f)
	}
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
}

func write(t *testing.T, path, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func paths(files []File) string {
	var p []string
	for _, f := range files {
		p = append(p, f.Path)
	}
	return strings.Join(p, " ")
}

func TestDiffFeatureBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	repo := t.TempDir()
	run(t, repo, "git", "init", "-q", "-b", "main")
	write(t, filepath.Join(repo, "a.go"), "package a\n\nfunc A() {}\n")
	write(t, filepath.Join(repo, ".gitignore"), "*.log\n")
	run(t, repo, "git", "add", ".")
	run(t, repo, "git", "commit", "-qm", "base")

	run(t, repo, "git", "switch", "-qc", "feat")
	write(t, filepath.Join(repo, "a.go"), "package a\n\nfunc A() { B() }\n")
	run(t, repo, "git", "commit", "-qam", "committed change")
	write(t, filepath.Join(repo, "b.go"), "package a\n\nfunc B() {}\n") // untracked
	write(t, filepath.Join(repo, "debug.log"), "ignored\n")
	write(t, filepath.Join(repo, "sub", "c.go"), "package sub\n") // untracked, nested

	sub := filepath.Join(repo, "sub")
	base, err := Base(ctx, sub, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	files, err := Diff(ctx, sub, base)
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(files); got != "a.go b.go sub/c.go" {
		t.Fatalf("files = %q", got)
	}
	if a, r := files[0].Counts(); a != 1 || r != 1 {
		t.Errorf("a.go counts = +%d -%d", a, r)
	}
	if !files[1].Created || len(files[1].Hunks) != 1 || files[1].Hunks[0].Lines[2] != "+func B() {}" {
		t.Errorf("untracked b.go = %+v", files[1])
	}
}

func TestBaseOnDefaultBranchUsesSessionStart(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	repo := t.TempDir()
	commit := func(msg, at string) {
		run(t, repo, "env", "GIT_COMMITTER_DATE="+at, "GIT_AUTHOR_DATE="+at, "git", "commit", "-qam", msg)
	}
	run(t, repo, "git", "init", "-q", "-b", "main")
	write(t, filepath.Join(repo, "a.txt"), "one\n")
	run(t, repo, "git", "add", ".")
	commit("before the session", "2026-10-08T08:00:00Z")
	write(t, filepath.Join(repo, "a.txt"), "one\ntwo\n")
	commit("agent work on main", "2026-10-08T10:00:00Z")
	write(t, filepath.Join(repo, "a.txt"), "one\ntwo\nthree\n") // uncommitted

	// Without a session start only uncommitted work shows.
	base, err := Base(ctx, repo, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	files, _ := Diff(ctx, repo, base)
	if len(files) != 1 {
		t.Fatalf("zero since: files = %q", paths(files))
	}
	if a, _ := files[0].Counts(); a != 1 {
		t.Errorf("zero since: +%d, want only the uncommitted line", a)
	}

	// With one, the commit made during the session shows too.
	since, _ := time.Parse(time.RFC3339, "2026-10-08T09:00:00Z")
	base, err = Base(ctx, repo, since)
	if err != nil {
		t.Fatal(err)
	}
	files, _ = Diff(ctx, repo, base)
	if len(files) != 1 {
		t.Fatalf("since: files = %q", paths(files))
	}
	if a, _ := files[0].Counts(); a != 2 {
		t.Errorf("since: +%d, want the committed and the uncommitted line", a)
	}

	// A session older than the repository reviews everything.
	early, _ := time.Parse(time.RFC3339, "2026-10-01T00:00:00Z")
	base, err = Base(ctx, repo, early)
	if err != nil {
		t.Fatal(err)
	}
	files, _ = Diff(ctx, repo, base)
	if len(files) != 1 || !files[0].Created {
		t.Errorf("early: files = %+v", files)
	}
}
