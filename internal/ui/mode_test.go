package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/api"
	"github.com/GlacierHubAB/den/internal/config"
	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/roster"
)

// inDefault is a config.toml that asks for default.
const inDefault = "[agents]\npermission_mode = \"default\"\n"

// modeModel is a Model with the global mode started from the file at
// DEN_CONFIG, a temporary one of the test's own.
func modeModel(t *testing.T, file string) Model {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	t.Setenv("DEN_CONFIG", p)
	if file != "" {
		os.WriteFile(p, []byte(file), 0o644)
	}
	m := testModel(140, 40)
	cfg := config.Load()
	m.startMode(cfg.Agents.PermissionMode, cfg.Agents.ModeRefused)
	return m
}

func modeCards(m Model) []*inbox.Item {
	var out []*inbox.Item
	for _, it := range m.store.Items() {
		if it.Kind == inbox.ModeSet {
			out = append(out, it)
		}
	}
	return out
}

func openModeCard(t *testing.T, m Model) *inbox.Item {
	t.Helper()
	var open []*inbox.Item
	for _, it := range modeCards(m) {
		if it.Answerable() {
			open = append(open, it)
		}
	}
	if len(open) != 1 {
		t.Fatalf("%d open mode cards, want 1", len(open))
	}
	return open[0]
}

func fileMode(t *testing.T) string {
	t.Helper()
	mode, refused, err := config.ModeSetting()
	if err != nil || refused != "" {
		t.Fatalf("config: %q refused %q %v", mode, refused, err)
	}
	return mode
}

func runMode(m Model, args ...string) Model {
	mm, _ := m.modeCommand(args)
	return mm.(Model)
}

// A lowering applies at once and is written to config.toml; no card.
func TestModeLoweringAppliesAtOnce(t *testing.T) {
	m := modeModel(t, inDefault)
	if m.agentMode() != "default" {
		t.Fatalf("started in %q", m.agentMode())
	}
	for _, step := range []string{"dontAsk"} {
		m = runMode(m, step)
		if m.agentMode() != step || fileMode(t) != step || m.flashErr {
			t.Errorf("/mode %s: mode %q, file %q, flash %q", step, m.agentMode(), fileMode(t), m.flash)
		}
	}
	if len(modeCards(m)) != 0 {
		t.Error("a lowering raised a card")
	}
}

// A raise waits on a card, typed in Den too: old, new, what each allows
// and who asked. ^Y applies it and writes the file; ^N keeps the mode.
func TestModeRaiseWaitsOnACard(t *testing.T) {
	m := modeModel(t, inDefault)
	m = runMode(m, "AUTO")
	if m.agentMode() != "default" {
		t.Fatalf("raised without a card: %q", m.agentMode())
	}
	it := openModeCard(t, m)
	body := strings.Join(it.Body, "\n")
	for _, want := range []string{"now: default: reads without asking", "asked for: auto: never asks", "asked by: you in Den", "running agents keep their mode"} {
		if !strings.Contains(body, want) {
			t.Errorf("the card says %q, not %q", body, want)
		}
	}
	mm, _ := m.answerItem(it, "deny")
	m = mm.(Model)
	if m.agentMode() != "default" || it.State != inbox.Resolved {
		t.Errorf("after ^N: %q, %v", m.agentMode(), it.State)
	}
	m = runMode(m, "accept-edits")
	mm, _ = m.answerItem(openModeCard(t, m), "allow")
	m = mm.(Model)
	if m.agentMode() != "acceptEdits" || fileMode(t) != "acceptEdits" {
		t.Errorf("after ^Y: %q, file %q", m.agentMode(), fileMode(t))
	}
}

// bypassPermissions is never the global mode, in any spelling; nor is
// anything off the line.
func TestModeRefusesBypass(t *testing.T) {
	m := modeModel(t, inDefault)
	for _, bad := range [][]string{{"bypassPermissions"}, {"bypass-permissions"}, {"yolo"}, {"auto", "plan"}} {
		m = runMode(m, bad...)
		if !m.flashErr || m.agentMode() != "default" || len(modeCards(m)) != 0 {
			t.Errorf("%v: flash %q, mode %q, %d cards", bad, m.flash, m.agentMode(), len(modeCards(m)))
		}
	}
}

// A newer request replaces the open card, which is spent; a lowering
// spends a card that raised from the old mode; a card whose old mode is
// no longer the global one is refused on ^Y.
func TestModeCardReplacedAndStale(t *testing.T) {
	m := modeModel(t, inDefault)
	m = runMode(m, "acceptEdits")
	first := openModeCard(t, m)
	m = runMode(m, "auto")
	second := openModeCard(t, m)
	if first.State != inbox.Resolved || second == first {
		t.Fatalf("first %v, second the same: %v", first.State, second == first)
	}
	m = runMode(m, "dontAsk")
	if second.State != inbox.Resolved || m.agentMode() != "dontAsk" {
		t.Errorf("after a lowering: card %v, mode %q", second.State, m.agentMode())
	}
	m = runMode(m, "auto")
	third := openModeCard(t, m)
	m.mode.global = "default" // moved under the card
	mm, _ := m.answerItem(third, "allow")
	m = mm.(Model)
	if m.agentMode() != "default" || third.State != inbox.Resolved || !m.flashErr {
		t.Errorf("a stale card: mode %q, state %v, flash %q", m.agentMode(), third.State, m.flash)
	}
}

// At start config.toml is taken up to the shipped mode, auto, which is
// what Claude Code runs with no flag: every ranked mode is at most that.
// bypassPermissions never is: Den starts in auto and says why.
func TestModeConfigAtStart(t *testing.T) {
	for file, want := range map[string]string{
		"":              "auto",
		`"auto"`:        "auto",
		`"plan"`:        "plan",
		`"dontAsk"`:     "dontAsk",
		`"default"`:     "default",
		`"acceptEdits"`: "acceptEdits",
	} {
		body := ""
		if file != "" {
			body = "[agents]\npermission_mode = " + file + "\n"
		}
		m := modeModel(t, body)
		if m.agentMode() != want || len(modeCards(m)) != 0 || m.flashErr {
			t.Errorf("config %s: %q, %d cards, flash %q", file, m.agentMode(), len(modeCards(m)), m.flash)
		}
	}
	m := modeModel(t, "[agents]\npermission_mode = \"bypassPermissions\"\n")
	if m.agentMode() != "auto" || len(modeCards(m)) != 0 || !m.flashErr {
		t.Errorf("bypass at start: %q, %d cards, flash %q", m.agentMode(), len(modeCards(m)), m.flash)
	}
}

// From auto, every other mode is a lowering, so one step away.
func TestModeEveryModeLowersFromAuto(t *testing.T) {
	for _, to := range []string{"acceptEdits", "plan", "default", "dontAsk"} {
		m := runMode(modeModel(t, ""), to)
		if m.agentMode() != to || len(modeCards(m)) != 0 || fileMode(t) != to {
			t.Errorf("/mode %s from auto: %q, %d cards, file %q", to, m.agentMode(), len(modeCards(m)), fileMode(t))
		}
	}
}

// While Den runs, a change to the file is acted on once: a raise is a
// card, a lowering applies.
func TestModeConfigWatch(t *testing.T) {
	m := modeModel(t, inDefault)
	m.applyModeRead(modeReadMsg{ok: true, mode: "auto"})
	if m.agentMode() != "default" {
		t.Fatalf("the file raised the mode: %q", m.agentMode())
	}
	it := openModeCard(t, m)
	if !strings.Contains(strings.Join(it.Body, "\n"), "asked by: config.toml") {
		t.Errorf("card %q", it.Body)
	}
	m.applyModeRead(modeReadMsg{ok: true, mode: "auto"}) // unchanged: nothing new
	if len(modeCards(m)) != 1 {
		t.Errorf("%d cards for one change", len(modeCards(m)))
	}
	m.applyModeRead(modeReadMsg{ok: true, mode: "dontAsk"})
	if m.agentMode() != "dontAsk" || it.State != inbox.Resolved {
		t.Errorf("a lowering in the file: %q, card %v", m.agentMode(), it.State)
	}
	m.applyModeRead(modeReadMsg{ok: true, refused: "bypassPermissions"})
	if m.agentMode() != "dontAsk" || !m.flashErr {
		t.Errorf("a refused value: %q, flash %q", m.agentMode(), m.flash)
	}
	m.applyModeRead(modeReadMsg{}) // did not read: nothing changes
	if m.agentMode() != "dontAsk" {
		t.Errorf("an unread file changed the mode: %q", m.agentMode())
	}
}

// A read of the file begun before Den wrote it may come back with what the
// file said before: it never counts, so Den's own /mode is not taken for
// config.toml asking for more.
func TestModeStaleReadIsDropped(t *testing.T) {
	m := modeModel(t, "")
	stale := modeReadMsg{ok: true, mode: "auto", gen: m.mode.gen}
	m = runMode(m, "dontAsk")
	m.applyModeRead(stale)
	if m.agentMode() != "dontAsk" || len(modeCards(m)) != 0 {
		t.Errorf("a stale read: mode %q, %d cards", m.agentMode(), len(modeCards(m)))
	}
	m.applyModeRead(modeReadMsg{ok: true, mode: "auto", gen: m.mode.gen})
	if len(modeCards(m)) != 1 {
		t.Errorf("a fresh read of a raise: %d cards", len(modeCards(m)))
	}
}

// From the dock: a lowering applies and says so; a raise names its card,
// which the console may answer.
func TestModeFromDock(t *testing.T) {
	m := modeModel(t, inDefault)
	r := &api.ModeSet{Mode: "dontAsk", Console: "app-host", Reply: make(chan api.ModeSetResult, 1)}
	m.modeFromDock(r)
	if res := <-r.Reply; !res.Applied || res.Code != "" || m.agentMode() != "dontAsk" {
		t.Errorf("a lowering: %+v, mode %q", res, m.agentMode())
	}
	r = &api.ModeSet{Mode: "auto", Console: "app-host", Reply: make(chan api.ModeSetResult, 1)}
	m.modeFromDock(r)
	res := <-r.Reply
	it := openModeCard(t, m)
	if res.Card != it.ID || res.Applied || m.agentMode() != "dontAsk" {
		t.Errorf("a raise: %+v, mode %q", res, m.agentMode())
	}
	if !strings.Contains(strings.Join(it.Body, "\n"), "asked by: the console app-host") || !dockMayAnswer(it) {
		t.Errorf("card %q, dock may answer: %v", it.Body, dockMayAnswer(it))
	}
	if m.dockFrom != "" {
		t.Error("dockFrom outlived the request")
	}
	// The same through cmd.run: plan is above dontAsk, so a card, in
	// place of the one for auto.
	cr := &api.CommandRun{Text: "/mode PLAN", Console: "app-host", Reply: make(chan api.CommandResult, 1)}
	m.runFromDock(cr)
	if res := <-cr.Reply; res.Code != "" || m.agentMode() != "dontAsk" || it.State != inbox.Resolved {
		t.Errorf("/mode from cmd.run: %+v, mode %q, the auto card %v", res, m.agentMode(), it.State)
	}
	if body := strings.Join(openModeCard(t, m).Body, "\n"); !strings.Contains(body, "asked for: plan") {
		t.Errorf("card %q", body)
	}
}

func TestModeDockLine(t *testing.T) {
	for line, want := range map[string]string{
		"/mode":                   "/mode",
		"/mode AUTO":              "/mode auto",
		"/mode acceptEdits":       "/mode acceptedits",
		"/mode bypassPermissions": "",
		"/mode yolo":              "",
		"/mode plan auto":         "",
	} {
		words, _, why := dockLine(line)
		if got := strings.Join(words, " "); got != want || (want == "") != (why != "") {
			t.Errorf("%q: %q (%s), want %q", line, got, why, want)
		}
	}
}

// Den shows the agents whose own hooks report another mode, and those it
// knows none for; "mixed" covers both.
func TestModeMixed(t *testing.T) {
	m := modeModel(t, inDefault)
	m.agents = []fleet.Agent{
		{Session: discover.Session{SessionID: "s1", Alive: true}, Name: "ui"},
		{Session: discover.Session{SessionID: "s2", Alive: true}, Name: "api"},
		{Session: discover.Session{SessionID: "s3", Alive: true}, Name: "db"},
		{Session: discover.Session{SessionID: "s4", Alive: true}, Name: "far", Host: "box"},
	}
	m.ctl.modes = map[string]modeSeen{"s1": {mode: "default"}, "s2": {mode: "auto"}, "s4": {mode: "auto"}}
	got := strings.Join(m.modeDiffers(), ", ")
	if got != "api (auto), db (unknown)" || m.modeKnownDiffers() != 1 {
		t.Errorf("differ %q, known %d", got, m.modeKnownDiffers())
	}
	var snap api.Snapshot
	publishedHook = func(s api.Snapshot) { snap = s }
	t.Cleanup(func() { publishedHook = nil })
	m.publishAPI()
	if snap.Mode != (api.ModeInfo{Global: "default", Mixed: true}) || snap.Modes["s2"] != "auto" || snap.Modes["s3"] != "" {
		t.Errorf("snapshot mode %+v, modes %v", snap.Mode, snap.Modes)
	}
}

// Reopened agents start in the mode in force now; nothing in their record
// picks it.
func TestModeRestoreUsesTheGlobalMode(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	m := modeModel(t, inDefault)
	m = runMode(m, "dontAsk")
	m.ensureRestore()
	reqs := m.restoreRequests(restorePlan{agents: []roster.Agent{{SessionID: "s1", CWD: "/tmp", Peer: "ui"}}})
	if len(reqs) != 1 || reqs[0].PermissionMode != "dontAsk" {
		t.Errorf("requests %+v", reqs)
	}
}
