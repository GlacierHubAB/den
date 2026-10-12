// Package gitsafe runs git in a checkout Den does not control: an agent's,
// or one on a remote host that `den remote serve` reads for Den. The
// repository's own config and attributes are written by whoever owns the
// checkout, and several of git's settings name a program to run. A read
// through Command runs none of them; a change through Change runs none but
// the filters a checkout needs.
package gitsafe

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Command is git with args, run in dir, such that the repository cannot
// make it execute anything:
//
//   - core.fsmonitor is off (a hook git would run before reading the tree);
//   - every filter driver the config names has its clean, smudge and process
//     commands emptied and is not required (git diff and status run a clean
//     filter on files whose stat changed);
//   - every merge driver the config names is emptied (git merge-tree runs
//     one on a file both sides changed);
//   - no lazy fetch from a promisor remote (which would run ssh or a remote
//     helper the config chose), no prompt, no optional locks.
//
// External diff and text conversion are off per command: callers pass
// --no-ext-diff and --no-textconv to diff. Hooks never run on reads.
// Commands that change the checkout on the human's request use Change.
func Command(ctx context.Context, dir string, args ...string) *exec.Cmd {
	over := [][2]string{{"core.fsmonitor", "false"}}
	for _, d := range names(ctx, dir, `^filter\.`, "filter") {
		for _, v := range []string{"clean", "smudge", "process"} {
			over = append(over, [2]string{"filter." + d + "." + v, ""})
		}
		over = append(over, [2]string{"filter." + d + ".required", "false"})
	}
	return command(ctx, dir, append(over, noMergeDrivers(ctx, dir)...), args)
}

// Change is git with args, run in dir, for the commands that change a
// checkout on the human's request: merge, worktree remove, branch -d. No
// hook runs (core.hooksPath is /dev/null), and neither does the
// filesystem monitor or a merge driver the config names. Filter drivers do
// run: a checkout needs them to write files, as Git LFS does. The
// environment is Command's.
func Change(ctx context.Context, dir string, args ...string) *exec.Cmd {
	over := [][2]string{{"core.fsmonitor", "false"}, {"core.hooksPath", os.DevNull}}
	return command(ctx, dir, append(over, noMergeDrivers(ctx, dir)...), args)
}

// MergeDrivers names the merge drivers the config of dir defines a program
// for. A merge that needs one cannot be checked or made without running it.
func MergeDrivers(ctx context.Context, dir string) []string {
	return names(ctx, dir, `^merge\..+\.driver$`, "merge")
}

func noMergeDrivers(ctx context.Context, dir string) [][2]string {
	var over [][2]string
	for _, d := range MergeDrivers(ctx, dir) {
		over = append(over, [2]string{"merge." + d + ".driver", ""})
	}
	return over
}

// command runs git with the config overrides over. They go in the
// environment, not as -c, whose key=value a driver name containing '='
// would split in the wrong place.
func command(ctx context.Context, dir string, over [][2]string, args []string) *exec.Cmd {
	env := append(environ(), "GIT_CONFIG_COUNT="+strconv.Itoa(len(over)))
	for i, kv := range over {
		n := strconv.Itoa(i)
		env = append(env, "GIT_CONFIG_KEY_"+n+"="+kv[0], "GIT_CONFIG_VALUE_"+n+"="+kv[1])
	}
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = env
	return cmd
}

// environ is Den's environment without git's own variables, which could
// point git at another repository, index or config, plus Env.
func environ() []string {
	var out []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			out = append(out, kv)
		}
	}
	return append(out, Env...)
}

// Env is added to every git Command runs.
var Env = []string{"GIT_OPTIONAL_LOCKS=0", "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C"}

// names lists the drivers of section (filter or merge) that the config of
// dir has keys for matching re, from any config file git reads there.
// Reading config runs nothing.
func names(ctx context.Context, dir, re, section string) []string {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "config", "--null", "--name-only", "--get-regexp", re)
	cmd.Env = environ()
	out, err := cmd.Output()
	if err != nil {
		return nil // exit 1: none; any other failure: the command after fails too
	}
	seen := map[string]bool{}
	var names []string
	for _, key := range strings.Split(string(out), "\x00") {
		// <section>.<driver>.<var>; the driver may itself contain dots.
		rest, ok := strings.CutPrefix(key, section+".")
		i := strings.LastIndexByte(rest, '.')
		if !ok || i <= 0 {
			continue
		}
		d := rest[:i]
		if !seen[d] {
			seen[d] = true
			names = append(names, d)
		}
	}
	return names
}
