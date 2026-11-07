package ui

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"golang.org/x/text/unicode/norm"

	"github.com/GlacierHubAB/den/internal/api"
	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/desktop"
	"github.com/GlacierHubAB/den/internal/permmode"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// While an agent has the desktop (deskHolds), Den's window takes only what
// cannot widen anything: ^N and deny, stopping, pausing and ending, every
// lowering, moving around and typing into the line. Everything else, every
// ^Y and every other Enter, waits until the mission is paused or ends.
// This is a list of what runs, not of what waits: a key or command added
// later waits until it is added here.

// deskHeldNote is what a key that waits says.
const deskHeldNote = "An agent has the desktop: pause or end it first (/desktop pause, /desktop stop)."

// deskHeldDock is what a console's answer, mode.set or command that waits
// gets back (deskHeldAct).
const deskHeldDock = "a desktop agent has the desktop: pause or end it in Den first"

// deskHeldVerbs are the card answers taken while held: Den's window takes
// only ^N's deny then (deskHoldRuns; ^Y and ^X wait), and so does a
// console. Every kind a console may answer (todayDock) denies by leaving
// things as they are or stopping something.
var deskHeldVerbs = map[string]bool{"deny": true}

// deskHeldAct answers a console's act that Den's window wouldn't take
// while held, and reports whether it did: a card answer but a deny, a
// mode.set but a lowering, a turn for the orchestrator, a command
// deskHeldCommand doesn't list. A line
// the dock refuses anyway (dockLine) is left to runFromDock, which keeps
// the dock's own narrower rules (/desktop pause and stop only).
func (m *Model) deskHeldAct(act api.Act) bool {
	switch {
	case act.CardAnswer != nil:
		if deskHeldVerbs[act.CardAnswer.Verb] {
			return false
		}
		act.CardAnswer.Reply <- api.CardResult{Code: api.CodeUnavailable, Note: deskHeldDock}
	case act.ModeSet != nil:
		if to, ok := permmode.Parse(act.ModeSet.Mode); ok && permmode.Lowers(m.mode.global, to) {
			return false
		}
		act.ModeSet.Reply <- api.ModeSetResult{Code: api.CodeUnavailable, Note: deskHeldDock}
	case act.Say != nil:
		// A turn for the orchestrator is a message to an agent, which
		// waits in Den's window too: an app's and a console's alike.
		deskSayFail(act.Say, deskHeldDock)
	case act.Command != nil:
		words, _, why := dockLine(act.Command.Text)
		if why != "" {
			return false
		}
		if deskHeldDockLine(words, m.mode.global) {
			return false
		}
		act.Command.Reply <- api.CommandResult{Code: api.CodeUnavailable, Note: deskHeldDock}
	default:
		return false
	}
	return true
}

// deskSayFail ends a turn that waits (a seam: tests make turns with no
// connection).
var deskSayFail = func(y *api.Say, why string) { y.Fail(why) }

// deskHeldDockLine is deskHeldCommand on exactly the words runFromDock
// will run (dockLine lowers only the words it judges): a word the window
// would lower first and take is refused here, never the other way round.
func deskHeldDockLine(words []string, mode string) bool {
	return deskHeldCommand(words[0], words[1:], mode)
}

// deskHeldAsk is what the first Enter on a line typed while an agent had
// the desktop says, once the hold is over.
const deskHeldAsk = "This line was typed while an agent had the desktop. Check it, then press Enter again to send it."

// deskHeldMoved is what the first answer after the hold says when the
// selection moved during it.
const deskHeldMoved = "The selection moved while an agent had the desktop. Check what is selected, then answer again."

// deskHold is what happened in Den's window while an agent had the
// desktop: a line edited then asks before it is sent, and a selection
// moved then holds the next answer once.
type deskHold struct {
	edited  bool // the line was changed while held
	confirm bool // its first Enter after the hold asked; the next one sends
	moved   bool // the selection or the view moved while held
}

// Keys of handleKey while held. deskHoldMoves move around (the selection,
// the view, a scroll); deskHoldTypes write into the line; deskHoldRuns
// only deny or quit. Enter is judged by its line (deskHeldLine), and every
// other key handleKey handles waits. Keys handleKey doesn't handle go to
// the line only, which is typing. TestDeskHoldKnowsEveryKey reads
// handleKey's cases, so a new one fails until it is placed here.
var (
	deskHoldMoves = map[string]bool{
		"esc": true, "up": true, "down": true, "left": true, "right": true, "pgup": true, "pgdown": true,
		"shift+up": true, "shift+down": true, "tab": true, "shift+tab": true, "ctrl+e": true, "ctrl+o": true, "ctrl+t": true,
	}
	deskHoldTypes = map[string]bool{
		"shift+enter": true, "alt+enter": true, "ctrl+j": true, "ctrl+v": true,
		"1": true, "2": true, "3": true, "4": true, "5": true, "6": true, "7": true, "8": true, "9": true,
	}
	deskHoldRuns = map[string]bool{"ctrl+c": true, "ctrl+n": true}
	// deskHoldStays are the commands that run while held and leave the
	// view and its selection as they are; any other that runs then (a
	// view command) counts as a move.
	deskHoldStays = map[string]bool{"/desktop": true, "/quit": true, "/pause": true, "/stop": true, "/mode": true, "/api": true, "/god": true, "/usage": true, "/help": true}
	deskHoldWaits = map[string]bool{"ctrl+y": true, "ctrl+x": true}
)

// deskHeldKey takes a key while held: done with the key (it waits, or an
// Enter's command ran), or not done, and handleKey goes on with it as
// usual (moving, typing, denying).
func (m Model) deskHeldKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	k := msg.String()
	switch {
	case deskHoldMoves[k]:
		m.hold.moved = true
		return m, nil, false
	case k == "ctrl+n":
		// A deny stands, but the selection moves on to the next card:
		// the first answer after the hold is held once.
		m.hold.moved = true
		return m, nil, false
	case deskHoldTypes[k], deskHoldRuns[k]:
		return m, nil, false
	case k == "enter":
		words, ok := m.deskHeldLine(m.input.Value())
		if !ok {
			mm, cmd := m.say(deskHeldNote, true)
			return mm, cmd, true
		}
		m.input.SetValue("")
		m.fitInput()
		m.hold.edited, m.hold.confirm = false, false
		if !deskHoldStays[words[0]] {
			m.hold.moved = true // it may change the view, and so what ^Y answers
		}
		mm, cmd := m.command(strings.Join(words, " "))
		return mm, cmd, true
	case deskHoldWaits[k]:
		mm, cmd := m.say(deskHeldNote, true)
		return mm, cmd, true
	}
	return m, nil, false // the line's own keys: typing
}

// deskHeldLine is the command a line runs while held, in the words that
// were judged, or false: only a command of the list below, one command,
// plain text, NFKC first (as dockLine). An empty line (Enter focuses a
// terminal), a message and an answer all wait.
func (m Model) deskHeldLine(text string) ([]string, bool) {
	t := strings.TrimSpace(norm.NFKC.String(text))
	for _, r := range t {
		if unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return nil, false
		}
	}
	for _, bad := range []string{";", "&&", "||", "`", "$("} {
		if strings.Contains(t, bad) {
			return nil, false
		}
	}
	fields := strings.Fields(strings.ToLower(t))
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return nil, false
	}
	v, ok := dockVerbOf(fields[0])
	if !ok {
		return nil, false
	}
	for _, f := range fields[1:] {
		if _, again := dockVerbOf(f); again {
			return nil, false
		}
	}
	a := fields[1:]
	words := append([]string{v.name}, a...)
	if deskHeldCommand(v.name, a, m.mode.global) {
		return words, true
	}
	return nil, false
}

// deskHeldCommand is the list: a command with these words runs while held.
func deskHeldCommand(verb string, a []string, mode string) bool {
	switch verb {
	case "/deny", "/pause", "/stop", "/quit", "/help", "/permissions", "/inbox", "/overview", "/review", "/map":
		return len(a) == 0
	case "/desktop":
		return len(a) == 0 || len(a) == 1 && (a[0] == "stop" || a[0] == "pause")
	case "/god":
		return len(a) == 1 && a[0] == "off"
	case "/api":
		switch {
		case len(a) == 0, len(a) == 1 && (a[0] == "status" || a[0] == "list"):
			return true
		case len(a) == 1 && (a[0] == "off" || a[0] == "disconnect" || a[0] == "unpair" || a[0] == "lock"):
			return true
		case len(a) == 2 && a[0] == "control" && a[1] == "off":
			return true
		}
	case "/usage":
		return len(a) == 0 || len(a) == 1 && a[0] == "stop"
	case "/mode":
		if len(a) == 0 {
			return true
		}
		to, ok := permmode.Parse(a[0])
		return ok && len(a) == 1 && permmode.Lowers(mode, to)
	case "/fleet":
		return len(a) == 0 || len(a) == 1 && (a[0] == "project" || a[0] == "projects" || a[0] == "flat")
	case "/orchestrator":
		return len(a) == 0
	}
	return false
}

// deskHoldAfter notes what a message did to the line while held, and
// clears the marks the human's own acts settle: an emptied line, a change
// made after the hold.
func (m *Model) deskHoldAfter(held bool, line string, msg tea.Msg) {
	now := m.input.Value()
	switch {
	case strings.TrimSpace(now) == "":
		m.hold.edited, m.hold.confirm = false, false
	case now != line && held:
		m.hold.edited, m.hold.confirm = true, false
	case now != line:
		m.hold.confirm = false
	}
	if k, ok := msg.(tea.KeyPressMsg); ok && !held && deskHoldMoves[k.String()] {
		m.hold.moved = false // the human moved it since: theirs now
	}
}

// deskHeldSend is false, with the question, the first time a line edited
// while held is sent after the hold; true when it may go.
func (m *Model) deskHeldSend() bool {
	if !m.hold.edited {
		return true
	}
	if !m.hold.confirm {
		m.hold.confirm = true
		return false
	}
	m.hold.edited, m.hold.confirm = false, false
	return true
}

// deskHeldAnswer is false the first time an answer comes after a hold in
// which the selection moved: that answer is held once.
func (m *Model) deskHeldAnswer() bool {
	if !m.hold.moved {
		return true
	}
	m.hold.moved = false
	return false
}

// paused reports whether the mission is paused in any way: the human's
// /desktop pause, a your-turn card open, or the dock's overlay down.
func (dm *deskMission) paused() bool {
	return dm.humanPaused || dm.Paused || dm.overlayPaused || len(dm.cards) > 0
}

// deskQueued is an action the human approved while its mission was
// paused, and the reply that lets it run.
type deskQueued struct {
	p     deskPending
	reply func(bridge.Decision)
}

// deskFlushMsg is the desktop as read when a mission with queued actions
// went on.
type deskFlushMsg struct {
	mission string
	snap    desktop.Snapshot
	err     error
}

// deskFlush starts a fresh read of the desktop for each mission that went
// on with actions queued; an ended mission's are refused now.
func (m *Model) deskFlush() tea.Cmd {
	var cmds []tea.Cmd
	for id, dm := range m.desk.missions {
		switch {
		case len(dm.queued) == 0 || dm.flushing:
		case dm.Ended != "":
			for _, q := range dm.queued {
				q.reply(bridge.Decision{Behavior: "deny", Message: "Den: the mission ended before it could run"})
			}
			dm.queued = nil
		case !dm.paused():
			dm.flushing = true
			cmds = append(cmds, func() tea.Msg {
				snap, err := deskSnapshot(context.Background())
				return deskFlushMsg{mission: id, snap: snap, err: err}
			})
		}
	}
	return tea.Batch(cmds...)
}

// deskFlushed runs a mission's queued actions on a fresh read: each is
// decided again, as a new call would be, and refused if it no longer
// holds (its window gone, changed, or one of Den's own). The hold is back
// by now, since the mission went on.
func (m *Model) deskFlushed(msg deskFlushMsg) {
	dm := m.deskMission(msg.mission)
	if dm == nil {
		return
	}
	dm.flushing = false
	queued := dm.queued
	dm.queued = nil
	mission := m.deskGateMission(dm, &msg.snap)
	for _, q := range queued {
		deny := func(why string) { q.reply(bridge.Decision{Behavior: "deny", Message: "Den: " + why}) }
		switch {
		case dm.Ended != "":
			deny("the mission ended before it could run")
			continue
		case dm.paused():
			dm.queued = append(dm.queued, q) // paused again meanwhile: it waits on
			continue
		case msg.err != nil:
			deny("Den could not read the desktop, so it did not run")
			continue
		}
		if why := dm.refusal(); why != "" {
			deny(why)
			continue
		}
		v := desktop.Decide(q.p.tool, mustJSON(q.p.verdict.Args), mission, msg.snap)
		was := q.p.verdict.Target
		switch {
		case v.Kind == desktop.Deny:
			deny(v.Reason)
			continue
		case (v.Target == nil) != (was == nil) || v.Target != nil && v.Target.Address != was.Address:
			deny("the target window changed while the mission was paused")
			continue
		}
		a := desktop.Answer{Args: mustJSON(v.Args), Bounds: v.Bounds, Call: dm.issueCall(q.p.tool)}
		if q.p.pieces > 0 && v.Target != nil {
			a.Token = newToken()
			dm.tokens[a.Token] = deskToken{window: v.Target.Address, left: q.p.pieces, until: time.Now().Add(2 * time.Minute)}
		}
		data, _ := json.Marshal(a)
		q.reply(bridge.Decision{Behavior: "allow", Data: data})
	}
}

// deskHumanPaused is what an agent's call hears while the human paused it.
const deskHumanPaused = "paused: the human paused this mission in Den; Den goes on when they resume it"

// deskPause is /desktop pause and /desktop resume: while paused, every
// call of the agent but a look waits, and Den's window answers again.
func (m Model) deskPause(dm *deskMission, pause bool) (tea.Model, tea.Cmd) {
	switch {
	case pause && dm.humanPaused:
		return m.say("The desktop mission is paused already. /desktop resume goes on.", false)
	case pause:
		dm.humanPaused = true
		m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: "Desktop agent paused: its actions wait until /desktop resume.", at: time.Now()}))
		return m.say("Desktop agent paused: its actions wait until /desktop resume. Den's window answers again.", false)
	case !dm.humanPaused:
		return m.say("The desktop mission is not paused.", false)
	}
	if dm.buildChanged != "" {
		// C1: only on the checked build again; never wider instead. The
		// read runs off the update loop, and the task stays paused until
		// its answer is in (finishResumeCheck).
		if dm.resumeChecking {
			return m.say("Den is still checking Hyprland's build; the desktop agent stays paused until it knows.", false)
		}
		dm.resumeChecking = true
		id, gen := dm.ID, dm.buildGen
		check := func() tea.Msg {
			return deskResumeCheckMsg{mission: id, gen: gen, why: deskScopedBuild(context.Background())}
		}
		mm, flash := m.say("Checking Hyprland's build before the desktop agent goes on…", false)
		return mm, tea.Batch(check, flash)
	}
	return m.deskResumed(dm)
}

// deskResumeCheckMsg is a held task's build read, asked for by
// /desktop resume after a build change: why is "" on the checked build.
type deskResumeCheckMsg struct {
	mission string
	gen     int
	why     string
}

// finishResumeCheck resumes the task only if the build is the checked one
// again and it is still the same paused, held, live mission; otherwise it
// stays as it is.
func (m Model) finishResumeCheck(msg deskResumeCheckMsg) (tea.Model, tea.Cmd) {
	dm := m.deskMission(msg.mission)
	if dm == nil {
		return m, nil
	}
	dm.resumeChecking = false
	if dm.Ended != "" || !dm.humanPaused || dm.buildChanged == "" {
		return m, nil
	}
	if msg.gen != dm.buildGen {
		return m.say("The desktop agent stays paused: Hyprland changed again while Den checked. /desktop resume checks again; /desktop stop ends it.", true)
	}
	if msg.why != "" {
		return m.say("The desktop agent stays paused: "+sanitize.Line(msg.why)+". /desktop stop ends it.", true)
	}
	dm.buildChanged = ""
	return m.deskResumed(dm)
}

func (m Model) deskResumed(dm *deskMission) (tea.Model, tea.Cmd) {
	dm.humanPaused = false
	m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: "Desktop agent resumed.", at: time.Now()}))
	return m.say("Desktop agent resumed: Den's window takes no approving answer until you pause or end it.", false)
}
