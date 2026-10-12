package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/gitsafe/gitsafetest"
	"github.com/GlacierHubAB/den/internal/review"
	"github.com/GlacierHubAB/den/internal/transcript"
)

// Remote Review over io.Pipe: the full policy's content in snapshots, and diff.

// sessionAt writes a live session file whose folder is cwd, and a
// transcript of lines.
func (s *scratch) sessionAt(t *testing.T, n int, id, cwd string, lines ...string) {
	t.Helper()
	now := time.Now()
	sess := fmt.Sprintf(`{"pid":%d,"sessionId":%q,"cwd":%q,"startedAt":%d,"status":"idle","statusUpdatedAt":%d,"version":"2.1.300"}`,
		os.Getpid(), id, cwd, now.Add(-time.Hour).UnixMilli(), now.Add(-time.Minute).UnixMilli())
	if err := os.WriteFile(filepath.Join(s.cfg, "sessions", fmt.Sprintf("%d.json", n)), []byte(sess), 0o600); err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(s.cfg, "projects", "-p")
	os.MkdirAll(proj, 0o755)
	os.WriteFile(filepath.Join(proj, id+".jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=user", "GIT_AUTHOR_EMAIL=user@example.com",
		"GIT_COMMITTER_NAME=user", "GIT_COMMITTER_EMAIL=user@example.com",
		// Older than the session, so its base is main, not the empty tree.
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

// workRepo is a checkout on a feature branch with every kind of change
// Review must carry: an edit, an untracked file the session wrote, a binary
// file, a huge file, and a file full of escape sequences.
func workRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repo := filepath.Join(t.TempDir(), "work")
	os.MkdirAll(repo, 0o755)
	gitIn(t, repo, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "huge.txt"), []byte("x\n"), 0o644)
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-qm", "init")
	gitIn(t, repo, "checkout", "-qb", "feature")
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\ntwo\n"), 0o644)
	var huge strings.Builder
	for i := range 3000 {
		fmt.Fprintf(&huge, "line %d\n", i)
	}
	os.WriteFile(filepath.Join(repo, "huge.txt"), []byte(huge.String()), 0o644)
	os.WriteFile(filepath.Join(repo, "new.txt"), []byte("hello\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "bin.dat"), []byte("ab\x00cd"), 0o644)
	os.WriteFile(filepath.Join(repo, "esc.txt"), []byte("hi \x1b]52;c;aGk=\x07there\x1b[2J\x9b31m!\n"), 0o644)
	return repo
}

// wroteNew is a transcript in which the session wrote new.txt in repo.
func wroteNew(repo string) []string {
	p := filepath.Join(repo, "new.txt")
	return []string{
		`{"type":"assistant","timestamp":"` + time.Now().Add(-time.Minute).UTC().Format(time.RFC3339) + `","message":{"content":[{"type":"tool_use","id":"t1","name":"Write","input":{"file_path":"` + p + `"}}]}}`,
		`{"type":"user","timestamp":"` + time.Now().Add(-time.Minute).UTC().Format(time.RFC3339) + `","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]},"toolUseResult":{"type":"create","filePath":"` + p + `","content":"hello\n"}}`,
		`{"type":"ai-title","aiTitle":"SECRET-title"}`,
	}
}

// wrote is a transcript in which the session wrote each of files in repo.
func wrote(repo string, files ...string) []string {
	at := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	var lines []string
	for i, f := range files {
		p := filepath.Join(repo, f)
		lines = append(lines,
			fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"content":[{"type":"tool_use","id":"w%d","name":"Write","input":{"file_path":%q}}]}}`, at, i, p),
			fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"content":[{"type":"tool_result","tool_use_id":"w%d","content":"ok"}]},"toolUseResult":{"type":"create","filePath":%q,"content":"x\n"}}`, at, i, p))
	}
	return lines
}

func (sv *served) diff(id, session string) {
	writeMsg(sv.in, DiffCmd{Type: TypeDiff, ID: id, Session: session})
}

func TestServeDiffUnderFull(t *testing.T) {
	s := newScratch(t)
	repo := workRepo(t)
	s.sessionAt(t, 1, sidA, repo, wroteNew(repo)...)
	sv := actHost(t, s, Full, newFakeProcs())
	sv.diff("d1", sidA)
	r := sv.result("d1")
	if !r.OK || r.Diff == nil {
		t.Fatalf("result = %+v", r)
	}
	d := r.Diff
	if d.Repo != "work" || d.Branch != "feature" || d.Worktree || d.Root != repo {
		t.Errorf("checkout = %q %q %v %q", d.Repo, d.Branch, d.Worktree, d.Root)
	}
	// The session wrote new.txt only: that is its work.
	if d.Unowned || len(d.Files) != 1 || d.Files[0].Path != "new.txt" || len(d.Files[0].Hunks[0].Mine) != 1 {
		t.Fatalf("files = %+v", d.Files)
	}

	// A session that wrote none of the changes gets none of them: the
	// rest of a dirty checkout never leaves the host.
	s.sessionAt(t, 2, "22222222-2222-4333-8444-555555555555", repo)
	sv.diff("d2", "22222222-2222-4333-8444-555555555555")
	if d = sv.result("d2").Diff; !d.Unowned || len(d.Files) != 0 {
		t.Fatalf("unowned diff = %+v", d)
	}

	// One that wrote every file, by path, gets each kind of change.
	s.sessionAt(t, 3, "33333333-2222-4333-8444-555555555555", repo, wrote(repo, "a.txt", "huge.txt", "new.txt", "bin.dat", "esc.txt")...)
	sv.diff("d3", "33333333-2222-4333-8444-555555555555")
	d = sv.result("d3").Diff
	byPath := map[string]DiffFile{}
	for _, f := range d.Files {
		byPath[f.Path] = f
	}
	if d.Unowned || len(byPath) != 5 {
		t.Fatalf("diff = %+v", d)
	}
	if f := byPath["bin.dat"]; !f.Binary || len(f.Hunks) != 0 {
		t.Errorf("binary = %+v", f)
	}
	if f := byPath["huge.txt"]; !f.Cut || !strings.Contains(d.Cut, "cut: files over 2000 lines") {
		t.Errorf("huge cut %v, %q", f.Cut, d.Cut)
	}
	if f := byPath["a.txt"]; len(f.Hunks) != 1 || strings.Join(f.Hunks[0].Lines, "|") != " one|+two" {
		t.Errorf("a.txt = %+v", f)
	}
	esc := strings.Join(byPath["esc.txt"].Hunks[0].Lines, "")
	if strings.ContainsAny(esc, "\x1b\x07\u009b") || !strings.Contains(esc, "hi") || !strings.Contains(esc, "there") {
		t.Errorf("escapes crossed: %q", esc)
	}
	raw, _ := json.Marshal(r)
	if len(raw) > MaxLine {
		t.Errorf("result is %d bytes", len(raw))
	}
}

// A repository whose config names programs for git to run: serve's diff
// runs none of them.
func TestServeDiffRunsNothingTheRepoNames(t *testing.T) {
	s := newScratch(t)
	repo, marks := gitsafetest.Hostile(t)
	s.sessionAt(t, 1, sidA, repo, wrote(repo, "f.txt", "g.md")...)
	sv := actHost(t, s, Full, newFakeProcs())
	sv.diff("d1", sidA)
	if r := sv.result("d1"); !r.OK || len(r.Diff.Files) != 2 {
		t.Fatalf("result = %+v", r)
	}
	if ents, _ := os.ReadDir(marks); len(ents) > 0 {
		t.Errorf("the repository made serve run %s", ents[0].Name())
	}
}

func TestServeDiffRefused(t *testing.T) {
	s := newScratch(t)
	repo := workRepo(t)
	s.sessionAt(t, 1, sidA, repo)
	for _, p := range []Policy{Approve, Observe} {
		sv := actHost(t, s, p, newFakeProcs())
		sv.diff("d1", sidA)
		if r := sv.result("d1"); r.OK || r.Diff != nil || !strings.Contains(r.Error, "policy is "+string(p)) {
			t.Errorf("%s: %+v", p, r)
		}
		// Wait for serve to end: under approve it holds den-remote.sock, and
		// the next serve would find it taken and apply observe.
		sv.stop()
		select {
		case <-sv.errc:
		case <-time.After(5 * time.Second):
			t.Fatal("serve did not end")
		}
	}
	sv := actHost(t, s, Full, newFakeProcs())
	sv.diff("d2", "33333333-2222-4333-8444-555555555555")
	if r := sv.result("d2"); r.OK || r.Error != "no live session with that id" {
		t.Errorf("unknown session: %+v", r)
	}
	// A path where the session id goes is just an id nobody has.
	sv.diff("d3", repo)
	if r := sv.result("d3"); r.OK || r.Diff != nil {
		t.Errorf("a path as session: %+v", r)
	}
	// Not a checkout.
	s.sessionAt(t, 2, "22222222-2222-4333-8444-555555555555", t.TempDir())
	sv.diff("d4", "22222222-2222-4333-8444-555555555555")
	if r := sv.result("d4"); r.OK || r.Error != "its folder is not in a git repository" {
		t.Errorf("no repo: %+v", r)
	}
}

func TestPackDiffByteCap(t *testing.T) {
	// Each of these takes six bytes on the wire for one (<) or three
	// (U+2028) in the string.
	for _, ch := range []string{"<", "\u2028"} {
		var lines []string
		for range MaxDiffFileLines - 1 {
			lines = append(lines, "+"+strings.Repeat(ch, 300))
		}
		var files []review.File
		for i := range 3 {
			files = append(files, review.File{Path: fmt.Sprintf("f%d", i), Hunks: []transcript.Hunk{{NewStart: 1, Lines: lines}}})
		}
		d := &Diff{}
		packDiff(d, files, "s")
		raw, _ := json.Marshal(Result{Type: TypeResult, ID: "x", OK: true, Diff: d})
		if len(raw) > MaxLine || !strings.Contains(d.Cut, "KB") {
			t.Errorf("%q: %d bytes, cut %q", ch, len(raw), d.Cut)
		}
	}
}

// A diff in a result its host's policy does not allow is dropped on arrival,
// the request fails and the host is flagged.
func TestResultDiffDroppedBelowFull(t *testing.T) {
	s := newScratch(t)
	d := &pipeDialer{serve: func(ctx context.Context, in io.Reader, out io.Writer) error {
		lr := newLineReader(in, MaxLine)
		lr.next()
		writeMsg(out, Hello{Type: TypeHello, Proto: Proto, Den: "v0.2.0", Hostname: "b", RemoteNow: time.Now(), Policy: Approve, Labels: true})
		for {
			line, err := lr.next()
			if err != nil {
				return nil
			}
			var c SendCmd
			if json.Unmarshal(line, &c) == nil && c.Type == TypeSend {
				writeMsg(out, Result{Type: TypeResult, ID: c.ID, OK: true, Diff: &Diff{Root: "/SECRET", Files: []DiffFile{{Path: "SECRET"}}}})
			}
		}
	}}
	h := hostB()
	h.Policy = Approve
	m := s.manager(d, h)
	m.Start()
	defer m.Close()
	waitFor(t, "live", func() bool { return m.Hosts()[0].State == Live })
	if err := m.Send("b", sidA, "hi"); err == nil || !strings.Contains(err.Error(), "policy does not allow") {
		t.Errorf("send = %v", err)
	}
	if !strings.Contains(strings.Join(m.Hosts()[0].Flags, ","), "a diff its policy does not allow") {
		t.Errorf("flags = %v", m.Hosts()[0].Flags)
	}
}

// Content travels only under full: the wire under observe and approve has
// no SECRET marker, under full it has the title, reply and folder, and the
// local state on disk has none under any policy.
func TestFullContentOnlyUnderFull(t *testing.T) {
	for _, p := range []Policy{Observe, Approve, Full} {
		t.Run(string(p), func(t *testing.T) {
			s := newScratch(t)
			s.session(t, 1, sidA, "coder")
			d := &pipeDialer{opts: s.serveOptions}
			h := hostB()
			h.Policy = p
			m := s.manager(d, h)
			m.Start()
			defer m.Close()
			waitFor(t, "the agent", func() bool { return len(m.Agents()) == 1 && !m.Agents()[0].Stale })
			if p == Full {
				waitFor(t, "its title", func() bool { return m.Agents()[0].Title == "SECRET-title" })
			}
			wire := d.wireBytes()
			a := m.Agents()[0]
			switch p {
			case Full:
				for _, want := range []string{"SECRET-title", "SECRET-reply", "SECRET-prompt", "SECRET-cwd"} {
					if !strings.Contains(wire, want) {
						t.Errorf("full did not carry %s", want)
					}
				}
				if !strings.Contains(a.RemoteCWD, "SECRET-cwd") || a.Session.CWD != "" || len(a.Tail) == 0 {
					t.Errorf("agent = cwd %q local cwd %q tail %d", a.RemoteCWD, a.Session.CWD, len(a.Tail))
				}
			default:
				if strings.Contains(wire, "SECRET") || strings.Contains(wire, `"full"`) {
					t.Errorf("%s carried content: %s", p, wire)
				}
				if a.Title != "" || a.RemoteCWD != "" || len(a.Tail) > 0 {
					t.Errorf("%s agent has content: %+v", p, a)
				}
			}
			filepath.Walk(filepath.Dir(s.localEvts), func(path string, info os.FileInfo, err error) error {
				if err == nil && !info.IsDir() {
					if raw, _ := os.ReadFile(path); strings.Contains(string(raw), "SECRET") {
						t.Errorf("%s holds a SECRET marker", path)
					}
				}
				return nil
			})
		})
	}
}

// A host that sends content its policy does not allow has it dropped and
// is flagged; so is a diff.
func TestRecheckDropsFullContent(t *testing.T) {
	for _, c := range []struct{ configured, says Policy }{{Approve, Full}, {Full, Approve}, {Observe, Full}} {
		s := newScratch(t)
		d := &pipeDialer{serve: func(ctx context.Context, in io.Reader, out io.Writer) error {
			newLineReader(in, MaxLine).next()
			writeMsg(out, Hello{Type: TypeHello, Proto: Proto, Den: "v0.2.0", Hostname: "b", RemoteNow: time.Now(), Policy: c.says, Labels: true})
			io.WriteString(out, `{"type":"snapshot","remote_now":"2026-10-12T12:00:00Z","agents":[{"session":"11111111-2222-4333-8444-555555555555","status":"idle","name":"Mossy Lemur","full":{"title":"SECRET-title","cwd":"/SECRET","tail":[{"age_ms":1,"kind":"text","text":"SECRET"}]}}]}`+"\n")
			<-ctx.Done()
			return nil
		}}
		h := hostB()
		h.Policy = c.configured
		m := s.manager(d, h)
		m.Start()
		waitFor(t, "the agent", func() bool { return len(m.Agents()) == 1 })
		a := m.Agents()[0]
		if a.Title != "" || a.RemoteCWD != "" || len(a.Tail) > 0 {
			t.Errorf("%+v: content kept: %+v", c, a)
		}
		waitFor(t, "the flag", func() bool {
			return len(m.Hosts()[0].Flags) > 0 && strings.Contains(strings.Join(m.Hosts()[0].Flags, ","), "policy does not allow")
		})
		if _, err := m.Diff("b", "s1"); err == nil || !strings.Contains(err.Error(), `policy = "full"`) {
			t.Errorf("%+v: diff not refused locally: %v", c, err)
		}
		m.Close()
	}
}

func TestManagerDiff(t *testing.T) {
	s := newScratch(t)
	repo := workRepo(t)
	s.sessionAt(t, 1, sidA, repo, wroteNew(repo)...)
	d := &pipeDialer{opts: s.serveOptions}
	h := hostB()
	h.Policy = Full
	m := s.manager(d, h)
	m.Start()
	defer m.Close()
	waitFor(t, "live", func() bool { return m.Hosts()[0].State == Live })
	got, err := m.Diff("b", sidA)
	if err != nil || len(got.Files) != 1 || got.Files[0].Path != "new.txt" {
		t.Fatalf("diff = %+v %v", got, err)
	}
	// Offline: said plainly, nothing asked.
	d.down.Store(true)
	d.cut()
	waitFor(t, "offline", func() bool { return m.Hosts()[0].State != Live })
	if _, err := m.Diff("b", sidA); err == nil || !strings.Contains(err.Error(), "b is offline") {
		t.Errorf("offline diff: %v", err)
	}
}

func TestFullMetaCaps(t *testing.T) {
	a := fleet.Agent{Title: strings.Repeat("t", 500)}
	for range 40 {
		a.Tail = append(a.Tail, transcript.Entry{Kind: "text", Text: strings.Repeat("y", 10<<10), At: time.Now()})
	}
	f := a.FullMeta(time.Now(), 24<<10)
	raw, _ := json.Marshal(f)
	if len([]rune(f.Title)) != fleet.FullTitleMax || !f.TailCut || len(raw) > 30<<10 || len(f.Tail) == 0 {
		t.Errorf("title %d, cut %v, %d bytes, %d entries", len([]rune(f.Title)), f.TailCut, len(raw), len(f.Tail))
	}
}
