// Package worktree is what Den knows about git worktrees: which repository
// a worktree belongs to, and the two things a human decides about an
// agent's worktree, merging its branch and removing it. Both are planned
// first without changing anything (Plan, CleanupPlan), and carried out only
// after the plan is checked again (Merge, Cleanup), so a half-merged tree
// or a deleted piece of work is never the result.
package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/GlacierHubAB/den/internal/gitsafe"
)

// git runs git in dir. The repository's own config must not run programs
// while Den only looks, so reads use no filesystem monitor and take no
// optional locks.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	return output(gitsafe.Command(ctx, dir, args...), args)
}

func run(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir, "-c", "core.fsmonitor=false"}, args...)...)
	cmd.Env = append(os.Environ(), env...)
	return output(cmd, args)
}

func output(cmd *exec.Cmd, args []string) (string, error) {
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		return out.String(), &gitError{args: args, err: err, msg: msg}
	}
	return out.String(), nil
}

type gitError struct {
	args []string
	err  error
	msg  string
}

func (e *gitError) Error() string {
	return fmt.Sprintf("git %s: %v: %s", strings.Join(e.args, " "), e.err, e.msg)
}

func (e *gitError) Unwrap() error { return e.err }

// exitCode is git's exit status, -1 when it did not run.
func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	var ge *gitError
	if errors.As(err, &ge) {
		return exitCode(ge.err)
	}
	return -1
}

func trim(s string) string { return strings.TrimSpace(s) }

// MainRoot is the primary checkout of the repository dir belongs to: the
// folder above the shared .git directory. For a plain checkout that is its
// own top; for a linked worktree it is the checkout the worktree was
// made from. A bare repository has no checkout, so its git directory stands
// in. "" when dir is not in a repository.
func MainRoot(ctx context.Context, dir string) string {
	common, err := git(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return ""
	}
	common = trim(common)
	if filepath.Base(common) == ".git" {
		return filepath.Dir(common)
	}
	return common
}

// Linked reports whether dir is the top of a linked worktree: its .git is
// a file, not a directory.
func Linked(dir string) bool {
	st, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil && st.Mode().IsRegular()
}

// Branch is the branch checked out in dir, "" when HEAD is detached.
func Branch(ctx context.Context, dir string) string {
	out, err := git(ctx, dir, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return trim(out)
}

// gitDir is the worktree's private git directory (<common>/worktrees/<name>
// for a linked one).
func gitDir(ctx context.Context, dir string) string {
	out, err := git(ctx, dir, "rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil {
		return ""
	}
	return trim(out)
}

// A Refusal is a reason Den will not merge or remove, said the way the
// human reads it.
type Refusal string

func (r Refusal) Error() string { return string(r) }

// Moved is a Refusal that says the repository changed between the question
// and the answer: asking again is the way forward, not retrying the answer.
type Moved string

func (r Moved) Error() string { return string(r) }

// IsMoved reports whether err says something moved since the question.
func IsMoved(err error) bool {
	var m Moved
	return errors.As(err, &m)
}

// IsRefusal reports whether err is a Refusal and returns its text.
func IsRefusal(err error) (string, bool) {
	var r Refusal
	if errors.As(err, &r) {
		return string(r), true
	}
	return "", false
}

// File is one file of a merge, with its added and removed lines (-1 for a
// binary file).
type File struct {
	Path           string
	Added, Removed int
}

// MergePlan is what merging an agent's branch into the main checkout
// would do, worked out without changing anything.
type MergePlan struct {
	Main        string // the primary checkout, where the merge happens
	Worktree    string
	Branch      string // the agent's branch
	BranchHead  string // its tip when planned
	Target      string // the branch checked out in Main
	TargetHead  string // its tip when planned
	Commits     []string
	CommitCount int
	Files       []File
	FastForward bool     // Target is an ancestor of Branch: no merge commit
	Conflicts   []string // files that would conflict; empty when it merges cleanly
	// Uncommitted lists changes in the worktree that a merge does not carry.
	Uncommitted []string
}

// listed commits and files in a plan's text; the count says the rest.
const planListLimit = 12

// Plan works out the merge of the branch checked out in worktree into the
// branch checked out in its repository's primary checkout. It refuses
// (Refusal) when the merge could not be done safely right now: the primary
// checkout is not on a branch, has changes or an unfinished merge, the
// worktree is detached, the two share a branch, or there is nothing to merge.
func Plan(ctx context.Context, worktree string) (MergePlan, error) {
	p := MergePlan{Worktree: worktree}
	if !Linked(worktree) {
		return p, Refusal("that folder is not a linked git worktree")
	}
	p.Main = MainRoot(ctx, worktree)
	if p.Main == "" || p.Main == worktree {
		return p, Refusal("could not find the repository this worktree belongs to")
	}
	if p.Branch = Branch(ctx, worktree); p.Branch == "" {
		return p, Refusal("the worktree has no branch checked out (detached HEAD), so there is nothing to merge by name")
	}
	if p.Target = Branch(ctx, p.Main); p.Target == "" {
		return p, Refusal("the main checkout at " + p.Main + " is on a detached HEAD; check out the branch to merge into first")
	}
	if p.Target == p.Branch {
		return p, Refusal("the worktree and the main checkout are both on " + p.Branch)
	}
	if why := mainBlocked(ctx, p.Main); why != "" {
		return p, Refusal(why)
	}
	var err error
	if p.BranchHead, err = revParse(ctx, worktree, "HEAD"); err != nil {
		return p, err
	}
	if p.TargetHead, err = revParse(ctx, p.Main, "HEAD"); err != nil {
		return p, err
	}
	count, err := git(ctx, p.Main, "rev-list", "--count", p.TargetHead+".."+p.BranchHead)
	if err != nil {
		return p, err
	}
	p.CommitCount, _ = strconv.Atoi(trim(count))
	if p.CommitCount == 0 {
		return p, Refusal(p.Branch + " has no commits that " + p.Target + " does not already have")
	}
	log, err := git(ctx, p.Main, "log", "--format=%h %s", "-n", strconv.Itoa(planListLimit), p.TargetHead+".."+p.BranchHead)
	if err != nil {
		return p, err
	}
	p.Commits = lines(log)
	stat, err := git(ctx, p.Main, "diff", "--numstat", "-z", p.TargetHead+"..."+p.BranchHead)
	if err != nil {
		return p, err
	}
	p.Files = parseNumstat(stat)
	_, err = git(ctx, p.Main, "merge-base", "--is-ancestor", p.TargetHead, p.BranchHead)
	p.FastForward = err == nil
	if !p.FastForward {
		if code := exitCode(err); code != 1 {
			return p, err
		}
		if p.Conflicts, err = dryRun(ctx, p.Main, p.TargetHead, p.BranchHead); err != nil {
			return p, err
		}
	}
	if st, err := git(ctx, worktree, "status", "--porcelain=v1", "--untracked-files=all"); err == nil {
		p.Uncommitted = lines(st)
	}
	return p, nil
}

// Describe is the plan as the lines of an inbox question.
func (p MergePlan) Describe() []string {
	out := []string{"merge " + p.Branch + " into " + p.Target + " in " + p.Main}
	switch {
	case len(p.Conflicts) > 0:
		out = append(out, "conflicts in: "+strings.Join(p.Conflicts, ", ")+" — Den will not merge this")
	case p.FastForward:
		out = append(out, "fast-forward: no merge commit, "+p.Target+" just moves up to "+short(p.BranchHead))
	default:
		out = append(out, "a merge commit: both branches have moved since they split, and they combine without conflicts")
	}
	out = append(out, plural(p.CommitCount, "commit")+":")
	for _, c := range p.Commits {
		out = append(out, "  "+c)
	}
	if p.CommitCount > len(p.Commits) {
		out = append(out, fmt.Sprintf("  … and %d more", p.CommitCount-len(p.Commits)))
	}
	out = append(out, plural(len(p.Files), "file")+":")
	for i, f := range p.Files {
		if i == planListLimit*2 {
			out = append(out, fmt.Sprintf("  … and %d more", len(p.Files)-i))
			break
		}
		if f.Added < 0 {
			out = append(out, "  "+f.Path+" (binary)")
		} else {
			out = append(out, fmt.Sprintf("  %s +%d −%d", f.Path, f.Added, f.Removed))
		}
	}
	if len(p.Uncommitted) > 0 {
		out = append(out, "the worktree has "+plural(len(p.Uncommitted), "uncommitted change")+"; a merge does not carry them")
	}
	return out
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// Merge carries out p: it checks everything again (the primary checkout
// still clean and on the same branch at the same commit, the agent's
// branch at the same tip, the dry run still clean) and only then merges,
// with a fast-forward or a merge commit as the question said. If the merge
// fails anyway it is aborted, so the checkout is never left half-merged.
func Merge(ctx context.Context, p MergePlan) error {
	now, err := Plan(ctx, p.Worktree)
	if err != nil {
		return err
	}
	switch {
	case now.Target != p.Target:
		return Moved("the main checkout is on " + now.Target + " now, not " + p.Target + "; ask again")
	case now.TargetHead != p.TargetHead:
		return Moved(p.Target + " has moved since you were asked; ask again")
	case now.BranchHead != p.BranchHead:
		return Moved(p.Branch + " has new commits since you were asked; ask again")
	case len(now.Conflicts) > 0:
		return Refusal("it would conflict in " + strings.Join(now.Conflicts, ", "))
	}
	args := []string{"merge", "--no-edit", "--ff-only", p.BranchHead}
	if !p.FastForward {
		args = []string{"merge", "--no-edit", "--no-ff", "-m", "Merge branch '" + p.Branch + "'", p.BranchHead}
	}
	if _, err := run(ctx, p.Main, nil, args...); err != nil {
		abort := ""
		if _, aerr := run(ctx, p.Main, nil, "merge", "--abort"); aerr == nil {
			abort = "; the merge was aborted and nothing changed"
		}
		return fmt.Errorf("%w%s", err, abort)
	}
	return nil
}

// mainBlocked says why the primary checkout cannot take a merge now, ""
// when it can. Untracked folders under .claude/worktrees are Claude Code's
// own worktrees and do not count; every other change does.
func mainBlocked(ctx context.Context, main string) string {
	for _, f := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply"} {
		out, err := git(ctx, main, "rev-parse", "--path-format=absolute", "--git-path", f)
		if err != nil {
			continue
		}
		if _, err := os.Stat(trim(out)); err == nil {
			return "the main checkout at " + main + " is in the middle of a merge, rebase or cherry-pick; finish or abort it first"
		}
	}
	out, err := git(ctx, main, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return "could not read the state of the main checkout: " + err.Error()
	}
	var dirty []string
	for _, e := range splitZ(out) {
		if len(e) < 4 {
			continue
		}
		path := e[3:]
		if strings.HasPrefix(e, "?? ") && strings.HasPrefix(path, ".claude/worktrees/") {
			continue
		}
		dirty = append(dirty, e)
	}
	if len(dirty) == 0 {
		return ""
	}
	first := dirty[0]
	why := fmt.Sprintf("the main checkout at %s has uncommitted changes (%d, first: %s)", main, len(dirty), strings.TrimSpace(first))
	return why + "; commit, stash or discard them first"
}

// splitZ splits NUL-terminated porcelain output. A rename carries its old
// path as a separate entry, which is dropped.
func splitZ(s string) []string {
	var out []string
	parts := strings.Split(s, "\x00")
	for i := 0; i < len(parts); i++ {
		e := parts[i]
		if e == "" {
			continue
		}
		out = append(out, e)
		if len(e) > 1 && (e[0] == 'R' || e[0] == 'C' || e[1] == 'R' || e[1] == 'C') {
			i++
		}
	}
	return out
}

// dryRun merges the two commits in memory and returns the conflicting
// files, none when they combine cleanly. It writes only objects, never the
// index or a working tree.
func dryRun(ctx context.Context, dir, target, branch string) ([]string, error) {
	out, err := git(ctx, dir, "merge-tree", "--write-tree", "--name-only", "--no-messages", target, branch)
	switch exitCode(err) {
	case 0:
		return nil, nil
	case 1:
		ls := lines(out)
		if len(ls) > 0 {
			ls = ls[1:] // the tree id
		}
		if len(ls) == 0 {
			ls = []string{"(unknown file)"}
		}
		return ls, nil
	}
	return nil, err
}

func revParse(ctx context.Context, dir, rev string) (string, error) {
	out, err := git(ctx, dir, "rev-parse", "--verify", rev+"^{commit}")
	return trim(out), err
}

func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimRight(l, "\r"); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func parseNumstat(z string) []File {
	var out []File
	parts := strings.Split(z, "\x00")
	for i := 0; i < len(parts); i++ {
		e := parts[i]
		if e == "" {
			continue
		}
		f := strings.SplitN(e, "\t", 3)
		if len(f) < 3 {
			continue
		}
		file := File{Path: f[2]}
		if file.Path == "" && i+2 < len(parts) { // a rename: old and new path follow
			file.Path = parts[i+2]
			i += 2
		}
		if f[0] == "-" {
			file.Added, file.Removed = -1, -1
		} else {
			file.Added, _ = strconv.Atoi(f[0])
			file.Removed, _ = strconv.Atoi(f[1])
		}
		out = append(out, file)
	}
	return out
}

// CleanupPlan is what removing a worktree would do.
type CleanupPlan struct {
	Main      string
	Worktree  string
	Branch    string
	BranchSHA string
	Merged    bool   // the branch is already part of the main checkout's HEAD
	Locked    bool   // git holds a lock on the worktree
	LockPID   int    // the process Claude Code recorded in the lock, 0 if none
	Target    string // the branch Merged is measured against
}

// CleanupPlanFor looks at the worktree. It refuses while an agent is still
// in it (live reports whether a session's folder is inside the worktree),
// while it has changes, or while its lock belongs to a process that is
// still running. It never refuses for unmerged commits: removing the
// worktree keeps the branch then, and says so.
func CleanupPlanFor(ctx context.Context, worktree string, live func(dir string) bool) (CleanupPlan, error) {
	p := CleanupPlan{Worktree: worktree}
	if !Linked(worktree) {
		return p, Refusal("that folder is not a linked git worktree")
	}
	p.Main = MainRoot(ctx, worktree)
	if p.Main == "" || p.Main == worktree {
		return p, Refusal("could not find the repository this worktree belongs to")
	}
	if live != nil && live(worktree) {
		return p, Refusal("an agent is still running in " + worktree + "; close it first")
	}
	if st, err := git(ctx, worktree, "status", "--porcelain=v1", "--untracked-files=all"); err != nil {
		return p, Refusal("could not read the state of the worktree: " + err.Error())
	} else if ls := lines(st); len(ls) > 0 {
		return p, Refusal(fmt.Sprintf("the worktree has %s (first: %s); Den never removes work that is not committed", plural(len(ls), "uncommitted change"), strings.TrimSpace(ls[0])))
	}
	p.Branch = Branch(ctx, worktree)
	p.Target = Branch(ctx, p.Main)
	if p.Branch != "" {
		p.BranchSHA, _ = revParse(ctx, worktree, "HEAD")
		if p.Target != "" {
			_, err := git(ctx, p.Main, "merge-base", "--is-ancestor", p.BranchSHA, "HEAD")
			p.Merged = err == nil
		}
	}
	p.Locked, p.LockPID = lockOf(ctx, worktree)
	if p.Locked {
		if p.LockPID == 0 {
			return p, Refusal("the worktree is locked, and the lock does not say which process holds it; unlock it yourself with git worktree unlock")
		}
		if alive(p.LockPID) {
			return p, Refusal(fmt.Sprintf("the worktree is locked by process %d, which is still running", p.LockPID))
		}
	}
	return p, nil
}

// Describe is the plan as the lines of an inbox question.
func (p CleanupPlan) Describe() []string {
	out := []string{"remove the worktree " + p.Worktree}
	switch {
	case p.Branch == "":
		out = append(out, "it is on a detached HEAD; there is no branch to delete")
	case p.Merged:
		out = append(out, "branch "+p.Branch+" is merged into "+p.Target+" and will be deleted with it")
	default:
		out = append(out, "branch "+p.Branch+" is not merged into "+p.Target+" and stays, with its commits")
	}
	if p.Locked {
		out = append(out, fmt.Sprintf("git still holds the lock Claude Code took (process %d has ended); it will be released first", p.LockPID))
	}
	out = append(out, "nothing is forced: git refuses if anything changed in the meantime")
	return out
}

// Cleanup removes the worktree, then deletes its branch when the branch is
// merged. It plans again first, never forces a removal, and releases a lock
// only when the process named in it has ended.
func Cleanup(ctx context.Context, p CleanupPlan, live func(dir string) bool) error {
	now, err := CleanupPlanFor(ctx, p.Worktree, live)
	if err != nil {
		return err
	}
	if now.BranchSHA != p.BranchSHA {
		return Moved("the worktree's branch has new commits since you were asked; ask again")
	}
	if now.Locked {
		if _, err := run(ctx, now.Main, nil, "worktree", "unlock", now.Worktree); err != nil {
			return err
		}
	}
	if _, err := run(ctx, now.Main, nil, "worktree", "remove", now.Worktree); err != nil {
		return err
	}
	if now.Branch != "" && now.Merged {
		// -d, not -D: git itself checks the branch is merged.
		if _, err := run(ctx, now.Main, nil, "branch", "-d", now.Branch); err != nil {
			return fmt.Errorf("the worktree is removed, but the branch stays: %w", err)
		}
	}
	return nil
}

// lockOf reads git's lock on a linked worktree: the reason Claude Code
// wrote is "claude session <name> (pid <n> start <t>)".
func lockOf(ctx context.Context, worktree string) (locked bool, pid int) {
	gd := gitDir(ctx, worktree)
	if gd == "" {
		return false, 0
	}
	raw, err := os.ReadFile(filepath.Join(gd, "locked"))
	if err != nil {
		return false, 0
	}
	reason := string(raw)
	if i := strings.Index(reason, "(pid "); i >= 0 {
		rest := reason[i+5:]
		if j := strings.IndexAny(rest, " )"); j > 0 {
			pid, _ = strconv.Atoi(rest[:j])
		}
	}
	return true, pid
}

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
