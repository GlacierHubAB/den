package ui

import (
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/api"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// The conversation mirror, Den's side: what each orchestrator turn was
// (the human's words, Den's own line for a report or a mission run, a
// console's words) and the orchestrator's completed reply go to den-api's
// mirror, which shows them on the paired console while the human lets it
// ([api] mirror_conversation: ask, always or off). An agent's own report
// text never goes; the orchestrator's words about it do.

// mirrorState is the mirror's state in the Model. The human's answer is
// the console's own (api.Console.Mirror, kept with its pairing's lineage
// in den-api's record), so a dock service restarted on this boot keeps it.
type mirrorState struct {
	// asked is the lineage the ask card is for, item that card; on the
	// console Den last turned the mirror on for.
	asked, on string
	item      string
	// reports are the agents of the report turns in flight, oldest first,
	// and whether each was a desktop agent's.
	reports []mirrorReport
	// reply is the orchestrator's text this turn so far, block by block.
	reply []string
}

type mirrorReport struct {
	agent, id string // the agent's name, and its session id
	desktop   bool
	// turn and lineage are the console turn the reported work came from
	// (reportTurn), "" for none.
	turn, lineage string
}

// reportTurnFor is how long after its card ended a report still belongs
// to the console turn the card was made in.
const reportTurnFor = 10 * time.Minute

// consoleTurnNow is the console's turn in flight, as the desktop start
// card's origin line sees it (deskOrigin): its id and its pairing's
// lineage, "" outside a console's turn. An app pairing's turn is not a
// console's.
func (m Model) consoleTurnNow() (turn, lineage string) {
	if y := m.ctl.turn; y != nil && m.ctl.sent && !m.ctl.ended && y.Console != "" {
		return y.Turn, y.Lineage
	}
	return "", ""
}

// currentLineage is the current pairing's lineage, "" for none; tests
// replace it.
var currentLineage = func(m Model) string {
	if m.api.srv == nil {
		return ""
	}
	cur, ok := m.api.srv.CurrentConsole()
	if !ok {
		return ""
	}
	return cur.Lineage
}

// reportTurn is the console turn a report from session sid is about: a
// desktop agent's mission's, else the newest card of sid's that was made
// in a console's turn of the current pairing and is live or ended within
// reportTurnFor. "" for none: then the report carries no turn, rather
// than an older one.
func (m Model) reportTurn(sid string, desktop bool, now time.Time) (turn, lineage string) {
	cur := currentLineage(m)
	if cur == "" {
		return "", ""
	}
	recent := func(ended time.Time) bool { return ended.IsZero() || now.Sub(ended) <= reportTurnFor }
	if desktop {
		for _, dm := range m.desk.missions {
			if dm != nil && dm.SessionID == sid && dm.consoleTurn != "" && dm.turnLineage == cur && recent(dm.endedAt) {
				return dm.consoleTurn, dm.turnLineage
			}
		}
		return "", ""
	}
	for i := len(m.orch.chat) - 1; i >= 0; i-- {
		d := m.orch.chat[i].card
		if d == nil || d.SessionID != sid || d.consoleTurn == "" || d.turnLineage != cur {
			continue
		}
		if d.live() || recent(d.EndedAt) {
			return d.consoleTurn, d.turnLineage
		}
	}
	return "", ""
}

// What mirrorTick does next (mirrorNext).
type mirrorStep int

const (
	mirrorStay  mirrorStep = iota // nothing changes
	mirrorFresh                   // on, under "always" or a fresh yes
	mirrorAgain                   // on again, under the human's kept yes
	mirrorAsk                     // put up the ask card for this lineage
)

// mirrorNext is mirrorTick's decision: whether to turn off the mirror Den
// shows now (the console went, lost its powers, or is another pairing's),
// and what to do for the console cur. mode is [api] mirror_conversation,
// onID and on what den-api mirrors now, asked the lineage the ask card was
// last put up for.
func mirrorNext(mode string, cur api.Console, ok bool, onID string, on bool, asked string) (off bool, step mirrorStep) {
	if !ok || !cur.Powers {
		return on, mirrorStay
	}
	if on && onID == cur.ID {
		return false, mirrorStay
	}
	switch mode {
	case "always":
		return on, mirrorFresh
	case "off":
		return on, mirrorStay
	}
	switch {
	case cur.Mirror == api.MirrorYes:
		return on, mirrorAgain
	case cur.Mirror == api.MirrorNo, cur.Lineage == "", asked == cur.Lineage:
		return on, mirrorStay
	}
	return on, mirrorAsk
}

// mirrorTick keeps the mirror as the human chose: on for the paired
// console with its powers under "always", asked once per pairing lineage
// under "ask" and then as answered, never under "off"; off whenever that
// console is gone or without its powers.
func (m *Model) mirrorTick() {
	srv := m.api.srv
	if srv == nil {
		return
	}
	cur, ok := srv.CurrentConsole()
	defer m.syncMirrorAsking()
	id, on := srv.Mirroring()
	off, step := mirrorNext(m.cfg.API.MirrorConversation, cur, ok, id, on, m.mirror.asked)
	if off {
		srv.SetMirror("", false)
		m.mirror.on = ""
	}
	switch step {
	case mirrorFresh:
		m.mirrorOn(cur)
	case mirrorAgain:
		m.mirrorOnAgain(cur)
	case mirrorAsk:
		m.mirror.asked = cur.Lineage
		label := consoleLabelOf(cur)
		it := m.store.AddMirrorAsk("Show your orchestrator conversation in "+label+"?", []string{
			"With ^Y, " + label + " (pid " + strconv.Itoa(cur.PID) + ") shows what you and the orchestrator say, from now on: your own words typed in Den, its replies, and its answers to agents' reports and mission runs. An agent's own report text never goes, only Den's line saying who reported.",
			"Until you unpair it, or /api mirror off; the same dock service, restarted on this boot, keeps your answer. Set [api] mirror_conversation = \"always\" to skip this question, or \"off\" to never be asked.",
		})
		m.mirror.item = it.ID
		m.refresh()
	}
}

// syncMirrorAsking tells den-api whether Den's ask card waits for the
// paired console now: the console then shows "asking". Den-api sends the
// console only a change.
func (m *Model) syncMirrorAsking() {
	srv := m.api.srv
	if srv == nil {
		return
	}
	cur, ok := srv.CurrentConsole()
	id, on := srv.Mirroring()
	asking := ""
	if ok && cur.Powers && cur.Mirror == "" && m.mirror.asked == cur.Lineage && !(on && id == cur.ID) {
		for _, it := range m.store.Items() {
			if it.ID == m.mirror.item && it.Answerable() {
				asking = cur.ID
			}
		}
	}
	srv.SetMirrorAsking(asking)
}

// mirrorOn turns the mirror on for console cur and says so in Den's chat.
func (m *Model) mirrorOn(cur api.Console) {
	m.api.srv.SetMirror(cur.ID, true)
	m.mirror.on = cur.ID
	m.logEntry(m.addUnthreaded(chatEntry{role: "note", at: time.Now(), text: "Den shows this conversation in " + consoleLabelOf(cur) + " (pid " + strconv.Itoa(cur.PID) + "), from now on. /api mirror off stops it."}))
}

// mirrorOnAgain turns the mirror on again for console cur under the human's
// kept yes (after the console reconnected, Den restarted, or its powers
// came back), from the time they said it, and says so in Den's chat each
// time: the human always hears it.
func (m *Model) mirrorOnAgain(cur api.Console) {
	m.api.srv.ResumeMirror(cur.ID, cur.MirrorSince)
	m.mirror.on = cur.ID
	m.logEntry(m.addUnthreaded(chatEntry{role: "note", at: time.Now(), text: "Den shows this conversation in " + consoleLabelOf(cur) + " again, as you allowed; /api mirror off stops it."}))
}

// consoleLabelOf is how Den names a console to the human.
func consoleLabelOf(c api.Console) string {
	if c.Exe != "" {
		return sanitize.Label(appName(c.Exe), 60)
	}
	return api.ConsoleLabel
}

// decideMirror is the human's ^Y or ^N on the ask card, in Den only.
func (m Model) decideMirror(it *inbox.Item, verb string) (tea.Model, tea.Cmd) {
	srv := m.api.srv
	if srv == nil {
		m.store.Settle(it, false, "stale")
		m.refresh()
		return m, nil
	}
	cur, ok := srv.CurrentConsole()
	if !ok || cur.Lineage == "" || cur.Lineage != m.mirror.asked || !cur.Powers || !srv.SetMirrorConsent(cur.Lineage, verb == "allow") {
		m.store.Settle(it, false, "the console changed; nothing was shown")
		m.refresh()
		return m.say("That console is no longer paired with its powers; nothing is shown.", true)
	}
	if verb == "allow" {
		m.mirrorOn(cur)
		m.store.Settle(it, true, "shown in the console")
	} else {
		m.store.Settle(it, false, "kept in Den")
	}
	m.refresh()
	return m, nil
}

// mirrorCommand is /api mirror on|off: the human's switch, in Den. Off is
// the human's no for the console's lineage, kept as the card's no is.
func (m *Model) mirrorCommand(arg string) (string, bool) {
	srv := m.api.srv
	if srv == nil {
		return "den-api.sock is off.", true
	}
	cur, ok := srv.CurrentConsole()
	switch arg {
	case "off":
		srv.SetMirror("", false)
		if ok {
			srv.SetMirrorConsent(cur.Lineage, false)
		}
		m.mirror.on = ""
		return "The conversation is no longer shown in any console.", false
	case "on":
		if !ok || !cur.Powers || !srv.SetMirrorConsent(cur.Lineage, true) {
			return "No paired console with its powers to show it in.", true
		}
		m.mirrorOn(cur)
		return "Shown in " + consoleLabelOf(cur) + ".", false
	}
	return "Use /api mirror on or /api mirror off.", true
}

// converse hands one entry to the mirror, when den-api runs.
func (m *Model) converse(c api.Converse) { mirrorSink(m.api.srv, c) }

// mirrorSink is where the entries go; tests replace it.
var mirrorSink = func(s *api.Server, c api.Converse) {
	if s != nil {
		s.Converse(c)
	}
}

// mirrorTurn records the start of a turn Den sends through tell: the
// human's words, or Den's own line for a mission run.
func (m *Model) mirrorTurn(origin, shown string) {
	switch origin {
	case originHuman:
		m.converse(api.Converse{Origin: api.OriginYou, Speaker: api.SpeakerYou, Text: shown})
	case originDen:
		m.converse(api.Converse{Origin: api.OriginMission, Speaker: api.SpeakerDen, Text: shown})
	}
}

// mirrorReportTurn records that an agent reported: Den's line naming it,
// never the report itself.
func (m *Model) mirrorReportTurn(agent, sessionID string) {
	desktop := m.isDesktopAgent(sessionID)
	turn, lineage := m.reportTurn(sessionID, desktop, time.Now())
	m.mirror.reports = append(m.mirror.reports, mirrorReport{agent, sessionID, desktop, turn, lineage})
	for _, e := range m.orch.chat {
		if d := e.card; d != nil && sessionID != "" && d.SessionID == sessionID {
			d.reported, d.noReportDue = true, time.Time{}
		}
	}
	origin, ending := api.OriginReport, ""
	if desktop {
		origin = api.OriginDesktop
		if turn != "" && m.deskFinishedSilent(sessionID) {
			ending = api.EndingNoReport
		}
	}
	m.converse(api.Converse{Origin: origin, Speaker: api.SpeakerDen, Agent: agent, AgentID: sessionID, Turn: turn, TurnLineage: lineage, Ending: ending, Text: "Report from " + agent + "."})
}

// deskFinishedSilent reports whether sid's desktop mission ended because
// its agent finished, with no report from it: not a limit, the overlay's
// timeout or a stop, which say why in their own note.
func (m Model) deskFinishedSilent(sid string) bool {
	for _, dm := range m.desk.missions {
		if dm != nil && dm.SessionID == sid && dm.Ended == deskAgentFinished && strings.TrimSpace(dm.report) == "" {
			return true
		}
	}
	return false
}

// isDesktopAgent reports whether a session is a desktop mission's agent.
func (m Model) isDesktopAgent(sessionID string) bool {
	if sessionID == "" {
		return false // a mission that ended before its agent started has none
	}
	for _, dm := range m.desk.missions {
		if dm != nil && dm.SessionID == sessionID {
			return true
		}
	}
	return false
}

// mirrorAppTurn records a console's (or an app's) turn as it goes out.
func (m *Model) mirrorAppTurn(y *api.Say) {
	label := api.ConsoleLabel
	if y.Console == "" {
		label = appName(y.Exe)
	}
	m.converse(api.Converse{Origin: api.OriginConsole, Speaker: api.SpeakerYou, Turn: y.Turn, ConsoleID: y.Console, ConsoleLabel: label, Text: y.Text})
}

// mirrorText keeps one text block of the orchestrator's reply.
func (m *Model) mirrorText(text string) {
	if t := strings.TrimSpace(text); t != "" {
		m.mirror.reply = append(m.mirror.reply, t)
	}
}

// mirrorReply records the orchestrator's completed reply when its turn
// ends: origin is the turn's, result the Result's text, used when no text
// block came. A console's turn is the one in m.ctl.turn.
func (m *Model) mirrorReply(origin, result string) {
	var appTurn *api.Say
	if origin == originApp {
		appTurn = m.ctl.turn
	}
	text := strings.Join(m.mirror.reply, "\n\n")
	m.mirror.reply = nil
	if strings.TrimSpace(text) == "" {
		text = result
	}
	c := api.Converse{Speaker: api.SpeakerOrchestrator, Text: text}
	switch origin {
	case originHuman:
		c.Origin = api.OriginYou
	case originDen:
		c.Origin = api.OriginMission
	case originReport:
		c.Origin = api.OriginReport
		if len(m.mirror.reports) > 0 {
			r := m.mirror.reports[0]
			m.mirror.reports = m.mirror.reports[1:]
			c.Agent, c.AgentID = r.agent, r.id
			c.Turn, c.TurnLineage = r.turn, r.lineage
			if r.desktop {
				c.Origin = api.OriginDesktop
			}
		}
	case originApp:
		c.Origin = api.OriginConsole
		if appTurn != nil {
			c.Turn, c.ConsoleID = appTurn.Turn, appTurn.Console
			c.ConsoleLabel = api.ConsoleLabel
			if appTurn.Console == "" {
				c.ConsoleLabel = appName(appTurn.Exe)
			}
		}
	default:
		return
	}
	m.converse(c)
}
