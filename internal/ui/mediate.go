package ui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/peer"
	"github.com/GlacierHubAB/den/internal/peermsg"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// Den mediates every message to an agent that runs without asking (God
// mode's bypass agents). Each such agent starts with a --settings file
// Den writes in its own state (guardAgent): a UserPromptSubmit hook, `den
// hook agent-inbound`, that passes a peer envelope only when Den says it
// delivered exactly those bytes to that session (deliverTo records each
// one), and blocks it otherwise, Den not answering included. The file
// carries "disableAllHooks": false, which wins over the agent's user,
// project and local settings: without it a project's own
// .claude/settings.json could switch the hook off.
//
// A same-uid program can still change that file or the den binary; Den
// hashes both at launch, before every delivery and on its tick, and a
// change marks the agent unguarded: Den delivers nothing more to it.

// deliveredFor is how long a recorded delivery may be matched by the hook.
const deliveredFor = 2 * time.Minute

// agentGuard is one guarded agent's --settings file and what it relies on.
type agentGuard struct {
	settings    string // the --settings file Den wrote
	settingsSHA string
	den         string // the den binary its hook runs
	denSHA      string
	unguarded   string      // why Den no longer trusts the guard, "" while it holds
	launched    time.Time   // when Den handed the guard out for a new agent
	registered  bool        // the agent started, with the session id Den drew
	watch       *guardWatch // what Den watches once it runs (guard.go)
}

// mediationUI is the mediation's part of the model.
type mediationUI struct {
	sent    map[string]map[string]time.Time // session id → envelope hash → when Den delivered it
	guards  map[string]*agentGuard          // session id → its guard
	blocked map[string]time.Time            // session id → when Den last raised a blocked-message notice
}

// blockedNoteEvery coalesces blocked-message notices, per session.
const blockedNoteEvery = 10 * time.Minute

// sendAs is peer.SendAs; tests replace it.
var sendAs = peer.SendAs

// agentHook is one hook Den puts in an agent's --settings: for an event,
// a tool matcher ("" for none), den's own arguments (den and its socket
// are added, as absolute paths) and Claude Code's timeout for it.
type agentHook struct {
	Event   string // UserPromptSubmit, PreToolUse, …
	Matcher string
	Args    []string // after `den hook`, e.g. {"desktop-gate"}
	Timeout int      // seconds
}

// agentHooks are what an agent's --settings file holds: the inbound
// mediation hook when Inbound (God mode's guarded agents), and Extra hooks
// other parts of Den need (a desktop agent's tool gate). One file per
// agent, so one tamper check covers it all. Without Inbound, peer
// messages are refused outright: an agent with a Den-written file never
// takes them unmediated.
type agentHooks struct {
	Inbound bool
	Extra   []agentHook
	// Deny are permission rules the file denies, and Env its environment:
	// a deny rule holds even when a hook fails, which lets a tool run.
	Deny []string
	Env  map[string]string
}

// mediationSettings is the --settings JSON for an agent: its hooks, with
// den and its socket as absolute paths (never a PATH lookup or the agent's
// environment), and hooks never switched off by the agent's own user,
// project or local settings. With the inbound hook, every peer message
// reaches it.
func mediationSettings(den, sock string, h agentHooks) ([]byte, error) {
	if !filepath.IsAbs(den) || !filepath.IsAbs(sock) {
		return nil, errors.New("den and its socket must be absolute paths")
	}
	// Every hook states its own timeout: a hook that hangs lets its tool
	// run, so none is left to Claude Code's default.
	for _, x := range h.Extra {
		if x.Timeout <= 0 {
			return nil, errors.New("the " + x.Event + " hook has no timeout")
		}
	}
	hooks := map[string][]any{}
	add := func(x agentHook) {
		cmd := shellQuote(den) + " hook"
		for _, a := range x.Args {
			cmd += " " + shellQuote(a)
		}
		cmd += " --socket " + shellQuote(sock)
		entry := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": cmd, "timeout": x.Timeout}}}
		if x.Matcher != "" {
			entry["matcher"] = x.Matcher
		}
		hooks[x.Event] = append(hooks[x.Event], entry)
	}
	settings := map[string]any{"disableAllHooks": false, "crossSessionInbound": "refuse"}
	if h.Inbound {
		add(agentHook{Event: "UserPromptSubmit", Args: []string{"agent-inbound"}, Timeout: 10})
		settings["crossSessionInbound"] = "accept"
	}
	for _, x := range h.Extra {
		add(x)
	}
	if len(h.Deny) > 0 {
		settings["permissions"] = map[string]any{"deny": h.Deny}
	}
	if len(h.Env) > 0 {
		settings["env"] = h.Env
	}
	settings["hooks"] = hooks
	return json.Marshal(settings)
}

// shellQuote quotes s for sh, as Claude Code runs a hook's command.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// fileSHA is the SHA-256 of a file's bytes, "" when it cannot be read.
func fileSHA(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// guardAgent writes the --settings file for a new guarded agent, in Den's
// own state (never the project, 0600 in a 0700 folder), and records what
// it relies on. It returns the file's path for the agent's --settings.
func (m *Model) guardAgent(sessionID, den, sock string, h agentHooks) (string, error) {
	raw, err := mediationSettings(den, sock, h)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(stateDir(), "mediation")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	name := sha256.Sum256([]byte(sessionID))
	path := filepath.Join(dir, hex.EncodeToString(name[:16])+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return "", err
	}
	if m.med.guards == nil {
		m.med.guards = map[string]*agentGuard{}
	}
	m.med.guards[sessionID] = &agentGuard{settings: path, settingsSHA: fileSHA(path), den: den, denSHA: fileSHA(den)}
	return path, nil
}

// pruneDeliveries forgets deliveries older than deliveredFor: a prompt
// that never came (its session ended) must not stay.
func (m *Model) pruneDeliveries() {
	for sid, hs := range m.med.sent {
		for h, at := range hs {
			if time.Since(at) > deliveredFor {
				delete(hs, h)
			}
		}
		if len(hs) == 0 {
			delete(m.med.sent, sid)
		}
	}
}

// checkGuards hashes every guarded agent's --settings file and den binary
// again; a change marks it unguarded, loudly, once.
func (m *Model) checkGuards() {
	for sid, g := range m.med.guards {
		if g.unguarded != "" {
			continue
		}
		if g.unguarded = g.check(); g.unguarded == "" {
			continue
		}
		m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: "UNGUARDED: " + m.nameFor(sid) + ": " + g.unguarded + ". Den delivers no more messages to it; close it, or restart it from Den.", at: time.Now()}))
		m.flash, m.flashErr = "UNGUARDED: "+m.nameFor(sid)+" ("+g.unguarded+")", true
	}
}

// forgetGuard drops a session's guard and its settings file, once its agent
// has gone: nothing is delivered to it any more, and a later change to the
// den binary (an update) is no reason to call it unguarded.
func (m *Model) forgetGuard(sessionID string) {
	if g := m.med.guards[sessionID]; g != nil {
		os.Remove(g.settings)
		delete(m.med.guards, sessionID)
	}
}

// check says why the guard no longer holds, now: the settings file or the
// den binary is not what Den wrote and hashed. "" while it holds; why it
// stopped once it has.
func (g *agentGuard) check() string {
	switch {
	case g.unguarded != "":
		return g.unguarded
	case fileSHA(g.settings) != g.settingsSHA:
		return "its Den settings file changed"
	case fileSHA(g.den) != g.denSHA:
		return "the den binary its hook runs changed"
	}
	return ""
}

// unguarded is why Den no longer trusts a guarded session's guard, "" for
// a session Den does not guard or whose guard holds.
func (m Model) unguarded(sessionID string) string {
	if g := m.med.guards[sessionID]; g != nil {
		return g.unguarded
	}
	return ""
}

// deliverTo is every message Den sends to a session as another session
// (send_message, relays): the one place Den calls peer.SendAs. On the
// model's goroutine it checks the session's guard and records the
// envelope's hash for its hook; the write itself, which may block, is the
// send it returns, to run off that goroutine.
func (m *Model) deliverTo(sessionID, sock, from, fromName, body string) (send func() error, err error) {
	if m.med.guards[sessionID] != nil {
		m.checkGuards()
		if why := m.unguarded(sessionID); why != "" {
			return nil, errors.New(m.nameFor(sessionID) + " is unguarded (" + why + "): Den delivers nothing more to it")
		}
	}
	if m.med.sent == nil {
		m.med.sent = map[string]map[string]time.Time{}
	}
	if m.med.sent[sessionID] == nil {
		m.med.sent[sessionID] = map[string]time.Time{}
	}
	// The hook hashes the prompt Claude Code hands it, which is the
	// content SendAs writes, byte for byte: the same function both ways.
	m.med.sent[sessionID][peermsg.Hash(peer.Envelope(from, fromName, body))] = time.Now()
	return func() error { return sendAs(sock, from, fromName, body) }, nil
}

// handleAgentInbound answers a guarded agent's hook: may this envelope
// reach it? Only from that session's own hook process, and only for one
// Den delivered to it in the last deliveredFor, once.
func (m *Model) handleAgentInbound(r *bridge.Request) tea.Cmd {
	deny := func(why string) tea.Cmd {
		r.Reply(bridge.Decision{Behavior: "deny", Message: why})
		return nil
	}
	var in struct {
		SHA256 string `json:"sha256"`
		From   string `json:"from"` // as the envelope claims it, for the notice
	}
	if json.Unmarshal(r.Envelope.Payload, &in) != nil || len(in.SHA256) != 64 {
		return deny("den: unreadable request")
	}
	var a fleet.Agent
	found := false
	for _, x := range m.base {
		if x.Host == "" && x.Session.SessionID == r.SessionID && x.Session.Alive {
			a, found = x, true
		}
	}
	if !found || !modeCaller(r.PeerPID, a) {
		return deny("den: only the session's own hook may ask")
	}
	if g := m.med.guards[r.SessionID]; g != nil && g.watch != nil && g.watch.selfTest != "" && in.SHA256 == g.watch.selfTest {
		// Den's own self-test: the hook works; blocked, quietly.
		g.watch.selfOK = true
		return deny("Den's self-test")
	}
	at, ok := m.med.sent[r.SessionID][in.SHA256]
	if ok {
		delete(m.med.sent[r.SessionID], in.SHA256)
	}
	if !ok || time.Since(at) > deliveredFor {
		// Evidence of a try at an agent that acts without asking: a red
		// notice, once per agent per blockedNoteEvery.
		if m.med.blocked == nil {
			m.med.blocked = map[string]time.Time{}
		}
		if time.Since(m.med.blocked[r.SessionID]) >= blockedNoteEvery {
			m.med.blocked[r.SessionID] = time.Now()
			from := sanitize.Label(in.From, 120)
			if from == "" {
				from = "(none)"
			}
			m.store.AddGuardNote(r.SessionID, "Den blocked a message to "+a.Name+" that Den did not deliver", []string{
				"It claimed to be from " + from + ", as the message says; it is not checked.",
				a.Name + " acts without asking, so Den lets only its own deliveries reach it.",
			})
			m.refresh()
		}
		return deny("Den did not deliver this message; it was blocked")
	}
	r.Reply(bridge.Decision{Behavior: "allow"})
	return nil
}
