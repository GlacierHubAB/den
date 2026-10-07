package gitinfo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLookupMainAndWorktree(t *testing.T) {
	root := t.TempDir()
	main := filepath.Join(root, "tasklog")
	if err := os.MkdirAll(filepath.Join(main, ".git", "worktrees", "feat"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(main, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644)
	os.WriteFile(filepath.Join(main, ".git", "worktrees", "feat", "HEAD"), []byte("ref: refs/heads/feat-pending-filter\n"), 0o644)
	sub := filepath.Join(main, "cmd", "x")
	os.MkdirAll(sub, 0o755)

	wt := filepath.Join(root, "wt", "feat")
	os.MkdirAll(wt, 0o755)
	os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+filepath.Join(main, ".git", "worktrees", "feat")+"\n"), 0o644)

	if got, ok := Lookup(sub); !ok || got.Repo != "tasklog" || got.Branch != "main" {
		t.Errorf("main lookup = %+v %v", got, ok)
	}
	if got, ok := Lookup(wt); !ok || got.Repo != "tasklog" || got.Branch != "feat-pending-filter" {
		t.Errorf("worktree lookup = %+v %v", got, ok)
	}
	if _, ok := Lookup(t.TempDir()); ok {
		t.Error("expected no repo")
	}
}
