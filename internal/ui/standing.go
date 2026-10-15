package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/api"
	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/orch"
	"github.com/GlacierHubAB/den/internal/procs"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// Standing allowances: the human, with /always on a card from an app's
// turn, lets the orchestrator use one of Den's tools in that app's turns
// without asking again: update_mission, or send_message to one session
// (bound by its id, pid and start, not its name). They live in den-api.sock
// with the pairing and go to the orchestrator for each app turn; taking one
// back reaches the turn that is running at once.

// askingModes are the permission modes in which a session asks before it
// acts, so Den sees what a message makes it do: only then is a standing
// send_message to it offered or used.
var askingModes = map[string]bool{"default": true, "plan": true}

// standingOffer is the allowance /always on a card for this request would
// give, or why there is none.
func (m Model) standingOffer(ev orch.Event) (api.Rule, string) {
	if !orch.Allowable(ev.Tool) {
		return api.Rule{}, "this tool always asks"
	}
	r := api.Rule{Tool: ev.Tool, At: time.Now()}
	if ev.Tool != orch.SendMessageTool {
		return r, ""
	}
	var in struct {
		To string `json:"to"`
	}
	json.Unmarshal(ev.Input, &in)
	to, why := m.sendTarget(in.To)
	if why != "" {
		return api.Rule{}, why
	}
	if mode := m.modeOf(to.Session.SessionID); !askingModes[mode] {
		if mode == "" {
			mode = "unknown to Den (none seen from its own hooks in the last 15 minutes)"
		}
		return api.Rule{}, to.Session.Name + "'s permission mode is " + sanitize.Label(mode, 30) + ", so Den could not see what a message makes it do"
	}
	p, ok := sessionProc(to.Session.PID, to.Session.StartedAt, "")
	if !ok {
		return api.Rule{}, to.Session.Name + "'s process is not the one its session started with"
	}
	r.Peer, r.SessionID, r.PID, r.Start = to.Session.Name, to.Session.SessionID, p.PID, p.Start
	return r, ""
}

// standingLines say on the card what /always would give, in full.
func (m Model) standingLines(app string, r api.Rule, why string) []string {
	if why != "" {
		return []string{"No standing allowance is offered here: " + why + "."}
	}
	if r.Tool == orch.MissionTool {
		return []string{"/always: the orchestrator may report on missions in " + app + "'s turns without asking; Den still asks you to confirm done, failed and anything it would apply."}
	}
	var a fleet.Agent
	for _, x := range m.base {
		if x.Session.SessionID == r.SessionID {
			a = x
		}
	}
	return []string{
		"/always: " + app + " may have the orchestrator message " + r.Peer + " in its turns without asking you: any message it writes.",
		"That lets " + app + " give " + r.Peer + " any instruction: in effect " + app + " can make " + r.Peer + " do whatever " + r.Peer + " may do without asking you.",
		r.Peer + " works in " + sanitize.Label(a.Session.CWD, 120) + ", permission mode " + sanitize.Label(m.modeOf(r.SessionID), 30) + " (as the session reports it, through its own hooks); the allowance is for this session only, never another that takes its name.",
	}
}

// standingFor is what a pairing's allowances allow now: each still in
// force (control on, the pairing there), and send_message's only while the
// session it was given for is the one its name reaches, alive, the same
// process, and still in a mode that asks.
func (m Model) standingFor(pairing string) []string {
	if m.api.srv == nil {
		return nil
	}
	var out []string
	for _, r := range m.api.srv.Rules(pairing) {
		if r.Tool != orch.SendMessageTool {
			out = append(out, orch.AllowRule(r.Tool, ""))
			continue
		}
		if m.ruleSession(r) {
			out = append(out, orch.AllowRule(r.Tool, r.Peer))
		}
	}
	return out
}

// ruleSession reports whether r's session is the one its peer name reaches
// now, the same process, in a mode that asks.
func (m Model) ruleSession(r api.Rule) bool {
	to, why := m.sendTarget(r.Peer)
	if why != "" || to.Session.SessionID != r.SessionID || !askingModes[m.modeOf(r.SessionID)] {
		return false
	}
	p, ok := sessionProc(to.Session.PID, to.Session.StartedAt, "")
	return ok && p.PID == r.PID && p.Start == r.Start
}

// pushStanding gives the turn that is running its pairing's allowances as
// they are now: after /api disallow, control off, an unpair or a revoke,
// its next request asks again.
func (m *Model) pushStanding() {
	if y := m.ctl.turn; y != nil && m.ctl.sent && m.orch.proc != nil {
		m.orch.proc.SetStanding(m.standingFor(y.Pairing)...)
	}
}

// noteStanding records in Den's chat what a standing allowance let
// through, the whole message a press away, and for send_message which
// session Den may deliver it to.
func (m *Model) noteStanding(ev orch.Event) {
	who := "an app"
	if y := m.ctl.turn; y != nil {
		who = appName(y.Exe)
	}
	e := chatEntry{role: "note", text: "allowed by your standing allowance for " + who + ": " + firstLineOf(orch.Describe(ev.Tool, ev.Input)), at: time.Now()}
	if ev.Tool == orch.SendMessageTool {
		var in struct {
			To      string `json:"to"`
			Message string `json:"message"`
		}
		json.Unmarshal(ev.Input, &in)
		e.full = sanitize.Text(in.Message)
	}
	m.logEntry(m.addChat(e))
}

// standingCommand is /api disallow <n>|all and /api control on|off.
func (m Model) standingCommand(sub string, args []string) (tea.Model, tea.Cmd) {
	if m.api.srv == nil {
		return m.say("The API is not running.", true)
	}
	if sub == "control" {
		if len(args) == 0 || (args[0] != "on" && args[0] != "off") {
			return m.say("/api control on|off", true)
		}
		m.api.srv.SetControl(args[0] == "on")
		m.pushStanding()
		if args[0] == "off" {
			return m.say("Control off: paired apps' turns are refused, and no standing allowance applies, until /api control on.", false)
		}
		return m.say("Control on.", false)
	}
	if len(args) == 0 {
		return m.say("/api disallow <number from /api>|all", true)
	}
	n := 0
	if args[0] != "all" {
		var err error
		if n, err = strconv.Atoi(args[0]); err != nil || n < 1 {
			return m.say("/api disallow <number from /api>|all", true)
		}
	}
	if !m.api.srv.Disallow(n) {
		return m.say("No such standing allowance; /api lists them.", true)
	}
	m.pushStanding()
	return m.say("Standing allowance taken back.", false)
}

// standingReport is /api's list of standing allowances.
func (m Model) standingReport() string {
	var b strings.Builder
	for _, r := range m.api.srv.AllRules() {
		what := "update_mission"
		if r.Tool == orch.SendMessageTool {
			what = "send_message to " + sanitize.Label(r.Peer, 60) + " (this session only)"
		}
		b.WriteString("\nstanding " + strconv.Itoa(r.N) + ": " + appName(r.Exe) + " · " + what + " · since " + r.At.Format("15:04") + " · /api disallow " + strconv.Itoa(r.N))
	}
	return b.String()
}

// modeSeen is a permission mode a session's own hook reported, and when.
type modeSeen struct {
	mode string
	at   time.Time
}

// modeFresh is how long a reported mode stands: the human can change it in
// the agent's terminal (Shift+Tab) without a hook event saying so.
const modeFresh = 15 * time.Minute

// modeOf is a session's permission mode as its own hooks last said it, ""
// when Den has none from the last modeFresh.
func (m Model) modeOf(sid string) string {
	seen, ok := m.ctl.modes[sid]
	if !ok || time.Since(seen.at) > modeFresh {
		return ""
	}
	return seen.mode
}

// noteMode records the permission mode a hook request reports, only when
// it comes from that session's own hook: Den's binary, started by the
// session's process (the one that started with the session) or a shell it
// started. Any other process could claim any mode for any session. After a
// message a standing allowance delivered, a mode that does not ask ends
// the allowances for that session, loudly.
func (m *Model) noteMode(r *bridge.Request) {
	if r.SessionID == "" || r.Input.PermissionMode == "" || r.Host != "" {
		return
	}
	var a fleet.Agent
	found := false
	for _, x := range m.base {
		if x.Host == "" && x.Session.SessionID == r.SessionID && x.Session.Alive {
			a, found = x, true
		}
	}
	if !found || !modeCaller(r.PeerPID, a) {
		return
	}
	if m.ctl.modes == nil {
		m.ctl.modes = map[string]modeSeen{}
	}
	m.ctl.modes[r.SessionID] = modeSeen{mode: r.Input.PermissionMode, at: time.Now()}
	if !m.ctl.watch[r.SessionID] || r.Event != "UserPromptSubmit" {
		return
	}
	delete(m.ctl.watch, r.SessionID)
	if askingModes[r.Input.PermissionMode] || m.api.srv == nil {
		return
	}
	if n := m.api.srv.RemoveRulesFor(r.SessionID); n > 0 {
		m.pushStanding()
		m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: "den-api.sock: a message sent under a standing allowance reached " + sanitize.Label(a.Session.Name, 60) + ", which runs in " + sanitize.Label(r.Input.PermissionMode, 30) + " and does not ask before it acts. Den removed the allowance; the message was delivered and cannot be taken back.", at: time.Now()}))
		m.flash, m.flashErr = "A standing allowance reached "+sanitize.Label(a.Session.Name, 40)+" in "+sanitize.Label(r.Input.PermissionMode, 20)+": removed. See the chat.", true
	}
}

// modeCaller reports whether pid is a's own hook process; tests replace
// it.
var modeCaller = func(pid int, a fleet.Agent) bool {
	p, ok := sessionProc(a.Session.PID, a.Session.StartedAt, "")
	if !ok {
		return false
	}
	exe, _ := os.Executable()
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	return procs.ChildOf(pid, a.Session.PID, strconv.FormatUint(p.Start, 10), exe)
}
