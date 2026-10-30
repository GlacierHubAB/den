package ui

import (
	"testing"

	"github.com/GlacierHubAB/den/internal/api"
)

// The powers card says what Den found of its stored levels and the levels
// its Ctrl+Y confirms, each on a line of its own after the "Until you
// press Ctrl+Y here" line; with none, the card is as before.
func TestPowersCardSaysTheLevels(t *testing.T) {
	m := testModel(140, 40)
	levels := []string{
		"Den stopped unexpectedly last time and could not check its stored permission levels.",
		"Ctrl+Y also confirms the levels you set above Den's defaults: Stop and close 2 of 5 (default 1). Until then they are at the defaults.",
	}
	m.applyConsoleAct(api.Act{ConsolePowers: &api.ConsolePowersAsk{ID: "p-1", PID: 4242, Why: "Den restarted", Levels: levels}})
	var body []string
	for it := range m.con.powers {
		body = it.Body
	}
	if len(body) != 5 || body[2] != levels[0] || body[3] != levels[1] || body[4][:len("Only Den's own window")] != "Only Den's own window" {
		t.Fatalf("card body %q", body)
	}
	m.applyConsoleAct(api.Act{ConsolePowers: &api.ConsolePowersAsk{ID: "p-2", PID: 4242, Why: "Den restarted"}})
	for it := range m.con.powers {
		body = it.Body
	}
	if len(body) != 3 {
		t.Errorf("a card with no levels to confirm: %q", body)
	}
}
