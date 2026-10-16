package process

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/gitenvtest"
)

// TestMain starts the tests without git's inherited variables, which would
// send their git commands to another repository.
func TestMain(m *testing.M) { os.Exit(gitenvtest.Main(m)) }

func sh(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Test", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, body string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// repo is a repository with a dev branch and two worktrees, each on its
// own branch from dev.
func repo(t *testing.T) (main, one, two string) {
	t.Helper()
	root := t.TempDir()
	main = filepath.Join(root, "proj")
	sh(t, root, "init", "-q", "-b", "dev", main)
	write(t, filepath.Join(main, "shared.go"), "package p\n")
	write(t, filepath.Join(main, "only.go"), "package p\n")
	sh(t, main, "add", ".")
	sh(t, main, "commit", "-q", "-m", "base")
	one, two = filepath.Join(root, "one"), filepath.Join(root, "two")
	sh(t, main, "worktree", "add", "-q", "-b", "a", one)
	sh(t, main, "worktree", "add", "-q", "-b", "b", two)
	return main, one, two
}

// A branch's files: committed since the merge base, staged, unstaged and
// untracked, and never what dev changed after it branched.
func TestTouched(t *testing.T) {
	main, one, _ := repo(t)
	ctx := context.Background()
	write(t, filepath.Join(one, "committed.go"), "x")
	sh(t, one, "add", "committed.go")
	sh(t, one, "commit", "-q", "-m", "c")
	write(t, filepath.Join(one, "staged.go"), "x")
	sh(t, one, "add", "staged.go")
	write(t, filepath.Join(one, "shared.go"), "package p // changed\n")
	write(t, filepath.Join(one, "dir", "new.go"), "x")
	write(t, filepath.Join(main, "dev-only.go"), "x")
	sh(t, main, "add", ".")
	sh(t, main, "commit", "-q", "-m", "dev moves on")
	got, err := Touched(ctx, one)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"committed.go", "dir/new.go", "shared.go", "staged.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("touched %v, want %v", got, want)
	}
}

// No integration branch: an error, not a guess.
func TestTouchedNoBase(t *testing.T) {
	dir := t.TempDir()
	sh(t, dir, "init", "-q", "-b", "topic", dir)
	write(t, filepath.Join(dir, "f"), "x")
	sh(t, dir, "add", ".")
	sh(t, dir, "commit", "-q", "-m", "c")
	if _, err := Touched(context.Background(), dir); err != ErrNoBase {
		t.Errorf("err %v", err)
	}
}

// Every worktree of one repository has the same id; another repository
// another; the name part is safe for a file name.
func TestRepoID(t *testing.T) {
	main, one, two := repo(t)
	ctx := context.Background()
	ids := map[string]bool{}
	for _, d := range []string{main, one, two} {
		id, err := RepoID(ctx, d)
		if err != nil {
			t.Fatal(err)
		}
		ids[id] = true
	}
	if len(ids) != 1 {
		t.Fatalf("ids %v", ids)
	}
	for id := range ids {
		if !strings.HasPrefix(id, "proj-") || len(id) != len("proj-")+12 {
			t.Errorf("id %q", id)
		}
	}
	other, _, _ := repo(t)
	if id, _ := RepoID(ctx, other); ids[id] {
		t.Errorf("another repository has the same id %q", id)
	}
	for in, want := range map[string]string{"a b/../c": "a-b-..-c", "...hidden": "hidden", "": "repo", "x\x1b[31my": "xy"} {
		if got := safeName(in); got != want {
			t.Errorf("safeName(%q) = %q, want %q", in, got, want)
		}
	}
}

// Den's reads run nothing an agent's checkout names: no fsmonitor hook,
// no clean filter, no external diff, no text conversion.
func TestTouchedRunsNothingTheCheckoutNames(t *testing.T) {
	_, one, _ := repo(t)
	marks := t.TempDir()
	prog := filepath.Join(t.TempDir(), "mark")
	write(t, prog, "#!/bin/sh\ntouch "+marks+"/$1\ncat\n")
	os.Chmod(prog, 0o755)
	sh(t, one, "config", "core.fsmonitor", prog+" fsmonitor")
	sh(t, one, "config", "filter.evil.clean", prog+" clean")
	sh(t, one, "config", "diff.external", prog+" extdiff")
	sh(t, one, "config", "diff.evil.textconv", prog+" textconv")
	write(t, filepath.Join(one, ".gitattributes"), "*.go filter=evil diff=evil\n")
	write(t, filepath.Join(one, "shared.go"), "package p // changed\n")
	if _, err := Touched(context.Background(), one); err != nil {
		t.Fatal(err)
	}
	if left, _ := os.ReadDir(marks); len(left) != 0 {
		t.Errorf("ran %v", left)
	}
}

// Read, end to end: two worktrees that both change shared.go overlap on it
// alone; the main checkout, on dev and unchanged, on nothing.
func TestReadOverlap(t *testing.T) {
	main, one, two := repo(t)
	ctx := context.Background()
	write(t, filepath.Join(one, "shared.go"), "package p // one\n")
	write(t, filepath.Join(one, "mine.go"), "x")
	write(t, filepath.Join(two, "shared.go"), "package p // two\n")
	sh(t, two, "commit", "-q", "-am", "two")
	var works []Work
	for key, dir := range map[string]string{"s0": main, "s1": one, "s2": two} {
		w, err := Read(ctx, key, dir)
		if err != nil {
			t.Fatal(err)
		}
		works = append(works, w)
	}
	got := Overlaps(works)
	if len(got) != 1 || got[0].A != "s1" || got[0].B != "s2" || !reflect.DeepEqual(got[0].Files, []string{"shared.go"}) {
		t.Errorf("overlaps %+v", got)
	}
}

// A branch's name, base, age (its oldest commit since leaving the base)
// and how far behind the base it is.
func TestReadBranchAge(t *testing.T) {
	main, one, two := repo(t)
	ctx := context.Background()
	write(t, filepath.Join(one, "a.go"), "x")
	sh(t, one, "add", ".")
	cmd := exec.Command("git", "-C", one, "-c", "user.name=T", "-c", "user.email=t@example.com", "commit", "-q", "-m", "old")
	cmd.Env = append(os.Environ(), "GIT_COMMITTER_DATE=2026-10-10T08:00:00Z", "GIT_AUTHOR_DATE=2026-10-10T08:00:00Z")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	write(t, filepath.Join(one, "b.go"), "x")
	sh(t, one, "add", ".")
	sh(t, one, "commit", "-q", "-m", "new")
	for i := 0; i < 3; i++ {
		write(t, filepath.Join(main, "dev.go"), strings.Repeat("x", i+1))
		sh(t, main, "add", ".")
		sh(t, main, "commit", "-q", "-m", "dev")
	}
	w, err := Read(ctx, "s1", one)
	if err != nil {
		t.Fatal(err)
	}
	if w.Branch != "a" || w.Base != "refs/heads/dev" || w.Behind != 3 || !w.Since.Equal(time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("work %+v", w)
	}
	// A branch with no commits of its own has no age.
	if w, err := Read(ctx, "s2", two); err != nil || !w.Since.IsZero() || w.Behind != 3 {
		t.Errorf("an empty branch: %+v %v", w, err)
	}
}

// Two branches of one repository that change a file in common overlap,
// once, on exactly the common files; other repositories never do.
func TestOverlaps(t *testing.T) {
	got := Overlaps([]Work{
		{Key: "s2", Repo: "r", Files: []string{"a.go", "b.go", "c.go"}},
		{Key: "s1", Repo: "r", Files: []string{"c.go", "a.go"}},
		{Key: "s3", Repo: "r", Files: []string{"d.go"}},
		{Key: "s4", Repo: "other", Files: []string{"a.go"}},
		{Key: "s5", Repo: "", Files: []string{"a.go"}},
		{Key: "s6", Repo: "r2", Tree: "/w", Files: []string{"a.go"}},
		{Key: "s7", Repo: "r2", Tree: "/w", Files: []string{"a.go"}},
	})
	want := []Overlap{{A: "s1", B: "s2", Files: []string{"a.go", "c.go"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("overlaps %+v, want %+v", got, want)
	}
}
