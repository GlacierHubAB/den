package review

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/GlacierHubAB/den/internal/transcript"
	"github.com/GlacierHubAB/den/internal/worktree"
)

// BaseFor is the commit the work in root is diffed against: the commit
// Claude Code made a linked worktree from, when one of the sessions in it
// recorded that in its transcript and it is still in the branch's history,
// otherwise Base with since.
func BaseFor(ctx context.Context, root string, transcripts []string, since time.Time) (string, error) {
	if base, err := WorktreeBase(ctx, root, transcripts); err == nil {
		return base, nil
	}
	return Base(ctx, root, since)
}

// WorktreeBase is the commit a linked worktree at root was made from, as a
// session in it recorded; an error when there is none.
func WorktreeBase(ctx context.Context, root string, transcripts []string) (string, error) {
	if !worktree.Linked(root) {
		return "", errors.New("not a linked worktree")
	}
	for _, p := range transcripts {
		st, ok := transcript.ReadWorktree(p)
		if !ok || !samePath(st.Path, root) {
			continue
		}
		if base, ok := BaseAt(ctx, root, st.OriginalHead); ok {
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

// Checkout is where a piece of work lives: the repository's primary
// checkout, and for a linked worktree its branch and how many commits it
// is ahead of the primary checkout's branch.
type Checkout struct {
	Main     string
	Worktree bool
	Branch   string
	Ahead    int
}

// CheckoutOf describes the checkout at root.
func CheckoutOf(ctx context.Context, root string) Checkout {
	c := Checkout{Main: worktree.MainRoot(ctx, root), Worktree: worktree.Linked(root)}
	if c.Worktree {
		c.Branch = worktree.Branch(ctx, root)
		if target := worktree.Branch(ctx, c.Main); c.Branch != "" && target != "" {
			c.Ahead = Ahead(ctx, c.Main, target, c.Branch)
		}
	}
	return c
}
