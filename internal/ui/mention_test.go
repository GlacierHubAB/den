package ui

import (
	"testing"

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
