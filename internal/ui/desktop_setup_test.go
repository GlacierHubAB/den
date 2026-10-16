package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/desktop"
	"github.com/GlacierHubAB/den/internal/inbox"
)

// setupRig makes hypruse missing (or broken with verr) until the fake
// install runs, which then succeeds or fails with installErr. It counts
// the installs.
func setupRig(t *testing.T, verr, installErr error) *int {
	t.Helper()
	installed := false
	oldPy, oldFetch := deskFindPython, deskFetchInstall
	t.Cleanup(func() { deskFindPython, deskFetchInstall = oldPy, oldFetch })
	deskFindPython = func() (string, string, error) { return "/usr/bin/python3.14", "", nil }
	deskVerify = func() error {
		if installed {
			return nil
		}
		return verr
	}
	n := 0
	deskFetchInstall = func(context.Context) (string, error) {
		n++
		if installErr != nil {
			return "", installErr
		}
		installed = true
		return "hypruse 0.12.1 in /data/den/hypruse/venv (812 files recorded)", nil
	}
	return &n
}

// answerSetupCard presses ^Y on the mission card and returns the install
// step's message, without running it.
func answerSetupCard(t *testing.T, m *Model) (*inbox.Item, []any) {
	t.Helper()
	it := desktopCard(*m, inbox.DesktopMission)
	if it == nil {
		t.Fatal("no card")
	}
	m.tab = tabInbox
	m.selectItem(it)
	mm, cmd := m.answer("allow")
	*m = mm.(Model)
	var msgs []any
	for _, msg := range runBatch(cmd) {
		msgs = append(msgs, msg)
	}
	return it, msgs
}

// Not installed: the card says Den installs it first, from where and how
// checked; ^Y installs (the card says so, with no keys, and a second ^Y
// changes nothing), then the mission starts.
func TestDesktopCardInstallsFirst(t *testing.T) {
	m, spawned, _ := missionRig(t, false)
	n := setupRig(t, desktop.ErrNotInstalled, nil)
	r := missionReq("editor")
	runBatch(m.handleRequest(r))
	it := desktopCard(*m, inbox.DesktopMission)
	if it == nil {
		t.Fatal("no card: Den should offer to install")
	}
	body := strings.Join(it.Body, "\n")
	for _, want := range []string{"Den installs hypruse first", desktop.Pinned.URL, "commit " + desktop.Pinned.Commit[:7], "SHA-256", "/usr/bin/python3.14", "pinned by hash"} {
		if !strings.Contains(body, want) {
			t.Errorf("card lacks %q:\n%s", want, body)
		}
	}
	_, msgs := answerSetupCard(t, m)
	var done *deskSetupDoneMsg
	for _, msg := range msgs {
		if d, ok := msg.(deskSetupDoneMsg); ok {
			done = &d
		}
	}
	if done == nil || *n != 1 {
		t.Fatalf("no install ran (%d)", *n)
	}
	// Before the install's end is taken: the card is installing.
	if m.desk.installing != it.ID {
		t.Fatalf("installing %q", m.desk.installing)
	}
	card := ansi.Strip(strings.Join(m.inboxBlock(it, true, 120), "\n"))
	if !strings.Contains(card, "installing hypruse") || strings.Contains(card, "start it") {
		t.Errorf("the card during the install:\n%s", card)
	}
	mm, cmd := m.answer("allow")
	*m = mm.(Model)
	runBatch(cmd)
	if *n != 1 || !strings.Contains(m.flash, "still installing") {
		t.Fatalf("a second ^Y: %d installs, flash %q", *n, m.flash)
	}
	if _, decided := r.Decided(); decided {
		t.Fatal("the asker was answered during the install")
	}
	// A new desktop request meanwhile is refused.
	r2 := taskReq("open the notes")
	runBatch(m.handleRequest(r2))
	if d, _ := r2.Decided(); d.Behavior != "deny" || !strings.Contains(d.Message, "installing now") {
		t.Fatalf("a request during the install: %+v", d)
	}
	// The install ends: the mission starts as it would have.
	mm, cmd = m.finishSetup(*done)
	*m = mm.(Model)
	for _, msg := range runBatch(cmd) {
		if p, ok := msg.(deskPrepared); ok {
			for _, msg := range runBatch(m.finishPrepare(p)) {
				if s, ok := msg.(deskSpawned); ok {
					m.finishSpawn(s)
				}
			}
		}
	}
	if d, done := r.Decided(); !done || d.Behavior != "allow" || len(*spawned) != 1 || m.desk.installing != "" {
		t.Fatalf("after the install: %+v, %d spawns, installing %q", d, len(*spawned), m.desk.installing)
	}
}

// Not as installed (a planted .pyc, say): the card says what is wrong and
// that Den reinstalls; a task goes on to its handshake once installed.
func TestDesktopCardReinstallsThenStartsATask(t *testing.T) {
	m, d, spawned, _, _ := taskRig(t)
	n := setupRig(t, fmt.Errorf("%w: lib/python3.14/site-packages/hypruse/__pycache__/__init__.cpython-314.pyc was added", desktop.ErrNotAsInstalled), nil)
	r := taskReq("tidy my downloads folder")
	runBatch(m.handleRequest(r))
	it := desktopCard(*m, inbox.DesktopMission)
	if it == nil {
		t.Fatal("no card")
	}
	body := strings.Join(it.Body, "\n")
	if !strings.Contains(body, "is not as installed") || !strings.Contains(body, "__init__.cpython-314.pyc was added") || !strings.Contains(body, "Den reinstalls it first") {
		t.Fatalf("card:\n%s", body)
	}
	_, msgs := answerSetupCard(t, m)
	for _, msg := range msgs {
		done, ok := msg.(deskSetupDoneMsg)
		if !ok {
			continue
		}
		mm, cmd := m.finishSetup(done)
		*m = mm.(Model)
		d.title = m.View().WindowTitle
		if !strings.HasPrefix(d.title, ownTitlePrefix) {
			t.Fatalf("no handshake after the install: title %q", d.title)
		}
		for _, msg := range runBatch(cmd) {
			own, ok := msg.(deskOwnMsg)
			if !ok {
				continue
			}
			next := m.finishOwn(own)
			d.title = m.View().WindowTitle
			for _, msg := range runBatch(next) {
				if p, ok := msg.(deskPrepared); ok {
					for _, msg := range runBatch(m.finishPrepare(p)) {
						if s, ok := msg.(deskSpawned); ok {
							m.finishSpawn(s)
						}
					}
				}
			}
		}
	}
	if dv, done := r.Decided(); !done || dv.Behavior != "allow" || *n != 1 || len(*spawned) != 1 {
		t.Fatalf("asker %+v, installs %d, spawns %d", dv, *n, len(*spawned))
	}
}

// A failed install settles the card with the reason; nothing starts.
func TestDesktopCardInstallFails(t *testing.T) {
	m, spawned, _ := missionRig(t, false)
	setupRig(t, desktop.ErrNotInstalled, errors.New("download hypruse: src/hypruse/server.py is not the pinned file; nothing was installed"))
	r := missionReq("editor")
	runBatch(m.handleRequest(r))
	_, msgs := answerSetupCard(t, m)
	for _, msg := range msgs {
		if done, ok := msg.(deskSetupDoneMsg); ok {
			mm, cmd := m.finishSetup(done)
			*m = mm.(Model)
			runBatch(cmd)
		}
	}
	d, done := r.Decided()
	if !done || d.Behavior != "deny" || !strings.Contains(d.Message, "not the pinned file") || len(*spawned) != 0 || m.desk.installing != "" {
		t.Fatalf("asker %+v, %d spawns, installing %q", d, len(*spawned), m.desk.installing)
	}
}
