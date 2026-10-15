package ui

import (
	"encoding/json"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/orch"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// critterOf is the critter name, which the human knows an agent by, of the
// one live agent on this machine whose peer name is peer, "" when there is
// not exactly one.
func (m Model) critterOf(peer string) string {
	if peer == "" {
		return ""
	}
	n, name := 0, ""
	for _, a := range m.base {
		if a.Host == "" && a.Session.Alive && a.Status != fleet.Gone && a.Session.Name == peer {
			n++
			name = a.Name
		}
	}
	if n != 1 {
		return ""
	}
	return sanitize.Label(name, 40)
}

// peerWithCritter shows a peer name the way the human reads it: the critter
// name first and the peer name after it in parentheses, or the peer name
// alone for one that is not exactly one live agent here. The peer name is
// text an agent chose, so it is cleaned either way.
func (m Model) peerWithCritter(peer string) string {
	p := sanitize.Label(peer, 60)
	if c := m.critterOf(peer); c != "" {
		return c + " (" + p + ")"
	}
	return p
}

// sendTargetLine is the line a send_message card adds above the tool's
// input, which names its target by peer name only: "to Critter (peer)".
// It is "" when the target is not a live agent Den can name.
// sendTitle is a send_message card's title: "to <critter> (<peer>):" and
// the message's first words, "" for any other tool.
func (m Model) sendTitle(ev orch.Event) string {
	if ev.Tool != orch.SendMessageTool {
		return ""
	}
	var in struct {
		To      string `json:"to"`
		Message string `json:"message"`
	}
	if json.Unmarshal(ev.Input, &in) != nil {
		return ""
	}
	peer := sanitize.Label(orch.PeerName(in.To), 60)
	to := peer
	if c := m.critterOf(orch.PeerName(in.To)); c != "" {
		to = c + " (" + peer + ")"
	}
	return "to " + to + ": " + sanitize.Label(firstLineOf(in.Message), 120)
}

func (m Model) sendTargetLine(ev orch.Event) string {
	if ev.Tool != orch.SendMessageTool {
		return ""
	}
	var in struct {
		To string `json:"to"`
	}
	if json.Unmarshal(ev.Input, &in) != nil {
		return ""
	}
	peer := orch.PeerName(in.To)
	if c := m.critterOf(peer); c != "" {
		return "to: " + c + " (" + sanitize.Label(peer, 60) + ")"
	}
	return ""
}
