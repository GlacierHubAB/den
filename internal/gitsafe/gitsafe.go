// Package gitsafe runs git to look at a checkout Den does not control: an
// agent's, or one on a remote host that `den remote serve` reads for Den.
// The repository's own config and attributes are written by whoever owns
// the checkout, and several of git's settings name a program to run. A
// read through this package runs none of them.
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
//   - no lazy fetch from a promisor remote (which would run ssh or a remote
//     helper the config chose), no prompt, no optional locks.
//
// External diff and text conversion are off per command: callers pass
// --no-ext-diff and --no-textconv to diff. Hooks never run on reads.
// Commands that change the checkout on the human's request (merge, worktree
// remove) do not use this: they need the repository's filters, such as LFS.
func Command(ctx context.Context, dir string, args ...string) *exec.Cmd {
	// The overrides go in the environment, not as -c, whose key=value a
	// driver name containing '=' would split in the wrong place.
	over := [][2]string{{"core.fsmonitor", "false"}}
	for _, d := range filterDrivers(ctx, dir) {
		for _, v := range []string{"clean", "smudge", "process"} {
			over = append(over, [2]string{"filter." + d + "." + v, ""})
		}
		over = append(over, [2]string{"filter." + d + ".required", "false"})
	}
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

// filterDrivers names the filter drivers the config of dir defines, from
// any config file git reads there. Reading config runs nothing.
func filterDrivers(ctx context.Context, dir string) []string {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "config", "--null", "--name-only", "--get-regexp", `^filter\.`)
	cmd.Env = environ()
	out, err := cmd.Output()
	if err != nil {
		return nil // exit 1: none; any other failure: the command after fails too
	}
	seen := map[string]bool{}
	var names []string
	for _, key := range strings.Split(string(out), "\x00") {
		// filter.<driver>.<var>; the driver may itself contain dots.
		rest, ok := strings.CutPrefix(key, "filter.")
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
