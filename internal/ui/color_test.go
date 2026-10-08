package ui

import (
	"testing"

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
