package gitsafe

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/gitsafe/gitsafetest"
)

func TestNothingRuns(t *testing.T) {
	repo, marks := gitsafetest.Hostile(t)
	t.Setenv("GIT_DIR", "/nonexistent") // an inherited git variable is not used
	ctx := context.Background()
	for _, args := range [][]string{
		{"diff", "--no-color", "--no-ext-diff", "--no-textconv", "HEAD", "--"},
		{"status", "--porcelain", "--untracked-files=normal"},
		{"ls-files", "--others", "--exclude-standard", "-z"},
	} {
		out, err := Command(ctx, repo, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v %s", args, err, out)
		}
		if args[0] == "diff" && !strings.Contains(string(out), "+b") {
			t.Errorf("diff lost the change: %s", out)
		}
	}
	if ents, _ := os.ReadDir(marks); len(ents) > 0 {
		t.Errorf("the repository made git run %s", ents[0].Name())
	}
	// The fixture is live: plain git runs the filter.
	plain := exec.Command("git", "-C", repo, "-c", "core.fsmonitor=false", "diff", "--no-ext-diff", "--no-textconv", "HEAD")
	plain.Env = environ()
	plain.Run()
	if ents, _ := os.ReadDir(marks); len(ents) == 0 {
		t.Error("the fixture's filter never ran under plain git")
	}
}
