package ui

import (
	"maps"
	"os"
	"testing"

	"github.com/GlacierHubAB/den/internal/colors"
	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/theme"
)

func TestAgentsGetDistinctStableColors(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir()) // never touch the real colour record
	m := Model{st: newStyles(theme.Deepharness), store: inbox.New()}
	for i := 0; i < 8; i++ {
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
