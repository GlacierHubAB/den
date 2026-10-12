package ui

import (
	"context"
	"path/filepath"
	"strconv"

	"github.com/GlacierHubAB/den/internal/review"
)

// annotateWorktree tells the works whose root is a linked worktree which
// repository they belong to, their branch, and how far ahead of the main
// checkout's branch they are.
func annotateWorktree(ctx context.Context, root string, ws []work) {
	c := review.CheckoutOf(ctx, root)
	for i := range ws {
		ws[i].Main = c.Main
		ws[i].Worktree = c.Worktree
		ws[i].Branch = c.Branch
		ws[i].Ahead = c.Ahead
	}
}

// commitWord is "1 commit" or "3 commits".
func commitWord(n int) string {
	if n == 1 {
		return "1 commit"
	}
	return strconv.Itoa(n) + " commits"
}

func samePath(a, b string) bool {
	if a == b {
		return true
	}
	ea, err1 := filepath.EvalSymlinks(a)
	eb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ea == eb
}
