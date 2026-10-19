package main

import (
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/names"
	"github.com/GlacierHubAB/den/internal/orch"
	"github.com/GlacierHubAB/den/internal/spawn"
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

// A spawned agent's name is recorded in the registry from its role.
func TestSpawnedAgentIsNamed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", home+"/.claude")
	t.Setenv("XDG_STATE_HOME", home+"/state")
	nameSpawned(spawn.Result{SessionID: "s-new", Critter: "Pebble Quokka"}, orch.SpawnInput{Role: "backend engineer"})
	nameSpawned(spawn.Result{SessionID: "s-bare", Critter: "Mossy Lemur"}, orch.SpawnInput{})
	got := names.Load(names.RegistryPath())
	if e := got["s-new"]; e.Name() != "backend-engineer" || e.Alias != "Pebble Quokka" || e.Role != "backend engineer" {
		t.Errorf("entry %+v", e)
	}
	if _, ok := got["s-bare"]; ok {
		t.Error("a spawn with no role or title was recorded")
	}
}
