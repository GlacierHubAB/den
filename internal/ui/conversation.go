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

// mirrorState is the mirror's state in the Model.
type mirrorState struct {
	// asked is the console the ask card is for, item that card; declined
	// the console the human said no for; on the console Den last turned
	// the mirror on for.
	asked, declined, on string
	item                string
	// reports are the agents of the report turns in flight, oldest first,
	// and whether each was a desktop agent's.
	reports []mirrorReport
	// reply is the orchestrator's text this turn so far, block by block.
	reply []string
}

type mirrorReport struct {
	agent   string
	desktop bool
}

// mirrorTick keeps the mirror as the human chose: on for the paired
// console with its powers under "always", asked once per pairing under
// "ask", never under "off"; off whenever that console is gone.
func (m *Model) mirrorTick() {
	srv := m.api.srv
	if srv == nil {
		return
	}
	cur, ok := srv.CurrentConsole()
	defer m.syncMirrorAsking()
	id, on := srv.Mirroring()
	if !ok || !cur.Powers {
		if on {
			srv.SetMirror("", false)
			m.mirror.on = ""
		}
		return
	}
	if on && id == cur.ID {
		return
	}
	if on {
		srv.SetMirror("", false) // another pairing's
	}
	switch m.cfg.API.MirrorConversation {
	case "always":
		m.mirrorOn(cur)
	case "off":
	default:
		if m.mirror.asked == cur.ID || m.mirror.declined == cur.ID {
			return
		}
		m.mirror.asked = cur.ID
		label := consoleLabelOf(cur)
		it := m.store.AddMirrorAsk("Show your orchestrator conversation in "+label+"?", []string{
			"With ^Y, " + label + " (pid " + strconv.Itoa(cur.PID) + ") shows what you and the orchestrator say, from now on: your own words typed in Den, its replies, and its answers to agents' reports and mission runs. An agent's own report text never goes, only Den's line saying who reported.",
			"Until you unpair it, or /api mirror off. Set [api] mirror_conversation = \"always\" to skip this question, or \"off\" to never be asked.",
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
	if ok && cur.Powers && m.mirror.asked == cur.ID && !(on && id == cur.ID) {
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
	if !ok || cur.ID != m.mirror.asked || !cur.Powers {
		m.store.Settle(it, false, "the console changed; nothing was shown")
		m.refresh()
		return m.say("That console is no longer paired with its powers; nothing is shown.", true)
	}
	if verb == "allow" {
		m.mirrorOn(cur)
		m.store.Settle(it, true, "shown in the console")
	} else {
		m.mirror.declined = cur.ID
		m.store.Settle(it, false, "kept in Den")
	}
	m.refresh()
	return m, nil
}

// mirrorCommand is /api mirror on|off: the human's switch, in Den.
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
			m.mirror.declined = cur.ID
		}
		m.mirror.on = ""
		return "The conversation is no longer shown in any console.", false
	case "on":
		if !ok || !cur.Powers {
			return "No paired console with its powers to show it in.", true
		}
		m.mirror.declined = ""
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
	m.mirror.reports = append(m.mirror.reports, mirrorReport{agent, desktop})
	origin := api.OriginReport
	if desktop {
		origin = api.OriginDesktop
	}
	m.converse(api.Converse{Origin: origin, Speaker: api.SpeakerDen, Agent: agent, Text: "Report from " + agent + "."})
}

// isDesktopAgent reports whether a session is a desktop mission's agent.
func (m Model) isDesktopAgent(sessionID string) bool {
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
			c.Agent = r.agent
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
