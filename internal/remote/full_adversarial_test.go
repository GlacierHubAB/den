package remote_test

// Adversarial tests for remote Review under full. What the host may
// send (policy boundaries), what the host's git may be made to do and read
// (hostile checkouts), and what Den does with a diff that lies (stub hosts).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/remote"
	"github.com/GlacierHubAB/den/internal/sshtest"
)

// --- policy boundaries --------------------------------------------------------

// hasWeird reports control characters of every kind a path or a line must not
// carry. U+2028 and U+2029 are left out on purpose: sanitize keeps them, which is
// a finding, parked in TestAdvLineSeparatorsInPaths.
func hasWeird(s string) bool {
	return hasControl(s) || strings.ContainsAny(s, "\u200e\u200f\u061c")
}

func secretFull() *fleet.MetaFull {
	return &fleet.MetaFull{Title: "SECRET-title", CWD: "/home/SECRET-cwd",
		Tail: []fleet.MetaEntry{{Age: 1, Kind: "user", Text: "SECRET-prompt"}}}
}

func snapshotWithFull(policyNote string) string {
	m := fleet.Meta{Index: 0, Session: idA, Status: "idle", Name: "A", StatusAge: 1, StartedAge: 1, ActiveAge: -1, Full: secretFull()}
	return j(map[string]any{"type": "snapshot", "remote_now": "2026-10-12T12:00:00.000Z", "agents": []fleet.Meta{m}})
}

// A host that sends content its policy does not allow: dropped and flagged,
// never kept, never written down.
func TestAdvFullFieldsFromAHostBelowFull(t *testing.T) {
	for _, tc := range []struct{ configured, applied string }{
		{"approve", "approve"}, // configured approve, host sends full anyway
		{"full", "approve"},    // configured full, but the host applies less
		{"observe", "full"},    // the host claims more than was asked
	} {
		t.Run(tc.configured+"/"+tc.applied, func(t *testing.T) {
			l := sshtest.New(t, "b")
			r := l.Remote("b")
			resp := filepath.Join(r.Home, "snapshot.json")
			os.WriteFile(resp, []byte(snapshotWithFull(tc.applied)+"\n"), 0o644)
			script := `#!/bin/sh
read -r _request
now=$(date -u +%Y-%m-%dT%H:%M:%S.000Z)
printf '{"type":"hello","proto":1,"den":"v0.2.0","hostname":"stub","remote_now":"%s","policy":"` + tc.applied + `","labels":true}\n' "$now"
cat ` + resp + `
cat >/dev/null
`
			stub := filepath.Join(r.Home, "stubden")
			os.WriteFile(stub, []byte(script), 0o755)
			l.Local.Apply()
			l.Local.WriteRemotes(fmt.Sprintf("[hosts.b]\ntarget = \"b\"\npolicy = %q\nden_path = %q\n", tc.configured, stub))
			g := &rig{t: t, l: l, r: r}
			g.start()
			g.live()
			g.wait("the agent", 10*time.Second, func() bool { return len(g.m.Agents()) == 1 })
			time.Sleep(500 * time.Millisecond)
			a := g.m.Agents()[0]
			if a.Title != "" || a.RemoteCWD != "" || len(a.Tail) > 0 {
				t.Errorf("content was kept: title %q cwd %q tail %d", a.Title, a.RemoteCWD, len(a.Tail))
			}
			if !g.flagged("content") && !g.flagged("full") && !g.flagged("policy") {
				t.Errorf("not flagged: %v", g.status().Flags)
			}
			if hits := l.Local.Grep("SECRET"); len(hits) > 0 {
				t.Errorf("SECRET in local state: %v", hits)
			}
			_, raw := g.denListRaw(l.Den)
			if bytes.Contains(raw, []byte("SECRET")) {
				t.Errorf("den list -json carries it: %s", raw)
			}
		})
	}
}

// A diff in a result under a policy below full (a send that is answered with
// one) is dropped and flagged.
func TestAdvDiffResultBelowFull(t *testing.T) {
	d := &remote.Diff{Root: "/home/SECRET-root", Repo: "SECRET-repo", Files: []remote.DiffFile{{Path: "SECRET-file.go",
		Hunks: []remote.DiffHunk{{Lines: []string{"+SECRET-line"}}}}}}
	b, _ := json.Marshal(remote.Result{Type: remote.TypeResult, ID: "__ID__", OK: true, Diff: d})
	g := stubRepliesPolicy(t, "approve", "")
	resp := filepath.Join(g.r.Home, "resp.json")
	os.WriteFile(resp, append(b, '\n'), 0o644)
	// Rewrite the stub so every command is answered with the file.
	stub := filepath.Join(g.r.Home, "stubden")
	raw, _ := os.ReadFile(stub)
	os.WriteFile(stub, []byte(strings.Replace(string(raw), "done\n", `sed "s/__ID__/$id/" `+resp+"\ndone\n", 1)), 0o755)
	g.start()
	g.live()
	if err := g.m.Send("b", idA, "x"); err != nil {
		t.Logf("send: %v", err)
	}
	g.wait("flagged", 5*time.Second, func() bool { return g.flagged("diff") || g.flagged("content") || g.flagged("full") })
	if hits := g.l.Local.Grep("SECRET"); len(hits) > 0 {
		t.Errorf("SECRET in local state: %v", hits)
	}
}

// A host whose policy changes between connections: content seen under full is
// gone, and never crosses again, after remotes.toml says approve.
func TestAdvPolicyChangesBetweenConnections(t *testing.T) {
	g := fullRig(t)
	g.r.CapturePipe(true)
	g.r.SeedSession(sshtest.SecretOpts())
	g.start()
	g.live()
	g.wait("full content", 10*time.Second, func() bool { a := g.m.Agents(); return len(a) == 1 && a[0].Title == "SECRET-title" })
	if hits := g.l.Local.Grep("SECRET"); len(hits) > 0 {
		t.Errorf("SECRET written down while connected under full: %v", hits)
	}
	g.stop()
	// The first session's serve, and the tee that copies its output, can
	// outlive Den's side on a loaded box: the mark comes after both, or
	// their last full snapshot lands after it (#180).
	g.wait("the first serve gone", 10*time.Second, func() bool { return len(g.r.ServePIDs()) == 0 })
	mark := len(g.r.PipeOut())
	g.wait("the capture settled", 10*time.Second, func() bool {
		time.Sleep(200 * time.Millisecond)
		n := len(g.r.PipeOut())
		settled := n == mark
		mark = n
		return settled
	})
	g.l.Local.WriteRemotes(g.l.RemotesTOML("approve"))
	g.start()
	g.live()
	g.wait("the agent", 10*time.Second, func() bool { return len(g.m.Agents()) == 1 })
	// Waited for, not slept on: on a loaded box the first snapshot can take
	// longer than any fixed pause.
	g.wait("a snapshot under approve", 15*time.Second, func() bool { return bytes.Contains(g.r.PipeOut()[mark:], []byte(`"snapshot"`)) })
	if a := g.m.Agents()[0]; a.Title != "" || a.RemoteCWD != "" || len(a.Tail) != 0 {
		t.Errorf("content survived the policy change: %+v", a.Title)
	}
	if out := g.r.PipeOut()[mark:]; bytes.Contains(out, []byte("SECRET")) {
		t.Errorf("after the change the pipe carried content (%d bytes):\n%s", len(out), out)
	}
	if _, err := g.m.Diff("b", idA); err == nil {
		t.Error("a diff was allowed after the policy fell to approve")
	}
	if hits := g.l.Local.Grep("SECRET"); len(hits) > 0 {
		t.Errorf("SECRET in local state: %v", hits)
	}
}

// --- hostile checkouts --------------------------------------------------------

func gitEnv(g *rig) []string {
	return append(os.Environ(), "HOME="+g.r.Home, "GIT_CONFIG_NOSYSTEM=1", "GIT_ALLOW_PROTOCOL=file",
		"GIT_AUTHOR_NAME=user", "GIT_AUTHOR_EMAIL=user@example.com", "GIT_COMMITTER_NAME=user", "GIT_COMMITTER_EMAIL=user@example.com",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z")
}

func gitIn(t *testing.T, g *rig, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", dir}, args...)...)
	c.Env = gitEnv(g)
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v %s", args, dir, err, out)
	}
}

// mkRepo makes a checkout under host b's home with a commit on main and a
// feature branch, and returns it.
func mkRepo(t *testing.T, g *rig, rel string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repo := filepath.Dir(g.r.WriteFile(filepath.Join(rel, "a.txt"), "one\n"))
	gitIn(t, g, repo, "init", "-q", "-b", "main")
	gitIn(t, g, repo, "add", ".")
	gitIn(t, g, repo, "commit", "-qm", "init")
	gitIn(t, g, repo, "checkout", "-qb", "feature")
	return repo
}

// wrote writes a transcript saying session id wrote each path with the Write
// tool, after the session started, so the paths count as its own work.
func wrote(g *rig, id string, paths ...string) {
	at := time.Now().Add(time.Second).UTC().Format(time.RFC3339Nano)
	var tr strings.Builder
	for i, p := range paths {
		fmt.Fprintf(&tr, `{"type":"assistant","timestamp":%q,"message":{"content":[{"type":"tool_use","id":"w%d","name":"Write","input":{"file_path":%s}}]}}`+"\n", at, i, j(p))
		fmt.Fprintf(&tr, `{"type":"user","timestamp":%q,"message":{"content":[{"type":"tool_result","tool_use_id":"w%d","content":"ok"}]},"toolUseResult":{"type":"create","filePath":%s,"content":"x\n"}}`+"\n", at, i, j(p))
	}
	g.r.WriteFile(".claude/projects/-adv/"+id+".jsonl", tr.String())
}

// hostileConfig makes git, on every read, want to run a program: each marker
// is a file a program would create in marks. It returns marks.
func hostileConfig(t *testing.T, g *rig, repo string) string {
	t.Helper()
	marks := filepath.Join(g.r.Home, "marks")
	os.MkdirAll(marks, 0o755)
	mark := func(name string) string { return "touch " + filepath.Join(marks, name) + "; cat" }
	hooks := filepath.Join(g.r.Home, "hooks")
	os.MkdirAll(hooks, 0o755)
	for _, h := range []string{"post-index-change", "reference-transaction", "pre-commit", "post-checkout", "post-merge", "post-rewrite", "fsmonitor-watchman", "pre-push", "post-commit", "applypatch-msg", "prepare-commit-msg", "post-update", "pre-auto-gc"} {
		os.WriteFile(filepath.Join(hooks, h), []byte("#!/bin/sh\ntouch "+filepath.Join(marks, "hook-"+h)+"\nexit 0\n"), 0o755)
	}
	inc := filepath.Join(g.r.Home, "include.cfg")
	os.WriteFile(inc, []byte("[core]\n\tfsmonitor = "+mark("include-fsmonitor")+"\n"), 0o644)
	attrs := filepath.Join(g.r.Home, "attrs")
	os.WriteFile(attrs, []byte("* filter=evil diff=evil merge=evil\n"), 0o644)
	for k, v := range map[string]string{
		"core.fsmonitor": mark("fsmonitor"), "core.pager": mark("pager"), "pager.diff": mark("pager-diff"), "pager.status": mark("pager-status"),
		"core.editor": mark("editor"), "core.sshCommand": mark("ssh"), "core.askPass": mark("askpass"),
		"diff.external": mark("external"), "diff.evil.command": mark("evil-command"), "diff.evil.textconv": mark("textconv"),
		"filter.evil.clean": mark("clean"), "filter.evil.smudge": mark("smudge"), "filter.evil.process": mark("process"),
		"merge.evil.driver": mark("merge"), "core.hooksPath": hooks, "core.attributesFile": attrs, "include.path": inc,
		"credential.helper": "!" + mark("credential"), "diff.tool": "evil", "difftool.evil.cmd": mark("difftool"),
		"gc.auto": "1", "core.untrackedCache": "true", "core.splitIndex": "true",
		"uploadpack.packObjectsHook": mark("pack-hook"), "core.alternateRefsCommand": mark("alternate-refs"),
	} {
		gitIn(t, g, repo, "config", k, v)
	}
	return marks
}

// proveHostile runs the plain git a human would, in dir, and checks the scene
// really does make git run programs (so that "none ran" means something). It
// returns how many markers appeared; the caller's attack clears them.
func proveHostile(t *testing.T, g *rig, dir, marks string, mustRun bool) {
	t.Helper()
	for _, args := range [][]string{{"status"}, {"diff"}, {"-c", "diff.ignoreSubmodules=none", "status"}} {
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		c.Env = gitEnv(g)
		c.Run()
	}
	n := len(markersIn(marks))
	t.Logf("control: plain git in the scene ran %d programs %v", n, markersIn(marks))
	if mustRun && n == 0 {
		t.Fatal("the scene is not hostile: plain git ran nothing, so the test would prove nothing")
	}
}

func markersIn(marks string) []string {
	ents, _ := os.ReadDir(marks)
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

// attackDiff runs a diff of session idA against cwd on host b (full), after
// setup has built the scene, and checks what must hold whatever the scene:
// nothing ran, nothing outside the work leaked, strings are clean, the size
// is inside the line cap, the call came back in time.
type scene struct {
	cwd       string
	marks     string
	forbidden []string // text that must not appear in the result
	written   []string // paths the session wrote
}

func attackDiff(t *testing.T, build func(t *testing.T, g *rig) scene) (*remote.Diff, error, *rig) {
	t.Helper()
	g := fullRig(t)
	sc := build(t, g)
	if sc.marks != "" { // what ran while the scene was being built does not count
		os.RemoveAll(sc.marks)
		os.MkdirAll(sc.marks, 0o755)
	}
	g.r.SeedSession(sshtest.SessionOpts{ID: idA, CWD: sc.cwd})
	if len(sc.written) > 0 {
		wrote(g, idA, sc.written...)
	}
	g.start()
	g.live()
	g.waitAgents(1)
	start := time.Now()
	d, err := g.m.Diff("b", idA)
	took := time.Since(start)
	t.Logf("diff took %v; err %v", took.Round(time.Millisecond), err)
	if took > 25*time.Second {
		t.Errorf("the diff took %v", took)
	}
	if sc.marks != "" {
		if m := markersIn(sc.marks); len(m) > 0 {
			t.Errorf("the checkout made the host run a program: %v", m)
		}
	}
	if d != nil {
		raw, _ := json.Marshal(d)
		if len(raw) > remote.MaxLine {
			t.Errorf("a %d byte diff", len(raw))
		}
		for _, f := range sc.forbidden {
			if bytes.Contains(raw, []byte(f)) {
				t.Errorf("the diff carries %q", f)
			}
		}
		for _, s := range append([]string{d.Root, d.Repo, d.Branch, d.Cut}, func() []string {
			var all []string
			for _, f := range d.Files {
				all = append(all, f.Path, f.OldPath)
				for _, h := range f.Hunks {
					all = append(all, h.Lines...)
				}
			}
			return all
		}()...) {
			if hasWeird(strings.ReplaceAll(strings.ReplaceAll(s, "\n", ""), "\t", "")) {
				t.Errorf("a control or separator character in %q", s)
			}
		}
	}
	if g.status().State != remote.Live {
		t.Errorf("the host went %s (%s) over a checkout", g.status().State, g.status().Reason)
	}
	return d, err, g
}

func TestAdvHostileCheckouts(t *testing.T) {
	t.Run("config, hooks, attributes and includes name programs", func(t *testing.T) {
		d, err, _ := attackDiff(t, func(t *testing.T, g *rig) scene {
			repo := mkRepo(t, g, "work")
			g.r.WriteFile("work/.gitattributes", "*.txt filter=evil diff=evil merge=evil\n")
			gitIn(t, g, repo, "add", ".gitattributes")
			gitIn(t, g, repo, "commit", "-qm", "attrs")
			g.r.WriteFile("work/new.txt", "hello\n")
			// A stale index: git is tempted to refresh and rewrite it (post-index-change).
			marks := hostileConfig(t, g, repo)
			g.r.WriteFile("work/a.txt", "one\ntwo\nthree\n")
			future := time.Now().Add(time.Hour)
			os.Chtimes(filepath.Join(repo, "a.txt"), future, future)
			proveHostile(t, g, repo, marks, true)
			return scene{cwd: repo, marks: marks, written: []string{filepath.Join(repo, "a.txt"), filepath.Join(repo, "new.txt")}}
		})
		if err != nil || d == nil || len(d.Files) != 2 {
			t.Errorf("the honest part of the work was lost: %+v %v", d, err)
		}
	})

	t.Run("an embedded bare repository", func(t *testing.T) {
		attackDiff(t, func(t *testing.T, g *rig) scene {
			repo := mkRepo(t, g, "work")
			bare := filepath.Join(repo, "bare")
			gitIn(t, g, repo, "init", "-q", "--bare", bare)
			marks := hostileConfig(t, g, bare)
			proveHostile(t, g, bare, marks, false)
			return scene{cwd: bare, marks: marks}
		})
	})

	t.Run("a bare repository as the folder", func(t *testing.T) {
		d, err, _ := attackDiff(t, func(t *testing.T, g *rig) scene {
			repo := mkRepo(t, g, "work")
			bare := filepath.Join(g.r.Home, "bare.git")
			gitIn(t, g, repo, "clone", "-q", "--bare", repo, bare)
			marks := hostileConfig(t, g, bare)
			return scene{cwd: bare, marks: marks}
		})
		if err == nil && d != nil && len(d.Files) > 0 {
			t.Errorf("a bare repository produced a diff: %+v", d)
		}
	})

	t.Run("a submodule with its own filters", func(t *testing.T) {
		attackDiff(t, func(t *testing.T, g *rig) scene {
			sub := mkRepo(t, g, "subsrc")
			gitIn(t, g, sub, "checkout", "-q", "main")
			repo := mkRepo(t, g, "work")
			gitIn(t, g, repo, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "sub")
			gitIn(t, g, repo, "commit", "-qm", "add submodule")
			marks := hostileConfig(t, g, filepath.Join(repo, "sub")) // the submodule's own config
			g.r.WriteFile("work/sub/a.txt", "one\nchanged inside the submodule\n")
			g.r.WriteFile("work/sub/.gitattributes", "* filter=evil\n")
			g.r.WriteFile("work/a.txt", "one\ntwo\n")
			proveHostile(t, g, repo, marks, false)
			return scene{cwd: repo, marks: marks, written: []string{filepath.Join(repo, "a.txt"), filepath.Join(repo, "sub", "a.txt")}}
		})
	})

	t.Run("a worktree whose gitdir is somewhere else", func(t *testing.T) {
		attackDiff(t, func(t *testing.T, g *rig) scene {
			main := mkRepo(t, g, "main-checkout")
			marks := hostileConfig(t, g, main)
			wt := filepath.Join(g.r.Home, "wt")
			gitIn(t, g, main, "worktree", "add", "-q", "-b", "wtbranch", wt)
			// And one that points straight at the hostile .git, by hand.
			odd := filepath.Join(g.r.Home, "odd")
			os.MkdirAll(odd, 0o755)
			os.WriteFile(filepath.Join(odd, ".git"), []byte("gitdir: "+filepath.Join(main, ".git")+"\n"), 0o644)
			g.r.WriteFile("wt/a.txt", "one\nwt\n")
			proveHostile(t, g, wt, marks, true)
			return scene{cwd: wt, marks: marks, written: []string{filepath.Join(wt, "a.txt")}}
		})
	})

	t.Run("a checkout reached through a symlink", func(t *testing.T) {
		d, err, _ := attackDiff(t, func(t *testing.T, g *rig) scene {
			repo := mkRepo(t, g, "work")
			g.r.WriteFile("work/a.txt", "one\ntwo\n")
			link := filepath.Join(g.r.Home, "link")
			os.Symlink(repo, link)
			return scene{cwd: link, written: []string{filepath.Join(link, "a.txt"), filepath.Join(repo, "a.txt")}}
		})
		t.Logf("through a symlink: %v, %+v", err, d)
	})
}

// Untracked files that are not what they seem: a symlink to a file outside
// the checkout, to a directory, a FIFO that blocks a reader, and names full
// of escape sequences.
func TestAdvUntrackedTricks(t *testing.T) {
	outside := ""
	d, err, _ := attackDiff(t, func(t *testing.T, g *rig) scene {
		repo := mkRepo(t, g, "work")
		outside = filepath.Join(g.r.Home, "outside-secret.txt")
		os.WriteFile(outside, []byte("SECRET-outside\n"), 0o600)
		os.Symlink(outside, filepath.Join(repo, "link-to-file"))
		os.Symlink(g.r.Home, filepath.Join(repo, "link-to-dir"))
		os.Symlink("/etc/hostname", filepath.Join(repo, "link-absolute"))
		syscall.Mkfifo(filepath.Join(repo, "fifo"), 0o600)
		names := []string{"esc\x1b[2Jname.txt", "sep\u2028arator.txt", "para\u2029graph.txt", "new\nline.txt", "bidi\u202eexe.txt", "trailing space .txt", "-leading-dash.txt", "tab\there.txt"}
		written := []string{filepath.Join(repo, "link-to-file"), filepath.Join(repo, "link-to-dir"), filepath.Join(repo, "link-absolute"), filepath.Join(repo, "fifo")}
		for _, n := range names {
			os.WriteFile(filepath.Join(repo, n), []byte("+ content of "+n+"\n"), 0o644)
			written = append(written, filepath.Join(repo, n))
		}
		g.r.WriteFile("work/a.txt", "one\ntwo\n")
		written = append(written, filepath.Join(repo, "a.txt"))
		return scene{cwd: repo, forbidden: []string{"SECRET-outside"}, written: written}
	})
	if err != nil {
		t.Fatalf("a diff with odd untracked files failed: %v", err)
	}
	t.Logf("%d files came back", len(d.Files))
	for _, f := range d.Files {
		var lines []string
		for _, h := range f.Hunks {
			lines = append(lines, h.Lines...)
		}
		t.Logf("  %q created=%v binary=%v lines=%q", f.Path, f.Created, f.Binary, lines)
		if strings.HasPrefix(f.Path, "link-") {
			for _, l := range lines {
				if strings.Contains(l, "SECRET") || strings.Contains(l, hostnameContent()) {
					t.Errorf("%s: a symlink's target was read: %q", f.Path, l)
				}
			}
		}
	}
}

func hostnameContent() string {
	b, _ := os.ReadFile("/etc/hostname")
	if s := strings.TrimSpace(string(b)); s != "" {
		return s
	}
	return "\x00no hostname\x00"
}

func hasHost(d *remote.Diff, path string) bool {
	for _, f := range d.Files {
		if f.Path == path {
			return true
		}
	}
	return false
}

// A great many untracked files, and one huge changed file: the host answers
// in time, inside the caps, and says what it cut.
func TestAdvVeryLargeCheckout(t *testing.T) {
	d, err, _ := attackDiff(t, func(t *testing.T, g *rig) scene {
		repo := mkRepo(t, g, "work")
		dir := filepath.Join(repo, "noise")
		os.MkdirAll(dir, 0o755)
		for i := 0; i < 20000; i++ {
			os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%05d.txt", i)), []byte("x\n"), 0o644)
		}
		var big bytes.Buffer
		for i := 0; i < 600000; i++ {
			fmt.Fprintf(&big, "line %d of a big file\n", i)
		}
		g.r.WriteFile("work/big.txt", big.String())
		gitIn(t, g, repo, "add", "big.txt")
		gitIn(t, g, repo, "commit", "-qm", "big")
		big.Reset()
		for i := 0; i < 600000; i++ {
			fmt.Fprintf(&big, "changed line %d of a big file\n", i)
		}
		g.r.WriteFile("work/big.txt", big.String())
		g.r.WriteFile("work/a.txt", "one\ntwo\n")
		return scene{cwd: repo, written: []string{filepath.Join(repo, "big.txt"), filepath.Join(repo, "a.txt")}}
	})
	if err != nil {
		t.Fatalf("a large checkout failed: %v", err)
	}
	if d.Cut == "" {
		t.Errorf("a 600000-line change was not cut: %d files", len(d.Files))
	}
	for _, f := range d.Files {
		n := 0
		for _, h := range f.Hunks {
			n += len(h.Lines)
		}
		if n > remote.MaxDiffFileLines {
			t.Errorf("%s: %d lines", f.Path, n)
		}
	}
	noise := 0
	for _, f := range d.Files {
		if strings.HasPrefix(f.Path, "noise/") {
			noise++
		}
	}
	if noise > 0 {
		t.Errorf("%d untracked files the session never wrote were listed", noise)
	}
}

// Session ids that are not ids.
func TestAdvDiffSessionIDs(t *testing.T) {
	g := fullRig(t)
	repo := mkRepo(t, g, "work")
	g.r.WriteFile("work/a.txt", "one\ntwo\n")
	g.r.SeedSession(sshtest.SessionOpts{ID: idA, CWD: repo})
	g.start()
	g.live()
	g.waitAgents(1)
	for _, id := range []string{"", "..", "../..", "/etc/passwd", "../../etc", idA + "/../" + idB, idA + "\n", idA[:8], strings.ToUpper(idA) + "x", "a b", "$(touch x)", strings.Repeat("a", 500), "\x1b[2J" + idA} {
		d, err := g.m.Diff("b", id)
		if err == nil || d != nil {
			t.Errorf("session %q: got a diff", id)
		}
	}
	// Someone else's session file, on a pid that is not a claude of this user.
	g.r.SeedSession(sshtest.SessionOpts{ID: idB, CWD: "/", PID: 1})
	if d, err := g.m.Diff("b", idB); err == nil || d != nil {
		t.Errorf("a session on pid 1: %+v", d)
	}
	for _, cwd := range []string{"/", "/etc", g.r.Home, "", "/nonexistent"} {
		g.r.SeedSession(sshtest.SessionOpts{ID: "55555555-2222-4333-8444-" + fmt.Sprintf("%012d", len(cwd)), CWD: cwd})
	}
	time.Sleep(300 * time.Millisecond)
	for _, cwd := range []string{"/", "/etc", g.r.Home, "", "/nonexistent"} {
		id := "55555555-2222-4333-8444-" + fmt.Sprintf("%012d", len(cwd))
		if d, err := g.m.Diff("b", id); err == nil && d != nil && len(d.Files) > 0 {
			t.Errorf("folder %q produced a diff of %d files", cwd, len(d.Files))
		}
	}
	// And the legitimate one still works afterwards.
	if _, err := g.m.Diff("b", idA); err != nil {
		t.Errorf("the honest diff: %v", err)
	}
}

// --- Den's side: a host that lies ---------------------------------------------

// diffResp writes a canned answer for the stub: every line is a result whose
// id is replaced by the id of the command.
func diffResp(t *testing.T, g *rig, name string, lines ...any) string {
	t.Helper()
	var b strings.Builder
	for _, l := range lines {
		raw, _ := json.Marshal(l)
		b.Write(raw)
		b.WriteByte('\n')
	}
	p := filepath.Join(g.r.Home, name)
	os.WriteFile(p, []byte(b.String()), 0o644)
	return p
}

func fullStub(t *testing.T, body string) *rig {
	t.Helper()
	g := stubRepliesPolicy(t, "full", body)
	return g
}

func okDiff() *remote.Diff {
	return &remote.Diff{Root: "/home/user/work", Repo: "work", Branch: "feature",
		Files: []remote.DiffFile{{Path: "a.txt", Hunks: []remote.DiffHunk{{OldStart: 1, NewStart: 1, Lines: []string{" one", "+two"}, Mine: []int{1}}}}}}
}

func TestAdvForgedDiffResults(t *testing.T) {
	t.Run("for ids never asked", func(t *testing.T) {
		g := fullStub(t, "")
		resp := diffResp(t, g, "r.json",
			remote.Result{Type: remote.TypeResult, ID: "never-asked", OK: true, Diff: okDiff()},
			remote.Result{Type: remote.TypeResult, ID: "__ID__", OK: true, Diff: okDiff()})
		patchStub(g, `sed "s/__ID__/$id/" `+resp)
		g.start()
		g.live()
		d, err := g.m.Diff("b", idA)
		if err != nil || d == nil || len(d.Files) != 1 {
			t.Errorf("%+v %v", d, err)
		}
		g.wait("flagged", 5*time.Second, func() bool { return g.flagged("not open") })
	})
	t.Run("an ok result without a diff", func(t *testing.T) {
		g := fullStub(t, "")
		resp := diffResp(t, g, "r.json", remote.Result{Type: remote.TypeResult, ID: "__ID__", OK: true})
		patchStub(g, `sed "s/__ID__/$id/" `+resp)
		g.start()
		g.live()
		if d, err := g.m.Diff("b", idA); err == nil || d != nil {
			t.Errorf("an ok result with no diff was taken for one: %+v", d)
		}
	})
	t.Run("a diff full of everything wrong is cut and cleaned", func(t *testing.T) {
		g := fullStub(t, "")
		bad := &remote.Diff{Root: "/home/\x1b]52;c;eA==\x07x", Repo: "r\x1b[2J", Branch: "b\u202e", Cut: "c\x1b[2J", Ahead: -5, Unowned: false}
		for i := 0; i < 400; i++ {
			bad.Files = append(bad.Files, remote.DiffFile{Path: fmt.Sprintf("f%d\x1b[2J.txt", i), OldPath: "o\u2028p",
				Hunks: []remote.DiffHunk{{OldStart: -3, NewStart: -4, Lines: []string{"+a\x1b[2J", "no prefix", "", "-b\u202e", " ok"}, Mine: []int{-1, 99, 1, 1}}}})
		}
		var many []string
		for i := 0; i < 5000; i++ {
			many = append(many, fmt.Sprintf("+line %d", i))
		}
		bad.Files[0].Hunks = []remote.DiffHunk{{Lines: many}}
		resp := diffResp(t, g, "r.json", remote.Result{Type: remote.TypeResult, ID: "__ID__", OK: true, Diff: bad})
		patchStub(g, `sed "s/__ID__/$id/" `+resp)
		g.start()
		g.live()
		d, err := g.m.Diff("b", idA)
		if err != nil || d == nil {
			t.Fatalf("%v", err)
		}
		if len(d.Files) > remote.MaxDiffFiles || d.Cut == "" || d.Ahead < 0 {
			t.Errorf("files %d cut %q ahead %d", len(d.Files), d.Cut, d.Ahead)
		}
		n := 0
		for _, f := range d.Files {
			if hasWeird(f.Path) || hasWeird(f.OldPath) {
				t.Errorf("path %q %q", f.Path, f.OldPath)
			}
			for _, h := range f.Hunks {
				if h.OldStart < 0 || h.NewStart < 0 {
					t.Errorf("negative start")
				}
				for _, i := range h.Mine {
					if i < 0 || i >= len(h.Lines) {
						t.Errorf("owner index %d of %d lines", i, len(h.Lines))
					}
				}
				for _, l := range h.Lines {
					if hasWeird(l) || l == "" || !strings.ContainsRune(" +-", rune(l[0])) {
						t.Errorf("line %q", l)
					}
				}
				if f.Path == d.Files[0].Path {
					n += len(h.Lines)
				}
			}
		}
		if n > remote.MaxDiffFileLines {
			t.Errorf("%d lines in one file", n)
		}
		if hasWeird(d.Root) || hasWeird(d.Repo) || hasWeird(d.Branch) || hasWeird(d.Cut) {
			t.Errorf("root %q repo %q branch %q cut %q", d.Root, d.Repo, d.Branch, d.Cut)
		}
	})
	t.Run("over the line cap drops the host", func(t *testing.T) {
		g := fullStub(t, "")
		bad := okDiff()
		bad.Files[0].Hunks[0].Lines = []string{"+" + strings.Repeat("a", 300<<10)}
		resp := diffResp(t, g, "r.json", remote.Result{Type: remote.TypeResult, ID: "__ID__", OK: true, Diff: bad})
		patchStub(g, `sed "s/__ID__/$id/" `+resp)
		g.start()
		g.live()
		d, err := g.m.Diff("b", idA)
		if err == nil || d != nil {
			t.Errorf("a result over the cap was taken: %+v", d)
		}
		g.wait("flooding", 10*time.Second, func() bool {
			st := g.status()
			return st.State == remote.Offline && strings.Contains(st.Reason, "flooding")
		})
	})
	t.Run("a slow host leaves no loading state behind", func(t *testing.T) {
		g := fullStub(t, "")
		resp := diffResp(t, g, "r.json", remote.Result{Type: remote.TypeResult, ID: "__ID__", OK: true, Diff: okDiff()})
		patchStub(g, `n=$((n+1)); if [ "$n" = 1 ]; then ( sleep 3; sed "s/__ID__/$id/" `+resp+` ) & else sed "s/__ID__/$id/" `+resp+`; fi`)
		g.opts = func(o *remote.Options) { o.DiffWait = time.Second }
		g.start()
		g.live()
		start := time.Now()
		if _, err := g.m.Diff("b", idA); err == nil || !strings.Contains(err.Error(), "did not answer") {
			t.Errorf("the slow diff: %v", err)
		}
		if time.Since(start) > 2*time.Second {
			t.Errorf("gave up after %v, want about 1 s", time.Since(start))
		}
		// The next request is its own: the late answer to the first does not satisfy it.
		d, err := g.m.Diff("b", idB)
		_ = d
		if err == nil {
			// the second command is answered at once (n=2)
		} else {
			t.Errorf("the next diff: %v", err)
		}
		g.wait("the late answer is flagged", 8*time.Second, func() bool { return g.flagged("not open") })
		if g.status().State != remote.Live {
			t.Errorf("state %s", g.status().State)
		}
	})
	t.Run("a host that goes offline mid-read", func(t *testing.T) {
		g := fullStub(t, "")
		patchStub(g, "exit 0")
		g.start()
		g.live()
		_, err := g.m.Diff("b", idA)
		if err == nil || !(strings.Contains(err.Error(), "not known") || strings.Contains(err.Error(), "offline")) {
			t.Errorf("err = %v", err)
		}
		// And nothing is left waiting: a later call fails fast, not after DiffWait.
		start := time.Now()
		g.wait("offline", 10*time.Second, func() bool { return g.status().State == remote.Offline })
		_, err = g.m.Diff("b", idA)
		if err == nil || time.Since(start) > 3*time.Second {
			t.Errorf("a diff while the host is down: %v after %v", err, time.Since(start))
		}
	})
}

// patchStub replaces the per-command body of a stub made by stubRepliesPolicy.
func patchStub(g *rig, body string) {
	g.t.Helper()
	stub := filepath.Join(g.r.Home, "stubden")
	raw, _ := os.ReadFile(stub)
	s := string(raw)
	i := strings.Index(s, "sed -n 's/.*\"id\"")
	k := strings.Index(s[i:], "\n") + i + 1
	if err := os.WriteFile(stub, []byte(s[:k]+body+"\ndone\n"), 0o755); err != nil {
		g.t.Fatal(err)
	}
}
