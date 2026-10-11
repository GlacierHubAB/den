package ui

import (
	"encoding/json"
	"strings"
	"syscall"
	"testing"

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
