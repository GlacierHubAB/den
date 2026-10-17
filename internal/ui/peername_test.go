package ui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/api"
	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/orch"
)

// namedModel has a live agent Pebble Quokka (peer den-ci-engineer), a gone
// one, one on another host and two live ones that share a peer name.
func namedModel() Model {
	m := testModel(140, 30)
	ag := func(critter, peer, sid string, st fleet.Status, host string) fleet.Agent {
		return fleet.Agent{Name: critter, Status: st, Host: host, Session: discover.Session{Name: peer, SessionID: sid, Alive: st != fleet.Gone}}
	}
	m.base = []fleet.Agent{
		ag("Pebble Quokka", "den-ci-engineer", "s-1", fleet.Idle, ""),
		ag("Mossy Otter", "old-worker", "s-2", fleet.Gone, ""),
		ag("Far Fox", "remote-worker", "s-3", fleet.Idle, "devbox"),
		ag("Twin One", "twin", "s-4", fleet.Idle, ""),
		ag("Twin Two", "twin", "s-5", fleet.Working, ""),
	}
	return m
}

// The human reads an agent by its critter name; the peer name follows in
// parentheses only for exactly one live agent on this machine.
func TestPeerWithCritter(t *testing.T) {
	m := namedModel()
	for peer, want := range map[string]string{
		"den-ci-engineer": "Pebble Quokka (den-ci-engineer)",
		"old-worker":      "old-worker",    // gone
		"remote-worker":   "remote-worker", // on another host
		"twin":            "twin",          // two agents: no guess
		"nobody":          "nobody",
		"":                "",
	} {
		if got := m.peerWithCritter(peer); got != want {
			t.Errorf("%q shown as %q, want %q", peer, got, want)
		}
	}
	// A peer name is text an agent chose: no control byte gets through, for
	// a known agent or not.
	m.base[0].Session.Name = "den-\x1b[2Jci"
	for _, peer := range []string{"den-\x1b[2Jci", "other-\x1b]0;x\x07name"} {
		if got := m.peerWithCritter(peer); strings.ContainsAny(got, "\x1b\x07") {
			t.Errorf("%q shown as %q, with a control byte", peer, got)
		}
	}
}

// /always's card names the agent by critter first, once with the peer name
// the rule is keyed to, and the peer name only in that one place.
func TestStandingCardNamesTheCritterFirst(t *testing.T) {
	m := namedModel()
	lines := m.standingLines("Drey", api.Rule{Tool: orch.SendMessageTool, Peer: "den-ci-engineer", SessionID: "s-1"}, "")
	if len(lines) != 3 {
		t.Fatalf("lines %q", lines)
	}
	if !strings.Contains(lines[0], "message Pebble Quokka (den-ci-engineer) in its turns") {
		t.Errorf("first line: %q", lines[0])
	}
	for _, l := range lines[1:] {
		if strings.Contains(l, "den-ci-engineer") || !strings.Contains(l, "Pebble Quokka") {
			t.Errorf("a later line does not use the critter name alone: %q", l)
		}
	}
	// An agent Den does not know keeps its peer name, as before.
	lines = m.standingLines("Drey", api.Rule{Tool: orch.SendMessageTool, Peer: "stranger", SessionID: "s-9"}, "")
	if !strings.Contains(lines[0], "message stranger in its turns") || !strings.HasPrefix(lines[2], "stranger works in") {
		t.Errorf("lines for an unknown agent: %q", lines)
	}
}

// A send_message card shows its target as "to Critter (peer)" above the
// tool's input, which keeps the peer name only.
func TestSendMessageCardNamesTheTarget(t *testing.T) {
	m := namedModel()
	ev := func(tool, to string) orch.Event {
		in, _ := json.Marshal(map[string]string{"to": to, "message": "run the tests"})
		return orch.Event{Tool: tool, Input: in}
	}
	if got := m.sendTargetLine(ev(orch.SendMessageTool, "den-ci-engineer")); got != "to: Pebble Quokka (den-ci-engineer)" {
		t.Errorf("known target: %q", got)
	}
	for name, e := range map[string]orch.Event{
		"unknown target": ev(orch.SendMessageTool, "stranger"),
		"another tool":   ev(orch.SpawnAgentTool, "den-ci-engineer"),
		"bad input":      {Tool: orch.SendMessageTool, Input: json.RawMessage(`[1]`)},
	} {
		if got := m.sendTargetLine(e); got != "" {
			t.Errorf("%s: %q", name, got)
		}
	}
}
