package remote

import (
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/events"
)

// A remote host's turn_start keeps a source only if it is one den hook
// writes: the field is shown and counted here, and a host can send anything.
func TestCleanEventKeepsOnlyTheSourcesDenHookWrites(t *testing.T) {
	for in, want := range map[string]string{
		events.SourcePeer:              events.SourcePeer,
		events.SourceTerminal:          events.SourceTerminal,
		"":                             "",
		"Peer":                         "",
		"terminal ":                    "",
		"\x1b]0;title\x07peer":         "",
		strings.Repeat("peer", 10_000): "",
	} {
		ev := events.New(events.TurnStart, sidA)
		ev.Source = in
		got, ok := cleanEvent(ev, "devbox")
		if !ok || got.Source != want {
			t.Errorf("source %.20q: kept %.20q (%v), want %q", in, got.Source, ok, want)
		}
	}
}
