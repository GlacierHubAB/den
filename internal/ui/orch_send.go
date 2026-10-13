package ui

import (
	"encoding/json"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/peer"
	"github.com/GlacierHubAB/den/internal/peermsg"
)

// handleOrchSend delivers the orchestrator's send_message (control on) the
// way SendMessage would: framed as a message from the orchestrator's own
// inbox socket and name, so the agent sees what it always saw and its
// report goes back to the orchestrator. Only the orchestrator's `den mcp`
// asks (checked by the caller); by then Den has allowed the call.
func (m *Model) handleOrchSend(r *bridge.Request) tea.Cmd {
	var in bridge.OrchSendInput
	if json.Unmarshal(r.Envelope.Payload, &in) != nil || strings.TrimSpace(in.To) == "" || strings.TrimSpace(in.Message) == "" {
		r.Reply(bridge.Decision{Behavior: "deny", Message: "give to (a peer name) and message"})
		return nil
	}
	self, ok := m.orchSession()
	if !ok {
		r.Reply(bridge.Decision{Behavior: "deny", Message: "the orchestrator's own session is not in Den's fleet yet; try again in a moment"})
		return nil
	}
	to, why := m.sendTarget(in.To)
	if why != "" {
		r.Reply(bridge.Decision{Behavior: "deny", Message: why})
		return nil
	}
	from, name, sock, peerName := "uds:"+self.Session.Socket, self.Session.Name, to.Session.Socket, to.Session.Name
	body := peermsg.Defuse(in.Message)
	return func() tea.Msg {
		if err := peer.SendAs(sock, from, name, body); err != nil {
			r.Reply(bridge.Decision{Behavior: "deny", Message: peerName + ": " + err.Error()})
			return nil
		}
		r.Reply(bridge.Decision{Behavior: "allow", Message: "Message sent to " + peerName})
		return nil
	}
}

// orchSession is the orchestrator's own session in the fleet, with its
// inbox socket.
func (m Model) orchSession() (fleet.Agent, bool) {
	if m.orch.proc == nil {
		return fleet.Agent{}, false
	}
	for _, a := range m.base {
		if a.Host == "" && a.Session.PID == m.orch.proc.PID && a.Session.Socket != "" {
			return a, true
		}
	}
	return fleet.Agent{}, false
}

// sendTarget is the live local session a send_message names by its peer
// name: never the orchestrator itself or the app lane.
func (m Model) sendTarget(to string) (fleet.Agent, string) {
	p := strings.TrimPrefix(strings.TrimSpace(to), "@")
	if i := strings.Index(p, " ["); i > 0 {
		p = p[:i]
	}
	var hits []fleet.Agent
	for _, a := range m.base {
		if a.Host == "" && a.Session.Alive && a.Status != fleet.Gone && a.Session.Name == p {
			hits = append(hits, a)
		}
	}
	switch {
	case len(hits) == 0:
		return fleet.Agent{}, "no live session on this machine is named " + p
	case len(hits) > 1:
		return fleet.Agent{}, "more than one session is named " + p
	case m.isOrchestrator(hits[0]) || p == D1LaneName:
		return fleet.Agent{}, "not to " + p
	case hits[0].Session.Socket == "":
		return fleet.Agent{}, p + " has no inbox socket"
	}
	return hits[0], ""
}
