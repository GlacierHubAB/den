package gitsafe

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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

// Emptying only the outer repository's filters is not enough: git diff
// looks into a submodule with the submodule's own config.
func TestSubmoduleFilterIsLive(t *testing.T) {
	repo, marks := gitsafetest.Hostile(t)
	cmd := exec.Command("git", "-C", repo, "diff", "--no-ext-diff", "--no-textconv", "HEAD", "--")
	cmd.Env = append(environ(), "GIT_CONFIG_COUNT=4",
		"GIT_CONFIG_KEY_0=core.fsmonitor", "GIT_CONFIG_VALUE_0=false",
		"GIT_CONFIG_KEY_1=filter.evil.clean", "GIT_CONFIG_VALUE_1=",
		"GIT_CONFIG_KEY_2=filter.evil.process", "GIT_CONFIG_VALUE_2=",
		"GIT_CONFIG_KEY_3=filter.a=b.clean", "GIT_CONFIG_VALUE_3=")
	cmd.Run()
	if _, err := os.Stat(filepath.Join(marks, "submodule-clean")); err != nil {
		t.Fatal("the fixture's submodule filter did not run without the submodule overrides; the test would prove nothing")
	}
}
