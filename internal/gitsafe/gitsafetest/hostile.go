// Package gitsafetest builds a repository whose config tries to make git
// run programs, for tests of code that reads checkouts it does not control.
package gitsafetest

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Hostile is a repository whose config names a program at every hook git
// would run while it is only looked at, and whose hooks directory has a
// hook for merging and committing; each one writes a marker file. Its
// branches ours and theirs both change h.txt, so merging them needs its
// merge driver. It returns the repository, on main, and the marker
// directory.
func Hostile(t testing.TB) (repo, marks string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	marks, repo = t.TempDir(), t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+t.TempDir())
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "user@example.com")
	run("config", "user.name", "user")
	os.WriteFile(filepath.Join(repo, "f.txt"), []byte("a\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "h.txt"), []byte("1\n2\n3\n"), 0o644)
	run("add", ".")
	run("commit", "-qm", "init")
	for _, b := range []struct{ name, text string }{{"ours", "1\n2\nC\n"}, {"theirs", "A\n2\n3\n"}} {
		run("checkout", "-qb", b.name, "main")
		os.WriteFile(filepath.Join(repo, "h.txt"), []byte(b.text), 0o644)
		run("commit", "-qam", b.name)
	}
	run("checkout", "-q", "main")
	mark := func(name string) string { return "touch " + filepath.Join(marks, name) + "; cat" }
	run("config", "core.fsmonitor", mark("fsmonitor"))
	run("config", "filter.evil.clean", mark("clean"))
	run("config", "filter.evil.process", mark("process"))
	run("config", "filter.evil.required", "true")
	run("config", "filter.a=b.clean", mark("clean-eq"))
	run("config", "diff.evil.textconv", mark("textconv"))
	run("config", "diff.evil.command", mark("extdiff"))
	run("config", "diff.external", mark("external"))
	run("config", "merge.evil.driver", "touch "+filepath.Join(marks, "merge-driver"))
	Signer(t, repo, marks)
	os.WriteFile(filepath.Join(repo, ".git", "info", "attributes"), []byte("*.txt filter=evil diff=evil\n*.md filter=a=b\nh.txt merge=evil\n"), 0o644)
	Hooks(t, filepath.Join(repo, ".git", "hooks"), marks)
	os.WriteFile(filepath.Join(repo, "f.txt"), []byte("a\nb\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "g.md"), []byte("x\n"), 0o644)
	return repo, marks
}

// HookNames are the hooks Hooks installs: those a merge, a commit and a
// ref update run.
var HookNames = []string{"pre-merge-commit", "prepare-commit-msg", "commit-msg", "post-merge", "post-commit", "post-checkout", "reference-transaction"}

// Hooks puts a hook into dir for each of HookNames that writes a marker
// named hook-<name> into marks.
func Hooks(t testing.TB, dir, marks string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, h := range HookNames {
		script := "#!/bin/sh\ntouch '" + filepath.Join(marks, "hook-"+h) + "'\n"
		if err := os.WriteFile(filepath.Join(dir, h), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// Signer makes the repository at repo sign every commit with a program
// that writes the marker "signer" into marks and then fails, as it makes
// no signature.
func Signer(t testing.TB, repo, marks string) {
	t.Helper()
	prog := filepath.Join(t.TempDir(), "sign")
	script := "#!/bin/sh\ntouch '" + filepath.Join(marks, "signer") + "'\nexit 1\n"
	if err := os.WriteFile(prog, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, kv := range [][2]string{{"commit.gpgSign", "true"}, {"gpg.program", prog}} {
		cmd := exec.Command("git", "-C", repo, "config", kv[0], kv[1])
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git config %s: %v %s", kv[0], err, out)
		}
	}
}
