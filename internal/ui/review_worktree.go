package ui

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"

	"github.com/GlacierHubAB/den/internal/review"
	"github.com/GlacierHubAB/den/internal/transcript"
	"github.com/GlacierHubAB/den/internal/worktree"
)

// worktreeBase is the commit an agent's work in a linked worktree is
// diffed against: the commit Claude Code made the worktree from, when one
// of the sessions in it recorded that and it is still in the branch's
// history. The diff itself is always read inside the worktree.
func worktreeBase(ctx context.Context, root string, transcripts []string) (string, error) {
	if !worktree.Linked(root) {
		return "", errors.New("not a linked worktree")
	}
	for _, p := range transcripts {
		st, ok := transcript.ReadWorktree(p)
		if !ok || !samePath(st.Path, root) {
			continue
		}
		if base, ok := review.BaseAt(ctx, root, st.OriginalHead); ok {
			return base, nil
		}
	}
	return "", errors.New("no recorded base")
}

func samePath(a, b string) bool {
	if a == b {
		return true
	}
	ea, err1 := filepath.EvalSymlinks(a)
	eb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ea == eb
}

// annotateWorktree tells the works whose root is a linked worktree which
// repository they belong to, their branch, and how far ahead of the main
// checkout's branch they are.
func annotateWorktree(ctx context.Context, root string, ws []work) {
	main := worktree.MainRoot(ctx, root)
	linked := worktree.Linked(root)
	branch, ahead := "", 0
	if linked {
		branch = worktree.Branch(ctx, root)
		if target := worktree.Branch(ctx, main); branch != "" && target != "" {
			ahead = review.Ahead(ctx, main, target, branch)
		}
	}
	for i := range ws {
		ws[i].Main = main
		ws[i].Worktree = linked
		ws[i].Branch = branch
		ws[i].Ahead = ahead
	}
}

// commitWord is "1 commit" or "3 commits".
func commitWord(n int) string {
	if n == 1 {
		return "1 commit"
	}
	return strconv.Itoa(n) + " commits"
}
