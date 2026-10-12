package gitsafe

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/gitsafe/gitsafetest"
)

func marked(t *testing.T, marks string) []string {
	t.Helper()
	ents, _ := os.ReadDir(marks)
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

func TestNothingRuns(t *testing.T) {
	repo, marks := gitsafetest.Hostile(t)
	t.Setenv("GIT_DIR", "/nonexistent") // an inherited git variable is not used
	ctx := context.Background()
	for _, args := range [][]string{
		{"diff", "--no-color", "--no-ext-diff", "--no-textconv", "HEAD", "--"},
		{"status", "--porcelain", "--untracked-files=normal"},
		{"ls-files", "--others", "--exclude-standard", "-z"},
		{"merge-tree", "--write-tree", "--name-only", "--no-messages", "ours", "theirs"},
	} {
		out, err := Command(ctx, repo, args...).CombinedOutput()
		var ee *exec.ExitError
		if args[0] == "merge-tree" && errors.As(err, &ee) && ee.ExitCode() == 1 {
			err = nil // without its driver the file does not merge
		}
		if err != nil {
			t.Fatalf("%v: %v %s", args, err, out)
		}
		if args[0] == "diff" && !strings.Contains(string(out), "+b") {
			t.Errorf("diff lost the change: %s", out)
		}
	}
	if got := marked(t, marks); len(got) > 0 {
		t.Errorf("the repository made git run %v", got)
	}
	if got := MergeDrivers(ctx, repo); len(got) != 1 || got[0] != "evil" {
		t.Errorf("merge drivers = %v", got)
	}
	// The fixture is live: plain git runs the filter and the merge driver.
	for _, args := range [][]string{
		{"-c", "core.fsmonitor=false", "diff", "--no-ext-diff", "--no-textconv", "HEAD"},
		{"-c", "core.fsmonitor=false", "merge-tree", "--write-tree", "ours", "theirs"},
	} {
		plain := exec.Command("git", append([]string{"-C", repo}, args...)...)
		plain.Env = environ()
		plain.Run()
	}
	got := strings.Join(marked(t, marks), " ")
	if !strings.Contains(got, "process") && !strings.Contains(got, "clean") {
		t.Errorf("the fixture's filter never ran under plain git (ran: %s)", got)
	}
	if !strings.Contains(got, "merge-driver") {
		t.Errorf("the fixture's merge driver never ran under plain git (ran: %s)", got)
	}
}

// Change makes a merge commit and deletes a branch without running a hook
// or the signing program the repository has; plain git runs them.
func TestChangeRunsNoHook(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repo, marks := t.TempDir(), t.TempDir()
	ctx := context.Background()
	git := func(cmd *exec.Cmd) {
		t.Helper()
		cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "HOME="+repo)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v %s", cmd.Args, err, out)
		}
	}
	plain := func(args ...string) *exec.Cmd {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = os.Environ()
		return cmd
	}
	git(plain("init", "-q", "-b", "main"))
	git(plain("config", "user.email", "user@example.com"))
	git(plain("config", "user.name", "user"))
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a\n"), 0o644)
	git(plain("add", "."))
	git(plain("commit", "-qm", "init"))
	git(plain("checkout", "-qb", "side"))
	os.WriteFile(filepath.Join(repo, "b.txt"), []byte("b\n"), 0o644)
	git(plain("add", "."))
	git(plain("commit", "-qm", "side"))
	git(plain("checkout", "-q", "main"))
	gitsafetest.Hooks(t, filepath.Join(repo, ".git", "hooks"), marks)
	gitsafetest.Signer(t, repo, marks)

	git(Change(ctx, repo, "merge", "--no-edit", "--no-ff", "-m", "Merge side", "side"))
	git(Change(ctx, repo, "branch", "-d", "side"))
	if got := marked(t, marks); len(got) > 0 {
		t.Errorf("the repository made git run %v", got)
	}
	if out, _ := plain("rev-list", "--parents", "-n", "1", "HEAD").Output(); len(strings.Fields(string(out))) != 3 {
		t.Errorf("no merge commit: %s", out)
	}
	// The hooks and the signer are live; the signer makes plain git fail.
	plain("commit", "-q", "--allow-empty", "-m", "plain").Run()
	got := strings.Join(marked(t, marks), " ")
	for _, want := range []string{"hook-commit-msg", "signer"} {
		if !strings.Contains(got, want) {
			t.Errorf("%s never ran under plain git (ran: %s)", want, got)
		}
	}
}

// Emptying only the outer repository's filters is not enough: git diff
// looks into a submodule with the submodule's own config.
func TestSubmoduleFilterIsLive(t *testing.T) {
	repo, marks := gitsafetest.Hostile(t)
	cmd := exec.Command("git", "-C", repo, "diff", "--no-ext-diff", "--no-textconv", "HEAD", "--")
	cmd.Env = append(environ(), "GIT_CONFIG_COUNT=4",
		"GIT_CONFIG_KEY_0=core.fsmonitor", "GIT_CONFIG_VALUE_0=false",
		"GIT_CONFIG_KEY_1=filter.evil.clean", "GIT_CONFIG_VALUE_1=",
		"GIT_CONFIG_KEY_2=filter.evil.process", "GIT_CONFIG_VALUE_2=",
		"GIT_CONFIG_KEY_3=filter.a=b.clean", "GIT_CONFIG_VALUE_3=")
	cmd.Run()
	if _, err := os.Stat(filepath.Join(marks, "submodule-clean")); err != nil {
		t.Fatal("the fixture's submodule filter did not run without the submodule overrides; the test would prove nothing")
	}
}
