package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/closer"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
)

// closeModel is a fleet with distinct pids and peer names: Mossy needs
// you, Snug works, Fuzzy and Maple are idle, Comet is gone.
func closeModel(t *testing.T) Model {
	t.Helper()
	m := treeModel()
	for i := range m.base {
		m.base[i].Session.PID = 50000 + i
		m.base[i].Session.Name = strings.ToLower(strings.Fields(m.base[i].Name)[0])
	}
	m.base[3].Status = fleet.Idle // Maple
	m.refresh()
	return m
}

type fakeCloseOps struct{ signals, windows []string }

func (f *fakeCloseOps) Signal(pid int, sig syscall.Signal) error {
	f.signals = append(f.signals, sig.String())
	return nil
}
func (f *fakeCloseOps) Alive(int) bool             { return false }
func (f *fakeCloseOps) CloseWindow(a string) error { f.windows = append(f.windows, a); return nil }

// openClose is the one close question still waiting, nil when none.
func openClose(m Model) *inbox.Item {
	for _, it := range closeItems(m) {
		if it.Answerable() {
			return it
		}
	}
	return nil
}

func closeItems(m Model) []*inbox.Item {
	var out []*inbox.Item
	for _, it := range m.store.Items() {
		if it.Kind == inbox.Close {
			out = append(out, it)
		}
	}
	return out
}

func TestCloseIdleListsExactlyTheIdleAgents(t *testing.T) {
	m := closeModel(t)
	mm, _ := m.closeCommand([]string{"idle"})
	m = mm.(Model)
	items := closeItems(m)
	if len(items) != 1 || items[0].Title != "Close 2 agents?" || !items[0].Answerable() {
		t.Fatalf("items = %+v", items)
	}
	body := strings.Join(items[0].Body, "\n")
	for _, want := range []string{"Fuzzy Sprite (fuzzy) · idle · pid 50002", "Maple Owl (maple) · idle · pid 50003", "no window found, only the process ends", "graceful stop first"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	for _, no := range []string{"Mossy", "Snug", "Comet"} {
		if strings.Contains(body, no) {
			t.Errorf("body names %s, which is not idle:\n%s", no, body)
		}
	}
	if m.tab != tabInbox || m.store.Waiting() != 1 {
		t.Errorf("tab %v waiting %d", m.tab, m.store.Waiting())
	}
	out := ansi.Strip(m.View().Content)
	if !strings.Contains(out, "Close 2 agents?") || !strings.Contains(out, "Fuzzy Sprite (fuzzy)") {
		t.Errorf("inbox view:\n%s", out)
	}
}

func TestCloseRefusesWorkingUnlessForcedAndNeverTheOrchestrator(t *testing.T) {
	m := closeModel(t)
	m.orch.proc = nil
	mm, _ := m.closeCommand([]string{"Snug Capybara"})
	m = mm.(Model)
	if len(closeItems(m)) != 0 || !strings.Contains(m.flash, "Snug Capybara: working · add --force") {
		t.Fatalf("working agent should be refused: items %d flash %q", len(closeItems(m)), m.flash)
	}
	mm, _ = m.closeCommand([]string{"Snug Capybara", "--force"})
	m = mm.(Model)
	items := closeItems(m)
	if len(items) != 1 || items[0].Title != "Close Snug Capybara?" {
		t.Fatalf("forced: %+v", items)
	}
	body := strings.Join(items[0].Body, "\n")
	if !strings.Contains(body, "it directs Mossy Lemur, Fuzzy Sprite; they stay open") || !strings.Contains(body, "Forced:") {
		t.Errorf("forced body:\n%s", body)
	}
	// A name not in the fleet is refused by name.
	mm, _ = m.closeCommand([]string{"Nobody Here"})
	if !strings.Contains(mm.(Model).flash, "Nobody Here: not in the fleet") {
		t.Errorf("unknown name: %q", mm.(Model).flash)
	}
}

func TestCtrlXAsksAboutTheSelectedAgent(t *testing.T) {
	m := closeModel(t)
	m.cursor = 0
	for i, a := range m.agents {
		if a.Name == "Fuzzy Sprite" {
			m.cursor = i
		}
	}
	mm, _ := m.handleKey(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	m = mm.(Model)
	items := closeItems(m)
	if len(items) != 1 || items[0].Title != "Close Fuzzy Sprite?" || items[0].SessionID != "fuzzy-sprite" {
		t.Fatalf("items = %+v", items)
	}
	// ^N keeps it, and the question is settled without any signal.
	ops := &fakeCloseOps{}
	closeOps = ops
	t.Cleanup(func() { closeOps = closer.System{} })
	mm, _ = m.handleKey(tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	m = mm.(Model)
	if items[0].State != inbox.Resolved || items[0].Resolved != "declined in den; nothing closed" || len(ops.signals) != 0 {
		t.Errorf("after ^N: %+v signals %v", items[0], ops.signals)
	}
}

func TestAllowRunsTheCloseAndClearsTheAgent(t *testing.T) {
	ops := &fakeCloseOps{}
	closeOps = ops
	t.Cleanup(func() { closeOps = closer.System{} })
	m := closeModel(t)
	mm, _ := m.closeCommand([]string{"maple"})
	m = mm.(Model)
	it := closeItems(m)[0]
	mm, cmd := m.answer("allow")
	m = mm.(Model)
	if cmd == nil || it.Resolved != "closing…" {
		t.Fatalf("allow did not start the run: %+v", it)
	}
	// The batch holds the flash and the run; run them and feed the result back.
	var done closeDoneMsg
	var walk func(tea.Cmd)
	walk = func(c tea.Cmd) {
		if c == nil {
			return
		}
		switch v := c().(type) {
		case closeDoneMsg:
			done = v
		case tea.BatchMsg:
			for _, x := range v {
				walk(x)
			}
		}
	}
	walk(cmd)
	if len(done.results) != 1 || !done.results[0].Ended || strings.Join(ops.signals, ",") != "terminated" {
		t.Fatalf("run: %+v signals %v", done.results, ops.signals)
	}
	mm, _ = m.Update(done)
	m = mm.(Model)
	if it.Resolved != "closed Maple Owl" || it.State != inbox.Resolved {
		t.Errorf("settled: %+v", it)
	}
	if a, ok := m.agentFor("maple-owl"); !ok || a.Status != fleet.Gone {
		t.Errorf("Maple should be gone in the roster: %+v %v", a, ok)
	}
	if _, ok := m.closing[it.ID]; ok {
		t.Error("plan not cleared")
	}
}

func TestOrchestratorCloseRequestWaitsForTheHuman(t *testing.T) {
	m := closeModel(t)
	payload, _ := json.Marshal(bridge.CloseInput{Peers: []string{"fuzzy", "ghost", "snug"}, Reason: "idle for an hour"})
	req := bridge.TestRequest(bridge.Envelope{Event: bridge.CloseEvent, Payload: payload})
	m.handleRequest(req)
	items := closeItems(m)
	if len(items) != 1 || items[0].Request() != req {
		t.Fatalf("items = %+v", items)
	}
	body := strings.Join(items[0].Body, "\n")
	for _, want := range []string{"The orchestrator asks to close these agents. Its reason: idle for an hour", "Fuzzy Sprite (fuzzy) · idle", "Not ghost: not in the fleet", "Not Snug Capybara: working · add --force"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	mm, _ := m.answer("deny")
	m = mm.(Model)
	d, ok := req.Decided()
	if !ok || d.Behavior != "deny" || !strings.Contains(d.Message, "declined") {
		t.Errorf("the orchestrator should hear the decline: %+v %v", d, ok)
	}
	// Nothing closable: the request is answered at once.
	payload, _ = json.Marshal(bridge.CloseInput{Peers: []string{"snug"}})
	req = bridge.TestRequest(bridge.Envelope{Event: bridge.CloseEvent, Payload: payload})
	m.handleRequest(req)
	if d, ok := req.Decided(); !ok || d.Behavior != "deny" || !strings.Contains(d.Message, "Snug Capybara: working") {
		t.Errorf("nothing to close should answer at once: %+v %v", d, ok)
	}
}

// staleModel: Fuzzy idle 3h, Maple idle 20m, Mossy needs you, Snug works;
// Comet gone. Fuzzy's peer is protected, Maple is flagged.
func staleModel(t *testing.T) Model {
	t.Helper()
	m := closeModel(t)
	now := time.Date(2026, 10, 11, 20, 0, 0, 0, time.UTC)
	closeNow = func() time.Time { return now }
	t.Cleanup(func() { closeNow = time.Now })
	for i := range m.base {
		switch m.base[i].Name {
		case "Fuzzy Sprite":
			m.base[i].LastActive = now.Add(-3 * time.Hour)
		case "Maple Owl":
			m.base[i].LastActive = now.Add(-20 * time.Minute)
		case "Mossy Lemur":
			m.base[i].LastActive = now.Add(-5 * time.Hour)
		}
	}
	m.guard = &closer.Guard{}
	m.guardPath = filepath.Join(t.TempDir(), "protected.json")
	m.refresh()
	return m
}

func TestCloseIdleForPicksByLastActivityAndHonoursTheGuard(t *testing.T) {
	m := staleModel(t)
	// Three hours idle: Fuzzy. Maple is too recent; Mossy needs you.
	mm, _ := m.closeCommand([]string{"idle", "1h"})
	items := closeItems(mm.(Model))
	if len(items) != 1 || items[0].Title != "Close Fuzzy Sprite?" {
		t.Fatalf("items = %+v", items)
	}
	body := strings.Join(items[0].Body, "\n")
	for _, want := range []string{"Agents idle for 1h or more, by their last change of busy/idle status.", "Fuzzy Sprite (fuzzy) · idle 3h00m · pid 50002"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Maple") || strings.Contains(body, "Mossy") {
		t.Errorf("body names an agent that is not stale:\n%s", body)
	}
	// An open question keeps its agent as "needs you"; settle it so the
	// agent reads as idle again for the next steps.
	settle := func() {
		for _, it := range closeItems(m) {
			if it.Answerable() {
				m.store.Settle(it, false, "kept")
			}
		}
		m.refresh()
	}
	settle()
	// Unknown last activity is never stale.
	m2 := staleModel(t)
	for i := range m2.base {
		m2.base[i].LastActive = time.Time{}
	}
	m2.refresh()
	mm, _ = m2.closeCommand([]string{"idle", "1h"})
	if len(closeItems(mm.(Model))) != 0 || !strings.Contains(mm.(Model).flash, "No agent has been idle for 1h") {
		t.Errorf("unknown activity should not be stale: %q", mm.(Model).flash)
	}
	// A bad duration is refused.
	mm, _ = m.closeCommand([]string{"idle", "soon"})
	if !strings.Contains(mm.(Model).flash, "not a duration") {
		t.Errorf("bad duration: %q", mm.(Model).flash)
	}

	// Protected by peer name: skipped and said so, even when named.
	m.guard.Protect("fuzzy")
	m.guard.SetFlag("Maple Owl", "issue #11080 pending")
	before := len(closeItems(m))
	mm, _ = m.closeCommand([]string{"idle", "10m"})
	m3 := mm.(Model)
	if n := len(closeItems(m3)); n != before {
		t.Fatalf("nothing should be proposed, got %d new", n-before)
	}
	if !strings.Contains(m3.flash, "skipped Fuzzy Sprite: protected (fuzzy)") || !strings.Contains(m3.flash, "Maple Owl holds context") {
		t.Errorf("flash = %q", m3.flash)
	}
	mm, _ = m.closeCommand([]string{"Fuzzy Sprite"})
	if !strings.Contains(mm.(Model).flash, "Fuzzy Sprite: protected (fuzzy) · /unprotect to allow") {
		t.Errorf("named protected agent: %q", mm.(Model).flash)
	}
	// A flagged agent is included when named.
	mm, _ = m.closeCommand([]string{"Maple Owl"})
	if it := openClose(mm.(Model)); it == nil || it.Title != "Close Maple Owl?" {
		t.Errorf("flagged agent by name: %+v", it)
	}
	settle()
	// With another stale agent present, the skipped and held ones are listed in the body.
	m.base = append(m.base, m.base[2])
	m.base[len(m.base)-1].Name, m.base[len(m.base)-1].Session.Name, m.base[len(m.base)-1].Session.SessionID, m.base[len(m.base)-1].Session.PID = "Dusty Mole", "dusty", "dusty-mole", 50009
	m.refresh()
	mm, _ = m.closeCommand([]string{"idle", "10m"})
	it := openClose(mm.(Model))
	if it == nil || it.Title != "Close Dusty Mole?" {
		t.Fatalf("item = %+v", it)
	}
	body = strings.Join(it.Body, "\n")
	for _, want := range []string{"Skipped Fuzzy Sprite: protected (fuzzy)", "Holds context, not selected: Maple Owl (issue #11080 pending) · /close Maple Owl to include it"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
}

func TestProtectByFolderAndTaskAndTheCommandsPersist(t *testing.T) {
	m := staleModel(t)
	m.guard.Paths = []string{"~/projects/xml-converter"}
	home, _ := os.UserHomeDir()
	for i := range m.base {
		if m.base[i].Name == "Fuzzy Sprite" {
			m.base[i].Session.CWD = filepath.Join(home, "projects", "xml-converter", "src")
		}
		if m.base[i].Name == "Maple Owl" {
			m.base[i].Labels.Task = "convert the xml-converter export to JSON"
		}
	}
	m.refresh()
	fuzzy, _ := m.agentNamed("Fuzzy Sprite")
	maple, _ := m.agentNamed("Maple Owl")
	if why := m.guardReason(fuzzy); why != "protected: works in ~/projects/xml-converter" {
		t.Errorf("folder rule: %q", why)
	}
	if why := m.guardReason(maple); why != "protected: its task mentions xml-converter" {
		t.Errorf("task rule: %q", why)
	}
	// Commands change the guard and the file.
	mm, _ := m.guardCommand("/protect", []string{"Snug", "Capybara"})
	m = mm.(Model)
	if !strings.Contains(m.flash, "Snug Capybara is protected") {
		t.Errorf("protect: %q", m.flash)
	}
	mm, _ = m.guardCommand("/flag", []string{"Mossy", "Lemur:", "interview", "in", "progress"})
	m = mm.(Model)
	g := closer.LoadGuard(m.guardPath)
	if len(g.Names) != 1 || g.Names[0] != "Snug Capybara" || len(g.Flagged) != 1 || g.Flagged[0].Note != "interview in progress" {
		t.Errorf("saved guard = %+v", g)
	}
	mm, _ = m.guardCommand("/protect", nil)
	if !strings.Contains(mm.(Model).flash, "Protected: Snug Capybara, folder ~/projects/xml-converter, flagged Mossy Lemur") {
		t.Errorf("listing: %q", mm.(Model).flash)
	}
	mm, _ = m.guardCommand("/unprotect", []string{"Snug", "Capybara"})
	mm, _ = mm.(Model).guardCommand("/unflag", []string{"Mossy", "Lemur"})
	g = closer.LoadGuard(mm.(Model).guardPath)
	if len(g.Names) != 0 || len(g.Flagged) != 0 {
		t.Errorf("after unprotect and unflag: %+v", g)
	}
}

func TestOrchestratorIdleForRequestPicksAndWaits(t *testing.T) {
	m := staleModel(t)
	m.guard.Protect("Fuzzy Sprite")
	m.base = append(m.base, m.base[2])
	m.base[len(m.base)-1].Name, m.base[len(m.base)-1].Session.Name, m.base[len(m.base)-1].Session.SessionID, m.base[len(m.base)-1].Session.PID = "Dusty Mole", "dusty", "dusty-mole", 50009
	m.refresh()
	payload, _ := json.Marshal(bridge.CloseInput{IdleFor: "1h", Reason: "unused for an hour"})
	req := bridge.TestRequest(bridge.Envelope{Event: bridge.CloseEvent, Payload: payload})
	m.handleRequest(req)
	items := closeItems(m)
	if len(items) != 1 || items[0].Title != "Close Dusty Mole?" {
		t.Fatalf("items = %+v", items)
	}
	body := strings.Join(items[0].Body, "\n")
	for _, want := range []string{"The orchestrator asks to close these agents. Its reason: unused for an hour", "Agents idle for 1h or more", "Dusty Mole (dusty) · idle 3h00m", "Skipped Fuzzy Sprite: protected (Fuzzy Sprite)"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	if _, ok := req.Decided(); ok {
		t.Error("the request should wait for the human")
	}
	// A bad duration answers at once.
	payload, _ = json.Marshal(bridge.CloseInput{IdleFor: "later"})
	req = bridge.TestRequest(bridge.Envelope{Event: bridge.CloseEvent, Payload: payload})
	m.handleRequest(req)
	if d, ok := req.Decided(); !ok || d.Behavior != "deny" || !strings.Contains(d.Message, "not a duration") {
		t.Errorf("bad duration: %+v %v", d, ok)
	}
}
