package ui

import (
	"maps"
	"os"
	"slices"
	"testing"

	"github.com/GlacierHubAB/den/internal/colors"
	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/theme"
)

// As many agents as the palette has colours each get their own; past that
// colours repeat.
func TestAgentsGetDistinctStableColors(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir()) // never touch the real colour record
	m := Model{st: newStyles(theme.Deepharness), store: inbox.New()}
	for i := 0; i < len(theme.Deepharness.Bodies); i++ {
		m.base = append(m.base, fleet.Agent{Session: discover.Session{SessionID: string(rune('a'+i)) + "0123456789abcdef", StartedAt: int64(i), Alive: true}})
	}
	m.refresh()
	seen := map[string]string{}
	for _, a := range m.agents {
		c := m.agentColor(a)
		if other, dup := seen[c]; dup {
			t.Errorf("%s and %s share %s", other, a.Session.SessionID, c)
		}
		seen[c] = a.Session.SessionID
	}
	first := m.agentColor(m.agents[0])
	m.base = m.base[:1]
	m.refresh()
	if m.agentColor(m.agents[0]) != first {
		t.Errorf("oldest agent changed colour after others left")
	}
}

// The colour record is a file any process of the user can write. What it
// holds when Den starts can only pick among Den's own hues: a colour
// outside the palette, even one the agent colour rule takes, or one the
// rule refuses, is replaced, and of two sessions recorded with one hue the
// older keeps it. Den never reads the record again, so a later rewrite
// changes no agent's colour. Every agent's colour, as drawn and as the API
// gets it, is one of the palette's.
func TestColourRecordCanOnlyPickPaletteHues(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if ok, _ := theme.AgentColor("#b8be7a"); !ok || slices.Contains(theme.Deepharness.Bodies, "#b8be7a") {
		t.Fatal("#b8be7a must pass the rule and be outside the palette")
	}
	sid := func(i int) string { return string(rune('a'+i)) + "0123456789abcdef" }
	tampered := map[string]string{sid(0): "#b8be7a", sid(1): "#ea9e9e", sid(2): "#86c6e8", sid(3): "#86c6e8"}
	if err := colors.Save(colors.Path(), tampered); err != nil {
		t.Fatal(err)
	}
	m := Model{st: newStyles(theme.Deepharness), store: inbox.New()}
	for i := 0; i < 4; i++ {
		m.base = append(m.base, fleet.Agent{Session: discover.Session{SessionID: sid(i), StartedAt: int64(i), Alive: true}})
	}
	m.refresh()
	inPalette := func(when string) map[string]string {
		shown := map[string]string{}
		for _, a := range m.agents {
			c, api := m.agentColor(a), m.colorFor(a.Session.SessionID)
			if !slices.Contains(theme.Deepharness.Bodies, c) || api != c {
				t.Errorf("%s: %s drawn %s, sent %s; the palette is %v", when, a.Session.SessionID, c, api, theme.Deepharness.Bodies)
			}
			shown[a.Session.SessionID] = c
		}
		return shown
	}
	shown := inPalette("after a tampered record at start")
	if shown[sid(2)] != "#86c6e8" || shown[sid(3)] == "#86c6e8" {
		t.Errorf("the older session keeps a recorded palette hue, the younger yields it: %v", shown)
	}
	if err := colors.Save(colors.Path(), map[string]string{sid(0): "#b8be7a", sid(1): "#b8be7a", sid(2): "#ea9e9e", sid(3): "#ea9e9e"}); err != nil {
		t.Fatal(err)
	}
	m.refresh()
	if again := inPalette("after a later rewrite"); !maps.Equal(again, shown) {
		t.Errorf("a later rewrite recoloured agents: %v → %v", shown, again)
	}
}

// The badge in each terminal reads the record on disk, so when something
// else rewrites it (a test run against the real state folder, a second Den)
// Den puts back the colours it shows, on the next refresh, without
// recolouring anyone.
func TestColourRecordIsRestoredAfterAnotherWriter(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	m := Model{st: newStyles(theme.Deepharness), store: inbox.New()}
	for i := 0; i < 4; i++ {
		m.base = append(m.base, fleet.Agent{Session: discover.Session{SessionID: string(rune('a'+i)) + "0123456789abcdef", StartedAt: int64(i), Alive: true}})
	}
	m.refresh()
	want := colors.Load(colors.Path())
	if len(want) != 4 {
		t.Fatalf("record after first refresh: %v", want)
	}
	shown := map[string]string{}
	for _, a := range m.agents {
		shown[a.Session.SessionID] = m.agentColor(a)
	}
	for _, other := range []map[string]string{{"fuzzy-sprite": "#a7d98c"}, nil} {
		if other == nil {
			os.Remove(colors.Path())
		} else if err := colors.Save(colors.Path(), other); err != nil {
			t.Fatal(err)
		}
		m.refresh()
		if got := colors.Load(colors.Path()); !maps.Equal(got, want) {
			t.Errorf("after another writer left %v, record = %v, want %v", other, got, want)
		}
		for _, a := range m.agents {
			if c := m.agentColor(a); c != shown[a.Session.SessionID] {
				t.Errorf("%s recoloured %s → %s", a.Session.SessionID, shown[a.Session.SessionID], c)
			}
		}
	}
}
