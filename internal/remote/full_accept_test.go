package remote_test

// The acceptance tests for remote Review, run for real over a private sshd:
// content (Tail, title, folder) crosses only under full, and so does a diff,
// which `den remote serve` reads on host b from a real git checkout in its
// scratch home. The checkout's config tries to make git run programs; none
// runs.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/remote"
	"github.com/GlacierHubAB/den/internal/sshtest"
)

func fullRig(t *testing.T) *rig {
	t.Helper()
	g := newRig(t)
	g.l.Local.WriteRemotes(g.l.RemotesTOML("full"))
	return g
}

func TestAcceptS4ContentOnlyUnderFull(t *testing.T) {
	for _, policy := range []string{"observe", "approve", "full"} {
		t.Run(policy, func(t *testing.T) {
			g := newRig(t)
			g.l.Local.WriteRemotes(g.l.RemotesTOML(policy))
			g.r.CapturePipe(true)
			g.r.SeedSession(sshtest.SecretOpts())
			g.start()
			g.live()
			g.wait("the agent", 5*time.Second, func() bool { return len(g.m.Agents()) == 1 })
			time.Sleep(500 * time.Millisecond) // a later snapshot crosses too
			out := g.r.PipeOut()
			if !bytes.Contains(out, []byte(`"snapshot"`)) {
				t.Fatalf("capture is empty (%d bytes): the test would prove nothing", len(out))
			}
			a := g.m.Agents()[0]
			if policy == "full" {
				for _, want := range []string{"SECRET-title", "SECRET-reply", "SECRET-prompt", "SECRET-command", "SECRET-cwd"} {
					if !bytes.Contains(out, []byte(want)) {
						t.Errorf("full did not carry %s", want)
					}
				}
				if a.Title != "SECRET-title" || a.RemoteCWD != "/home/user/SECRET-cwd" || len(a.Tail) == 0 || a.Session.CWD != "" {
					t.Errorf("agent: title %q remote cwd %q tail %d local cwd %q", a.Title, a.RemoteCWD, len(a.Tail), a.Session.CWD)
				}
			} else {
				if bytes.Contains(out, []byte("SECRET")) || bytes.Contains(out, []byte(`"full"`)) {
					t.Errorf("%s: content crossed the pipe:\n%s", policy, out)
				}
				if a.Title != "" || a.RemoteCWD != "" || len(a.Tail) > 0 {
					t.Errorf("%s: the agent has content: %+v", policy, a)
				}
			}
			// Under every policy, nothing of it is written down locally.
			if hits := g.l.Local.Grep("SECRET"); len(hits) > 0 {
				t.Errorf("SECRET found in local state: %v", hits)
			}
		})
	}
}

// remoteRepo makes a git checkout in host b's home on a feature branch with
// an edit, an untracked file, a binary file, a huge file and a file full of
// escape sequences, and a config that names a program for git at every
// hook it would run while reading; each writes a marker into marks.
func remoteRepo(t *testing.T, g *rig) (repo, marks string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repo = filepath.Dir(g.r.WriteFile("work/a.txt", "one\n"))
	marks = filepath.Dir(g.r.WriteFile("marks/.keep", ""))
	os.Remove(filepath.Join(marks, ".keep"))
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "HOME="+g.r.Home, "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=user", "GIT_AUTHOR_EMAIL=user@example.com", "GIT_COMMITTER_NAME=user", "GIT_COMMITTER_EMAIL=user@example.com",
			"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	g.r.WriteFile("work/huge.txt", "x\n")
	run("add", ".")
	run("commit", "-qm", "init")
	run("checkout", "-qb", "feature")
	g.r.WriteFile("work/a.txt", "one\ntwo\n")
	var huge strings.Builder
	for i := range 3000 {
		fmt.Fprintf(&huge, "line %d\n", i)
	}
	g.r.WriteFile("work/huge.txt", huge.String())
	g.r.WriteFile("work/new.txt", "hello\n")
	g.r.WriteFile("work/bin.dat", "ab\x00cd")
	g.r.WriteFile("work/esc.txt", "hi \x1b]52;c;aGk=\x07there\x1b[2J!\n")
	mark := func(name string) string { return "touch " + filepath.Join(marks, name) + "; cat" }
	run("config", "core.fsmonitor", mark("fsmonitor"))
	run("config", "filter.evil.clean", mark("clean"))
	run("config", "filter.evil.process", mark("process"))
	run("config", "diff.evil.textconv", mark("textconv"))
	run("config", "diff.external", mark("external"))
	os.WriteFile(filepath.Join(repo, ".git", "info", "attributes"), []byte("* filter=evil diff=evil\n"), 0o644)
	return repo, marks
}

func TestAcceptS4DiffOverSSH(t *testing.T) {
	g := fullRig(t)
	repo, marks := remoteRepo(t, g)
	g.r.SeedSession(sshtest.SessionOpts{ID: idA, CWD: repo})
	g.r.SeedSession(sshtest.SessionOpts{ID: idB, CWD: repo})
	// idA wrote every changed file; idB wrote none of them.
	at := time.Now().Add(time.Second).UTC().Format(time.RFC3339Nano) // after the session started
	var tr strings.Builder
	for i, f := range []string{"a.txt", "huge.txt", "new.txt", "bin.dat", "esc.txt"} {
		p := filepath.Join(repo, f)
		fmt.Fprintf(&tr, `{"type":"assistant","timestamp":%q,"message":{"content":[{"type":"tool_use","id":"w%d","name":"Write","input":{"file_path":%q}}]}}`+"\n", at, i, p)
		fmt.Fprintf(&tr, `{"type":"user","timestamp":%q,"message":{"content":[{"type":"tool_result","tool_use_id":"w%d","content":"ok"}]},"toolUseResult":{"type":"create","filePath":%q,"content":"x\n"}}`+"\n", at, i, p)
	}
	g.r.WriteFile(".claude/projects/-work/"+idA+".jsonl", tr.String())
	g.start()
	g.live()
	g.wait("the agents", 5*time.Second, func() bool { return len(g.m.Agents()) == 2 })

	if d, err := g.m.Diff("b", idB); err != nil || !d.Unowned || len(d.Files) != 0 {
		t.Errorf("a session that wrote none of it got %+v, %v", d, err)
	}
	start := time.Now()
	d, err := g.m.Diff("b", idA)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("diff across ssh took %v, %d files, %s", time.Since(start).Round(time.Millisecond), len(d.Files), d.Cut)
	byPath := map[string]remote.DiffFile{}
	for _, f := range d.Files {
		byPath[f.Path] = f
	}
	if d.Repo != "work" || d.Branch != "feature" || d.Root != repo || d.Unowned || len(byPath) != 5 {
		t.Fatalf("diff = %+v", d)
	}
	if f := byPath["a.txt"]; len(f.Hunks) != 1 || strings.Join(f.Hunks[0].Lines, "|") != " one|+two" {
		t.Errorf("a.txt = %+v", f)
	}
	if !byPath["new.txt"].Created || !byPath["bin.dat"].Binary {
		t.Errorf("new %+v, bin %+v", byPath["new.txt"], byPath["bin.dat"])
	}
	if f := byPath["huge.txt"]; !f.Cut || !strings.HasPrefix(d.Cut, "cut:") {
		t.Errorf("huge.txt cut %v, %q", f.Cut, d.Cut)
	}
	esc := strings.Join(byPath["esc.txt"].Hunks[0].Lines, "")
	if strings.ContainsAny(esc, "\x1b\x07") || !strings.Contains(esc, "there") {
		t.Errorf("escapes crossed: %q", esc)
	}
	if ents, _ := os.ReadDir(marks); len(ents) > 0 {
		t.Errorf("the checkout's config made the host run %s", ents[0].Name())
	}
	// Nothing of it is written down locally.
	if hits := g.l.Local.Grep("line 1999"); len(hits) > 0 {
		t.Errorf("diff content in local state: %v", hits)
	}

	g.r.Down()
	g.l.KillSSH()
	g.wait("offline", 20*time.Second, func() bool { return g.status().State == remote.Offline })
	if _, err := g.m.Diff("b", idA); err == nil || err.Error() != "b is offline; no diff was read" {
		t.Errorf("diff while offline: %v", err)
	}
	g.r.Up()
}

func TestAcceptS4DiffRefusedBelowFull(t *testing.T) {
	for _, policy := range []string{"observe", "approve"} {
		t.Run(policy, func(t *testing.T) {
			g := newRig(t)
			g.l.Local.WriteRemotes(g.l.RemotesTOML(policy))
			g.r.CapturePipe(true)
			repo, _ := remoteRepo(t, g)
			g.r.SeedSession(sshtest.SessionOpts{ID: idA, CWD: repo})
			g.start()
			g.live()
			_, err := g.m.Diff("b", idA)
			if err == nil || !strings.Contains(err.Error(), "policy "+policy) || !strings.Contains(err.Error(), `policy = "full"`) {
				t.Errorf("diff under %s: %v", policy, err)
			}
			if bytes.Contains(g.r.PipeIn(), []byte(`"diff"`)) {
				t.Errorf("Den asked for a diff under %s", policy)
			}
		})
	}
}
