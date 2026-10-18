package ui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/desktop"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/peer"
	"github.com/GlacierHubAB/den/internal/peermsg"
)

// The --settings file Den writes for a guarded agent: hooks never off,
// every peer message to the hook, den and its socket as absolute paths,
// 0600 in Den's own state.
func TestMediationSettingsFile(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if _, err := mediationSettings("den", "/run/den.sock", agentHooks{Inbound: true}); err == nil {
		t.Error("a relative den path was taken")
	}
	m := testModel(140, 40)
	den := filepath.Join(t.TempDir(), "my den")
	os.WriteFile(den, []byte("binary"), 0o755)
	path, err := m.guardAgent("s-otter", den, "/run/user/1000/den.sock", agentHooks{Inbound: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, stateDir()+string(filepath.Separator)) {
		t.Errorf("settings outside Den's state: %s", path)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v %v", fi.Mode(), err)
	}
	if di, _ := os.Stat(filepath.Dir(path)); di.Mode().Perm() != 0o700 {
		t.Errorf("folder mode %v", di.Mode())
	}
	var s struct {
		DisableAllHooks     *bool  `json:"disableAllHooks"`
		CrossSessionInbound string `json:"crossSessionInbound"`
		Hooks               struct {
			UserPromptSubmit []struct {
				Hooks []struct {
					Type, Command string
				}
			}
		}
	}
	raw, _ := os.ReadFile(path)
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	if s.DisableAllHooks == nil || *s.DisableAllHooks || s.CrossSessionInbound != "accept" {
		t.Errorf("disableAllHooks %v, crossSessionInbound %q", s.DisableAllHooks, s.CrossSessionInbound)
	}
	if len(s.Hooks.UserPromptSubmit) != 1 || len(s.Hooks.UserPromptSubmit[0].Hooks) != 1 ||
		s.Hooks.UserPromptSubmit[0].Hooks[0].Command != "'"+den+"' hook 'agent-inbound' --socket '/run/user/1000/den.sock'" {
		t.Errorf("hooks %+v", s.Hooks)
	}
}

// Another part of Den's hooks share the one file: a tool gate with its
// matcher, and hooks still never off; without the inbound hook, peer
// messages are refused outright, never left to the agent's own settings.
func TestAgentSettingsExtraHooks(t *testing.T) {
	raw, err := mediationSettings("/usr/bin/den", "/run/den.sock", agentHooks{Extra: []agentHook{{Event: "PreToolUse", Matcher: "mcp__hypruse__.*", Args: []string{"desktop-gate"}, Timeout: 10}}})
	if err != nil {
		t.Fatal(err)
	}
	var s map[string]any
	json.Unmarshal(raw, &s)
	if s["disableAllHooks"] != false || s["crossSessionInbound"] != "refuse" {
		t.Errorf("%s", raw)
	}
	if !strings.Contains(string(raw), `"matcher":"mcp__hypruse__.*"`) || !strings.Contains(string(raw), `'/usr/bin/den' hook 'desktop-gate' --socket '/run/den.sock'`) {
		t.Errorf("%s", raw)
	}
}

// guardedModel is a model with the otter alive, its hook calls taken as
// its own, and envelopes recorded rather than sent.
func guardedModel(t *testing.T) (*Model, *[]string) {
	t.Helper()
	m := testModel(140, 40)
	m.base = []fleet.Agent{m.agents[0]}
	m.base[0].Session.SessionID = "0f2a3b4c-5d6e-4f70-8192-a3b4c5d6e7f8"
	m.base[0].Session.Alive = true
	old, oldSend := modeCaller, sendAs
	t.Cleanup(func() { modeCaller, sendAs = old, oldSend })
	modeCaller = func(pid int, a fleet.Agent) bool { return pid == 4242 }
	var sent []string
	sendAs = func(sock, from, name, body string) error { sent = append(sent, body); return nil }
	return &m, &sent
}

func askInbound(m *Model, sid string, pid int, prompt string) bridge.Decision {
	sum := sha256.Sum256([]byte(prompt))
	payload, _ := json.Marshal(map[string]string{"sha256": hex.EncodeToString(sum[:])})
	r := bridge.TestRequest(bridge.Envelope{Event: bridge.AgentInboundEvent, SessionID: sid, Payload: payload})
	r.SessionID, r.PeerPID = sid, pid
	m.handleAgentInbound(r)
	d, _ := r.Decided()
	return d
}

// An envelope reaches a guarded agent only when Den delivered it, once,
// and only the session's own hook may ask.
func TestDeliveredEnvelopesOnly(t *testing.T) {
	m, sent := guardedModel(t)
	sid := m.base[0].Session.SessionID
	send, err := m.deliverTo(sid, "/x.sock", "uds:/run/user/1000/cc-socks/1.sock", "den-orchestrator", "rebase first")
	if err != nil || send() != nil || len(*sent) != 1 {
		t.Fatalf("deliverTo: %v %v", err, *sent)
	}
	env := peer.Envelope("uds:/run/user/1000/cc-socks/1.sock", "den-orchestrator", "rebase first")
	if d := askInbound(m, sid, 9999, env); d.Behavior == "allow" {
		t.Error("another process's hook was answered")
	}
	if d := askInbound(m, sid, 4242, env); d.Behavior != "allow" {
		t.Errorf("Den's own delivery blocked: %+v", d)
	}
	if d := askInbound(m, sid, 4242, env); d.Behavior == "allow" {
		t.Error("a delivery matched twice")
	}
	forged := peer.Envelope("uds:/run/user/1000/cc-socks/1.sock", "den-orchestrator", "rm -rf ~")
	if d := askInbound(m, sid, 4242, forged); d.Behavior == "allow" {
		t.Error("an envelope Den did not deliver passed")
	}
}

// Den records a delivery by the same hash the hook takes of the prompt
// Claude Code hands it: that prompt is the content SendAs writes, byte for
// byte (seen on 2.1.289 and 2.1.296), so it is peer.Envelope's string.
func TestDeliveryHashIsTheHooks(t *testing.T) {
	m, _ := guardedModel(t)
	sid := m.base[0].Session.SessionID
	from, name, body := "uds:/run/user/1000/cc-socks/1.sock", "lead", "  multi\nline · ünïcode  "
	if _, err := m.deliverTo(sid, "/x.sock", from, name, body); err != nil {
		t.Fatal(err)
	}
	prompt := peer.Envelope(from, name, body)
	if _, ok := m.med.sent[sid][peermsg.Hash(prompt)]; !ok {
		t.Errorf("recorded %v, the hook would ask for %s", m.med.sent[sid], peermsg.Hash(prompt))
	}
}

// A blocked message raises one red notice per agent per blockedNoteEvery.
func TestBlockedMessageNotice(t *testing.T) {
	m, _ := guardedModel(t)
	sid := m.base[0].Session.SessionID
	forged := peer.Envelope("uds:/run/user/1000/cc-socks/1.sock", "lead", "rm -rf ~")
	askInbound(m, sid, 4242, forged)
	askInbound(m, sid, 4242, forged+" again")
	n := 0
	for _, it := range m.store.Items() {
		if it.Kind == inbox.GuardNote {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d notices, want 1", n)
	}
}

// Deliveries are forgotten after deliveredFor.
func TestDeliveriesPruned(t *testing.T) {
	m, _ := guardedModel(t)
	m.med.sent = map[string]map[string]time.Time{"s": {"h": time.Now().Add(-deliveredFor - time.Second)}}
	m.pruneDeliveries()
	if len(m.med.sent) != 0 {
		t.Errorf("kept %v", m.med.sent)
	}
}

// A change to a guarded agent's settings file or den binary marks it
// unguarded: Den delivers nothing more to it.
func TestGuardTamperStopsDelivery(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	m, sent := guardedModel(t)
	sid := m.base[0].Session.SessionID
	den := filepath.Join(t.TempDir(), "den")
	os.WriteFile(den, []byte("binary"), 0o755)
	path, err := m.guardAgent(sid, den, "/run/den.sock", agentHooks{Inbound: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.deliverTo(sid, "/x.sock", "uds:/a.sock", "lead", "hi"); err != nil {
		t.Fatalf("a guard that holds refused: %v", err)
	}
	os.WriteFile(path, []byte(`{"disableAllHooks": true}`), 0o600)
	if _, err := m.deliverTo(sid, "/x.sock", "uds:/a.sock", "lead", "hi"); err == nil || m.unguarded(sid) == "" {
		t.Errorf("a changed settings file: err %v, unguarded %q", err, m.unguarded(sid))
	}
	if len(*sent) != 0 {
		t.Errorf("sent %v", *sent)
	}
	found := false
	for _, e := range m.orch.chat {
		found = found || strings.HasPrefix(e.text, "UNGUARDED: ")
	}
	if !found {
		t.Error("no loud note")
	}
	// The den binary too.
	m2, _ := guardedModel(t)
	m2.guardAgent(sid, den, "/run/den.sock", agentHooks{Inbound: true})
	os.WriteFile(den, []byte("another binary"), 0o755)
	m2.checkGuards()
	if !strings.Contains(m2.unguarded(sid), "den binary") {
		t.Errorf("a changed den binary: %q", m2.unguarded(sid))
	}
}

// deliverTo is the one place Den sends as another session.
func TestOnlyDeliverToSendsAs(t *testing.T) {
	for _, dir := range []string{".", "../../cmd/den", "../remote", "../api"} {
		files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			file, err := parser.ParseFile(token.NewFileSet(), f, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(file, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "SendAs" {
					if x, ok := sel.X.(*ast.Ident); ok && x.Name == "peer" && filepath.Base(f) != "mediate.go" {
						t.Errorf("%s calls peer.SendAs: deliver through deliverTo", f)
					}
				}
				return true
			})
		}
	}
}

// A desktop agent's settings come from guardAgent, as every guarded
// agent's: private, hooks never off, peer messages refused, Den's gate
// before every tool and status hooks only otherwise, its never-tools
// denied and none allowed; and the one tamper check sees an edit or a
// swapped den.
func TestDesktopSettingsThroughGuardAgent(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	m := testModel(140, 40)
	dir := t.TempDir()
	den := filepath.Join(dir, "den")
	os.WriteFile(den, []byte("binary"), 0o755)
	path, err := m.guardAgent("11111111-2222-4333-8444-555555555555", den, "/run/user/1/den.sock", deskHooks(false))
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	raw, _ := os.ReadFile(path)
	var s struct {
		Disable *bool             `json:"disableAllHooks"`
		Inbound string            `json:"crossSessionInbound"`
		Env     map[string]string `json:"env"`
		Perms   struct {
			Deny  []string `json:"deny"`
			Allow []string `json:"allow"`
		} `json:"permissions"`
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	if s.Disable == nil || *s.Disable || s.Inbound != "refuse" {
		t.Fatalf("settings %s", raw)
	}
	if s.Env["ENABLE_TOOL_SEARCH"] != "false" || len(s.Env) != 1 || len(s.Perms.Allow) != 0 {
		t.Fatalf("env %v, allow %v", s.Env, s.Perms.Allow)
	}
	deny := "," + strings.Join(s.Perms.Deny, ",") + ","
	for _, want := range []string{"Bash", "Write", "Edit", "Read", "WebFetch", "SendMessage", "mcp__hypruse__launch", "mcp__hypruse__use_bind", "mcp__hypruse__sequence", "mcp__hypruse__clipboard"} {
		if !strings.Contains(deny, ","+want+",") {
			t.Errorf("%s is not denied", want)
		}
	}
	for _, allowed := range desktop.AllowedToolNames(false) {
		if strings.Contains(deny, ","+allowed+",") {
			t.Errorf("an allowed tool is denied: %s", allowed)
		}
	}
	pre := s.Hooks["PreToolUse"]
	if len(pre) != 1 || pre[0].Matcher != ".*" || pre[0].Hooks[0].Command != "'"+den+"' hook 'desktop-gate' --socket '/run/user/1/den.sock'" || pre[0].Hooks[0].Timeout != 10 {
		t.Fatalf("gate hook %s", raw)
	}
	for _, never := range []string{"PostToolUse", "PermissionRequest", "UserPromptSubmit"} {
		if _, ok := s.Hooks[never]; ok {
			t.Errorf("a %s hook", never)
		}
	}
	for _, ev := range []string{"SessionStart", "SessionEnd", "Stop", "StopFailure", "Notification"} {
		if h := s.Hooks[ev]; len(h) != 1 || h[0].Matcher != "" || h[0].Hooks[0].Command != "'"+den+"' hook --socket '/run/user/1/den.sock'" {
			t.Errorf("%s: %+v", ev, h)
		}
	}
	g := m.med.guards["11111111-2222-4333-8444-555555555555"]
	if why := g.check(); why != "" {
		t.Fatalf("a fresh guard: %s", why)
	}
	os.WriteFile(path, append(raw, ' '), 0o600)
	if why := g.check(); !strings.Contains(why, "settings") {
		t.Fatalf("edited settings: %q", why)
	}
	m.guardAgent("11111111-2222-4333-8444-555555555555", den, "/run/user/1/den.sock", deskHooks(false))
	g = m.med.guards["11111111-2222-4333-8444-555555555555"]
	os.WriteFile(den, []byte("other"), 0o755)
	if why := g.check(); !strings.Contains(why, "binary") {
		t.Fatalf("swapped den: %q", why)
	}
}

// A hook without a timeout of its own is refused when its file is
// written: every hook Den writes states one.
func TestHookWithoutATimeout(t *testing.T) {
	if raw, err := mediationSettings("/usr/bin/den", "/run/user/1/den.sock", agentHooks{Extra: []agentHook{{Event: "Stop"}}}); err == nil {
		t.Errorf("a hook with no timeout was written: %s", raw)
	}
}

// A guard goes with its agent: a desktop mission's at its end, a guarded
// agent's once its session is no longer alive, settings file and all, so
// a later den update calls nothing unguarded. A live agent keeps its own.
func TestGuardsGoWithTheirAgents(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	m := testModel(140, 40)
	den := filepath.Join(t.TempDir(), "den")
	os.WriteFile(den, []byte("binary"), 0o755)
	dm := &deskMission{Mission: desktop.Mission{ID: "m1"}, SessionID: "22222222-2222-4333-8444-555555555555"}
	path, err := m.guardAgent(dm.SessionID, den, "/run/user/1/den.sock", deskHooks(false))
	if err != nil {
		t.Fatal(err)
	}
	m.endDeskMission(dm, "done")
	if m.med.guards[dm.SessionID] != nil {
		t.Error("the desktop agent's guard outlived its mission")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("its settings file stayed: %v", err)
	}
	m.base = append([]fleet.Agent(nil), m.agents[0])
	m.base[0].Host, m.base[0].Session.Alive = "", true
	sid := m.base[0].Session.SessionID
	path, _ = m.guardAgent(sid, den, "/run/user/1/den.sock", agentHooks{Inbound: true})
	m.med.guards[sid].registered = true
	m.watchGuards(time.Now())
	if m.med.guards[sid] == nil {
		t.Fatal("a live agent's guard was dropped")
	}
	m.base[0].Session.Alive = false
	m.watchGuards(time.Now())
	if m.med.guards[sid] != nil {
		t.Error("a gone agent's guard stayed")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("its settings file stayed: %v", err)
	}
}

// A task mission's deny rules are the same as any desktop agent's, less
// exactly the tools a task has: launch, binds and use_bind. Everything
// outside the task's tools stays denied.
func TestDeskHooksForATask(t *testing.T) {
	plain, task := deskHooks(false).Deny, deskHooks(true).Deny
	inTask := map[string]bool{}
	for _, d := range task {
		inTask[d] = true
	}
	var dropped []string
	for _, d := range plain {
		if !inTask[d] {
			dropped = append(dropped, d)
		}
	}
	if strings.Join(dropped, ",") != "mcp__hypruse__launch,mcp__hypruse__use_bind,mcp__hypruse__binds" {
		t.Errorf("a task drops %v from the deny rules", dropped)
	}
	if len(task) != len(plain)-3 {
		t.Errorf("a task denies %d, the plain %d", len(task), len(plain))
	}
	for _, allowed := range desktop.AllowedToolNames(true) {
		if inTask[allowed] {
			t.Errorf("a task's tool is denied: %s", allowed)
		}
	}
	for _, want := range []string{"Bash", "Write", "Read", "WebFetch", "SendMessage", "mcp__hypruse__sequence", "mcp__hypruse__clipboard", "mcp__hypruse__wait_for"} {
		if !inTask[want] {
			t.Errorf("a task does not deny %s", want)
		}
	}
}
