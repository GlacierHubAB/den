package badge

import "testing"

func TestNameMatchesAgentBadge(t *testing.T) {
	// Values produced by agent-badge's own critterName() for these ids.
	cases := map[string]string{
		"f1b9d688-f348-46da-9eb0-7608eb4d89df": "Mossy Lemur",
		"33503402-df6e-4023-8df1-fcd6e8e482ed": "Plucky Capybara",
		"5c89371d-f364-4ebe-97d1-8cc39b699c16": "Snug Capybara",
		"6bb2b895-f662-42be-a8e8-1657ec52fbee": "Fuzzy Sprite",
	}
	for id, want := range cases {
		if got := Name(id); got != want {
			t.Errorf("Name(%s) = %q, want %q", id, got, want)
		}
	}
}
