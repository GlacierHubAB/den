package ui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/remote"
	"github.com/GlacierHubAB/den/internal/review"
	"github.com/GlacierHubAB/den/internal/transcript"
)

// runReview runs a command the Review view started and applies what it
// produces, as the update loop would.
func runReview(m *Model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			runReview(m, c)
		}
	case reviewRemoteMsg:
		runReview(m, m.applyRemoteReview(msg))
	case reviewLoadedMsg:
		m.applyReview(msg)
	case sentMsg:
		m.applyReviewSent(msg)
	}
}

func setPolicy(f *fakeFeed, label string, p remote.Policy) {
	for i := range f.hosts {
		if f.hosts[i].Label == label {
			f.hosts[i].Policy, f.hosts[i].Applied = p, p
		}
	}
}

func remoteDiff() *remote.Diff {
	return &remote.Diff{
		Root: "/home/user/den", Repo: "den", Branch: "feature", Worktree: true, Ahead: 2,
		Cut: "cut: files over 2000 lines",
		Files: []remote.DiffFile{
			{Path: "main.go", Hunks: []remote.DiffHunk{{OldStart: 1, NewStart: 1, Lines: []string{" package main", "+func run() {}"}, Mine: []int{1}}}},
			{Path: "huge.txt", Cut: true, Hunks: []remote.DiffHunk{{NewStart: 1, Lines: []string{"+x"}, Mine: []int{0}}}},
		},
	}
}

// Agents on hosts whose policy is not full stay greyed, saying why and how
// to change it; an offline host says so; nothing is asked of either.
func TestReviewRemoteGreyedBelowFull(t *testing.T) {
	m, feed, _ := remoteModel(t)
	m.tab = tabReview
	m.proj.cur = "" // every project: both hosts
	runReview(m, m.loadReview())
	out := frame(m)
	for _, want := range []string{"Mossy Lemur @devbox", "diff on devbox, policy observe", `policy = "full"`, "gpu is offline"} {
		if !strings.Contains(strings.Join(strings.Fields(out), " "), want) {
			t.Errorf("Review lacks %q:\n%s", want, out)
		}
	}
	if len(feed.diffs) > 0 {
		t.Errorf("asked for a diff below full: %v", feed.diffs)
	}
	setPolicy(feed, "devbox", remote.Approve)
	m.syncRemote()
	if out := frame(m); !strings.Contains(out, "diff on devbox, policy approve") {
		t.Errorf("approve not said:\n%s", out)
	}
}

func TestReviewRemoteUnderFull(t *testing.T) {
	m, feed, _ := remoteModel(t)
	setPolicy(feed, "devbox", remote.Full)
	m.syncRemote()
	m.tab = tabReview
	m.proj.cur = "den @devbox"
	feed.diff = remoteDiff()
	runReview(m, m.loadReview())
	if len(feed.diffs) != 1 || feed.diffs[0] != "devbox/"+dupSession {
		t.Fatalf("diffs asked = %v (the host gets the bare session id)", feed.diffs)
	}
	if len(m.rev.works) != 1 {
		t.Fatalf("works = %+v", m.rev.works)
	}
	w := m.rev.works[0]
	if w.Host != "devbox" || w.SessionID != dupSession+"@devbox" || w.Root != "/home/user/den @devbox" || w.Main != "den @devbox" || w.Added != 2 {
		t.Errorf("work = %+v", w)
	}
	out := frame(m)
	for _, want := range []string{"Mossy Lemur @devbox", "den · feature", "cut: files over 2000 lines", "func run"} {
		if !strings.Contains(out, want) {
			t.Errorf("Review lacks %q:\n%s", want, out)
		}
	}

	// Merging or removing its worktree stays on this machine.
	for _, run := range []func([]string) (tea.Model, tea.Cmd){m.mergeCommand, m.cleanupCommand} {
		mm, _ := run(nil)
		if got := mm.(Model).flash; !strings.Contains(got, "on devbox") || !strings.Contains(got, "this machine only") {
			t.Errorf("worktree action: %q", got)
		}
	}

	// A comment /sends through the host's session.
	m.rev.commentsPath = filepath.Join(t.TempDir(), "reviews.json")
	m.rev.comments = append(m.rev.comments, review.Comment{ID: "c1", Root: w.Root, Host: "devbox", Path: "main.go", Side: "new", Start: 2, End: 2,
		Quote: []string{"+func run() {}"}, Body: "name it start", SessionID: w.SessionID, Created: time.Now()})
	mm, cmd := m.sendReview(nil)
	*m = mm.(Model)
	runReview(m, cmd)
	if len(feed.sent) != 1 || !strings.HasPrefix(feed.sent[0], "devbox/"+dupSession+": ") || !strings.Contains(feed.sent[0], "name it start") {
		t.Errorf("sent = %v", feed.sent)
	}
	if m.rev.comments[0].Draft() {
		t.Error("the comment is still a draft")
	}
	// It quotes the host's diff: it is never written down.
	if raw, err := os.ReadFile(m.rev.commentsPath); err != nil || strings.Contains(string(raw), "name it start") || strings.Contains(string(raw), "func run") {
		t.Errorf("reviews.json holds a remote comment: %s", raw)
	}

	// The link drops: the diff goes, and is not shown again as current.
	feed.setState("devbox", remote.Offline)
	m.syncRemote()
	m.remoteReviewChanged("devbox")
	if len(m.rev.works) != 0 || !strings.Contains(frame(m), "devbox is offline") {
		t.Errorf("after the drop: works %d\n%s", len(m.rev.works), frame(m))
	}
}

func TestReviewRemoteLoadingAndFailure(t *testing.T) {
	m, feed, _ := remoteModel(t)
	setPolicy(feed, "devbox", remote.Full)
	m.syncRemote()
	m.tab = tabReview
	m.proj.cur = "den @devbox"
	cmd := m.loadRemoteReviews()
	if out := frame(m); !strings.Contains(out, "reading its diff on devbox") {
		t.Errorf("no loading state:\n%s", out)
	}
	if again := m.loadRemoteReviews(); again != nil {
		t.Error("a second read started while one is in flight")
	}
	feed.diffErr = errors.New("devbox did not answer within 30s; whether it acted is not known")
	runReview(m, cmd)
	if out := frame(m); !strings.Contains(out, "did not answer within 30s") {
		t.Errorf("no failure said:\n%s", out)
	}
}

// The details pane shows a remote agent's folder, title and recent
// activity when its host sent them (full), and none otherwise.
func TestRemoteDetailsUnderFull(t *testing.T) {
	m, feed, _ := remoteModel(t)
	for i := range feed.agents {
		if feed.agents[i].Host == "devbox" {
			feed.agents[i].Title = "port the parser to Go"
			feed.agents[i].RemoteCWD = "/home/user/den"
			feed.agents[i].Tail = []transcript.Entry{{Kind: "text", Text: "Ported the lexer.", At: time.Now()}}
		}
	}
	m.syncRemote()
	m.refresh()
	for i, a := range m.agents {
		if a.Host == "devbox" {
			m.cursor = i
		}
	}
	out := m.detailPane(80, 60)
	for _, want := range []string{"/home/user/den", "port the parser to Go", "Ported the lexer."} {
		if !strings.Contains(out, want) {
			t.Errorf("details lack %q:\n%s", want, out)
		}
	}
	for i, a := range m.agents {
		if a.Host == "gpu" && a.Status != fleet.Gone {
			m.cursor = i
		}
	}
	if out := m.detailPane(80, 60); strings.Contains(out, "recent") || strings.Contains(out, "cwd") {
		t.Errorf("an agent without content shows some:\n%s", out)
	}
}

// serve reads at most remote.MaxDiffs diffs at once; Review asks for no
// more per host, and the next as each answer comes.
func TestReviewRemoteAsksMaxDiffsPerHost(t *testing.T) {
	m, feed, _ := remoteModel(t)
	setPolicy(feed, "devbox", remote.Full)
	for _, id := range []string{"33333333-2222-4333-8444-555555555555", "44444444-2222-4333-8444-555555555555", "55555555-2222-4333-8444-555555555555"} {
		feed.agents = append(feed.agents, remoteAgent("devbox", id, "Agent "+id[:2], fleet.Idle, 0))
	}
	m.syncRemote()
	m.refresh()
	m.tab = tabReview
	cmd := m.loadRemoteReviews()
	loading := 0
	for _, rr := range m.rev.remote {
		if rr.loading {
			loading++
		}
	}
	if loading != remote.MaxDiffs {
		t.Fatalf("%d diffs asked at once, want %d", loading, remote.MaxDiffs)
	}
	feed.diff = remoteDiff()
	runReview(m, cmd)
	if len(feed.diffs) != 4 {
		t.Errorf("asked %d of 4 agents: %v", len(feed.diffs), feed.diffs)
	}
}

// A remote agent's content never goes into the orchestrator's prompt, and
// a host that is not live shows none of it.
func TestRemoteContentStaysOutOfTheRosterAndOffline(t *testing.T) {
	m, feed, _ := remoteModel(t)
	for i := range feed.agents {
		feed.agents[i].Title, feed.agents[i].RemoteCWD = "SECRET-title", "/home/user/SECRET"
		feed.agents[i].Labels.Task = ""
		feed.agents[i].Tail = []transcript.Entry{{Kind: "text", Text: "SECRET-reply", At: time.Now()},
			{Kind: "tool", Tool: "Edit", Changes: []transcript.FileChange{{Path: "/home/user/SECRET/a.go"}}}}
	}
	m.syncRemote()
	m.refresh()
	for _, r := range m.roster() {
		if r.Host != "" && (strings.Contains(fmt.Sprint(r), "SECRET")) {
			t.Errorf("roster carries remote content: %+v", r)
		}
	}
	for _, a := range m.agents {
		if a.Host == "gpu" && (a.Title != "" || len(a.Tail) > 0 || a.RemoteCWD != "") {
			t.Errorf("an offline host's agent keeps content: %+v", a)
		}
	}
}

// Agents whose diff fails never keep a host's slots from the others: each
// round reads every agent once, the one read longest ago first, and a
// failing agent waits out a growing backoff, still showing its error.
func TestReviewRemoteFailingDiffsDoNotStarveTheOthers(t *testing.T) {
	m, feed, _ := remoteModel(t)
	setPolicy(feed, "devbox", remote.Full)
	bad := "33333333-2222-4333-8444-555555555555"
	good := "44444444-2222-4333-8444-555555555555"
	// The failing agents come first in the fleet.
	feed.agents[0].Name = "Agent One"
	feed.agents = append(feed.agents, remoteAgent("devbox", bad, "Agent Two", fleet.Idle, 0), remoteAgent("devbox", good, "Zesty Otter", fleet.Idle, 0))
	fail := errors.New("git found no branch to compare its work with")
	feed.diffErrs = map[string]error{dupSession: fail, bad: fail}
	feed.diff = remoteDiff()
	now := time.Now()
	reviewNow = func() time.Time { return now }
	t.Cleanup(func() { reviewNow = time.Now })
	m.syncRemote()
	m.refresh()
	m.tab = tabReview
	m.proj.cur = ""
	asked := func(sid string) int {
		n := 0
		for _, d := range feed.diffs {
			if d == "devbox/"+sid {
				n++
			}
		}
		return n
	}
	failing := map[string]int{}
	for round := 1; round <= 6; round++ {
		feed.diffs = nil
		cmd := m.loadRemoteReviews()
		loading := 0
		for _, rr := range m.rev.remote {
			if rr.loading {
				loading++
			}
		}
		if loading > remote.MaxDiffs {
			t.Fatalf("round %d: %d diffs asked at once", round, loading)
		}
		runReview(m, cmd)
		if asked(good) != 1 {
			t.Fatalf("round %d: the working agent's diff was not read again (asked %v)", round, feed.diffs)
		}
		for _, sid := range []string{dupSession, bad} {
			failing[sid] += asked(sid)
		}
		now = now.Add(5 * time.Second)
	}
	// Asked at 0 s, again at 15 s (backoff 15 s), then not before 45 s.
	if failing[dupSession] != 2 || failing[bad] != 2 {
		t.Errorf("failing agents asked %v times in 25 s", failing)
	}
	if !strings.Contains(frame(m), "func run") {
		t.Errorf("Review lacks the working agent's diff:\n%s", frame(m))
	}
	for _, a := range m.agents {
		if a.Host == "devbox" && a.Name != "Zesty Otter" {
			if note := m.remoteNote(a); note != fail.Error() {
				t.Errorf("%s in backoff says %q", a.Name, note)
			}
		}
	}
}

func TestDiffBackoffGrowsAndIsCapped(t *testing.T) {
	for fails, want := range map[int]time.Duration{1: 15 * time.Second, 2: 30 * time.Second, 3: time.Minute, 5: 4 * time.Minute, 6: 5 * time.Minute, 40: 5 * time.Minute} {
		if got := backoff(fails); got != want {
			t.Errorf("backoff(%d) = %v, want %v", fails, got, want)
		}
	}
}

// An agent whose diff keeps failing is asked less and less often, as a
// real host's 20 s rounds go by, while another agent is read every round;
// one good read puts it back in every round.
func TestReviewRemoteBackoffGrowsWhileTheDiffKeepsFailing(t *testing.T) {
	m, feed, _ := remoteModel(t)
	setPolicy(feed, "devbox", remote.Full)
	good := "44444444-2222-4333-8444-555555555555"
	feed.agents = append(feed.agents, remoteAgent("devbox", good, "Zesty Otter", fleet.Idle, 0))
	feed.diffErrs = map[string]error{dupSession: errors.New("git found no branch to compare its work with")}
	feed.diff = remoteDiff()
	start := time.Now()
	now := start
	reviewNow = func() time.Time { return now }
	t.Cleanup(func() { reviewNow = time.Now })
	m.syncRemote()
	m.refresh()
	m.tab = tabReview
	m.proj.cur = ""
	round := func() (failing, working bool) {
		feed.diffs = nil
		runReview(m, m.loadRemoteReviews())
		for _, d := range feed.diffs {
			failing = failing || d == "devbox/"+dupSession
			working = working || d == "devbox/"+good
		}
		return failing, working
	}
	var asked []time.Duration
	for at := time.Duration(0); at <= 3*time.Minute; at += 20 * time.Second {
		now = start.Add(at)
		failing, working := round()
		if !working {
			t.Fatalf("at %v the working agent was not read", at)
		}
		if failing {
			asked = append(asked, at)
		}
	}
	// Backoffs of 15 s, 30 s, 1 min and 2 min: asked at 0, 20 s, 1 min and 2 min.
	want := []time.Duration{0, 20 * time.Second, time.Minute, 2 * time.Minute}
	if fmt.Sprint(asked) != fmt.Sprint(want) {
		t.Errorf("the failing agent was asked at %v, want %v", asked, want)
	}

	// Once its diff reads, it is read every round again.
	delete(feed.diffErrs, dupSession)
	now = start.Add(5 * time.Minute)
	if failing, _ := round(); !failing {
		t.Fatal("not asked after its backoff")
	}
	now = now.Add(20 * time.Second)
	if failing, _ := round(); !failing {
		t.Errorf("not read in the next round after a good read: fails %d", m.rev.remote[dupSession].fails)
	}
}

// A remote file whose name has control characters keeps that mark, so
// Review says its content is not shown.
func TestRemoteUnsafeNameIsMarked(t *testing.T) {
	ws := workFromDiff("s@devbox", "devbox", &remote.Diff{Root: "/home/user/den", Files: []remote.DiffFile{{Path: "evilname.txt", Created: true, Unsafe: true}}})
	if len(ws) != 1 || len(ws[0].Files) != 1 || !ws[0].Files[0].Unsafe || len(ws[0].Files[0].Hunks) > 0 {
		t.Errorf("works = %+v", ws)
	}
}

// Drafts on a remote agent's work quote its host's diff: they never go to
// the orchestrator, and while the host is down they wait for it, said as
// such, never as an ended session.
func TestRemoteDraftsNeverGoToTheOrchestrator(t *testing.T) {
	m, feed, _ := remoteModel(t)
	setPolicy(feed, "devbox", remote.Full)
	m.syncRemote()
	m.tab = tabReview
	m.rev.commentsPath = filepath.Join(t.TempDir(), "reviews.json")
	remoteDraft := review.Comment{ID: "r1", Root: "/home/user/den @devbox", Host: "devbox", Path: "main.go", Side: "new", Start: 2, End: 2,
		Quote: []string{"+SECRET-remote-line"}, Body: "rename it", SessionID: dupSession + "@devbox", Created: time.Now()}
	m.rev.comments = append(m.rev.comments, remoteDraft)
	feed.setState("devbox", remote.Offline)
	m.syncRemote()

	mm, _ := m.sendReview(nil) // the cmd is not run: nothing is started
	*m = mm.(Model)
	if !strings.Contains(m.flash, "until devbox is back") || strings.Contains(m.flash, "orchestrator") {
		t.Errorf("/send: %q", m.flash)
	}
	mm, _ = m.sendReviewToOrchestrator()
	*m = mm.(Model)
	if !strings.Contains(m.flash, "until devbox is back") || strings.Contains(m.flash, "ended") {
		t.Errorf("/send orchestrator: %q", m.flash)
	}
	if !m.rev.comments[0].Draft() || len(m.orch.pending) > 0 {
		t.Fatalf("handed over: draft %v, pending %q", m.rev.comments[0].Draft(), m.orch.pending)
	}

	// With a draft for an ended local session too, only that one goes.
	m.rev.comments = append(m.rev.comments, review.Comment{ID: "l1", Root: "/home/user/proj", Path: "a.go", Side: "new", Start: 1, End: 1,
		Quote: []string{"+local line"}, Body: "local note", SessionID: "99999999-2222-4333-8444-555555555555", Created: time.Now()})
	mm, _ = m.sendReviewToOrchestrator()
	*m = mm.(Model)
	prompt := strings.Join(m.orch.pending, "\n")
	if !strings.Contains(prompt, "local note") || strings.Contains(prompt, "SECRET") || strings.Contains(prompt, "rename it") {
		t.Errorf("orchestrator prompt: %q", prompt)
	}
	if !m.rev.comments[0].Draft() || m.rev.comments[1].Draft() {
		t.Errorf("remote draft %v, local draft %v", m.rev.comments[0].Draft(), m.rev.comments[1].Draft())
	}
	if !strings.Contains(m.flash, "Handed 1 comment") || !strings.Contains(m.flash, "until devbox is back") {
		t.Errorf("flash: %q", m.flash)
	}
}
