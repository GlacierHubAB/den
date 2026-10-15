package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/orch"
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
	if !m.orch.control {
		// send_message exists only with control on; one from anywhere
		// else is refused whoever asks.
		r.Reply(bridge.Decision{Behavior: "deny", Message: "send_message works only while a paired app has control; use SendMessage"})
		return nil
	}
	if !orch.SendVerifiedVersion(m.orch.version) {
		r.Reply(bridge.Decision{Behavior: "deny", Message: "send_message needs a Claude Code release Den has checked (" + strings.Join(orch.SendVerified, ", ") + "); this orchestrator runs " + m.orch.version})
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
	if y := m.ctl.turn; y != nil && m.ctl.sent && m.api.srv != nil {
		// In an app's turn: a standing allowance for this name holds only
		// for the session it was given for, while that session asks.
		for _, rule := range m.api.srv.Rules(y.Pairing) {
			if rule.Tool != orch.SendMessageTool || rule.Peer != orch.PeerName(in.To) {
				continue
			}
			if rule.SessionID != to.Session.SessionID || !m.ruleSession(rule) {
				r.Reply(bridge.Decision{Behavior: "deny", Message: "the standing allowance for " + rule.Peer + " was for another session, or that session no longer asks before it acts"})
				return nil
			}
			if m.ctl.watch == nil {
				m.ctl.watch = map[string]bool{}
			}
			m.ctl.watch[to.Session.SessionID] = true
		}
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
// inbox socket at the place Claude Code puts it.
func (m Model) orchSession() (fleet.Agent, bool) {
	if m.orch.proc == nil {
		return fleet.Agent{}, false
	}
	for _, a := range m.base {
		if a.Host == "" && a.Session.PID == m.orch.proc.PID && inboxPath(a.Session.Socket, a.Session.PID) {
			return a, true
		}
	}
	return fleet.Agent{}, false
}

// inboxPath reports whether path is where Claude Code binds pid's inbox
// socket for this user: <dir>/<pid>.sock in $XDG_RUNTIME_DIR/cc-socks,
// /run/user/<uid>/cc-socks, /tmp/cc-socks-<uid> or /tmp/cc-socks. Den
// writes to no other path.
func inboxPath(path string, pid int) bool {
	if pid <= 1 || filepath.Base(path) != strconv.Itoa(pid)+".sock" || filepath.Clean(path) != path {
		return false
	}
	uid := strconv.Itoa(os.Getuid())
	dirs := []string{"/run/user/" + uid + "/cc-socks", "/tmp/cc-socks-" + uid, "/tmp/cc-socks"}
	if x := os.Getenv("XDG_RUNTIME_DIR"); x != "" {
		dirs = append(dirs, filepath.Join(x, "cc-socks"))
	}
	for _, d := range dirs {
		if filepath.Dir(path) == d {
			return true
		}
	}
	return false
}

// sendTarget is the live local session a send_message names by its peer
// name: never the orchestrator itself or the app lane.
func (m Model) sendTarget(to string) (fleet.Agent, string) {
	p := orch.PeerName(to) // the name standing allowances match too
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
	case !inboxPath(hits[0].Session.Socket, hits[0].Session.PID):
		return fleet.Agent{}, p + " has no inbox socket where Claude Code puts one"
	case !orch.SendVerifiedVersion(hits[0].Session.Version):
		return fleet.Agent{}, p + " runs a Claude Code release Den has not checked send_message on: Den cannot deliver to that session while control is on; tell the human"
	}
	if _, ok := sessionProc(hits[0].Session.PID, hits[0].Session.StartedAt, ""); !ok {
		return fleet.Agent{}, p + "'s process is not the one its session started with"
	}
	return hits[0], ""
}
