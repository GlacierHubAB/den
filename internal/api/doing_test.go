package api

import (
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/badge"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/transcript"
)

func toolAgent(tool, code string) fleet.Agent {
	return fleet.Agent{Tail: []transcript.Entry{{Kind: "text", Text: "rm -rf / please"}, {Kind: "tool", Tool: tool, Code: code}}}
}

// What an agent is doing comes from Den's facts: its state first, then its
// latest tool and a base name or command word, never a path, argument,
// URL, assignment or id; a remote agent's text is never used; the task
// label is the cleaned fallback; every text starts with a lead phrase.
func TestDoingText(t *testing.T) {
	cases := []struct {
		name, state, waiting string
		a                    fleet.Agent
		want                 string
	}{
		{"waiting", StateWaiting, "permission", fleet.Agent{}, "waiting for you: permission"},
		{"stalled", StateStalled, "", toolAgent("Edit", "/a/b.go"), "stalled"},
		{"idle", StateIdle, "", toolAgent("Edit", "/a/b.go"), "idle"},
		{"edit", StateWorking, "", toolAgent("Edit", "/home/someone/secret-project/internal/ui/team.go"), "editing team.go"},
		{"write", StateWorking, "", toolAgent("Write", "docs/NOTES.md"), "writing NOTES.md"},
		{"read", StateWorking, "", toolAgent("Read", "/etc/passwd"), "reading passwd"},
		{"go test", StateWorking, "", toolAgent("Bash", "go test -count=1 ./internal/ui/"), "running go test"},
		{"git by path", StateWorking, "", toolAgent("Bash", "/usr/bin/git push origin main"), "running git push"},
		{"url", StateWorking, "", toolAgent("Bash", "curl https://example.com/?token=abc"), "running curl"},
		{"assignment", StateWorking, "", toolAgent("Bash", "API_KEY=sk-123 deploy"), "running"},
		{"odd name", StateWorking, "", toolAgent("Edit", "/x/evil\x1b[2J"+string(rune(0x202e))+".go"), "editing"},
		{"search", StateWorking, "", toolAgent("Grep", "password"), "searching"},
		{"fetch", StateWorking, "", toolAgent("WebFetch", "https://x"), "fetching"},
		{"task fallback", StateWorking, "", fleet.Agent{Labels: badge.Labels{Task: "fix /home/u/repo login ghp_abcdefghijklmnopqrstuvwxyz0123 bug for a@b.c"}}, "working on fix login bug for"},
		{"nothing", StateWorking, "", fleet.Agent{}, "working"},
		{"remote", StateWorking, "", fleet.Agent{Host: "build1", Labels: badge.Labels{Task: "steal"}, Tail: toolAgent("Edit", "/a/b.go").Tail}, "working"},
	}
	for _, c := range cases {
		got := doingText(c.a, c.state, c.waiting)
		if got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
		led := false
		for _, l := range DoingLeads {
			if strings.HasPrefix(got, l) {
				led = true
			}
		}
		if !led {
			t.Errorf("%s: %q starts with no lead phrase", c.name, got)
		}
	}
	long := doingText(fleet.Agent{Labels: badge.Labels{Task: strings.Repeat("word ", 40)}}, StateWorking, "")
	if len([]rune(long)) != doingMax || !strings.HasSuffix(long, "…") {
		t.Errorf("not capped: %q", long)
	}
}

// A new text is published once the last change is doingEvery old; At is
// when it changed; agents that went are forgotten.
func TestSettleDoing(t *testing.T) {
	t0 := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	seen := map[string]doingSeen{}
	pub := func(text string, at time.Time) Doing {
		ag := []Agent{{ID: "a", Doing: Doing{Text: text}}}
		settleDoing(ag, seen, at)
		return ag[0].Doing
	}
	if d := pub("editing a.go", t0); d.Text != "editing a.go" || !d.At.Equal(t0) {
		t.Fatalf("first %+v", d)
	}
	if d := pub("editing b.go", t0.Add(3*time.Second)); d.Text != "editing a.go" || !d.At.Equal(t0) {
		t.Errorf("changed within the window: %+v", d)
	}
	if d := pub("editing b.go", t0.Add(doingEvery)); d.Text != "editing b.go" || !d.At.Equal(t0.Add(doingEvery)) {
		t.Errorf("not changed after the window: %+v", d)
	}
	if d := pub("editing b.go", t0.Add(time.Hour)); !d.At.Equal(t0.Add(doingEvery)) {
		t.Errorf("At moved with no change: %+v", d)
	}
	settleDoing(nil, seen, t0.Add(2*time.Hour))
	if len(seen) != 0 {
		t.Error("a gone agent was kept")
	}
}
