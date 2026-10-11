package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
)

// wtRepo is a repository on main with a worktree made the way Claude Code
// makes them, and one commit on the worktree's branch.
func wtRepo(t *testing.T) (main, wt, forkedFrom string) {
	t.Helper()
	main = t.TempDir()
	if r, err := filepath.EvalSymlinks(main); err == nil {
		main = r
	}
	gitRun(t, main, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(main, "a.go"), []byte("package a\n\nfunc A() {}\n"), 0o644)
	gitRun(t, main, "add", ".")
	gitRun(t, main, "-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "-m", "init")
	forkedFrom = gitOut(t, main, "rev-parse", "HEAD")
	wt = filepath.Join(main, ".claude", "worktrees", "mossy")
	gitRun(t, main, "worktree", "add", "-q", wt, "-b", "worktree-mossy")
	os.WriteFile(filepath.Join(wt, "a.go"), []byte("package a\n\nfunc A() { B() }\n"), 0o644)
	gitRun(t, wt, "-c", "user.name=t", "-c", "user.email=t@x", "commit", "-qam", "Call B from A")
	return main, wt, forkedFrom
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// worktreeTranscript is a session that ran `claude --worktree mossy` and
// edited a.go there.
func worktreeTranscript(t *testing.T, main, wt, forkedFrom string) string {
	t.Helper()
	file := filepath.Join(wt, "a.go")
	at := time.Now().UTC().Format(time.RFC3339)
	path := filepath.Join(t.TempDir(), "s.jsonl")
	os.WriteFile(path, []byte(strings.Join([]string{
		`{"type":"worktree-state","worktreeSession":{"originalCwd":"` + main + `","worktreePath":"` + wt + `","worktreeName":"mossy","worktreeBranch":"worktree-mossy","originalBranch":"main","originalHeadCommit":"` + forkedFrom + `","sessionId":"s1"},"sessionId":"s1"}`,
		`{"type":"assistant","timestamp":"` + at + `","message":{"content":[{"type":"tool_use","id":"t1","name":"Edit","input":{"file_path":"` + file + `"}}]}}`,
		`{"type":"user","timestamp":"` + at + `","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]},"toolUseResult":{"filePath":"` + file + `","structuredPatch":[{"oldStart":3,"newStart":3,"lines":["-func A() {}","+func A() { B() }"]}]}}`,
	}, "\n")+"\n"), 0o644)
	return path
}

func TestAWorktreeBelongsToItsProjectButIsReviewedInsideTheWorktree(t *testing.T) {
	main, wt, forkedFrom := wtRepo(t)
	// Project grouping: the worktree is the repository it was made from.
	if got := gitRootOf(wt); got != main {
		t.Errorf("gitRootOf(worktree) = %q, want %q", got, main)
	}
	a := fleet.Agent{Session: sample()[0].Session, HasGit: true, Path: worktreeTranscript(t, main, wt, forkedFrom)}
	a.Session.CWD = wt
	a.Session.StartedAt = time.Now().Add(-time.Hour).UnixMilli()
	if roots := rootsOf(a); len(roots) != 1 || roots[0] != main {
		t.Errorf("rootsOf = %v, want [%s]", roots, main)
	}

	// Review: the diff is the worktree's, on its branch, from the commit it
	// was made from.
	msg := loadWorks([]reviewInput{{SessionID: "s1", Name: "Mossy", CWD: wt, HasGit: true, Path: a.Path, Started: time.Now().Add(-time.Hour)}})
	if len(msg.errs) > 0 || len(msg.works) != 1 {
		t.Fatalf("works = %+v errs=%v note=%q", msg.works, msg.errs, msg.note)
	}
	w := msg.works[0]
	if w.Root != wt || w.Main != main || !w.Worktree || w.Branch != "worktree-mossy" || w.Ahead != 1 || w.project() != main {
		t.Errorf("work = %+v", w)
	}
	if len(w.Files) != 1 || w.Files[0].Path != "a.go" || w.Added != 1 || w.Removed != 1 {
		t.Errorf("files = %+v added=%d removed=%d", w.Files, w.Added, w.Removed)
	}
	// Main moves on after the fork: the diff still starts at the fork.
	os.WriteFile(filepath.Join(main, "other.txt"), []byte("o\n"), 0o644)
	gitRun(t, main, "add", ".")
	gitRun(t, main, "-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "-m", "main moves")
	msg = loadWorks([]reviewInput{{SessionID: "s1", CWD: wt, HasGit: true, Path: a.Path, Started: time.Now().Add(-time.Hour)}})
	if len(msg.works) != 1 || len(msg.works[0].Files) != 1 {
		t.Errorf("after main moved: %+v", msg.works)
	}

	// The project pick on the main repository shows the agent's work.
	m := missionModel()
	m.proj.cur = main
	m.applyReview(msg)
	if len(m.rev.works) != 1 {
		t.Errorf("the project pick on %s lost the worktree's work: %d", main, len(m.rev.works))
	}
	m.proj.cur = "/elsewhere"
	m.applyReview(msg)
	if len(m.rev.works) != 0 {
		t.Error("another project shows it")
	}
	_ = forkedFrom
}

// worktreeModel is a Review view with one agent working in the worktree.
func worktreeModel(t *testing.T, main, wt string) Model {
	m := missionModel()
	m.tab = tabReview
	sid := m.base[0].Session.SessionID
	m.base[0].Session.CWD = "/home/x/tasklog" // not in the worktree: nobody is running there
	m.rev.works = []work{{SessionID: sid, Root: wt, Main: main, Repo: "demo · worktree-mossy", Worktree: true, Branch: "worktree-mossy", Ahead: 1}}
	m.rev.loadedAt = time.Now()
	m.refresh()
	return m
}

func ctrl(m Model, k rune) Model {
	mm, _ := m.handleKey(tea.KeyPressMsg{Code: k, Mod: tea.ModCtrl})
	return mm.(Model)
}

func TestMergeIsOneInboxQuestionAndCtrlYMerges(t *testing.T) {
	main, wt, _ := wtRepo(t)
	m := worktreeModel(t, main, wt)
	branchTip := gitOut(t, wt, "rev-parse", "HEAD")

	m = run(t, m, "/merge")
	if m.tab != tabInbox || m.store.Waiting() != 1 {
		t.Fatalf("tab=%v waiting=%d flash=%q", m.tab, m.store.Waiting(), m.flash)
	}
	it := m.inboxSelected()
	if it == nil || it.Kind != inbox.Merge || it.Title != "Merge worktree-mossy into main?" {
		t.Fatalf("item = %+v", it)
	}
	text := strings.Join(it.Body, "\n")
	for _, want := range []string{"merge worktree-mossy into main", "fast-forward", "1 commit:", "Call B from A", "a.go +1 −1"} {
		if !strings.Contains(text, want) {
			t.Errorf("question lacks %q:\n%s", want, text)
		}
	}
	out := ansi.Strip(m.View().Content)
	if !strings.Contains(out, "MERGE") || !strings.Contains(out, "^Y merge it") {
		t.Errorf("inbox view:\n%s", out)
	}
	// Asking again does not pile up questions.
	m.tab = tabReview
	if m = run(t, m, "/merge"); m.store.Waiting() != 1 {
		t.Errorf("a second question was added: %d", m.store.Waiting())
	}
	if gitOut(t, main, "rev-parse", "HEAD") == branchTip {
		t.Fatal("the question merged already")
	}

	m = ctrl(m, 'y')
	if got := gitOut(t, main, "rev-parse", "HEAD"); got != branchTip {
		t.Errorf("main = %s, want %s", got, branchTip)
	}
	if !strings.Contains(it.Resolved, "fast-forwarded worktree-mossy into main") {
		t.Errorf("resolved = %q", it.Resolved)
	}
	// Removing the worktree is a second, separate question, and nothing is removed yet.
	if _, err := os.Stat(wt); err != nil {
		t.Fatal("the worktree went with the merge")
	}
	next := m.inboxSelected()
	if next == nil || next.Kind != inbox.Cleanup || m.store.Waiting() != 1 {
		t.Fatalf("no cleanup question: %+v waiting=%d flash=%q", next, m.store.Waiting(), m.flash)
	}
	if !strings.Contains(strings.Join(next.Body, "\n"), "is merged into main and will be deleted with it") {
		t.Errorf("cleanup body: %v", next.Body)
	}
	m = ctrl(m, 'y')
	if _, err := os.Stat(wt); err == nil {
		t.Error("the worktree is still there")
	}
	if out := gitOut(t, main, "branch", "--list", "worktree-mossy"); out != "" {
		t.Errorf("branch survived: %q", out)
	}
	if !strings.Contains(next.Resolved, "removed the worktree and deleted worktree-mossy") {
		t.Errorf("resolved = %q", next.Resolved)
	}
}

func TestMergeThatConflictsIsRefusedAndLeavesTheCheckoutAlone(t *testing.T) {
	main, wt, _ := wtRepo(t)
	os.WriteFile(filepath.Join(main, "a.go"), []byte("package a\n\nfunc A() { C() }\n"), 0o644)
	gitRun(t, main, "-c", "user.name=t", "-c", "user.email=t@x", "commit", "-qam", "Main calls C")
	head := gitOut(t, main, "rev-parse", "HEAD")
	m := worktreeModel(t, main, wt)
	m = run(t, m, "/merge")
	it := m.inboxSelected()
	if it == nil || it.Kind != inbox.Merge || !strings.Contains(strings.Join(it.Body, "\n"), "conflicts in: a.go") {
		t.Fatalf("item = %+v flash=%q", it, m.flash)
	}
	m = ctrl(m, 'y')
	if !strings.Contains(m.flash, "conflict") || !m.flashErr {
		t.Errorf("flash = %q", m.flash)
	}
	if it.State != inbox.Waiting {
		t.Error("the question was closed by a refusal")
	}
	if gitOut(t, main, "rev-parse", "HEAD") != head || gitOut(t, main, "status", "--porcelain", "--untracked-files=no") != "" {
		t.Error("the main checkout changed")
	}
	m = ctrl(m, 'n')
	if it.State != inbox.Resolved || !strings.Contains(it.Resolved, "declined") {
		t.Errorf("^N: %+v", it)
	}
}

func TestMergeAsksNothingWhileTheMainCheckoutIsDirtyOrDetached(t *testing.T) {
	main, wt, _ := wtRepo(t)
	os.WriteFile(filepath.Join(main, "notes.txt"), []byte("n\n"), 0o644)
	m := worktreeModel(t, main, wt)
	m = run(t, m, "/merge")
	if m.store.Waiting() != 0 || !m.flashErr || !strings.Contains(m.flash, "uncommitted changes") || !strings.Contains(m.flash, "notes.txt") {
		t.Errorf("dirty: waiting=%d flash=%q", m.store.Waiting(), m.flash)
	}
	os.Remove(filepath.Join(main, "notes.txt"))
	gitRun(t, main, "checkout", "-q", "--detach")
	m = run(t, m, "/merge")
	if m.store.Waiting() != 0 || !strings.Contains(m.flash, "detached HEAD") {
		t.Errorf("detached: waiting=%d flash=%q", m.store.Waiting(), m.flash)
	}
}

func TestMergeQuestionGoesStaleWhenTheBranchMoves(t *testing.T) {
	main, wt, _ := wtRepo(t)
	m := worktreeModel(t, main, wt)
	m = run(t, m, "/merge")
	it := m.inboxSelected()
	head := gitOut(t, main, "rev-parse", "HEAD")
	os.WriteFile(filepath.Join(wt, "more.txt"), []byte("m\n"), 0o644)
	gitRun(t, wt, "add", ".")
	gitRun(t, wt, "-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "-m", "One more")
	m = ctrl(m, 'y')
	if it.State != inbox.Resolved || !strings.Contains(it.Resolved, "new commits") || gitOut(t, main, "rev-parse", "HEAD") != head {
		t.Errorf("item = %+v flash=%q", it, m.flash)
	}
	if !strings.Contains(m.flash, "/merge asks again") {
		t.Errorf("flash = %q", m.flash)
	}
}

func TestCleanupWaitsForTheAgentAndNeverTouchesUncommittedWork(t *testing.T) {
	main, wt, _ := wtRepo(t)
	m := worktreeModel(t, main, wt)
	// The agent is still in the worktree.
	m.base[0].Session.CWD = filepath.Join(wt, "pkg")
	m.base[0].Status = fleet.Working
	m = run(t, m, "/cleanup")
	if m.store.Waiting() != 0 || !strings.Contains(m.flash, "still running") {
		t.Errorf("live: waiting=%d flash=%q", m.store.Waiting(), m.flash)
	}
	m.base[0].Session.CWD = "/home/x/tasklog"
	os.WriteFile(filepath.Join(wt, "wip.txt"), []byte("wip\n"), 0o644)
	m = run(t, m, "/cleanup")
	if m.store.Waiting() != 0 || !strings.Contains(m.flash, "uncommitted") {
		t.Errorf("dirty: waiting=%d flash=%q", m.store.Waiting(), m.flash)
	}
	if _, err := os.Stat(filepath.Join(wt, "wip.txt")); err != nil {
		t.Error("uncommitted work is gone")
	}
	os.Remove(filepath.Join(wt, "wip.txt"))
	// An unmerged branch is kept when the worktree goes.
	m = run(t, m, "/cleanup")
	it := m.inboxSelected()
	if it == nil || it.Kind != inbox.Cleanup || !strings.Contains(strings.Join(it.Body, "\n"), "is not merged into main and stays") {
		t.Fatalf("item = %+v flash=%q", it, m.flash)
	}
	m = ctrl(m, 'y')
	if _, err := os.Stat(wt); err == nil {
		t.Error("worktree still there")
	}
	if gitOut(t, main, "branch", "--list", "worktree-mossy") == "" {
		t.Error("an unmerged branch was deleted")
	}
}

func TestMergeNeedsAWorktreeAndASelection(t *testing.T) {
	main, wt, _ := wtRepo(t)
	m := worktreeModel(t, main, wt)
	m.rev.works[0].Worktree = false
	if m = run(t, m, "/merge"); !strings.Contains(m.flash, "does not work in a git worktree") {
		t.Errorf("flash = %q", m.flash)
	}
	m.tab = tabFleet
	if m = run(t, m, "/merge"); !strings.Contains(m.flash, "Open Review") {
		t.Errorf("flash = %q", m.flash)
	}
	if m = run(t, m, "/merge Nobody"); !strings.Contains(m.flash, "No agent called Nobody") {
		t.Errorf("flash = %q", m.flash)
	}
}

func TestWorktreeBaseIsTheCommitTheWorktreeWasMadeFrom(t *testing.T) {
	main, wt, forkedFrom := wtRepo(t)
	// A feature branch that main does not have, and a worktree made from it:
	// the default branch is the wrong base, the recorded commit is the right one.
	gitRun(t, main, "checkout", "-q", "-b", "feature")
	os.WriteFile(filepath.Join(main, "feature.txt"), []byte("f\n"), 0o644)
	gitRun(t, main, "add", ".")
	gitRun(t, main, "-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "-m", "feature work")
	featureHead := gitOut(t, main, "rev-parse", "HEAD")
	wt2 := filepath.Join(main, ".claude", "worktrees", "other")
	gitRun(t, main, "worktree", "add", "-q", wt2, "-b", "worktree-other")
	tr := worktreeTranscript(t, main, wt2, featureHead)

	ctx := t.Context()
	if got, err := worktreeBase(ctx, wt2, []string{tr}); err != nil || got != featureHead {
		t.Errorf("base = %q (%v), want %s", got, err, featureHead)
	}
	// A commit that is not in the worktree's history, a transcript from
	// another worktree, a folder that is not a worktree: no recorded base.
	bad := worktreeTranscript(t, main, wt2, strings.Repeat("0", 40))
	other := worktreeTranscript(t, main, wt, forkedFrom)
	for name, c := range map[string]struct {
		dir string
		tr  []string
	}{"unknown commit": {wt2, []string{bad}}, "other worktree": {wt2, []string{other}}, "not a worktree": {main, []string{tr}}, "no transcript": {wt2, nil}} {
		if got, err := worktreeBase(ctx, c.dir, c.tr); err == nil {
			t.Errorf("%s: base = %q, want none", name, got)
		}
	}
}
