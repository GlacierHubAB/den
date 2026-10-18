package gitenvtest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) { os.Exit(Main(m)) }

func TestScrubAndWithout(t *testing.T) {
	t.Setenv("GIT_DIR", "/nonexistent/decoy")
	t.Setenv("GIT_INDEX_FILE", "/nonexistent/index")
	t.Setenv("KEEP_ME", "1")
	env := Without(os.Environ())
	for _, kv := range env {
		if strings.HasPrefix(kv, "GIT_") {
			t.Errorf("Without kept %s", kv)
		}
	}
	if len(Scrub()) < 2 {
		t.Error("Scrub removed nothing")
	}
	if os.Getenv("GIT_DIR") != "" || os.Getenv("GIT_INDEX_FILE") != "" || os.Getenv("KEEP_ME") != "1" {
		t.Errorf("after Scrub: GIT_DIR=%q GIT_INDEX_FILE=%q KEEP_ME=%q", os.Getenv("GIT_DIR"), os.Getenv("GIT_INDEX_FILE"), os.Getenv("KEEP_ME"))
	}
}

// git runs git in dir with no inherited GIT_* variables.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(Without(os.Environ()), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// snapshot is everything a stray git command in a repository would change:
// its config, HEAD, refs, worktrees and the files in its git directory.
func snapshot(t *testing.T, repo string) string {
	t.Helper()
	cfg, _ := os.ReadFile(filepath.Join(repo, ".git", "config"))
	entries, _ := os.ReadDir(filepath.Join(repo, ".git"))
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return string(cfg) + "\n--\n" + strings.Join(names, ",") + "\n--\n" +
		git(t, repo, "rev-parse", "HEAD") + git(t, repo, "for-each-ref") + git(t, repo, "worktree", "list") +
		git(t, repo, "status", "--porcelain") + git(t, repo, "config", "--list", "--local")
}

// Run the way `git rebase --exec` runs a command, with GIT_DIR and friends
// pointing at a repository: the test packages that use git must leave that
// repository alone and still pass.
func TestInheritedRepositoryIsLeftAlone(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the test packages that use git")
	}
	decoy := t.TempDir()
	git(t, decoy, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(decoy, "keep.txt"), []byte("decoy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, decoy, "add", "keep.txt")
	git(t, decoy, "commit", "-q", "-m", "decoy")
	before := snapshot(t, decoy)

	cmd := exec.Command("go", "test", "-count=1", "-p", "2",
		"./internal/worktree", "./internal/gitsafe", "./internal/closer", "./internal/spawn", "./internal/review", "./internal/orch")
	cmd.Dir = filepath.Join("..", "..")
	cmd.Env = append(Without(os.Environ()),
		"GIT_DIR="+filepath.Join(decoy, ".git"), "GIT_WORK_TREE="+decoy, "GIT_INDEX_FILE="+filepath.Join(decoy, ".git", "index"),
		"XDG_STATE_HOME="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Errorf("tests under an inherited GIT_DIR: %v\n%s", err, out)
	}
	if after := snapshot(t, decoy); after != before {
		t.Errorf("the inherited repository was changed:\n--- before\n%s\n--- after\n%s", before, after)
	}
}

// A run has a state folder of its own, so no test writes the human's
// window markers, view record or colours.
func TestMainGivesTheRunItsOwnStateFolder(t *testing.T) {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" || !strings.HasPrefix(filepath.Base(dir), StatePrefix) {
		t.Fatalf("XDG_STATE_HOME = %q: the run would write the human's state", dir)
	}
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(dir, filepath.Join(home, ".local")) {
		t.Errorf("the state folder %q is the human's", dir)
	}
}
