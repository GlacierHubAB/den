package ui

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/api"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/orch"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// Control: a paired app speaks to the orchestrator (den-api.sock's
// orch.say). Den pairs only on the human's ^Y, runs the orchestrator in
// control mode while a pairing lasts, sends an app's turn only when no
// other turn is in flight and holds every other turn until its result,
// and puts every tool the orchestrator wants in that turn on an inbox card
// for the human. The app is told only that something waits in Den.

// Control's bounds on an app's turn; tests shorten them.
var (
	appTurnMax     = 10 * time.Minute
	appTurnSilence = 120 * time.Second
)

// originApp is a paired app's turn (orch.say).
const originApp = "app"

// controlState is control's part of the model.
type controlState struct {
	code      []string               // the pairing code on Den's screen, nil for none
	codeUntil time.Time              // when it stops being live
	pairs     map[*inbox.Item]string // open pairing cards → pairing id
	asks      map[*inbox.Item]string // open tool cards → permission request id
	turn      *api.Say               // the app's turn, queued or running
	sent      bool                   // it went to the orchestrator
	ended     bool                   // the app was told it ended; Den waits for its result
	started   time.Time
	last      time.Time // when the orchestrator last said anything in it
	held      []heldTurn
	noControl string // why the orchestrator runs without control, "" when it does or need not
	// modes is each local session's last permission mode, from its own
	// hooks; always the standing allowance /always on a tool card would
	// give; watch the sessions a message reached under a standing
	// allowance, whose next prompt's mode Den checks.
	modes  map[string]modeSeen
	always map[*inbox.Item]api.Rule
	watch  map[string]bool
}

// heldTurn is a turn held while an app's turn runs; report is an agent's
// report Den composed already, sent as it is.
type heldTurn struct {
	prompt, origin string
	report         bool
}

// orchVersion is the Claude Code release a command runs; tests replace it.
var orchVersion = orch.LaneVersion

// unpairConsole unpairs the console, if one is paired; tests replace it.
var unpairConsole = func(m *Model, why string) { m.api.srv.UnpairConsole(why) }

// controlWanted reports whether the orchestrator should run in control
// mode: a pairing is confirmed, or a console is paired (its orch.say is an
// app's turn too).
func (m Model) controlWanted() bool {
	if m.api.srv == nil {
		return false
	}
	_, console := m.api.srv.CurrentConsole()
	return m.api.srv.Paired() || console
}

// appName is a program's name as Den shows it: the base of the executable
// it read.
// appName is how the human sees the app: the executable's name Den read,
// or that Den cannot identify it (api.ExeLabel); never a path's ".".
func appName(exe string) string { return sanitize.Label(api.ExeLabel(exe), 40) }

// sayPreface is the fixed text before an app's words: who sent them and
// what follows from that. It names only the program Den read.
func sayPreface(exe string) string {
	return "Den: the human sends this turn through " + appName(exe) + ", an app on this machine they paired with Den, not through Den's own chat. Every tool you use in this turn waits for the human's approval in Den; the app sees only your text."
}

// controlCommand is /api pair and /api unpair.
func (m Model) controlCommand(sub string) (tea.Model, tea.Cmd) {
	if m.api.srv == nil || m.api.srv.Paused() {
		return m.say("The API is off: /api on turns it on first.", true)
	}
	if sub == "unpair" {
		console := m.api.srv.UnpairConsole("")
		if len(m.api.srv.Pairings()) == 0 && console == 0 {
			return m.say("No app is paired.", false)
		}
		m.api.srv.Unpair("", "")
		m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: "den-api.sock: every pairing was revoked in Den; apps must pair again for control", at: time.Now()}))
		m.restartForControl()
		return m.say("Unpaired.", false)
	}
	if m.api.srv.ControlState() == api.ControlOff {
		return m.say("Control is off: /api control on first.", true)
	}
	m.ctl.code = m.api.srv.StartPairing()
	m.ctl.codeUntil = time.Now().Add(2 * time.Minute)
	return m.say("Pairing code shown above for two minutes. Type it only into the app's own pairing window.", false)
}

// codeLine is the pairing code line Den shows while a code is live; ""
// for none. It is never logged.
func (m Model) codeLine() string {
	if m.ctl.code == nil || m.api.srv == nil {
		return ""
	}
	left := time.Until(m.ctl.codeUntil).Round(time.Second)
	if _, live := m.api.srv.CodeLive(); !live || left <= 0 {
		return ""
	}
	return "Pairing code (" + left.String() + "): " + strings.Join(m.ctl.code, " ") + " · Type this only into the app's own pairing window."
}

// applyControlAct handles control's acts from den-api.sock.
func (m *Model) applyControlAct(act api.Act) tea.Cmd {
	if m.ctl.pairs == nil {
		m.ctl.pairs = map[*inbox.Item]string{}
	}
	if act.CodeGone {
		m.ctl.code = nil
	}
	if p := act.Pair; p != nil {
		started := "start unknown"
		if !p.Started.IsZero() {
			started = "started " + p.Started.Local().Format("15:04:05")
		}
		parent := p.Parent
		if parent == "" {
			parent = "unknown"
		}
		title := "Pair " + appName(p.Exe) + " (pid " + strconv.Itoa(p.PID) + ", " + started + ") for dock control?"
		body := []string{
			"The program below sent the pairing code you showed. Pair it only if you just typed the code into the app's own pairing window.",
			"program: " + sanitize.Label(api.ExeShown(p.Exe), 160),
			"pid " + strconv.Itoa(p.PID) + " · " + started + " · parent " + sanitize.Label(parent, 40),
			"command line, as the program reports it (it can be spoofed): " + sanitize.Label(p.Cmdline, 200),
			"Paired, it may send turns to your orchestrator; every tool those turns want waits for you here. /api unpair revokes it.",
		}
		it := m.store.AddPair(title, body)
		m.ctl.pairs[it] = p.ID
		m.refresh()
	}
	if id := act.PairDone; id != "" {
		for it, pid := range m.ctl.pairs {
			if pid == id && it.State != inbox.Resolved {
				m.store.Settle(it, false, "no longer waiting")
				delete(m.ctl.pairs, it)
			}
		}
		m.refresh()
	}
	if act.Paired {
		m.restartForControl()
		m.pushStanding() // a revoked pairing's allowances end now
	}
	if y := act.Say; y != nil {
		return m.startAppTurn(y)
	}
	return nil
}

// decidePair is the human's ^Y or ^N on a pairing card, after the whole
// card was shown (answer's readGate).
func (m Model) decidePair(it *inbox.Item, verb string) (tea.Model, tea.Cmd) {
	id, ok := m.ctl.pairs[it]
	if !ok || m.api.srv == nil {
		m.store.Settle(it, false, "stale; not paired")
		m.refresh()
		return m.say("That pairing is stale; nothing was paired.", true)
	}
	if verb == "always" {
		return m.say("A pairing is answered once: ^Y pairs, ^N refuses.", true)
	}
	delete(m.ctl.pairs, it)
	m.api.srv.PairAnswered(id, verb == "allow")
	m.store.Settle(it, verb == "allow", map[bool]string{true: "paired", false: "refused"}[verb == "allow"])
	m.refresh()
	if verb != "allow" {
		return m.say("Not paired.", false)
	}
	m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: "den-api.sock: paired with " + appName(m.pairExe(id)) + " for control; /api unpair revokes it", at: time.Now()}))
	return m.say("Paired.", false)
}

func (m Model) pairExe(id string) string {
	for _, p := range m.api.srv.Pairings() {
		if p.ID == id {
			return p.Exe
		}
	}
	return "an app"
}

// restartForControl ends an idle orchestrator whose mode no longer fits
// (paired but not in control mode, or the other way); the next turn starts
// it in the right one. A turn in progress is never cut short: the tick and
// the turn's result try again.
func (m *Model) restartForControl() {
	// On a release control cannot run on (noControl), the orchestrator
	// stays as it is: restarting would not help.
	want := m.controlWanted() && m.ctl.noControl == ""
	if m.orch.proc == nil || m.orch.control == want {
		return
	}
	if m.orch.thinking || len(m.orch.origins) > 0 || len(m.orch.unanswered) > 0 || len(m.orch.pending) > 0 {
		return
	}
	proc := m.orch.proc
	m.orch.proc = nil
	go proc.Close()
	m.orch.started = false
	m.orch.status = ""
	if m.ctl.turn != nil && !m.ctl.sent {
		m.ctl.turn.Status("Starting the orchestrator for the app…")
	}
}

// startAppTurn takes an app's turn: sent at once when the orchestrator is
// in control mode with no turn in flight, else when it is.
func (m *Model) startAppTurn(y *api.Say) tea.Cmd {
	if m.ctl.turn != nil {
		y.Fail("an app's turn is running")
		return nil
	}
	m.ctl.turn, m.ctl.sent, m.ctl.ended = y, false, false
	m.ctl.started, m.ctl.last = time.Now(), time.Now()
	return tea.Batch(m.tryAppTurn(), waitAppCancel(y))
}

// appCancelMsg says an app's connection went: its turn stops.
type appCancelMsg struct{ turn string }

func waitAppCancel(y *api.Say) tea.Cmd {
	return func() tea.Msg {
		<-y.Cancelled()
		return appCancelMsg{turn: y.Turn}
	}
}

// tryAppTurn sends the app's turn if it can go now, or starts the
// orchestrator in control mode for it.
func (m *Model) tryAppTurn() tea.Cmd {
	y := m.ctl.turn
	if y == nil || m.ctl.sent {
		return nil
	}
	if !m.controlWanted() {
		m.endAppTurn("the app is not paired")
		return nil
	}
	if m.ctl.noControl != "" {
		m.endAppTurn(m.ctl.noControl)
		return nil
	}
	if m.orch.proc != nil && !m.orch.control {
		y.Status("Starting the orchestrator for the app…")
		m.restartForControl()
		if m.orch.proc != nil {
			return nil // a turn is in flight; its result tries again
		}
	}
	if m.orch.proc == nil {
		if m.ctl.noControl != "" {
			m.endAppTurn(m.ctl.noControl)
			return nil
		}
		if m.orch.starting {
			return nil
		}
		y.Status("Starting the orchestrator…")
		m.orch.starting = true
		m.orch.status = "starting…"
		return startOrchestrator(m.cfg, m.resumable(), m.guide.active, true)
	}
	if !m.orch.started || len(m.orch.origins) > 0 || m.orch.thinking {
		y.Status("Waiting for the orchestrator to finish another turn…")
		return nil
	}
	prompt := sayPreface(y.Exe) + "\n\n" + y.Text
	content := orch.ComposeWith(m.roster(), m.templateInfos(), prompt)
	if err := m.orch.proc.SendApp(y.Turn, content, m.standingFor(y.Pairing)...); err != nil {
		m.endAppTurn("Den could not send it: " + sanitize.Label(err.Error(), 120))
		return nil
	}
	m.ctl.sent = true
	m.ctl.last = time.Now()
	m.orch.origins = append(m.orch.origins, originApp)
	m.orch.thinking = true
	m.orch.status = "thinking… (" + appName(y.Exe) + ")"
	m.orch.lastEvent = time.Now()
	// "app", not "you": the app's words, never replayed as the human's.
	m.logEntry(m.addChat(chatEntry{role: "app", text: appName(y.Exe) + ": " + y.Text, at: time.Now()}))
	y.Status("Writing a reply…")
	return nil
}

// holdTurn reports whether a turn must wait for the app's turn, and holds
// it; the human's own words also stop that turn.
func (m *Model) holdTurn(prompt, origin string) bool {
	if m.ctl.turn == nil || !m.ctl.sent {
		return false
	}
	m.ctl.held = append(m.ctl.held, heldTurn{prompt: prompt, origin: origin})
	if origin == originHuman && !m.ctl.ended {
		// The orchestrator is stopped first: the app is never told its
		// turn is over while the orchestrator still runs it.
		if m.orch.proc != nil {
			m.orch.proc.Interrupt()
		}
		m.ctl.turn.Fail("the human wrote in Den; their message goes first")
		m.ctl.ended = true
	}
	return true
}

// holdReport holds an admitted agent's report while an app's turn runs,
// like any other turn: the orchestrator answers it on its own turn, never
// inside the app's.
func (m *Model) holdReport(content string) bool {
	if m.ctl.turn == nil || !m.ctl.sent {
		return false
	}
	m.ctl.held = append(m.ctl.held, heldTurn{prompt: content, origin: originReport, report: true})
	return true
}

// endAppTurn tells the app its turn ended (why, "" for done). Den still
// waits for the turn's result before anything else is sent, if it was
// sent.
func (m *Model) endAppTurn(why string) {
	y := m.ctl.turn
	if y == nil {
		return
	}
	if !m.ctl.ended {
		appTurnEndHook()
		if why == "" {
			y.Status("")
			y.Done()
		} else {
			y.Fail(why)
		}
		m.ctl.ended = true
	}
	if !m.ctl.sent {
		m.ctl.turn = nil
	}
}

// appTurnOver clears the app's turn once its result came, settles its open
// tool cards and sends the turns held behind it.
func (m *Model) appTurnOver() tea.Cmd {
	m.endAppTurn("")
	m.ctl.turn, m.ctl.sent, m.ctl.ended = nil, false, false
	for it := range m.ctl.asks {
		if it.State != inbox.Resolved {
			m.store.Settle(it, false, "the app's turn ended")
		}
		delete(m.ctl.asks, it)
		delete(m.ctl.always, it)
	}
	held := m.ctl.held
	m.ctl.held = nil
	var cmds []tea.Cmd
	for _, h := range held {
		if h.report {
			if m.orch.proc != nil {
				cmds = append(cmds, m.sendReportTurn(h.prompt))
			}
			continue // a report to a process that is gone is not owed
		}
		if m.orch.proc == nil {
			m.orch.pending = append(m.orch.pending, h.prompt)
			m.orch.pendingOrigins = append(m.orch.pendingOrigins, h.origin)
			continue
		}
		cmds = append(cmds, m.sendToOrchestrator(h.prompt, h.origin))
	}
	m.restartForControl()
	return tea.Batch(cmds...)
}

// controlEvent handles the orchestrator's events that concern control; it
// returns a command, and whether the event was control's alone.
func (m *Model) controlEvent(ev orch.Event) (tea.Cmd, bool) {
	y := m.ctl.turn
	switch ev.Kind {
	case orch.Delta:
		if y != nil && m.ctl.sent && !m.ctl.ended {
			y.Delta(ev.Text)
			m.ctl.last = time.Now()
		}
	case orch.PermissionAsk:
		m.ctl.last = time.Now()
		return m.askControlTool(ev), true
	case orch.PermissionStanding:
		m.ctl.last = time.Now()
		m.noteStanding(ev)
		return nil, true
	case orch.PermissionExpired:
		for it, id := range m.ctl.asks {
			if id == ev.RequestID {
				m.store.Settle(it, false, "no answer in a minute; denied")
				delete(m.ctl.asks, it)
				delete(m.ctl.always, it)
			}
		}
		m.refresh()
		return nil, true
	case orch.Backstop:
		if m.orch.control && m.api.srv != nil {
			if y != nil {
				m.endAppTurn("Den stopped the orchestrator: " + sanitize.Label(ev.Text, 120))
			}
			m.api.srv.Unpair("", "Den's guard stopped the orchestrator while it ran for a paired app or the console ("+sanitize.Label(ev.Text, 120)+"); every pairing is revoked, and the console unpaired")
			// The console's turns are app turns too: it is unpaired with
			// them, whoever's turn it was.
			unpairConsole(m, "Den's guard stopped the orchestrator: the console is unpaired; pair again with den pair")
		}
	case orch.Exited:
		if y != nil && m.ctl.sent {
			m.endAppTurn("the orchestrator stopped")
			m.ctl.sent = false
			m.ctl.turn = nil
		}
	}
	return nil, false
}

// askControlTool puts a tool the orchestrator wants in an app's turn on an
// inbox card, whole, and tells the app that something waits in Den.
func (m *Model) askControlTool(ev orch.Event) tea.Cmd {
	if m.ctl.asks == nil {
		m.ctl.asks = map[*inbox.Item]string{}
	}
	who := "an app"
	if m.ctl.turn != nil {
		who = appName(m.ctl.turn.Exe)
	}
	title := "Requested from " + who + ": " + firstLineOf(orch.Describe(ev.Tool, ev.Input))
	body := []string{
		"In a turn " + who + " sent, the orchestrator wants this tool. Nothing runs unless you allow it; it is denied in a minute without an answer.",
		"tool: " + sanitize.Label(ev.Tool, 80),
	}
	if to := m.sendTargetLine(ev); to != "" {
		body = append(body, to)
	}
	body = append(body, "──── its input, in full ────")
	var pretty any
	raw := []byte(ev.Input)
	if json.Unmarshal(raw, &pretty) == nil {
		raw, _ = json.MarshalIndent(pretty, "", "  ")
	}
	for _, l := range strings.Split(string(raw), "\n") {
		body = append(body, sanitize.Line(l))
	}
	body = append(body, "──── end ────")
	rule, why := m.standingOffer(ev)
	if orch.Allowable(ev.Tool) {
		body = append(body, m.standingLines(who, rule, why)...)
	}
	it := m.store.AddControlAsk(title, body)
	m.ctl.asks[it] = ev.RequestID
	if why == "" && m.ctl.turn != nil {
		if m.ctl.always == nil {
			m.ctl.always = map[*inbox.Item]api.Rule{}
		}
		rule.From = title
		m.ctl.always[it] = rule
	}
	if y := m.ctl.turn; y != nil && !m.ctl.ended {
		y.NeedsApproval(title, it.ID)
	}
	m.refresh()
	return nil
}

// decideControlTool is the human's ^Y or ^N on a tool card from an app's
// turn, after the whole card was shown. It is answered once.
func (m Model) decideControlTool(it *inbox.Item, verb string) (tea.Model, tea.Cmd) {
	id, ok := m.ctl.asks[it]
	if !ok || m.orch.proc == nil {
		m.store.Settle(it, false, "stale; nothing ran")
		m.refresh()
		return m.say("That request is stale; nothing ran.", true)
	}
	rule, offered := m.ctl.always[it]
	if verb == "always" && !offered {
		return m.say("No standing allowance for this one (the card says why): ^Y allows it once, ^N denies it.", true)
	}
	delete(m.ctl.asks, it)
	delete(m.ctl.always, it)
	allow := verb == "allow" || verb == "always"
	if !m.orch.proc.AnswerPermission(id, allow, "the human declined in Den") {
		m.store.Settle(it, false, "already answered; nothing more ran")
		m.refresh()
		return m.say("That request was already answered.", true)
	}
	if verb == "always" && m.ctl.turn != nil && m.api.srv != nil {
		if err := m.api.srv.AddRule(m.ctl.turn.Pairing, rule); err != nil {
			m.store.Settle(it, true, "allowed once; "+err.Error())
			m.refresh()
			return m.say("Allowed once; "+err.Error()+".", true)
		}
		m.pushStanding()
		m.store.Settle(it, true, "allowed, and from now on for this app")
		m.refresh()
		m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: "den-api.sock: a standing allowance for " + appName(m.ctl.turn.Exe) + ": " + firstLineOf(rule.From) + " · /api lists it, /api disallow takes it back", at: time.Now()}))
		return m.say("Allowed, and from now on for this app.", false)
	}
	m.store.Settle(it, allow, map[bool]string{true: "allowed", false: "denied"}[allow])
	m.refresh()
	return m.say(map[bool]string{true: "Allowed.", false: "Denied."}[allow], false)
}

// controlTick enforces an app's turn's bounds and retries a send or a
// restart that had to wait.
func (m *Model) controlTick(now time.Time) tea.Cmd {
	m.restartForControl()
	if m.ctl.turn == nil {
		return nil
	}
	if !m.ctl.sent {
		return m.tryAppTurn()
	}
	if m.ctl.ended {
		return nil
	}
	why := ""
	switch {
	case now.Sub(m.ctl.started) > appTurnMax:
		why = "the turn ran over ten minutes"
	case now.Sub(m.ctl.last) > appTurnSilence:
		why = "the orchestrator said nothing for two minutes"
	}
	if why != "" {
		// Stopped first, then told: the app never hears its turn is over
		// while the orchestrator still runs it.
		if m.orch.proc != nil {
			interruptHook()
			m.orch.proc.Interrupt()
		}
		m.endAppTurn(why)
	}
	return nil
}

// interruptHook and appTurnEndHook let a test see the order in which Den
// stops the orchestrator and ends an app's stream; they do nothing.
var interruptHook, appTurnEndHook = func() {}, func() {}

// controlStart checks, before an orchestrator starts in control mode,
// that its Claude Code release is one send_message was checked on.
func controlStart(cmd []string, version func(context.Context, []string) (string, error)) (bool, string) {
	if len(cmd) == 0 {
		cmd = []string{"claude"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	v, err := version(ctx, cmd)
	if err != nil {
		return false, "Den could not read the Claude Code release, so control stays off"
	}
	if !orch.SendVerifiedVersion(v) {
		return false, "control needs a Claude Code release Den has checked it on (" + strings.Join(orch.SendVerified, ", ") + "); this one is " + sanitize.Label(v, 20)
	}
	return true, ""
}
