package ui

import (
	"testing"

	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/fleet"
)

func TestResolveMention(t *testing.T) {
	agents := []fleet.Agent{{Name: "Mossy Lemur"}, {Name: "Snug Capybara"}, {Name: "Sunny Puffin"}}
	cases := []struct {
		in      string
		target  string
		body    string
		ambig   int
		missing string
	}{
		{"@Mossy Lemur rebase on main", "Mossy Lemur", "rebase on main", 0, ""},
		{"@mossy lemur", "Mossy Lemur", "", 0, ""},
		{"@mossy rebase", "Mossy Lemur", "rebase", 0, ""},
		{"@s hello", "", "hello", 2, ""},
		{"@zed hi", "", "", 0, "zed"},
		{"what is everyone doing", "", "what is everyone doing", 0, ""},
	}
	for _, c := range cases {
		m := resolveMention(c.in, agents)
		name := ""
		if m.target != nil {
			name = m.target.Name
		}
		if name != c.target || m.body != c.body || len(m.ambiguous) != c.ambig || m.missing != c.missing {
			t.Errorf("%q -> target=%q body=%q ambiguous=%v missing=%q", c.in, name, m.body, m.ambiguous, m.missing)
		}
	}
	if got := completions("@s", agents); len(got) != 2 {
		t.Errorf("completions(@s) = %d", len(got))
	}
	if got := completions("@Snug Capybara do it", agents); got != nil {
		t.Errorf("completions after full name = %v", got)
	}
	if got := completions("@", agents); len(got) != 3 {
		t.Errorf("completions(@) = %d", len(got))
	}
}

// A mention ends where Drey's dock ends its chip: at the end, white space,
// "@" (a host follows) or sentence punctuation (mentionPunct). The
// punctuation is not part of the name, and not part of the message when
// white space or the end follows it. A whole name is never cut short at
// punctuation into a one-word prefix ("@Mossy Lemur, rebase" once sent
// "Lemur, rebase"), a name that is a prefix of another is still the
// shorter agent's, and the query names.Resolve sees carries no
// punctuation.
func TestMentionEndsAtPunctuation(t *testing.T) {
	agents := []fleet.Agent{
		{Name: "Mossy Lemur", Session: discover.Session{SessionID: "s1", Name: "den-a"}},
		{Name: "Otter", Session: discover.Session{SessionID: "s2", Name: "den-b"}},
		{Name: "Otter Two", Session: discover.Session{SessionID: "s3", Name: "den-c"}},
		{Name: "Snug Capybara", Session: discover.Session{SessionID: "s4", Name: "backend-engineer2"}},
		{Name: "Mossy Lemur", Host: "devbox.lan", Session: discover.Session{SessionID: "r1", Name: "den-r"}},
	}
	cases := []struct {
		in, target, host, body string
	}{
		// A multi-word name, then punctuation.
		{"@Mossy Lemur, rebase", "Mossy Lemur", "", "rebase"},
		{"@Mossy Lemur: rebase now", "Mossy Lemur", "", "rebase now"},
		{"@Mossy Lemur; then stop", "Mossy Lemur", "", "then stop"},
		{"@Mossy Lemur. Then stop.", "Mossy Lemur", "", "Then stop."},
		{"@Mossy Lemur! hi", "Mossy Lemur", "", "hi"},
		{"@Mossy Lemur? are you there", "Mossy Lemur", "", "are you there"},
		{"@Mossy Lemur\u2026 hi", "Mossy Lemur", "", "hi"},
		{"@Mossy Lemur\u2019 hi", "Mossy Lemur", "", "hi"},
		{"@Mossy Lemur)", "Mossy Lemur", "", ""},
		{"@Mossy Lemur...", "Mossy Lemur", "", ""},
		{"@Mossy Lemur\trebase", "Mossy Lemur", "", "rebase"},
		{"@Mossy Lemur\nrebase\nnow", "Mossy Lemur", "", "rebase\nnow"},
		// Punctuation not followed by white space is the message's own.
		{"@Mossy Lemur's branch is red", "Mossy Lemur", "", "'s branch is red"},
		// One word, as a prefix.
		{"@mossy, rebase", "Mossy Lemur", "", "rebase"},
		{"@mossy: hi", "Mossy Lemur", "", "hi"},
		{"@snug.", "Snug Capybara", "", ""},
		// A name that is a prefix of another.
		{"@Otter, hi", "Otter", "", "hi"},
		{"@otter: hi", "Otter", "", "hi"},
		{"@Otter Two, hi", "Otter Two", "", "hi"},
		{"@Otter Two", "Otter Two", "", ""},
		// A peer handle with a hyphen and a digit.
		{"@backend-engineer2, check the logs", "Snug Capybara", "", "check the logs"},
		// A host, a dot inside it, punctuation after it.
		{"@Mossy Lemur@devbox.lan, rebase", "Mossy Lemur", "devbox.lan", "rebase"},
		{"@mossy@devbox.lan: hi", "Mossy Lemur", "devbox.lan", "hi"},
		{"@Mossy Lemur@devbox.lan.", "Mossy Lemur", "devbox.lan", ""},
		{"@Mossy Lemur@local, hi", "Mossy Lemur", "", "hi"},
	}
	for _, c := range cases {
		mt := resolveMention(c.in, agents)
		if mt.target == nil || mt.target.Name != c.target || mt.target.Host != c.host || mt.body != c.body {
			t.Errorf("%q -> %+v body %q, want %s@%s body %q", c.in, mt.target, mt.body, c.target, c.host, c.body)
		}
	}
	// No name: a lone "@", and punctuation right after it.
	for _, in := range []string{"@, hi", "@: hi", "@@Mossy Lemur hi"} {
		if mt := resolveMention(in, agents); mt.target != nil {
			t.Errorf("%q routed to %s", in, mt.target.Name)
		}
	}
}
