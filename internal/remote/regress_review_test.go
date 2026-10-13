package remote_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/sanitize"
	"github.com/GlacierHubAB/den/internal/sshtest"
)

// Regression tests for what a real host showed about Review.

// A file the agent wrote whose name has control characters is listed under
// its cleaned name and marked, with no content, rather than left out; files
// with unusual but harmless names are shown in full and not marked.
func TestRegressUnsafeNamesAreListedNotShown(t *testing.T) {
	odd := []string{"esc\x1b[2Jname.txt", "new\nline.txt", "bidi\u202eexe.txt", "sep\u2028arator.txt", "para\u2029graph.txt", "bell\a.txt"}
	plain := []string{"trailing space .txt", "-leading-dash.txt", "café.txt"}
	d, err, _ := attackDiff(t, func(t *testing.T, g *rig) scene {
		repo := mkRepo(t, g, "work")
		var written []string
		for i, n := range odd {
			p := filepath.Join(repo, n)
			if err := os.WriteFile(p, []byte("one\nSECRET-odd-"+string(rune('a'+i))+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			written = append(written, p)
		}
		for i, n := range plain {
			p := filepath.Join(repo, n)
			if err := os.WriteFile(p, []byte("one\nvisible-plain-"+string(rune('a'+i))+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			written = append(written, p)
		}
		return scene{cwd: repo, written: written}
	})
	if err != nil {
		t.Fatalf("the diff failed: %v", err)
	}
	unsafe, shown := 0, 0
	for _, f := range d.Files {
		var all strings.Builder
		for _, h := range f.Hunks {
			all.WriteString(strings.Join(h.Lines, "\n"))
		}
		t.Logf("%q unsafe=%v lines=%q", f.Path, f.Unsafe, all.String())
		if f.Path != sanitize.Code(f.Path) || f.Path == "" {
			t.Errorf("a path is listed uncleaned or empty: %q", f.Path)
		}
		if f.Unsafe {
			unsafe++
			if len(f.Hunks) != 0 {
				t.Errorf("%q is marked unsafe but its content came: %q", f.Path, all.String())
			}
		}
		if strings.Contains(all.String(), "visible-plain-") {
			shown++
			if f.Unsafe {
				t.Errorf("%q has a harmless name and is marked unsafe", f.Path)
			}
		}
		if strings.Contains(all.String(), "SECRET-odd-") {
			t.Errorf("content of a file with a control character in its name was sent: %q", f.Path)
		}
	}
	if unsafe != len(odd) {
		t.Errorf("%d files marked unsafe, want %d (every name with control characters)", unsafe, len(odd))
	}
	if shown != len(plain) {
		t.Errorf("%d harmless-named files shown with content, want %d", shown, len(plain))
	}
}

// Agents whose diff fails must not keep another agent's diff from being
// read: two agents in a repository with no branch to compare with, one in a
// normal repository that writes a new file after Review has read it once.
// Re-entering Review shows the new file, and the failing agents are asked
// with a backoff, not on every round.
func TestRegressFailingDiffsDoNotStarveOthers(t *testing.T) {
	if testing.Short() || os.Getenv("DEN_SLOW_TESTS") != "1" {
		t.Skip("types into the TUI in a private tmux: set DEN_SLOW_TESTS=1")
	}
	g := fullRig(t)
	l := g.l
	l.Local.WriteConfig("[orchestrator]\nspawn = false\n")
	good := mkRepo(t, g, "good")
	g.r.WriteFile("good/f.txt", "first\n")
	// A repository with one empty commit and no other branch.
	bare := filepath.Join(g.r.Home, "nobranch")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, g, bare, "init", "-q", "-b", "master")
	gitIn(t, g, bare, "commit", "-q", "--allow-empty", "-m", "init")
	failing := []string{"11111111-2222-4333-8444-5555555555a1", "11111111-2222-4333-8444-5555555555a2"}
	for i, id := range failing {
		g.r.SeedSession(sshtest.SessionOpts{ID: id, Name: "aaa-failing-" + string(rune('1'+i)), CWD: bare})
	}
	g.r.SeedSession(sshtest.SessionOpts{ID: idA, Name: "zzz-working", CWD: good})
	wrote(g, idA, filepath.Join(good, "f.txt"))
	g.r.CapturePipe(true)

	tm := l.Local.StartTUI(200, 56, []string{"DEN_ORCHESTRATOR_SPAWN=off", "DEN_RESTORE=off"})
	tm.WaitFor(30*time.Second, "3 agents")
	pick := func() {
		tm.Type("/project good @b") // leaves for the overview
		tm.Keys("Enter")
		time.Sleep(time.Second)
		tm.Type("/review")
		tm.Keys("Enter")
	}
	pick()
	f := tm.WaitFor(45*time.Second, "f.txt")
	t.Logf("--- review ---\n%s", sshtest.Squeeze(f))

	// The working agent writes another file; entering Review again must show it.
	g.r.WriteFile("good/plain-new.txt", "second\n")
	// A transcript only ever grows: the host reads on from where it stopped,
	// so the new write is appended, not the whole file written again.
	appendWrote(g, idA, filepath.Join(good, "plain-new.txt"))
	tm.Keys("Escape")
	tm.Type("/fleet")
	tm.Keys("Enter")
	time.Sleep(time.Second)
	pick()
	f = tm.WaitFor(60*time.Second, "plain-new.txt")
	t.Logf("--- review again ---\n%s", sshtest.Squeeze(f))

	// A minute on, the failing agents were not asked in a tight loop.
	time.Sleep(20 * time.Second)
	in := string(g.r.PipeIn())
	for _, id := range failing {
		n := 0
		for _, line := range strings.Split(in, "\n") {
			if strings.Contains(line, `"type":"diff"`) && strings.Contains(line, id) {
				n++
			}
		}
		t.Logf("%d diff requests for %s", n, id)
		if n > 8 {
			t.Errorf("%d diff requests for a failing agent: it is asked again and again", n)
		}
		if n == 0 {
			t.Errorf("a failing agent was never asked")
		}
	}
}

// appendWrote adds one Write of path to the end of session id's transcript,
// as Claude Code does, and leaves what is there alone.
func appendWrote(g *rig, id, path string) {
	file := filepath.Join(g.r.Home, ".claude/projects/-adv/"+id+".jsonl")
	old, err := os.ReadFile(file)
	if err != nil {
		g.t.Fatal(err)
	}
	at := time.Now().Add(time.Second).UTC().Format(time.RFC3339Nano)
	line := fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"content":[{"type":"tool_use","id":"w-more","name":"Write","input":{"file_path":%s}}]}}`+"\n"+
		`{"type":"user","timestamp":%q,"message":{"content":[{"type":"tool_result","tool_use_id":"w-more","content":"ok"}]},"toolUseResult":{"type":"create","filePath":%s,"content":"x\n"}}`+"\n", at, j(path), at, j(path))
	g.r.WriteFile(".claude/projects/-adv/"+id+".jsonl", string(old)+line)
}
