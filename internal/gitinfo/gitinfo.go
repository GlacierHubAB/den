// Package gitinfo answers "which repo and branch is this directory on"
// without shelling out to git, so a scan of many sessions stays cheap.
package gitinfo

import (
	"os"
	"path/filepath"
	"strings"
)

// Info is the repository name and checked-out branch of a directory.
type Info struct {
	Repo   string // basename of the main repository root
	Branch string // branch name, or a short commit id when detached
}

// Lookup walks up from dir to the nearest .git and reads HEAD. Linked
// worktrees (a .git file pointing at the main repo) are resolved so Repo is
// the main repository's name, not the worktree directory. A relative dir
// has no git info: session folders are absolute, and one that is not was
// not written by Claude Code, so it is not read relative to Den's own.
func Lookup(dir string) (Info, bool) {
	if !filepath.IsAbs(dir) {
		return Info{}, false
	}
	// Absolute and clean, so filepath.Dir reaches "/" and the walk ends.
	for d := filepath.Clean(dir); d != "/"; d = filepath.Dir(d) {
		g := filepath.Join(d, ".git")
		st, err := os.Stat(g)
		if err != nil {
			continue
		}
		gitDir := g
		repoRoot := d
		if !st.IsDir() {
			raw, err := os.ReadFile(g)
			if err != nil {
				return Info{}, false
			}
			target := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(raw)), "gitdir:"))
			if !filepath.IsAbs(target) {
				target = filepath.Join(d, target)
			}
			gitDir = target
			// <main>/.git/worktrees/<name> -> <main>
			if i := strings.Index(target, string(filepath.Separator)+".git"+string(filepath.Separator)+"worktrees"+string(filepath.Separator)); i > 0 {
				repoRoot = target[:i]
			}
		}
		head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
		if err != nil {
			return Info{Repo: filepath.Base(repoRoot)}, true
		}
		h := strings.TrimSpace(string(head))
		info := Info{Repo: filepath.Base(repoRoot)}
		if ref, ok := strings.CutPrefix(h, "ref: refs/heads/"); ok {
			info.Branch = ref
		} else if len(h) >= 7 {
			info.Branch = h[:7]
		}
		return info, true
	}
	return Info{}, false
}
