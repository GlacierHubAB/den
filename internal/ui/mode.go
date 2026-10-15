package ui

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/api"
	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/config"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/permmode"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// The global permission mode: the mode every agent Den starts runs in
// (permmode ranks them). Den holds it; config.toml only asks for one. A
// change down the line applies at once, from anywhere. Any other change,
// whoever asks (the human's /mode, a console, config.toml written while Den
// ran or while it was stopped), waits on a ModeSet card for the human's
// ^Y. Agents already running keep their mode until they restart.

// modeState is the global mode and the raise waiting on its card.
type modeState struct {
	global string
	// file is the mode config.toml last read as, or "!" and the value as
	// written when Den refused it; Den acts on the file only when it
	// changes.
	file    string
	reading bool
	// gen counts Den's own writes of the file; a read from before the
	// last one is dropped.
	gen int
	// load reads the setting; tests replace it.
	load func() modeReadMsg
	// The open card: its id, the mode it raises to, the global mode it
	// raises from, and who asked.
	askedID, askedTo, askedFrom, askedBy string
}

// modeReadMsg is one read of agents.permission_mode.
type modeReadMsg struct {
	ok      bool // the file read; when false nothing changes
	mode    string
	refused string
	gen     int // modeState.gen when the read began
}

func readMode() modeReadMsg {
	mode, refused, err := config.ModeSetting()
	if err != nil {
		return modeReadMsg{}
	}
	return modeReadMsg{ok: true, mode: mode, refused: refused}
}

// fileKey is a read as modeState.file keeps it.
func (r modeReadMsg) fileKey() string {
	if r.refused != "" {
		return "!" + r.refused
	}
	return r.mode
}

// agentMode is the mode a new local agent starts in. God mode wraps it.
func (m Model) agentMode() string {
	if m.mode.global == "" {
		return permmode.Shipped
	}
	return m.mode.global
}

// startMode takes the setting Den started with: at most permmode.Shipped,
// since the file may have been written by anyone while Den was stopped;
// more waits on a card. (With auto shipped, nothing is more today.) A
// refused value starts Den in the shipped mode, as no setting does.
func (m *Model) startMode(mode, refused string) {
	m.mode.load = readMode
	m.mode.global = permmode.Shipped
	read := modeReadMsg{ok: true, mode: mode, refused: refused}
	m.mode.file = read.fileKey()
	if refused != "" {
		m.modeRefusedNote(refused)
		return
	}
	m.mode.global = permmode.Lower(mode, permmode.Shipped)
	if permmode.Raises(m.mode.global, mode) {
		m.askMode(mode, "config.toml, written while Den was stopped")
	}
}

// pollMode is the tick's read of the setting, one at a time.
func (m *Model) pollMode() tea.Cmd {
	if m.mode.reading || m.mode.load == nil {
		return nil
	}
	m.mode.reading = true
	load, gen := m.mode.load, m.mode.gen
	return func() tea.Msg {
		r := load()
		r.gen = gen
		return r
	}
}

// applyModeRead acts on a read of the setting, only when it changed: a
// lowering applies, a raise asks on a card, a refused value is a red note.
func (m *Model) applyModeRead(r modeReadMsg) {
	m.mode.reading = false
	// A read begun before Den last wrote the file may hold what it said
	// before: never a change, whatever it says.
	if !r.ok || r.gen != m.mode.gen || r.fileKey() == m.mode.file {
		return
	}
	m.mode.file = r.fileKey()
	switch {
	case r.refused != "":
		m.modeRefusedNote(r.refused)
	case r.mode == m.mode.global:
	case permmode.Lowers(m.mode.global, r.mode):
		m.setMode(r.mode, "config.toml", false)
	default:
		m.askMode(r.mode, "config.toml")
	}
}

func (m *Model) modeRefusedNote(value string) {
	text := "config.toml: agents.permission_mode = " + sanitize.Label(value, 40) + " is not a mode Den starts agents in (" + strings.Join(permmode.Modes(), ", ") + "; bypassPermissions never is). Agents start in " + m.agentMode() + "."
	m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: text, at: time.Now()}))
	m.flash, m.flashErr = "config.toml names a permission mode Den refuses: see the chat.", true
}

// setMode makes mode the global mode: in memory, and in config.toml
// unless the change came from there. Only a lowering or the human's ^Y on
// a card calls it.
func (m *Model) setMode(mode, by string, write bool) {
	old := m.mode.global
	m.mode.global = mode
	note := "Agents Den starts now run in " + mode + " (was " + old + "), set by " + by + ": " + permmode.Allows(mode) + ". Running agents keep their mode until they restart."
	if write {
		m.mode.gen++
		if err := config.SetKey("agents", "permission_mode", `"`+mode+`"`); err != nil {
			note += " config.toml was not written (" + sanitize.Label(err.Error(), 120) + "): the mode holds until Den restarts."
		} else {
			m.mode.file = mode
		}
	}
	// A card raising from the old mode is spent: its "from" is gone.
	if m.mode.askedID != "" && m.mode.askedFrom != mode {
		m.dropModeCard("the mode changed to " + mode)
	}
	m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: note, at: time.Now()}))
}

// askMode puts a raise on a card, in place of one already open: the newer
// request is the one the human answers, and a card never changes under
// the reader. It returns the card's id, "" when Den could not raise one.
func (m *Model) askMode(to, by string) string {
	m.dropModeCard("replaced by a newer request")
	from := m.mode.global
	title := "Start agents in " + to + " instead of " + from + "? It asks you less."
	if !permmode.Lowers(to, from) {
		// plan and acceptEdits: each asks for something the other does not.
		title = "Start agents in " + to + " instead of " + from + "? It asks you for different things."
	}
	it := m.store.AddModeSet(title, []string{
		"now: " + from + ": " + permmode.Allows(from),
		"asked for: " + to + ": " + permmode.Allows(to),
		"asked by: " + sanitize.Label(by, 80),
		"Applies to agents Den starts or reopens from now on; running agents keep their mode until they restart.",
	})
	if it == nil {
		return ""
	}
	m.mode.askedID, m.mode.askedTo, m.mode.askedFrom, m.mode.askedBy = it.ID, to, from, by
	return it.ID
}

// dropModeCard settles the open mode card, if any, unanswered.
func (m *Model) dropModeCard(why string) {
	if m.mode.askedID == "" {
		return
	}
	for _, it := range m.store.Items() {
		if it.ID == m.mode.askedID && it.State != inbox.Resolved {
			m.store.Settle(it, false, why)
		}
	}
	m.mode.askedID, m.mode.askedTo, m.mode.askedFrom, m.mode.askedBy = "", "", "", ""
}

// decideModeSet is ^Y or ^N on a raise.
func (m Model) decideModeSet(it *inbox.Item, verb string) (tea.Model, tea.Cmd) {
	to, from, by, ok := m.mode.askedTo, m.mode.askedFrom, m.mode.askedBy, m.mode.askedID == it.ID
	switch {
	case !ok:
		m.store.Settle(it, false, "the request is gone")
		m.refresh()
		return m.say("That mode request is gone.", true)
	case verb != "allow":
		m.mode.askedID, m.mode.askedTo, m.mode.askedFrom, m.mode.askedBy = "", "", "", ""
		m.store.Settle(it, false, "kept "+m.mode.global)
		m.refresh()
		return m.say("Agents keep starting in "+m.mode.global+".", false)
	case m.mode.global != from:
		// The card raised from a mode that is no longer the global one.
		m.mode.askedID, m.mode.askedTo, m.mode.askedFrom, m.mode.askedBy = "", "", "", ""
		m.store.Settle(it, false, "the mode changed since")
		m.refresh()
		return m.say("The mode changed since that card was raised: nothing changed.", true)
	}
	m.mode.askedID, m.mode.askedTo, m.mode.askedFrom, m.mode.askedBy = "", "", "", ""
	m.store.Settle(it, true, "agents start in "+to)
	m.setMode(to, by+", allowed by you", true)
	m.refresh()
	return m.say("Agents Den starts now run in "+to+".", false)
}

// modeCommand is /mode: show the mode and the agents that differ, or set
// it. A lowering applies at once; a raise asks on a card, also when the
// human typed it: the card shows what the new mode lets agents do.
func (m Model) modeCommand(args []string) (tea.Model, tea.Cmd) {
	usageLine := "/mode " + strings.Join(permmode.Modes(), "|")
	if len(args) == 0 {
		text := "Agents start in " + m.agentMode() + ": " + permmode.Allows(m.agentMode()) + "."
		if differ := m.modeDiffers(); len(differ) > 0 {
			text += " In another mode: " + strings.Join(differ, ", ") + "."
		}
		return m.say(text+" "+usageLine, false)
	}
	if len(args) > 1 {
		return m.say(usageLine, true)
	}
	if strings.EqualFold(strings.ReplaceAll(args[0], "-", ""), "bypasspermissions") {
		return m.say("bypassPermissions is never the mode agents start in; /mode takes "+strings.Join(permmode.Modes(), ", ")+".", true)
	}
	to, ok := permmode.Parse(args[0])
	if !ok {
		return m.say(usageLine, true)
	}
	by := "you in Den"
	if m.dockFrom != "" {
		by = "the console " + m.dockFrom
	}
	switch {
	case to == m.mode.global:
		return m.say("Agents already start in "+to+".", false)
	case permmode.Lowers(m.agentMode(), to):
		m.setMode(to, by, true)
		m.refresh()
		return m.say("Agents Den starts now run in "+to+".", false)
	}
	if m.askMode(to, by) == "" {
		return m.say("A mode request is already waiting in the inbox.", true)
	}
	m.refresh()
	return m.say("Starting agents in "+to+" waits on a card in the inbox.", false)
}

// modeFromDock is a console's mode.set: a lowering applies, a raise is a
// card, and the reply says which.
func (m *Model) modeFromDock(r *api.ModeSet) tea.Cmd {
	m.dockFrom = sanitize.Label(r.Console, 40)
	defer func() { m.dockFrom = "" }()
	mm, cmd := m.modeCommand([]string{r.Mode})
	*m = mm.(Model)
	note := sanitize.Label(m.flash, 200)
	m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: "from " + m.dockFrom + ": mode.set " + r.Mode, at: time.Now()}))
	switch {
	case m.flashErr:
		r.Reply <- api.ModeSetResult{Code: api.CodeUnavailable, Note: note}
	case m.mode.askedID != "" && m.mode.askedTo == r.Mode && m.mode.global != r.Mode:
		r.Reply <- api.ModeSetResult{Card: m.mode.askedID, Note: note}
	default:
		r.Reply <- api.ModeSetResult{Applied: true, Note: note}
	}
	return cmd
}

// modeOfAgent is the mode an agent's own hooks last reported, however long
// ago, "" when none did: what it runs in, as far as Den knows.
func (m Model) modeOfAgent(sid string) string {
	return m.ctl.modes[sid].mode
}

// modeDiffers names the local agents whose mode is not the global one,
// with their mode, or "unknown" when none was reported.
func (m Model) modeDiffers() []string {
	var out []string
	for _, a := range m.agents {
		if a.Host != "" || !a.Session.Alive {
			continue
		}
		mode := m.modeOfAgent(a.Session.SessionID)
		if mode == m.agentMode() {
			continue
		}
		if mode == "" {
			mode = "unknown"
		}
		out = append(out, sanitize.Label(a.Name, 40)+" ("+sanitize.Label(mode, 30)+")")
	}
	sort.Strings(out)
	return out
}

// modeKnownDiffers counts the local agents that reported a mode other
// than the global one, for the header.
func (m Model) modeKnownDiffers() int {
	n := 0
	for _, a := range m.agents {
		if a.Host != "" || !a.Session.Alive {
			continue
		}
		if mode := m.modeOfAgent(a.Session.SessionID); mode != "" && mode != m.agentMode() {
			n++
		}
	}
	return n
}

// modeQuery answers den mcp's question: which mode does a new agent start
// in?
func (m *Model) modeQuery(r *bridge.Request) {
	var in struct {
		Lead string `json:"lead"`
	}
	json.Unmarshal(r.Envelope.Payload, &in)
	if m.god.on && in.Lead == "" {
		if d, ok := m.godSpawn(); ok {
			r.Reply(d)
			return
		}
	}
	r.Reply(bridge.Decision{Behavior: "allow", Message: m.agentMode()})
}
