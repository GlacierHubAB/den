// Package review builds what the Review view shows: the files an agent's
// work changed, as git sees them on disk, with every changed line attributed
// to the session that wrote it.
package review

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/GlacierHubAB/den/internal/transcript"
)

// File is one changed file, relative to the repository root.
type File struct {
	Path    string // new path; the old one for a deleted file
	OldPath string // set when the file was renamed
	Created bool
	Deleted bool
	Binary  bool
	Hunks   []transcript.Hunk
	// Owner holds, per hunk and per line, the session that wrote that line
	// ("" for context lines and for lines no agent wrote). Set by Attribute.
	Owner [][]string
}

// Counts returns the added and removed lines.
func (f File) Counts() (added, removed int) {
	return transcript.FileChange{Hunks: f.Hunks}.Counts()
}

// untrackedLimit is the largest untracked file shown in full; bigger ones
// are listed as binary so a stray dump never floods the view.
const untrackedLimit = 1 << 20

func git(ctx context.Context, dir string, args ...string) (string, error) {
	// The repository's own config must not run programs while Den looks at
	// it: no filesystem monitor hook, no external diff or text conversion.
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir, "-c", "core.fsmonitor=false"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// Root returns the top of the working tree that contains dir.
func Root(ctx context.Context, dir string) (string, error) {
	out, err := git(ctx, dir, "rev-parse", "--show-toplevel")
	return strings.TrimSpace(out), err
}

// defaultBranch names the branch work is reviewed against: origin's HEAD,
// else a local or remote main or master.
func defaultBranch(ctx context.Context, dir string) (string, error) {
	if out, err := git(ctx, dir, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if b := strings.TrimSpace(out); b != "" {
			return b, nil
		}
	}
	for _, b := range []string{"main", "master", "origin/main", "origin/master"} {
		if _, err := git(ctx, dir, "rev-parse", "--verify", "--quiet", b+"^{commit}"); err == nil {
			return b, nil
		}
	}
	return "", fmt.Errorf("no main or master branch in %s", dir)
}

// Base returns the commit the work in dir is reviewed against. On a
// feature branch that is where it left the default branch. On the default
// branch itself it is the last commit before since (when the session
// started), so work an agent committed straight to main still shows; with
// a zero since it is HEAD, which leaves only uncommitted changes.
func Base(ctx context.Context, dir string, since time.Time) (string, error) {
	def, err := defaultBranch(ctx, dir)
	if err != nil {
		return "", err
	}
	head, err := git(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	mb, err := git(ctx, dir, "merge-base", "HEAD", def)
	if err != nil {
		return "", err
	}
	head, mb = strings.TrimSpace(head), strings.TrimSpace(mb)
	if mb != head || since.IsZero() {
		return mb, nil
	}
	out, err := git(ctx, dir, "rev-list", "-1", "--first-parent", "--before="+strconv.FormatInt(since.Unix(), 10), "HEAD")
	if err != nil {
		return "", err
	}
	if b := strings.TrimSpace(out); b != "" {
		return b, nil
	}
	// The whole history is newer than the session: review everything.
	return emptyTree(ctx, dir)
}

func emptyTree(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "hash-object", "-t", "tree", "--stdin")
	cmd.Stdin = strings.NewReader("")
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// Diff returns the working tree of the repository containing dir against
// base: committed, staged and unstaged changes, and untracked files that
// are not ignored.
func Diff(ctx context.Context, dir, base string) ([]File, error) {
	root, err := Root(ctx, dir)
	if err != nil {
		return nil, err
	}
	out, err := git(ctx, root, "diff", "--no-color", "--no-ext-diff", "--no-textconv", "--find-renames", "-U3",
		"--src-prefix=a/", "--dst-prefix=b/", base, "--")
	if err != nil {
		return nil, err
	}
	files := ParseUnified(out)

	list, err := git(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	for _, p := range strings.Split(list, "\x00") {
		if p == "" {
			continue
		}
		files = append(files, untracked(root, p))
	}
	return files, nil
}

// untracked turns a new file git does not know about into an all-added File.
func untracked(root, rel string) File {
	f := File{Path: rel, Created: true}
	// Lstat: a symlink is listed, never followed to a file outside the repo.
	st, err := os.Lstat(filepath.Join(root, rel))
	if err != nil || !st.Mode().IsRegular() || st.Size() > untrackedLimit {
		f.Binary = true
		return f
	}
	raw, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil || bytes.IndexByte(raw, 0) >= 0 {
		f.Binary = true
		return f
	}
	if len(raw) == 0 {
		return f
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	h := transcript.Hunk{OldStart: 0, NewStart: 1, Lines: make([]string, len(lines))}
	for i, l := range lines {
		h.Lines[i] = "+" + l
	}
	f.Hunks = []transcript.Hunk{h}
	return f
}

// ParseUnified reads `git diff` output into files and hunks.
func ParseUnified(text string) []File {
	var files []File
	var cur *File
	var hunk *transcript.Hunk
	flush := func() {
		if cur != nil {
			files = append(files, *cur)
		}
		cur, hunk = nil, nil
	}
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flush()
			a, b := headerPaths(strings.TrimPrefix(line, "diff --git "))
			cur = &File{Path: b}
			if a != b {
				cur.OldPath = a
			}
		case cur == nil:
			continue
		case hunk == nil && strings.HasPrefix(line, "new file mode"):
			cur.Created = true
		case hunk == nil && strings.HasPrefix(line, "deleted file mode"):
			cur.Deleted = true
		case hunk == nil && strings.HasPrefix(line, "rename from "):
			cur.OldPath = unquote(strings.TrimPrefix(line, "rename from "))
		case hunk == nil && strings.HasPrefix(line, "rename to "):
			cur.Path = unquote(strings.TrimPrefix(line, "rename to "))
		case hunk == nil && strings.HasPrefix(line, "Binary files "):
			cur.Binary = true
		case hunk == nil && strings.HasPrefix(line, "--- "):
			if p := diffPath(strings.TrimPrefix(line, "--- ")); p != "" {
				cur.OldPath = p
			}
		case hunk == nil && strings.HasPrefix(line, "+++ "):
			if p := diffPath(strings.TrimPrefix(line, "+++ ")); p != "" {
				cur.Path = p
			}
		case strings.HasPrefix(line, "@@ "):
			old, nw, ok := hunkStarts(line)
			if !ok {
				continue
			}
			cur.Hunks = append(cur.Hunks, transcript.Hunk{OldStart: old, NewStart: nw})
			hunk = &cur.Hunks[len(cur.Hunks)-1]
		case hunk != nil && line != "" && strings.ContainsRune(" +-", rune(line[0])):
			hunk.Lines = append(hunk.Lines, line)
			// Appending may move the backing array; keep hunk pointing at it.
			hunk = &cur.Hunks[len(cur.Hunks)-1]
		}
	}
	flush()
	for i := range files {
		f := &files[i]
		if f.OldPath == f.Path {
			f.OldPath = ""
		}
		if f.Deleted && f.OldPath != "" {
			f.Path, f.OldPath = f.OldPath, ""
		}
	}
	return files
}

// diffPath reads the path on a ---/+++ line; "" for /dev/null.
func diffPath(s string) string {
	s = unquote(strings.TrimSuffix(s, "\t"))
	if s == "/dev/null" {
		return ""
	}
	for _, p := range []string{"a/", "b/"} {
		if rest, ok := strings.CutPrefix(s, p); ok {
			return rest
		}
	}
	return s
}

// headerPaths splits the "a/x b/y" of a diff --git line. Quoted paths are
// handled; for unquoted ones with spaces the two halves are equal length
// when the file was not renamed, which is the case that matters here.
func headerPaths(s string) (a, b string) {
	if strings.HasPrefix(s, `"`) {
		if end := closingQuote(s); end > 0 {
			a = diffPath(s[:end+1])
			b = diffPath(strings.TrimSpace(s[end+1:]))
			return a, b
		}
	}
	if i := strings.Index(s, " b/"); i >= 0 && (len(s)-1)/2 == i {
		return diffPath(s[:i]), diffPath(s[i+1:])
	}
	if i := strings.LastIndex(s, " b/"); i >= 0 {
		return diffPath(s[:i]), diffPath(s[i+1:])
	}
	return s, s
}

func closingQuote(s string) int {
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return i
		}
	}
	return -1
}

func unquote(s string) string {
	if strings.HasPrefix(s, `"`) {
		if u, err := strconv.Unquote(s); err == nil {
			return u
		}
	}
	return s
}

// hunkStarts reads the two start lines of "@@ -a,b +c,d @@".
func hunkStarts(line string) (old, nw int, ok bool) {
	f := strings.Fields(line)
	if len(f) < 3 || !strings.HasPrefix(f[1], "-") || !strings.HasPrefix(f[2], "+") {
		return 0, 0, false
	}
	o, err1 := strconv.Atoi(strings.SplitN(f[1][1:], ",", 2)[0])
	n, err2 := strconv.Atoi(strings.SplitN(f[2][1:], ",", 2)[0])
	return o, n, err1 == nil && err2 == nil
}
