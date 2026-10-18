package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/orch"
)

// spawn_agent asks Den about the team before it starts anything: Den's
// refusal stops the spawn with Den's words, an allow lets it go on, and
// with no Den to ask there is no team to count. The query carries the
// directory, the reason and the lead.
func TestTeamRoom(t *testing.T) {
	old := askTeam
	t.Cleanup(func() { askTeam = old })
	var sent bridge.TeamQueryInput
	answer := func(d bridge.Decision, err error) {
		askTeam = func(_ string, env bridge.Envelope, _ time.Duration) (bridge.Decision, error) {
			if env.Event != bridge.TeamEvent || json.Unmarshal(env.Payload, &sent) != nil {
				t.Errorf("query %+v", env)
			}
			return d, err
		}
	}
	in := orch.SpawnInput{Dir: "/w/proj", Reason: "a second reviewer"}
	answer(bridge.Decision{Behavior: "deny", Message: "the team is at its cap"}, nil)
	if err := teamRoom(in, "den-a"); err == nil || err.Error() != "the team is at its cap" {
		t.Errorf("refused: %v", err)
	}
	if sent != (bridge.TeamQueryInput{Dir: "/w/proj", Reason: "a second reviewer", Lead: "den-a"}) {
		t.Errorf("sent %+v", sent)
	}
	answer(bridge.Decision{Behavior: "allow"}, nil)
	if err := teamRoom(in, ""); err != nil {
		t.Errorf("allowed: %v", err)
	}
	answer(bridge.Decision{}, bridge.ErrNoDen)
	if err := teamRoom(in, ""); err != nil {
		t.Errorf("no Den: %v", err)
	}
}
