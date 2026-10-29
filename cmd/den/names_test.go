package main

import (
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/orch"
)

// A generic role needs a title; a specific role, a title, or no role at
// all does not.
func TestSpawnNeedsATitleForAGenericRole(t *testing.T) {
	for _, c := range []struct {
		role, title string
		refused     bool
	}{
		{"coding agent", "", true},
		{"dev agent", "  ", true},
		{"general engineer", "\u4e2d", true}, // a title with nothing usable
		{"coding agent", "release notes writer", false},
		{"backend engineer", "", false},
		{"", "", false},
	} {
		why := needsTitle(orch.SpawnInput{Role: c.role, Title: c.title})
		if (why != "") != c.refused {
			t.Errorf("%q / %q: %q", c.role, c.title, why)
		}
		if c.refused && !strings.Contains(why, "give title") {
			t.Errorf("the refusal does not say what to give: %q", why)
		}
	}
}
