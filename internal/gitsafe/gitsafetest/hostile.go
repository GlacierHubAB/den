// Package gitsafetest builds a repository whose config tries to make git
// run programs, for tests of code that reads checkouts it does not control.
package gitsafetest

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Hostile is a repository whose config names a program at every hook git
// would run while it is only looked at; each one writes a marker file. It
// returns the repository and the marker directory.
func Hostile(t testing.TB) (repo, marks string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	marks, repo = t.TempDir(), t.TempDir()
	runIn := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+t.TempDir())
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run := func(args ...string) { t.Helper(); runIn(repo, args...) }
	// A submodule (a nested checkout committed as a gitlink) whose own
	// config, which the outer repository's does not name, has a filter.
	sub := filepath.Join(repo, "sub")
	os.MkdirAll(sub, 0o755)
	runIn(sub, "init", "-q", "-b", "main")
	runIn(sub, "config", "user.email", "user@example.com")
	runIn(sub, "config", "user.name", "user")
	os.WriteFile(filepath.Join(sub, "s.dat"), []byte("s\n"), 0o644)
	os.WriteFile(filepath.Join(sub, ".gitattributes"), []byte("s.dat filter=subevil\n"), 0o644)
	runIn(sub, "add", ".")
	runIn(sub, "commit", "-qm", "init")
	run("init", "-q", "-b", "main")
	run("config", "user.email", "user@example.com")
	run("config", "user.name", "user")
	os.WriteFile(filepath.Join(repo, "f.txt"), []byte("a\n"), 0o644)
	run("add", ".")
	run("commit", "-qm", "init")
	mark := func(name string) string { return "touch " + filepath.Join(marks, name) + "; cat" }
	run("config", "core.fsmonitor", mark("fsmonitor"))
	run("config", "filter.evil.clean", mark("clean"))
	run("config", "filter.evil.process", mark("process"))
	run("config", "filter.evil.required", "true")
	run("config", "filter.a=b.clean", mark("clean-eq"))
	run("config", "diff.evil.textconv", mark("textconv"))
	run("config", "diff.evil.command", mark("extdiff"))
	run("config", "diff.external", mark("external"))
	os.WriteFile(filepath.Join(repo, ".git", "info", "attributes"), []byte("*.txt filter=evil diff=evil\n*.md filter=a=b\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "f.txt"), []byte("a\nb\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "g.md"), []byte("x\n"), 0o644)
	runIn(sub, "config", "filter.subevil.clean", mark("submodule-clean"))
	// Touched, not changed: git must read it again, through the filter.
	later := time.Now().Add(time.Minute)
	os.Chtimes(filepath.Join(sub, "s.dat"), later, later)
	return repo, marks
}
