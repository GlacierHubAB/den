package badge

import "testing"

func TestNameMatchesAgentBadge(t *testing.T) {
	// Values produced by agent-badge's own critterName() for these ids.
	cases := map[string]string{
		"11111111-2222-4333-8444-555555555501": "Wobbly Hedgehog",
		"11111111-2222-4333-8444-555555555502": "Spicy Tanuki",
		"11111111-2222-4333-8444-555555555503": "Plucky Axolotl",
		"11111111-2222-4333-8444-555555555504": "Fizzy Tanuki",
	}
	for id, want := range cases {
		if got := Name(id); got != want {
			t.Errorf("Name(%s) = %q, want %q", id, got, want)
		}
	}
}
