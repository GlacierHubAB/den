package ui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/orch"
	"github.com/GlacierHubAB/den/internal/peermsg"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// Peer messages to the orchestrator. Claude Code hands a message another
// session sends the orchestrator to it as a prompt; the orchestrator's own
// hook (`den hook orchestrator-inbound`) blocks every one and asks Den
// here. Den takes the message as a report only from a live session of its
// own fleet on this machine, named as the message says, and sends the body
// on as its own turn: <agent_report agent="critter">, the text fenced as
// data. Prompts with no envelope pass only when Den wrote them.

// Report limits: per sender, and reports Den has sent that the
// orchestrator has not answered yet.
const (
	reportBurst  = 3               // reports a sender may send at once
	reportEvery  = 2 * time.Minute // then one per this long
	reportQueue  = 4               // reports in flight, all senders
	refusalEvery = time.Minute     // one refusal note per sender this often
)

// D1LaneName is the session name of Den's read-only lane for apps; it
// never reports.
const D1LaneName = "den-drey-lane"

// peerState is the reports' bookkeeping.
type peerState struct {
	tokens   map[string]float64   // sender's report allowance
	refilled map[string]time.Time // when it was last topped up
	inFlight int                  // reports sent, not yet answered
	refused  map[string]time.Time // when a refusal note last went up, by sender
	// envelopes counts the peer messages the hook asked about whose
	// blocked turn has not come back yet: Blocked is a peer's while it is
	// above zero, and otherwise ends one of Den's own turns.
	envelopes int
}

// handleInbound answers the inbound hook. It always says deny to an
// envelope (the hook blocks it whatever Den says); the message says what
// became of it.
func (m *Model) handleInbound(r *bridge.Request) tea.Cmd {
	var in bridge.InboundInput
	if json.Unmarshal(r.Envelope.Payload, &in) != nil {
		r.Reply(bridge.Decision{Behavior: "deny", Message: "den: unreadable request"})
		return nil
	}
	switch in.Kind {
	case "prompt":
		if m.orch.proc != nil && in.SHA256 != "" && m.orch.proc.Sent(in.SHA256) {
			r.Reply(bridge.Decision{Behavior: "allow"})
		} else {
			r.Reply(bridge.Decision{Behavior: "deny", Message: "den did not write this prompt"})
		}
		return nil
	case "refused":
		// An envelope the hook could not read: blocked there, counted here.
		m.peers.envelopes++
		r.Reply(bridge.Decision{Behavior: "deny", Message: "Den refused the message"})
		m.noteRefusal("an unreadable peer message", sanitize.Label(in.Body, 120))
		return nil
	case "envelope":
		m.peers.envelopes++
		a, why := m.admitReport(in)
		if why != "" {
			r.Reply(bridge.Decision{Behavior: "deny", Message: "Den refused the message: " + why})
			m.noteRefusal(in.FromName, why)
			return nil
		}
		r.Reply(bridge.Decision{Behavior: "deny", Message: "Den passed the message on to the orchestrator as a report from " + a.Name})
		return m.sendReport(a, in.Body)
	}
	r.Reply(bridge.Decision{Behavior: "deny", Message: "den: unknown request"})
	return nil
}

// admitReport finds the sender in the fleet: a live session on this
// machine whose name is the message's from-name and whose process is the
// one the message's socket names, not the orchestrator and not the app
// lane, within its rate and the queue. why says why not.
func (m *Model) admitReport(in bridge.InboundInput) (fleet.Agent, string) {
	if m.orch.proc == nil {
		return fleet.Agent{}, "the orchestrator is not running"
	}
	var hits []fleet.Agent
	for _, a := range m.base {
		if a.Host == "" && a.Session.Alive && a.Status != fleet.Gone && a.Session.Name == in.FromName {
			hits = append(hits, a)
		}
	}
	switch {
	case in.FromName == "" || len(hits) == 0:
		return fleet.Agent{}, "no live session on this machine has that name"
	case len(hits) > 1:
		return fleet.Agent{}, "more than one session has that name"
	case in.PID != hits[0].Session.PID:
		// The socket a message comes from is named by the sending
		// process; the name alone could be anyone's claim.
		return fleet.Agent{}, "it did not come from that session's process"
	case m.isOrchestrator(hits[0]) || in.FromName == OrchestratorName:
		return fleet.Agent{}, "it is the orchestrator itself"
	case in.FromName == D1LaneName:
		return fleet.Agent{}, "the app lane does not report"
	}
	if m.peers.inFlight >= reportQueue {
		return fleet.Agent{}, "too many reports are waiting for the orchestrator"
	}
	if !m.takeReportToken(in.FromName, time.Now()) {
		return fleet.Agent{}, "that session is sending reports too fast"
	}
	return hits[0], ""
}

// takeReportToken spends one of a sender's reports, if it has one.
func (m *Model) takeReportToken(sender string, now time.Time) bool {
	p := &m.peers
	if p.tokens == nil {
		p.tokens, p.refilled = map[string]float64{}, map[string]time.Time{}
	}
	t, seen := p.tokens[sender]
	if !seen {
		t = reportBurst
	} else {
		t += float64(now.Sub(p.refilled[sender])) / float64(reportEvery)
		if t > reportBurst {
			t = reportBurst
		}
	}
	p.refilled[sender] = now
	if t < 1 {
		p.tokens[sender] = t
		return false
	}
	p.tokens[sender] = t - 1
	return true
}

// noteRefusal puts a refused message in the chat, at most once a minute
// per sender.
func (m *Model) noteRefusal(sender, why string) {
	p := &m.peers
	if p.refused == nil {
		p.refused = map[string]time.Time{}
	}
	now := time.Now()
	if at, ok := p.refused[sender]; ok && now.Sub(at) < refusalEvery {
		return
	}
	p.refused[sender] = now
	if len(p.refused) > 256 {
		for k, at := range p.refused {
			if now.Sub(at) >= refusalEvery {
				delete(p.refused, k)
			}
		}
	}
	m.addChat(chatEntry{role: "note", text: "Den refused a message to the orchestrator from " + sanitize.Label(sender, 60) + ": " + why, at: now})
}

// sendReport sends an admitted message as Den's own turn and shows it in
// the chat as one line, the body a press away.
func (m *Model) sendReport(a fleet.Agent, body string) tea.Cmd {
	content := peermsg.Report(a.Name, body)
	m.addChat(chatEntry{role: "fan", to: a.Name, sid: a.Session.SessionID, tool: "report to the orchestrator", text: actionSummary("", body), full: sanitize.Text(body), status: "passed on", at: time.Now()})
	m.peers.inFlight++
	m.orch.thinking = true
	m.orch.status = "thinking…"
	m.orch.lastEvent = time.Now()
	m.orch.origins = append(m.orch.origins, originReport)
	proc := m.orch.proc
	return func() tea.Msg {
		if err := proc.Send(content); err != nil {
			return orchEventMsg{ev: orch.Event{Kind: orch.Error, Text: "send failed: " + err.Error()}}
		}
		return nil
	}
}

// Turn origins: who started the turn the orchestrator is on.
const (
	originHuman  = "human"  // the human's own words
	originDen    = "den"    // Den asked on its own (a mission run or check)
	originReport = "report" // a peer's report Den passed on
)

// turnDone ends the oldest turn in flight.
func (m *Model) turnDone() {
	if len(m.orch.origins) == 0 {
		return
	}
	if m.orch.origins[0] == originReport && m.peers.inFlight > 0 {
		m.peers.inFlight--
	}
	m.orch.origins = m.orch.origins[1:]
}

// turnOrigin is who started the turn the orchestrator is on, "" when none
// is in flight.
func (m Model) turnOrigin() string {
	if len(m.orch.origins) == 0 {
		return ""
	}
	return m.orch.origins[0]
}

// onBackstop handles the orchestrator's guard tripping: the process is
// already killed. Its session is never resumed; a fresh one starts, which
// runs the hook's self-test first.
func (m *Model) onBackstop(why string) tea.Cmd {
	if m.orch.proc != nil && m.orch.proc.SessionID != "" {
		if m.orch.poisoned == nil {
			m.orch.poisoned = map[string]bool{}
		}
		m.orch.poisoned[m.orch.proc.SessionID] = true
	}
	m.orch.proc = nil
	m.orch.resume, m.orch.replay = "", ""
	m.orch.started, m.orch.thinking = false, false
	m.orch.origins, m.peers.inFlight, m.peers.envelopes = nil, 0, 0
	m.orch.unanswered = nil
	m.turn = missionTurn{}
	m.logSession("")
	m.logEntry(m.addChat(chatEntry{role: "note", text: "Den stopped the orchestrator: " + sanitize.Label(why, 120) + ". A message from another session may have reached it, so that conversation is closed for good; a fresh one starts now. Nothing it was asking was resent.", at: time.Now()}))
	m.orch.starting = true
	m.orch.status = "starting a fresh session…"
	return startOrchestrator(m.cfg, "", m.guide.active)
}

// authTrouble reports whether a failed first turn looks like the
// orchestrator could not sign in: it loads none of the user's settings
// sources, only the keys Den copies.
func authTrouble(text string) bool {
	t := strings.ToLower(text)
	for _, k := range []string{"auth", "log in", "login", "api key", "apikey", "401", "403", "credential", "oauth"} {
		if strings.Contains(t, k) {
			return true
		}
	}
	return false
}

// authNote is what Den says when the orchestrator cannot sign in.
func authNote(text string) string {
	return fmt.Sprintf("The orchestrator could not sign in to Claude (%s). It runs with none of your Claude Code settings files, only your login and the env, apiKeyHelper and cloud credential settings Den copies from your user settings; check that claude works in a terminal.", sanitize.Label(text, 120))
}
