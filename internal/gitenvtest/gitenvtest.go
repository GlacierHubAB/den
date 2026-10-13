// Package gitenvtest keeps the tests' git away from any repository but the
// ones they make. Run from `git rebase --exec`, a hook or a worktree script,
// a test process inherits GIT_DIR, GIT_WORK_TREE, GIT_INDEX_FILE and the
// like, and every `git -C <scratch dir>` then works on the outer repository
// instead: it changes its config, branches and worktrees. A package whose
// tests, or the code they run, call git has a TestMain that calls Main.
//
// Only test files import it.
package gitenvtest

import (
	"os"
	"strings"
	"testing"
)

// Scrub removes every GIT_* variable from this process's environment, so
// that every git it starts, directly or through a helper, and every
// program that starts git, begins without them (as gitsafe's environment
// does for Den itself). It returns what it removed.
func Scrub() []string {
	var gone []string
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "GIT_") {
			os.Unsetenv(name)
			gone = append(gone, name)
		}
	}
	return gone
}

// Main is a TestMain body: scrub, then run the tests. Use it as
// os.Exit(gitenvtest.Main(m)).
func Main(m *testing.M) int {
	Scrub()
	return m.Run()
}

// Without returns env without its GIT_* variables.
func Without(env []string) []string {
	var out []string
	for _, kv := range env {
		if name, _, _ := strings.Cut(kv, "="); !strings.HasPrefix(name, "GIT_") {
			out = append(out, kv)
		}
	}
	return out
}
