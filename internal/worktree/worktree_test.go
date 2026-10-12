package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/gitsafe/gitsafetest"
)

var ctx = context.Background()

func sh(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Test", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// repo makes a repository on main with one commit, and a worktree the way
// Claude Code makes them: .claude/worktrees/<name> on worktree-<name>.
func repo(t *testing.T) (main, wt string) {
	t.Helper()
	main = t.TempDir()
	if r, err := filepath.EvalSymlinks(main); err == nil {
		main = r
	}
	sh(t, main, "init", "-q", "-b", "main")
	// Den's own git calls carry no identity, and a CI runner has none.
	sh(t, main, "config", "user.name", "Test")
	sh(t, main, "config", "user.email", "t@example.com")
	write(t, filepath.Join(main, "app.txt"), "one\ntwo\nthree\n")
	sh(t, main, "add", ".")
	sh(t, main, "commit", "-q", "-m", "first")
	wt = filepath.Join(main, ".claude", "worktrees", "mossy")
	sh(t, main, "worktree", "add", "-q", wt, "-b", "worktree-mossy")
	return main, wt
}

func commit(t *testing.T, dir, file, text, msg string) {
	t.Helper()
	write(t, filepath.Join(dir, file), text)
	sh(t, dir, "add", file)
	sh(t, dir, "commit", "-q", "-m", msg)
}

func TestMainRootMapsAWorktreeToItsRepository(t *testing.T) {
	main, wt := repo(t)
	sub := filepath.Join(wt, "pkg", "x")
	os.MkdirAll(sub, 0o755)
	for _, dir := range []string{main, wt, sub} {
		if got := MainRoot(ctx, dir); got != main {
			t.Errorf("MainRoot(%s) = %q, want %q", dir, got, main)
		}
	}
	if !Linked(wt) || Linked(main) {
		t.Errorf("Linked: worktree %v, main %v", Linked(wt), Linked(main))
	}
	if got := MainRoot(ctx, t.TempDir()); got != "" {
		t.Errorf("MainRoot outside a repository = %q", got)
	}
}

func TestPlanAndMergeFastForward(t *testing.T) {
	main, wt := repo(t)
	commit(t, wt, "feature.txt", "x\ny\n", "Add the feature")
	commit(t, wt, "app.txt", "one\ntwo\nthree\nfour\n", "Extend app")
	// Claude's own worktree folder is untracked in the main checkout and must not block.
	p, err := Plan(ctx, wt)
	if err != nil {
		t.Fatal(err)
	}
	if !p.FastForward || len(p.Conflicts) > 0 || p.Branch != "worktree-mossy" || p.Target != "main" || p.CommitCount != 2 || len(p.Commits) != 2 || len(p.Files) != 2 {
		t.Fatalf("plan = %+v", p)
	}
	text := strings.Join(p.Describe(), "\n")
	for _, want := range []string{"merge worktree-mossy into main", "fast-forward", "2 commits", "Add the feature", "feature.txt +2 −0", "app.txt +1 −0"} {
		if !strings.Contains(text, want) {
			t.Errorf("describe lacks %q:\n%s", want, text)
		}
	}
	if err := Merge(ctx, p); err != nil {
		t.Fatal(err)
	}
	if got := sh(t, main, "rev-parse", "HEAD"); got != p.BranchHead {
		t.Errorf("main HEAD = %s, want the branch tip %s", got, p.BranchHead)
	}
	if st := sh(t, main, "status", "--porcelain", "--untracked-files=no"); st != "" {
		t.Errorf("main is dirty after the merge: %s", st)
	}
	if _, err := Plan(ctx, wt); err == nil {
		t.Error("planned a merge of a branch that is already merged")
	} else if why, ok := IsRefusal(err); !ok || !strings.Contains(why, "no commits") {
		t.Errorf("err = %v", err)
	}
}

func TestMergeCommitWhenBothMoved(t *testing.T) {
	main, wt := repo(t)
	commit(t, wt, "feature.txt", "x\n", "Add the feature")
	commit(t, main, "other.txt", "o\n", "Main moves on")
	p, err := Plan(ctx, wt)
	if err != nil {
		t.Fatal(err)
	}
	if p.FastForward || len(p.Conflicts) > 0 {
		t.Fatalf("plan = %+v", p)
	}
	if !strings.Contains(strings.Join(p.Describe(), "\n"), "a merge commit") {
		t.Errorf("describe: %v", p.Describe())
	}
	if err := Merge(ctx, p); err != nil {
		t.Fatal(err)
	}
	if parents := strings.Fields(sh(t, main, "rev-list", "--parents", "-n", "1", "HEAD")); len(parents) != 3 {
		t.Errorf("not a merge commit: %v", parents)
	}
	for _, f := range []string{"feature.txt", "other.txt"} {
		if _, err := os.Stat(filepath.Join(main, f)); err != nil {
			t.Errorf("%s missing after the merge", f)
		}
	}
}

func TestConflictIsNamedAndNothingChanges(t *testing.T) {
	main, wt := repo(t)
	commit(t, wt, "app.txt", "one\nTWO FROM AGENT\nthree\n", "Agent edit")
	commit(t, main, "app.txt", "one\ntwo from main\nthree\n", "Main edit")
	head := sh(t, main, "rev-parse", "HEAD")
	p, err := Plan(ctx, wt)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Conflicts) != 1 || p.Conflicts[0] != "app.txt" {
		t.Fatalf("conflicts = %v", p.Conflicts)
	}
	if !strings.Contains(strings.Join(p.Describe(), "\n"), "conflicts in: app.txt") {
		t.Errorf("describe: %v", p.Describe())
	}
	err = Merge(ctx, p)
	if why, ok := IsRefusal(err); !ok || !strings.Contains(why, "conflict") {
		t.Fatalf("merge err = %v", err)
	}
	if sh(t, main, "rev-parse", "HEAD") != head || sh(t, main, "status", "--porcelain", "--untracked-files=no") != "" {
		t.Error("the main checkout changed")
	}
	if _, err := os.Stat(filepath.Join(main, ".git", "MERGE_HEAD")); err == nil {
		t.Error("a merge was left in progress")
	}
}

func TestMainCheckoutMustBeCleanOnABranch(t *testing.T) {
	main, wt := repo(t)
	commit(t, wt, "feature.txt", "x\n", "Add the feature")

	// Only Claude's worktree folders may be untracked.
	if _, err := Plan(ctx, wt); err != nil {
		t.Fatalf("clean main refused: %v", err)
	}
	cases := map[string]func(){
		"untracked": func() { write(t, filepath.Join(main, "notes.txt"), "n\n") },
		"unstaged":  func() { write(t, filepath.Join(main, "app.txt"), "changed\n") },
		"staged": func() {
			write(t, filepath.Join(main, "app.txt"), "staged\n")
			sh(t, main, "add", "app.txt")
		},
	}
	for name, dirty := range cases {
		t.Run(name, func(t *testing.T) {
			dirty()
			defer func() {
				os.Remove(filepath.Join(main, "notes.txt"))
				sh(t, main, "checkout", "-q", "HEAD", "--", "app.txt")
			}()
			_, err := Plan(ctx, wt)
			why, ok := IsRefusal(err)
			if !ok || !strings.Contains(why, "uncommitted changes") {
				t.Fatalf("err = %v", err)
			}
		})
	}
	sh(t, main, "checkout", "-q", "--detach")
	if _, err := Plan(ctx, wt); err == nil || !strings.Contains(err.Error(), "detached HEAD") {
		t.Errorf("detached main: err = %v", err)
	}
}

func TestPlanRefusesWorktreeWithoutABranchOrCommits(t *testing.T) {
	main, wt := repo(t)
	if _, err := Plan(ctx, wt); err == nil || !strings.Contains(err.Error(), "no commits") {
		t.Errorf("no commits: err = %v", err)
	}
	if _, err := Plan(ctx, main); err == nil || !strings.Contains(err.Error(), "not a linked") {
		t.Errorf("main checkout: err = %v", err)
	}
	sh(t, wt, "checkout", "-q", "--detach")
	if _, err := Plan(ctx, wt); err == nil || !strings.Contains(err.Error(), "detached HEAD") {
		t.Errorf("detached worktree: err = %v", err)
	}
}

func TestMergeRefusesWhenSomethingMovedSinceTheQuestion(t *testing.T) {
	main, wt := repo(t)
	commit(t, wt, "feature.txt", "x\n", "Add the feature")
	p, err := Plan(ctx, wt)
	if err != nil {
		t.Fatal(err)
	}
	commit(t, main, "other.txt", "o\n", "Main moves")
	if err := Merge(ctx, p); err == nil || !strings.Contains(err.Error(), "has moved") {
		t.Fatalf("main moved: err = %v", err)
	}
	p, _ = Plan(ctx, wt)
	commit(t, wt, "more.txt", "m\n", "One more")
	if err := Merge(ctx, p); err == nil || !strings.Contains(err.Error(), "new commits") {
		t.Fatalf("branch moved: err = %v", err)
	}
	p, _ = Plan(ctx, wt)
	write(t, filepath.Join(main, "stray.txt"), "s\n")
	if err := Merge(ctx, p); err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("main dirtied: err = %v", err)
	}
}

func TestUncommittedWorkInTheWorktreeIsMentioned(t *testing.T) {
	_, wt := repo(t)
	commit(t, wt, "feature.txt", "x\n", "Add the feature")
	write(t, filepath.Join(wt, "wip.txt"), "wip\n")
	p, err := Plan(ctx, wt)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Uncommitted) != 1 || !strings.Contains(strings.Join(p.Describe(), "\n"), "1 uncommitted change;") {
		t.Errorf("uncommitted = %v", p.Uncommitted)
	}
}

func never(string) bool  { return false }
func always(string) bool { return true }

func TestCleanupRemovesAMergedWorktreeAndItsBranch(t *testing.T) {
	main, wt := repo(t)
	commit(t, wt, "feature.txt", "x\n", "Add the feature")
	mp, _ := Plan(ctx, wt)
	if err := Merge(ctx, mp); err != nil {
		t.Fatal(err)
	}
	if _, err := CleanupPlanFor(ctx, wt, always); err == nil || !strings.Contains(err.Error(), "still running") {
		t.Errorf("live agent: err = %v", err)
	}
	p, err := CleanupPlanFor(ctx, wt, never)
	if err != nil || !p.Merged {
		t.Fatalf("plan = %+v err=%v", p, err)
	}
	if err := Cleanup(ctx, p, never); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wt); err == nil {
		t.Error("worktree still there")
	}
	if out := sh(t, main, "branch", "--list", "worktree-mossy"); out != "" {
		t.Errorf("branch survived: %q", out)
	}
}

func TestCleanupKeepsAnUnmergedBranchAndRefusesUncommittedWork(t *testing.T) {
	main, wt := repo(t)
	commit(t, wt, "feature.txt", "x\n", "Add the feature")
	write(t, filepath.Join(wt, "wip.txt"), "wip\n")
	if _, err := CleanupPlanFor(ctx, wt, never); err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("dirty worktree: err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, "wip.txt")); err != nil {
		t.Fatal("the uncommitted file is gone")
	}
	os.Remove(filepath.Join(wt, "wip.txt"))
	p, err := CleanupPlanFor(ctx, wt, never)
	if err != nil || p.Merged {
		t.Fatalf("plan = %+v err=%v", p, err)
	}
	if !strings.Contains(strings.Join(p.Describe(), "\n"), "is not merged into main and stays") {
		t.Errorf("describe: %v", p.Describe())
	}
	if err := Cleanup(ctx, p, never); err != nil {
		t.Fatal(err)
	}
	if out := sh(t, main, "branch", "--list", "worktree-mossy"); out == "" {
		t.Error("an unmerged branch was deleted")
	}
}

func TestCleanupReleasesOnlyADeadLock(t *testing.T) {
	main, wt := repo(t)
	// A live process: this test.
	sh(t, main, "worktree", "lock", "--reason", "claude session mossy (pid "+itoa(os.Getpid())+" start 1)", wt)
	if _, err := CleanupPlanFor(ctx, wt, never); err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("live lock: err = %v", err)
	}
	// A process that has ended.
	cmd := exec.Command("true")
	cmd.Run()
	dead := cmd.Process.Pid
	sh(t, main, "worktree", "unlock", wt)
	sh(t, main, "worktree", "lock", "--reason", "claude session mossy (pid "+itoa(dead)+" start 1)", wt)
	p, err := CleanupPlanFor(ctx, wt, never)
	if err != nil || !p.Locked || p.LockPID != dead {
		t.Fatalf("plan = %+v err=%v", p, err)
	}
	if err := Cleanup(ctx, p, never); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wt); err == nil {
		t.Error("worktree still there")
	}
	// A lock with no pid in it is somebody else's.
	wt2 := filepath.Join(main, ".claude", "worktrees", "other")
	sh(t, main, "worktree", "add", "-q", wt2, "-b", "other")
	sh(t, main, "worktree", "lock", "--reason", "by hand", wt2)
	if _, err := CleanupPlanFor(ctx, wt2, never); err == nil || !strings.Contains(err.Error(), "does not say") {
		t.Errorf("hand lock: err = %v", err)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func marks(t *testing.T, dir string) []string {
	t.Helper()
	ents, _ := os.ReadDir(dir)
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

// A merge that needs a merge driver the repository's config names is
// refused, and checking it runs nothing; plain git would run the driver.
func TestPlanRefusesARepositoryWithItsOwnMergeDriver(t *testing.T) {
	main, wt := repo(t)
	marked := t.TempDir()
	commit(t, wt, "app.txt", "ONE\ntwo\nthree\n", "Change the first line")
	commit(t, main, "app.txt", "one\ntwo\nTHREE\n", "Change the last line")
	sh(t, main, "config", "merge.evil.driver", "touch "+filepath.Join(marked, "merge-driver")+"; git merge-file %A %O %B")
	write(t, filepath.Join(main, ".git", "info", "attributes"), "*.txt merge=evil\n")
	_, err := Plan(ctx, wt)
	if why, ok := IsRefusal(err); !ok || !strings.Contains(why, "merge driver (evil)") || !strings.Contains(why, "merge it yourself") {
		t.Fatalf("err = %v", err)
	}
	if got := marks(t, marked); len(got) > 0 {
		t.Fatalf("planning ran %v", got)
	}
	// The driver is live: plain git runs it.
	sh(t, main, "merge-tree", "--write-tree", "main", "worktree-mossy")
	if got := marks(t, marked); len(got) == 0 {
		t.Error("the driver never ran under plain git")
	}
}

// Merging, by fast-forward or with a merge commit, and removing the
// worktree run none of the repository's hooks, and sign nothing.
func TestMergeAndCleanupRunNoHook(t *testing.T) {
	for _, ff := range []bool{true, false} {
		name := "merge-commit"
		if ff {
			name = "fast-forward"
		}
		t.Run(name, func(t *testing.T) {
			main, wt := repo(t)
			marked := t.TempDir()
			commit(t, wt, "feature.txt", "x\n", "Add the feature")
			if !ff {
				commit(t, main, "other.txt", "o\n", "Main moves on")
			}
			gitsafetest.Hooks(t, filepath.Join(main, ".git", "hooks"), marked)
			gitsafetest.Signer(t, main, marked)
			p, err := Plan(ctx, wt)
			if err != nil || p.FastForward != ff {
				t.Fatalf("plan = %+v err=%v", p, err)
			}
			if err := Merge(ctx, p); err != nil {
				t.Fatal(err)
			}
			if parents := strings.Fields(sh(t, main, "rev-list", "--parents", "-n", "1", "HEAD")); (len(parents) == 3) == ff {
				t.Errorf("parents of HEAD: %v", parents)
			}
			cp, err := CleanupPlanFor(ctx, wt, never)
			if err != nil || !cp.Merged {
				t.Fatalf("cleanup plan = %+v err=%v", cp, err)
			}
			if err := Cleanup(ctx, cp, never); err != nil {
				t.Fatal(err)
			}
			if out := sh(t, main, "branch", "--list", "worktree-mossy"); out != "" {
				t.Errorf("branch survived: %q", out)
			}
			if got := marks(t, marked); len(got) > 0 {
				t.Errorf("the repository made git run %v", got)
			}
			// The hooks and the signer are live: plain git runs them (and
			// fails, as the signer signs nothing).
			exec.Command("git", "-C", main, "commit", "-q", "--allow-empty", "-m", "plain").Run()
			got := strings.Join(marks(t, marked), " ")
			for _, want := range []string{"hook-commit-msg", "signer"} {
				if !strings.Contains(got, want) {
					t.Errorf("%s never ran under plain git (ran: %s)", want, got)
				}
			}
		})
	}
}

// git deletes ignored files with a worktree, so the question names them,
// and an ignored file it did not name means asking again.
func TestCleanupNamesTheIgnoredFilesItDeletes(t *testing.T) {
	_, wt := repo(t)
	commit(t, wt, ".gitignore", ".env*\nbuild/\n", "Ignore local files")
	write(t, filepath.Join(wt, ".env"), "TOKEN=x\n")
	write(t, filepath.Join(wt, "build", "out.bin"), "b\n")
	p, err := CleanupPlanFor(ctx, wt, never)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(p.Ignored, " ") != ".env build/" {
		t.Errorf("ignored = %q", p.Ignored)
	}
	text := strings.Join(p.Describe(), "\n")
	if !strings.Contains(text, "2 ignored files or folders will be deleted with it") || !strings.Contains(text, ".env, build/") {
		t.Errorf("describe:\n%s", text)
	}
	write(t, filepath.Join(wt, "build", "more.bin"), "m\n") // inside a folder it named
	write(t, filepath.Join(wt, ".env.local"), "x\n")
	if err := Cleanup(ctx, p, never); !IsMoved(err) || !strings.Contains(err.Error(), ".env.local") {
		t.Fatalf("a new ignored file: err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, ".env")); err != nil {
		t.Fatal("the worktree went without asking about .env.local")
	}
	p, err = CleanupPlanFor(ctx, wt, never)
	if err != nil || len(p.Ignored) != 3 {
		t.Fatalf("plan = %+v err=%v", p, err)
	}
	if err := Cleanup(ctx, p, never); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wt); err == nil {
		t.Error("worktree still there")
	}
}
