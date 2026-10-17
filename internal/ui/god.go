package ui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/api"
	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/permmode"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// God mode: agents Den starts act without asking, and Den answers some of
// its own cards. It is turned on only here, by `/god on` typed in Den's
// TUI and then the phrase typed into the TUI within godArmFor; nothing
// else turns it on: no config key, environment variable, den.sock or
// den-api.sock verb, and no command from the dock. It lives in memory
// only, so every Den start begins with it off. Anything may turn it off.

// godPhrase is what the human types, after /god on, to turn God mode on.
const godPhrase = "let agents act without asking"

// godArmFor is how long /god on waits for the phrase.
const godArmFor = 60 * time.Second

// godWarning is shown, in red, when /god on is typed.
const godWarning = "GOD MODE: agents you start act without asking, and Den answers its own cards. An agent can then do anything you can, including changing Den itself. Den still refuses messages from other sessions, fences what hosts and agents report, and keeps remote hosts as they are."

// godUI is God mode's part of the model.
type godUI struct {
	on      bool
	since   time.Time
	armedAt time.Time // when /god on was typed, zero when not waiting for the phrase
}

// godChip is God mode in Den's header.
func godChip(since time.Time) string {
	return "GOD MODE since " + since.Local().Format("15:04") + " · /god off"
}

// godOn reports whether God mode is on.
func (m Model) godOn() bool { return m.god.on }

// godAutoKinds are Den's own cards God mode answers itself (yes).
var godAutoKinds = map[inbox.Kind]bool{
	inbox.Close: true, inbox.Cleanup: true, inbox.Restore: true, inbox.MissionUpdate: true, inbox.MissionMove: true,
}

// godTTYKinds are the cards that, while God mode is on, only the human
// answers, in Den's own window: never Den itself, never the dock. A
// bypass agent is what could mint a console, so a dock press is not
// enough for them.
var godTTYKinds = map[inbox.Kind]bool{
	inbox.Pair: true, inbox.Grant: true, inbox.APIOn: true, inbox.Install: true, inbox.Guidance: true,
	inbox.Skills: true, inbox.Budget: true, inbox.UsageResume: true, inbox.UsageSet: true, inbox.Message: true,
	inbox.ControlAsk: true, inbox.Merge: true, inbox.Console: true, inbox.ModeSet: true,
	// A desktop agent acts on your screen: its install, its mission, each
	// action it asks for and each step only you do are yours alone.
	inbox.DesktopInstall: true, inbox.DesktopMission: true, inbox.DesktopAct: true, inbox.YourTurn: true,
	// A mission's done criteria decide when it is done: changing them, and
	// saying it is done, are yours. Never Den's by rule: Den answers a
	// MissionUpdate only because no update can meet or change a criterion.
	inbox.MissionCriteria: true, inbox.MissionDone: true,
	// What the console may read of the human's own conversation.
	inbox.MirrorAsk: true,
	// The orchestrator's tuning, and its proposed change to its own
	// instructions, are the human's to keep or undo.
	inbox.ProcessTuned: true, inbox.PromptProposal: true,
}

// godTTYOnly reports whether, now, only the human answers it in Den.
func (m Model) godTTYOnly(it *inbox.Item) bool {
	return m.god.on && godTTYKinds[it.Kind]
}

// godCommand is /god: on (then the phrase), off, or how it stands.
func (m Model) godCommand(args []string) (tea.Model, tea.Cmd) {
	sub := ""
	if len(args) > 0 {
		sub = strings.ToLower(args[0])
	}
	switch sub {
	case "on":
		if m.fromDock {
			return m.say("God mode is turned on only in Den's own window.", true)
		}
		if m.god.on {
			return m.say("God mode is already on: /god off ends it.", false)
		}
		m.god.armedAt = time.Now()
		m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: godWarning, at: time.Now()}))
		mm, cmd := m.say("To turn it on, type: "+godPhrase+" (within a minute; anything else cancels).", true)
		if m.api.view == api.ViewUser {
			// In User view Den's window is hidden: the warning and the
			// phrase are read and typed there, so it comes back first.
			cmd = tea.Batch(m.showDen(), cmd)
		}
		return mm, cmd
	case "off":
		if !m.god.on {
			m.god.armedAt = time.Time{}
			return m.say("God mode is off.", false)
		}
		m.godOff("you turned it off")
		return m.say("God mode is off.", false)
	case "":
		if m.god.on {
			return m.say("God mode is on since "+m.god.since.Local().Format("15:04")+": /god off ends it.", true)
		}
		return m.say("God mode is off. /god on, then the phrase, turns it on.", false)
	}
	return m.say("/god on, /god off, or /god", true)
}

// godPhraseTyped takes the TUI line typed while /god on waits: the phrase
// turns God mode on, anything else (or too late) cancels. It reports
// whether it took the line, which then goes nowhere else.
func (m *Model) godPhraseTyped(text string) bool {
	if m.god.armedAt.IsZero() {
		return false
	}
	armed := m.god.armedAt
	m.god.armedAt = time.Time{}
	if text != godPhrase || time.Since(armed) > godArmFor {
		m.flash, m.flashErr = "God mode stays off.", false
		return true
	}
	m.god.on, m.god.since = true, time.Now()
	if m.api.srv != nil {
		m.api.srv.SetGod(true, m.god.since)
		// A console that answers cards and runs commands loses that until
		// you give it back on Den's powers card; God mode going off does
		// not give it back.
		lockConsolePowers(m, "God mode turned on")
	}
	m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: "God mode is on. /god off, the dock or den god off ends it; it is off again at Den's next start.", at: time.Now()}))
	m.flash, m.flashErr = "GOD MODE is on.", true
	return true
}

// lockConsolePowers takes the console's powers (api LockConsolePowers);
// tests replace it.
var lockConsolePowers = func(m *Model, why string) { m.api.srv.LockConsolePowers(why) }

// godOff turns God mode off, and says why. It only lowers, so any path
// may: /god off, the dock's god.off, den god off, an unpair.
func (m *Model) godOff(why string) {
	m.god.armedAt = time.Time{}
	if !m.god.on {
		return
	}
	m.god.on = false
	if m.api.srv != nil {
		m.api.srv.SetGod(false, time.Time{})
	}
	m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: "God mode is off: " + sanitize.Label(why, 80) + ".", at: time.Now()}))
	m.refresh()
}

// godSweep answers, yes, Den's own cards God mode answers itself, each
// with a chat line, and settled by "rule" for the dock. Only cards that
// came after God mode went on: the phrase agrees to what comes, not to
// decisions already waiting for the human. Never a card about a remote
// host: God mode is local only.
func (m *Model) godSweep() tea.Cmd {
	if !m.god.on {
		return nil
	}
	var cmds []tea.Cmd
	for _, it := range m.store.Items() {
		if !it.Answerable() || !godAutoKinds[it.Kind] || it.At.Before(m.god.since) || hostOf(itemKey(it)) != "" || strings.HasPrefix(it.SessionID, inbox.HostPrefix) {
			continue
		}
		title := sanitize.Label(it.Title, 100)
		it.AnsweredBy = "rule"
		mm, cmd := m.answerItem(it, "allow")
		*m = mm.(Model)
		if it.State != inbox.Resolved {
			it.AnsweredBy = ""
			continue
		}
		m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: "God mode: Den allowed " + it.Kind.String() + ": " + title, at: time.Now()}))
		cmds = append(cmds, cmd)
	}
	return tea.Batch(cmds...)
}

// godSpawn is the answer to den mcp for an agent the orchestrator starts
// in God mode: bypassPermissions, a session id Den draws, and the guarded
// --settings file Den wrote for that id. False when Den cannot write the
// guard: the agent then starts in the global mode. Agents a lead starts
// are never bypass (a bypass agent leads no team), nor are reopened ones.
func (m *Model) godSpawn() (bridge.Decision, bool) {
	if !m.orch.control {
		// Messages to the new agent must go through deliverTo, so the
		// orchestrator must be on send_message first (it restarts into it
		// when idle).
		m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: "God mode: the orchestrator is not on Den's send_message yet, so the new agent starts in " + m.agentMode() + ".", at: time.Now()}))
		return bridge.Decision{}, false
	}
	den, err := os.Executable()
	if err == nil {
		den, err = filepath.EvalSymlinks(den)
	}
	sock, serr := filepath.Abs(bridge.SocketPath())
	if err != nil || serr != nil {
		return bridge.Decision{}, false
	}
	if n := m.guardsWaiting(); n >= guardsWaitingMax {
		m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: "God mode: " + strconv.Itoa(n) + " new agents have not started yet, so the next starts in " + m.agentMode() + ".", at: time.Now()}))
		return bridge.Decision{}, false
	}
	sid := newSessionID()
	path, err := m.guardAgent(sid, den, sock, agentHooks{Inbound: true})
	if err != nil {
		m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: "God mode: Den could not write a new agent's guard (" + sanitize.Label(err.Error(), 80) + "), so it starts in " + m.agentMode() + ".", at: time.Now()}))
		return bridge.Decision{}, false
	}
	m.med.guards[sid].launched = time.Now()
	return bridge.Decision{Behavior: "allow", Message: permmode.Bypass, Answers: map[string]string{"session_id": sid, "settings": path}}, true
}

// guardsWaitingMax caps guards handed out whose agent has not started;
// guardWait is how long one waits before Den drops it.
const (
	guardsWaitingMax = 8
	guardWait        = 2 * time.Minute
)

// guardsWaiting counts guards handed out whose agent has not started.
func (m Model) guardsWaiting() int {
	n := 0
	for _, g := range m.med.guards {
		if !g.launched.IsZero() && !g.registered {
			n++
		}
	}
	return n
}

// dropStaleGuards forgets guards whose agent never started within
// guardWait, with their files.
func (m *Model) dropStaleGuards() {
	for sid, g := range m.med.guards {
		if !g.launched.IsZero() && !g.registered && time.Since(g.launched) > guardWait {
			os.Remove(g.settings)
			delete(m.med.guards, sid)
		}
	}
}

// handleGodOff is `den god off` over den.sock. It only lowers, so it is
// never refused for who asks.
func (m *Model) handleGodOff(r *bridge.Request) tea.Cmd {
	was := m.god.on
	m.godOff("den god off")
	msg := "God mode was off."
	if was {
		msg = "God mode is off."
	}
	r.Reply(bridge.Decision{Behavior: "allow", Message: msg})
	return nil
}
