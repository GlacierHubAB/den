package ui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/config"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// The console's automatic powers: config.toml's api.console_powers =
// "always" gives the console's service its powers whenever it pairs by
// itself. Den never acts on the file alone, since anything of the user's
// may write it: "always" waits on a PowersSet card for the human's ^Y in
// Den's own window, at every start and on every change, and holds until
// Den restarts. A change back to "unchanged" applies at once. Every lock of
// the powers (God mode, /api lock, another program, a wrong proof) ends it
// for the run, in the server (api.LockConsolePowers).

// powersState is the setting as Den last read it and the card waiting on
// it.
type powersState struct {
	// file is the setting as last read: "always", "unchanged", or "!" and
	// a value Den refused. Den acts on the file only when it changes.
	file    string
	reading bool
	// load reads the setting; tests replace it.
	load func() powersReadMsg
	// askedID is the open card, "" for none; gen is the server's count of
	// locks of the powers when it was raised, so a lock since stales it.
	askedID string
	gen     uint64
	// on is "always" as Den last set it on the server, to say so when a
	// lock ends it.
	on bool
}

// powersReadMsg is one read of api.console_powers.
type powersReadMsg struct {
	ok      bool // the file read; when false nothing changes
	value   string
	refused string
}

func readPowers() powersReadMsg {
	value, refused, err := config.PowersSetting()
	if err != nil {
		return powersReadMsg{}
	}
	return powersReadMsg{ok: true, value: value, refused: refused}
}

// fileKey is a read as powersState.file keeps it.
func (r powersReadMsg) fileKey() string {
	if r.refused != "" {
		return "!" + r.refused
	}
	return r.value
}

func (m *Model) powersLoad() func() powersReadMsg {
	if m.powers.load == nil {
		return readPowers
	}
	return m.powers.load
}

// startPowers reads the setting when the API has just started, with
// "always" off on the server: "always" asks on a card, whose source says
// by. Without the API it only remembers the read.
func (m *Model) startPowers(by string) {
	r := m.powersLoad()()
	if !r.ok {
		return
	}
	m.powers.file = r.fileKey()
	switch {
	case m.api.srv == nil:
	case r.refused != "":
		m.powersRefusedNote(r.refused)
	case r.value == "always":
		m.askPowers(by)
	}
}

// pollPowers is the tick's read of the setting, one at a time, while the
// API runs. It first says when a lock has ended "always" since.
func (m *Model) pollPowers() tea.Cmd {
	if m.api.srv == nil {
		return nil
	}
	if m.powers.on && !m.api.srv.ConsolePowersAlways() {
		m.powers.on = false
		m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: "Your console's powers were locked, so config.toml's \"always\" is off for the rest of this run: the console's service gets its powers again only by your ^Y. Den asks for \"always\" again when it restarts, or when config.toml changes.", at: time.Now()}))
	}
	// A card raised before a lock is spent: its ^Y would be refused anyway.
	if m.powers.askedID != "" && m.api.srv.ConsolePowersLockGen() != m.powers.gen {
		m.dropPowersCard("your console's powers were locked since")
	}
	if m.powers.reading {
		return nil
	}
	m.powers.reading = true
	load := m.powersLoad()
	return func() tea.Msg { return load() }
}

// applyPowersRead acts on a read of the setting, only when it changed:
// "always" asks on a card, anything else turns "always" off at once, a
// refused value is a red note and changes nothing. One read a tick, so a
// file changed again and again raises at most one card a tick.
func (m *Model) applyPowersRead(r powersReadMsg) {
	m.powers.reading = false
	if !r.ok || m.api.srv == nil || r.fileKey() == m.powers.file {
		return
	}
	m.powers.file = r.fileKey()
	switch {
	case r.refused != "":
		m.powersRefusedNote(r.refused)
	case r.value == "always":
		m.askPowers("changed in config.toml while Den runs")
	default:
		m.dropPowersCard("config.toml no longer asks for it")
		was := m.powers.on || m.api.srv.ConsolePowersAlways()
		m.api.srv.EndConsolePowersAlways()
		m.powers.on = false
		if was {
			m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: "config.toml: console_powers is \"unchanged\" again: your console's service gets its powers by itself only as you last gave them in this run.", at: time.Now()}))
		}
	}
}

func (m *Model) powersRefusedNote(value string) {
	text := "config.toml: api.console_powers = " + sanitize.Label(value, 40) + " is not a setting Den knows (\"unchanged\" or \"always\"); nothing changed."
	m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: text, at: time.Now()}))
	m.flash, m.flashErr = "config.toml names a console_powers setting Den refuses: see the chat.", true
}

// askPowers puts "always" on a card, in place of one already open: a new
// card for every change, so a ^Y aimed at the one before finds it gone.
func (m *Model) askPowers(by string) {
	m.dropPowersCard("replaced by a newer request")
	gen := m.api.srv.ConsolePowersLockGen()
	it := m.store.AddPowersSet("Let your console get its powers by itself whenever it pairs again, until Den restarts?", []string{
		"config.toml: api.console_powers = \"always\", " + sanitize.Label(by, 80) + ".",
		"Now: your console's service gets its powers by itself only as you last gave them in this run; anything else asks on Den's powers card.",
		"With it: whatever passes Den's checks as your console's service gets them by itself, until Den restarts or the powers are locked (God mode, /api lock, another program, a wrong proof). Who may pair does not change.",
		"Den never takes this from the file alone: ^N keeps asking.",
	})
	if it == nil {
		return
	}
	m.powers.askedID, m.powers.gen = it.ID, gen
}

// dropPowersCard settles the open card, if any, unanswered.
func (m *Model) dropPowersCard(why string) {
	if m.powers.askedID == "" {
		return
	}
	for _, it := range m.store.Items() {
		if it.ID == m.powers.askedID && it.State != inbox.Resolved {
			m.store.Settle(it, false, why)
		}
	}
	m.powers.askedID = ""
}

// decidePowersSet is ^Y or ^N on the card, in Den's own window only.
func (m Model) decidePowersSet(it *inbox.Item, verb string) (tea.Model, tea.Cmd) {
	switch {
	case m.powers.askedID != it.ID || m.api.srv == nil:
		m.store.Settle(it, false, "the request is gone")
		m.refresh()
		return m.say("That request is gone.", true)
	case verb != "allow":
		m.powers.askedID = ""
		m.store.Settle(it, false, "kept asking")
		m.refresh()
		return m.say("Your console gets its powers by itself only as you last gave them; anything else asks.", false)
	case m.powers.file != "always":
		m.powers.askedID = ""
		m.store.Settle(it, false, "config.toml changed since")
		m.refresh()
		return m.say("config.toml changed since that card was raised: nothing changed.", true)
	}
	m.powers.askedID = ""
	if !m.api.srv.AllowConsolePowersAlways(m.powers.gen) {
		m.store.Settle(it, false, "your console's powers were locked since")
		m.refresh()
		return m.say("Your console's powers were locked since that card was raised: nothing changed.", true)
	}
	m.store.Settle(it, true, "allowed until Den restarts")
	m.powers.on = true
	m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: "Your console's service gets its powers by itself whenever it pairs again, until Den restarts or the powers are locked (config.toml's \"always\", allowed by you in Den).", at: time.Now()}))
	m.refresh()
	return m.say("Allowed until Den restarts.", false)
}
