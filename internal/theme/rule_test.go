package theme

import (
	"testing"

	"github.com/GlacierHubAB/den/internal/api/convcases"
)

// Den's copy of the agent colour rule gives every row of the shared table
// its answer and its first failing check.
func TestAgentColorTable(t *testing.T) {
	for _, c := range convcases.ColorRuleCases {
		ok, why := AgentColor(c.Hex)
		if ok != c.Allowed || why != c.Why {
			t.Errorf("%q: %v %q, the table %v %q", c.Hex, ok, why, c.Allowed, c.Why)
		}
	}
}
