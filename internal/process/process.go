// Package process reads, from git, how the agents' work stands against
// the small-team process Den keeps: which files each branch changes, so
// two agents changing the same files hear it early. Den reads it itself,
// with gitsafe, in checkouts the agents write: never an agent's claim, and
// never running anything the checkout's config names.
package process

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/GlacierHubAB/den/internal/gitsafe"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// MaxFiles caps the files read of one branch: a branch that changes more
// is far past any overlap Den needs to name.
const MaxFiles = 2000

// BaseRefs are the integration branches Den measures a branch against, the
// first that exists: the shared one first, then a local one.
var BaseRefs = []string{
	"refs/remotes/origin/dev", "refs/remotes/origin/main", "refs/remotes/origin/master",
	"refs/heads/dev", "refs/heads/main", "refs/heads/master",
}

// git runs a read in dir through gitsafe and returns its output.
func git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return gitsafe.Command(ctx, dir, args...).Output()
}

// RepoID is how Den keys a project: its name, made safe for a file name,
// and 12 hex digits of the SHA-256 of its git common dir's real path, so
// every worktree of one repository has the same id.
func RepoID(ctx context.Context, dir string) (string, error) {
	out, err := git(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	common := strings.TrimSpace(string(out))
	if real, err := filepath.EvalSymlinks(common); err == nil {
		common = real
	}
	name := filepath.Base(common)
	if name == ".git" {
		name = filepath.Base(filepath.Dir(common))
	}
	name = strings.TrimSuffix(name, ".git")
	sum := sha256.Sum256([]byte(common))
	return safeName(name) + "-" + hex.EncodeToString(sum[:6]), nil
}

// safeName is name as a file name: letters, digits, '.', '_' and '-'
// only, at most 40, never empty and never starting with '.'.
func safeName(name string) string {
	var b strings.Builder
	for _, r := range sanitize.Label(name, 40) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	s := strings.TrimLeft(b.String(), ".")
	if s == "" {
		return "repo"
	}
	return s
}

// Base is the first of BaseRefs that exists in dir's repository, "" for
// none.
func Base(ctx context.Context, dir string) string {
	for _, ref := range BaseRefs {
		if _, err := git(ctx, dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err == nil {
			return ref
		}
	}
	return ""
}

// ErrNoBase is a repository with none of BaseRefs.
var ErrNoBase = errors.New("no integration branch (dev, main or master) to measure against")

// Touched are the files the checkout at dir changes against its
// integration branch: committed since the merge base, staged, unstaged and
// untracked (not ignored), sorted, at most MaxFiles. Names are as git
// lists them; a name that is not UTF-8 is left out.
func Touched(ctx context.Context, dir string) ([]string, error) {
	base := Base(ctx, dir)
	if base == "" {
		return nil, ErrNoBase
	}
	mb, err := git(ctx, dir, "merge-base", "HEAD", base)
	if err != nil {
		return nil, err
	}
	diff, err := git(ctx, dir, "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--name-only", "-z", strings.TrimSpace(string(mb)), "--")
	if err != nil {
		return nil, err
	}
	untracked, err := git(ctx, dir, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, part := range [][]byte{diff, untracked} {
		for _, name := range bytes.Split(part, []byte{0}) {
			if len(name) == 0 || !utf8.Valid(name) {
				continue
			}
			seen[string(name)] = true
		}
	}
	files := make([]string, 0, len(seen))
	for f := range seen {
		files = append(files, f)
	}
	sort.Strings(files)
	if len(files) > MaxFiles {
		files = files[:MaxFiles]
	}
	return files, nil
}

// Work is one agent's branch as Den read it: whose (a session id), in which
// repository (RepoID) and checkout (Tree, its top folder), and the files
// it changes.
type Work struct {
	Key   string
	Repo  string
	Tree  string
	Files []string
	// Branch is the checked-out branch ("HEAD" when detached), Base the
	// integration branch it is measured against, Since when its oldest
	// commit after the merge base was made (zero with none), and Behind
	// how many commits Base has that it lacks.
	Branch string
	Base   string
	Since  time.Time
	Behind int
	// Trunk: the checkout is on the repository's trunk (main, dev, ...).
	// Its commits are not its own work: Files are only what it has not
	// committed, and Den never nudges it.
	Trunk bool
}

// Read is the Work of the checkout at dir, for key.
func Read(ctx context.Context, key, dir string) (Work, error) {
	repo, err := RepoID(ctx, dir)
	if err != nil {
		return Work{}, err
	}
	top, err := git(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return Work{}, err
	}
	files, err := Touched(ctx, dir)
	if err != nil {
		return Work{}, err
	}
	w := Work{Key: key, Repo: repo, Tree: strings.TrimSpace(string(top)), Files: files, Base: Base(ctx, dir)}
	if out, err := git(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		w.Branch = strings.TrimSpace(string(out))
	}
	// The oldest commit on the branch since it left Base, by commit time.
	if out, err := git(ctx, dir, "log", "--format=%ct", "--reverse", "HEAD", "--not", w.Base); err == nil {
		if first, _, _ := strings.Cut(string(out), "\n"); first != "" {
			if secs, err := strconv.ParseInt(strings.TrimSpace(first), 10, 64); err == nil {
				w.Since = time.Unix(secs, 0)
			}
		}
	}
	if out, err := git(ctx, dir, "rev-list", "--count", "HEAD.."+w.Base); err == nil {
		w.Behind, _ = strconv.Atoi(strings.TrimSpace(string(out)))
	}
	// Only a feature branch's commits are its own work: on the trunk, only
	// what is not committed yet counts.
	w.Trunk = IsTrunk(ctx, dir, w.Branch)
	if w.Trunk {
		w.Files = Uncommitted(ctx, dir)
	}
	return w, nil
}

// trunkNames are the branches Den never takes for a feature branch.
var trunkNames = map[string]bool{"main": true, "master": true, "dev": true, "develop": true, "trunk": true}

// IsTrunk reports whether branch is the repository's trunk: one of
// trunkNames, or the branch origin's HEAD names.
func IsTrunk(ctx context.Context, dir, branch string) bool {
	if trunkNames[branch] {
		return true
	}
	if branch == "" || branch == "HEAD" {
		return false
	}
	out, err := git(ctx, dir, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	if err != nil {
		return false
	}
	return strings.TrimPrefix(strings.TrimSpace(string(out)), "origin/") == branch
}

// Uncommitted are the files the checkout at dir changes and has not
// committed: staged, unstaged and untracked (not ignored), sorted, at most
// MaxFiles.
func Uncommitted(ctx context.Context, dir string) []string {
	seen := map[string]bool{}
	for _, args := range [][]string{
		{"diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--name-only", "-z", "HEAD", "--"},
		{"ls-files", "--others", "--exclude-standard", "-z"},
	} {
		out, err := git(ctx, dir, args...)
		if err != nil {
			continue
		}
		for _, name := range bytes.Split(out, []byte{0}) {
			if len(name) > 0 && utf8.Valid(name) {
				seen[string(name)] = true
			}
		}
	}
	files := make([]string, 0, len(seen))
	for f := range seen {
		files = append(files, f)
	}
	sort.Strings(files)
	if len(files) > MaxFiles {
		files = files[:MaxFiles]
	}
	return files
}

// Overlap is two agents in one repository whose branches change the same
// files; A sorts before B.
type Overlap struct {
	A, B  string
	Files []string
}

// Overlaps finds every pair of works in the same repository, in different
// checkouts, that change a file in common, sorted by A then B. Two agents in
// one checkout share its branch: no merge of theirs can conflict.
func Overlaps(works []Work) []Overlap {
	var out []Overlap
	for i := range works {
		for j := i + 1; j < len(works); j++ {
			a, b := works[i], works[j]
			if a.Repo == "" || a.Repo != b.Repo || a.Key == b.Key || (a.Tree != "" && a.Tree == b.Tree) {
				continue
			}
			in := map[string]bool{}
			for _, f := range a.Files {
				in[f] = true
			}
			var common []string
			for _, f := range b.Files {
				if in[f] {
					common = append(common, f)
				}
			}
			if len(common) == 0 {
				continue
			}
			sort.Strings(common)
			if a.Key > b.Key {
				a, b = b, a
			}
			out = append(out, Overlap{A: a.Key, B: b.Key, Files: common})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].A != out[j].A {
			return out[i].A < out[j].A
		}
		return out[i].B < out[j].B
	})
	return out
}
