package ui

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/desktop"
	"github.com/GlacierHubAB/den/internal/inbox"
)

// fakeDesktopInstall stands in for the plan and the install; it records
// installs. planErr makes the plan fail (a clone that is not the pin).
func fakeDesktopInstall(t *testing.T, planErr, runErr error) *[]desktop.Plan {
	t.Helper()
	var ran []desktop.Plan
	oldPlan, oldRun, oldCaller := desktopPlan, desktopInstall, pairCaller
	desktopPlan = func(in bridge.DesktopInstallInput) (desktop.Plan, error) {
		p := desktop.Plan{Paths: desktop.Paths{Venv: "/data/den/hypruse/venv", Manifest: "/state/den/desktop/venv-manifest.json"}, Python: "/usr/bin/python3.14", Clone: in.Clone, Packages: 29, Pin: desktop.Pinned}
		return p, planErr
	}
	desktopInstall = func(_ context.Context, p desktop.Plan) (string, error) {
		ran = append(ran, p)
		return "hypruse 0.12.1 in /data/den/hypruse/venv", runErr
	}
	pairCaller = func(Model, int) string { return "" }
	t.Cleanup(func() { desktopPlan, desktopInstall, pairCaller = oldPlan, oldRun, oldCaller })
	return &ran
}

func desktopInstallRequest() *bridge.Request {
	payload, _ := json.Marshal(bridge.DesktopInstallInput{Clone: "/src/hypruse", AskedBy: "den install-hypruse, pid 42"})
	return bridge.TestRequest(bridge.Envelope{Event: bridge.DesktopInstallEvent, Payload: payload})
}

func desktopInstallItem(m Model) *inbox.Item {
	for _, it := range m.store.Items() {
		if it.Kind == inbox.DesktopInstall {
			return it
		}
	}
	return nil
}

func TestDesktopInstallWaitsForYes(t *testing.T) {
	ran := fakeDesktopInstall(t, nil, nil)
	m := closeModel(t)
	req := desktopInstallRequest()
	runBatch(m.handleRequest(req))
	it := desktopInstallItem(m)
	if it == nil || !it.Answerable() {
		t.Fatalf("no install question: %+v", it)
	}
	body := strings.Join(it.Body, "\n")
	for _, want := range []string{"Download 29 packages from PyPI", "pinned by hash", "0035d86", "/src/hypruse", "asked by den install-hypruse, pid 42"} {
		if !strings.Contains(body, want) {
			t.Errorf("card lacks %q:\n%s", want, body)
		}
	}
	if dockMayAnswer(it) {
		t.Error("the dock may never answer a hypruse install")
	}
	if _, done := req.Decided(); done || len(*ran) != 0 {
		t.Fatal("installed or answered before the human did")
	}
	m.tab = tabInbox
	m.selectItem(it)
	mm, cmd := m.answer("allow")
	m = mm.(Model)
	for _, msg := range runBatch(cmd) {
		if d, ok := msg.(desktopInstallDoneMsg); ok {
			m.finishDesktopInstall(d)
		}
	}
	if len(*ran) != 1 || (*ran)[0].Clone != "/src/hypruse" {
		t.Fatalf("installs = %+v", *ran)
	}
	d, done := req.Decided()
	if !done || d.Behavior != "allow" || !strings.Contains(d.Message, "hypruse 0.12.1") {
		t.Fatalf("asker got %+v (done %v)", d, done)
	}
}

// The install takes minutes and its question stays open until it ends: a
// second ^Y meanwhile (the human sees nothing happen yet) must not settle
// it. The command that asked gets exactly what Den did: the install's own
// result. Driven through the real ^Y path, both presses.
func TestDesktopInstallSecondYesWhileInstalling(t *testing.T) {
	ran := fakeDesktopInstall(t, nil, nil)
	m := closeModel(t)
	req := desktopInstallRequest()
	runBatch(m.handleRequest(req))
	it := desktopInstallItem(m)
	m.tab = tabInbox
	m.selectItem(it)
	mm, cmd := m.answer("allow")
	m = mm.(Model)
	// Again, before the install has finished.
	mm, _ = m.answer("allow")
	m = mm.(Model)
	if _, done := req.Decided(); done {
		d, _ := req.Decided()
		t.Fatalf("a second ^Y answered the command before the install ended: %+v", d)
	}
	if !strings.Contains(m.flash, "still installing") {
		t.Errorf("flash after the second ^Y: %q", m.flash)
	}
	// While it runs, the card says so and offers no keys to press again.
	for _, lines := range [][]string{m.inboxBlock(it, true, 120), m.requestLines(it, m.agentOrStub(it.SessionID), 120)} {
		card := ansi.Strip(strings.Join(lines, "\n"))
		if !strings.Contains(card, "installing hypruse") || strings.Contains(card, "install it") {
			t.Errorf("the card during the install:\n%s", card)
		}
	}
	for _, msg := range runBatch(cmd) {
		if d, ok := msg.(desktopInstallDoneMsg); ok {
			m.finishDesktopInstall(d)
		}
	}
	d, done := req.Decided()
	if !done || d.Behavior != "allow" || !strings.Contains(d.Message, "installed hypruse 0.12.1") || len(*ran) != 1 {
		t.Fatalf("asker got %+v (done %v), %d installs", d, done, len(*ran))
	}
	if it.Resolved != d.Message {
		t.Errorf("the inbox says %q, the command was told %q", it.Resolved, d.Message)
	}
	// A failed install reaches the command as a failure, with its reason.
	ran2 := fakeDesktopInstall(t, nil, errors.New("pip: hash mismatch"))
	m2 := closeModel(t)
	req2 := desktopInstallRequest()
	runBatch(m2.handleRequest(req2))
	m2.tab = tabInbox
	m2.selectItem(desktopInstallItem(m2))
	mm, cmd = m2.answer("allow")
	m2 = mm.(Model)
	mm, _ = m2.answer("allow")
	m2 = mm.(Model)
	for _, msg := range runBatch(cmd) {
		if d, ok := msg.(desktopInstallDoneMsg); ok {
			m2.finishDesktopInstall(d)
		}
	}
	if d, _ := req2.Decided(); d.Behavior != "deny" || !strings.Contains(d.Message, "hash mismatch") || len(*ran2) != 1 {
		t.Errorf("a failed install reached the command as %+v", d)
	}
}

func TestDesktopInstallDeclined(t *testing.T) {
	ran := fakeDesktopInstall(t, nil, nil)
	m := closeModel(t)
	req := desktopInstallRequest()
	runBatch(m.handleRequest(req))
	m.tab = tabInbox
	m.selectItem(desktopInstallItem(m))
	mm, cmd := m.answer("deny")
	m = mm.(Model)
	runBatch(cmd)
	if len(*ran) != 0 {
		t.Fatal("installed after ^N")
	}
	if d, done := req.Decided(); !done || d.Behavior != "deny" {
		t.Fatalf("asker got %+v", d)
	}
}

// An agent, Den's own tools and hooks may not ask, and a bad clone never
// reaches the inbox.
func TestDesktopInstallRefusals(t *testing.T) {
	fakeDesktopInstall(t, nil, nil)
	pairCaller = func(Model, int) string { return "pid 42 runs under Claude Code" }
	m := closeModel(t)
	req := desktopInstallRequest()
	runBatch(m.handleRequest(req))
	if d, done := req.Decided(); !done || d.Behavior != "deny" || !strings.Contains(d.Message, "Claude Code") {
		t.Fatalf("an agent's ask got %+v", d)
	}
	if desktopInstallItem(m) != nil {
		t.Fatal("an agent's ask reached the inbox")
	}

	fakeDesktopInstall(t, errors.New("the clone at /src/hypruse is not hypruse 0.12.1"), nil)
	m = closeModel(t)
	req = desktopInstallRequest()
	runBatch(m.handleRequest(req))
	if d, done := req.Decided(); !done || d.Behavior != "deny" || !strings.Contains(d.Message, "not hypruse") {
		t.Fatalf("a bad clone got %+v", d)
	}
	if desktopInstallItem(m) != nil {
		t.Fatal("a bad clone reached the inbox")
	}
}
