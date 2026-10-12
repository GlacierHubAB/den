package fleet

import (
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/badge"
	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/gitinfo"
	"github.com/GlacierHubAB/den/internal/hypr"
)

const evil = "\x1b]52;c;SGVsbG8=\x07\x1b[2J\x1b]0;pwned\x07\u202e"

func injected(s string) bool {
	return strings.ContainsAny(s, "\x1b\x07\r\u202e") || strings.Contains(s, "pwned")
}

func TestShownTextIsSanitised(t *testing.T) {
	a := Agent{
		Session: discover.Session{SessionID: "11111111-2222-4333-8444-555555555555", Name: "peer" + evil, CWD: "/tmp/dir" + evil},
		Labels:  badge.Labels{Role: "role" + evil, Task: "task" + evil, History: []string{"old" + evil}},
		Git:     gitinfo.Info{Repo: "repo" + evil, Branch: "branch" + evil},
		HasGit:  true,
		Window:  &hypr.Match{Client: hypr.Client{Title: "t" + evil, Class: "c" + evil}},
	}
	clean(&a)
	for _, s := range []string{a.Labels.Role, a.Labels.Task, a.Labels.History[0], a.Git.Repo, a.Git.Branch, a.Window.Client.Title, a.Window.Client.Class, a.Where(), a.Place()} {
		if injected(s) {
			t.Errorf("injected sequence survived in %q", s)
		}
	}
	if a.Labels.Role != "role" || a.Where() != "repo · branch" {
		t.Errorf("role %q where %q", a.Labels.Role, a.Where())
	}
	// The session's own fields stay raw for addressing; what shows them
	// sanitises.
	if a.Session.Name != "peer"+evil {
		t.Error("the peer name was changed in the model")
	}
	a.Labels.Task, a.HasGit = "", false
	if got := a.Task(); got != "peer" {
		t.Errorf("Task() = %q", got)
	}
	if injected(a.Where()) {
		t.Errorf("Where() = %q", a.Where())
	}
	for _, l := range Explain(a) {
		if injected(l) {
			t.Errorf("Explain line %q", l)
		}
	}
}
