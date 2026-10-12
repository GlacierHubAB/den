package ui

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/remote"
)

// fakeRemoteInstall stands in for ssh: devbox is a host whose plan copies,
// armhost one whose plan is the steps by hand. It records installs.
func fakeRemoteInstall(t *testing.T, failWith error) *[]remote.Plan {
	t.Helper()
	var ran []remote.Plan
	oldPlan, oldRun, oldHosts := installPlan, installRun, installHosts
	installHosts = func() (remote.Config, error) {
		return remote.Config{Hosts: []remote.Host{
			{Label: "devbox", Target: "user@devbox", Policy: remote.Observe, Enabled: true},
			{Label: "armhost", Target: "armhost", Policy: remote.Observe, Enabled: true},
		}}, nil
	}
	installPlan = func(_ context.Context, h remote.Host) (remote.Plan, error) {
		if h.Label == "armhost" {
			return remote.Plan{Host: h, Text: "download den_linux_arm64 from " + remote.ReleaseURL}, nil
		}
		return remote.Plan{Host: h, Copy: true, Version: "v0.2.0", Text: "Install den v0.2.0 on devbox:\n  1. copy this binary with scp\n  3. move it to ~/.local/bin/den"}, nil
	}
	installRun = func(_ context.Context, p remote.Plan) (string, error) {
		ran = append(ran, p)
		return "den v0.2.0", failWith
	}
	t.Cleanup(func() { installPlan, installRun, installHosts = oldPlan, oldRun, oldHosts })
	return &ran
}

func installRequest(host string) *bridge.Request {
	payload, _ := json.Marshal(bridge.InstallInput{Host: host, AskedBy: "den remote install (pid 42)"})
	return bridge.TestRequest(bridge.Envelope{Event: bridge.InstallEvent, Payload: payload})
}

func installItem(m Model) *inbox.Item {
	for _, it := range m.store.Items() {
		if it.Kind == inbox.Install {
			return it
		}
	}
	return nil
}

// ask runs the request through Den as the event loop would.
func ask(m *Model, req *bridge.Request) {
	for _, msg := range runBatch(m.handleRequest(req)) {
		if p, ok := msg.(installPlanMsg); ok {
			m.offerInstall(p)
		}
	}
}

func TestInstallWaitsForYesThenRunsAndAnswersTheAsker(t *testing.T) {
	ran := fakeRemoteInstall(t, nil)
	m := closeModel(t)
	req := installRequest("devbox")
	ask(&m, req)
	it := installItem(m)
	if it == nil || !it.Answerable() || it.Title != "Install den v0.2.0 on devbox?" {
		t.Fatalf("no install question: %+v", it)
	}
	if body := strings.Join(it.Body, "\n"); !strings.Contains(body, "~/.local/bin/den") || !strings.Contains(body, "asked by den remote install (pid 42)") {
		t.Errorf("body = %s", body)
	}
	if _, done := req.Decided(); done || len(*ran) != 0 {
		t.Fatal("installed or answered before the human did")
	}
	if m.nameFor(it.SessionID) != "@devbox" {
		t.Errorf("named %q", m.nameFor(it.SessionID))
	}
	m.tab = tabInbox
	m.selectItem(installItem(m))
	mm, cmd := m.answer("allow")
	m = mm.(Model)
	for _, msg := range runBatch(cmd) {
		if d, ok := msg.(installDoneMsg); ok {
			m.finishInstall(d)
		}
	}
	d, done := req.Decided()
	if len(*ran) != 1 || (*ran)[0].Host.Label != "devbox" || !done || d.Behavior != "allow" || !strings.Contains(d.Message, "den v0.2.0") {
		t.Errorf("ran %d, decision %+v %v", len(*ran), d, done)
	}
}

func TestInstallDeclinedOrFailedSaysSo(t *testing.T) {
	ran := fakeRemoteInstall(t, errors.New("sha256 mismatch"))
	m := closeModel(t)
	req := installRequest("devbox")
	ask(&m, req)
	m.tab = tabInbox
	m.selectItem(installItem(m))
	mm, _ := m.answer("deny")
	m = mm.(Model)
	if d, _ := req.Decided(); d.Behavior != "deny" || len(*ran) != 0 {
		t.Errorf("decline: %+v, ran %d", d, len(*ran))
	}
	req = installRequest("devbox")
	ask(&m, req)
	m.tab = tabInbox
	m.selectItem(installItem(m))
	mm, cmd := m.answer("allow")
	m = mm.(Model)
	for _, msg := range runBatch(cmd) {
		if d, ok := msg.(installDoneMsg); ok {
			m.finishInstall(d)
		}
	}
	if d, _ := req.Decided(); d.Behavior != "deny" || !strings.Contains(d.Message, "sha256 mismatch") {
		t.Errorf("failed install answered %+v", d)
	}
}

func TestInstallRefusedWithoutAQuestion(t *testing.T) {
	ran := fakeRemoteInstall(t, nil)
	m := closeModel(t)
	for host, want := range map[string]string{"gpu": "no host labelled", "armhost": remote.ReleaseURL} {
		req := installRequest(host)
		ask(&m, req)
		if d, done := req.Decided(); !done || d.Behavior != "deny" || !strings.Contains(d.Message, want) {
			t.Errorf("%s: %+v", host, d)
		}
	}
	if installItem(m) != nil || len(*ran) != 0 {
		t.Error("asked or installed anyway")
	}
	// A request relayed from a host is never an install question.
	relayed := installRequest("devbox")
	relayed.Host = "devbox"
	ask(&m, relayed)
	if d, _ := relayed.Decided(); d.Behavior != "deny" || installItem(m) != nil {
		t.Errorf("relayed: %+v", d)
	}
	// A second request for a host already asked about is refused.
	ask(&m, installRequest("devbox"))
	again := installRequest("devbox")
	ask(&m, again)
	if d, _ := again.Decided(); d.Behavior != "deny" || !strings.Contains(d.Message, "already waiting") {
		t.Errorf("duplicate: %+v", d)
	}
}

// installOn asks for an install on devbox and approves it, as the human
// would with ^Y.
func installOn(t *testing.T, m *Model) *bridge.Request {
	t.Helper()
	req := installRequest("devbox")
	ask(m, req)
	if installItem(*m) == nil {
		t.Fatalf("no install question; flash %q", m.flash)
	}
	m.tab = tabInbox
	m.selectItem(installItem(*m))
	mm, cmd := m.answer("allow")
	*m = mm.(Model)
	for _, msg := range runBatch(cmd) {
		if d, ok := msg.(installDoneMsg); ok {
			m.finishInstall(d)
		}
	}
	return req
}

func TestInstallReconnectsAHostWaitingForDen(t *testing.T) {
	fakeRemoteInstall(t, nil)
	m, feed, _ := remoteModel(t)
	feed.setState("devbox", remote.VersionMismatch)
	m.syncRemote()
	installOn(t, m)
	if len(feed.reconnect) != 1 || feed.reconnect[0] != "devbox" {
		t.Errorf("reconnect = %q after an install on a version-mismatch host", feed.reconnect)
	}
	if !strings.Contains(m.flash, "connecting to devbox") {
		t.Errorf("flash = %q", m.flash)
	}
}

func TestInstallLeavesALiveHostOrAFailureAlone(t *testing.T) {
	fakeRemoteInstall(t, nil)
	m, feed, _ := remoteModel(t) // devbox is live
	installOn(t, m)
	if len(feed.reconnect) != 0 {
		t.Errorf("a live host was reconnected: %q", feed.reconnect)
	}
	fakeRemoteInstall(t, errors.New("sha256 mismatch"))
	m, feed, _ = remoteModel(t)
	feed.setState("devbox", remote.VersionMismatch)
	m.syncRemote()
	installOn(t, m)
	if len(feed.reconnect) != 0 {
		t.Errorf("reconnected after a failed install: %q", feed.reconnect)
	}
}
