package ui

import (
	"context"
	"errors"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/desktop"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// A desktop card installs hypruse itself when it must: when Den's venv is
// missing, or no longer as Den recorded it, the card says what Den will
// install (or reinstall) and ^Y installs first, then starts. Den fetches
// the pinned commit from GitHub and checks every file against the hashes
// it was built with (desktop.FetchAndInstall); the dependencies come from
// PyPI, each pinned by hash. Only the human at Den's own window answers
// the card, as ever.

// deskSetup is the install a card does before it starts.
type deskSetup struct {
	reinstall bool
	why       string // what Verify found, sanitised, for a reinstall
	python    string // the interpreter the venv is made from
	note      string // why that interpreter is not the system's, "" when it is
}

// Seams for tests.
var (
	deskFindPython   = desktop.FindPython
	deskFetchInstall = func(ctx context.Context) (string, error) {
		return desktop.FetchAndInstall(ctx, desktop.DefaultPaths(), desktop.Pinned, desktop.Run)
	}
)

// setupFor is the install a card needs after Verify failed with verr, or
// why Den cannot offer one.
func setupFor(verr error) (*deskSetup, string) {
	py, note, err := deskFindPython()
	if err != nil {
		return nil, "desktop control needs hypruse, and Den cannot install it: " + sanitize.Line(err.Error())
	}
	s := &deskSetup{python: py, note: note}
	if !errors.Is(verr, desktop.ErrNotInstalled) {
		s.reinstall, s.why = true, sanitize.Label(verr.Error(), 240)
	}
	return s, ""
}

// lines are what the card says about the install.
func (s *deskSetup) lines() []string {
	pin := desktop.Pinned
	steps := fmt.Sprintf("it downloads hypruse %s (%s, commit %.7s, MIT) and checks every file against the SHA-256 Den was built with, makes a venv from %s, and installs %d packages from PyPI, each pinned by hash (wheels only). Nothing of it runs until the agent starts.",
		pin.Version, pin.URL, pin.Commit, s.python, desktop.LockPackages(desktop.Lock))
	out := []string{"Den installs hypruse first: " + steps}
	if s.reinstall {
		out = []string{"Den's hypruse is not as installed (" + s.why + "), so Den reinstalls it first: it removes that venv, then " + steps}
	}
	if s.note != "" {
		out = append(out, "Note on that interpreter: "+s.note+".")
	}
	return out
}

// deskSetupDoneMsg is the end of a card's install.
type deskSetupDoneMsg struct {
	itemID string
	offer  deskOffer
	got    string
	err    error
}

// installThenStart is ^Y on a card that installs first: the card says it
// is installing until the install ends, and a second ^Y changes nothing.
func (m Model) installThenStart(it *inbox.Item, offer deskOffer) (tea.Model, tea.Cmd) {
	if m.desk.installing != "" {
		m.store.Settle(it, false, "approved, but another hypruse install is running; nothing started")
		m.refresh()
		return m.say("Another hypruse install is running; ask again when it is done.", true)
	}
	m.store.Start(it, "installing hypruse…", 15*time.Minute)
	id := it.ID
	m.desk.installing = id
	cmd := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		got, err := deskFetchInstall(ctx)
		return deskSetupDoneMsg{itemID: id, offer: offer, got: got, err: err}
	}
	model, say := m.say("Installing hypruse, then starting…", false)
	return model, tea.Batch(say, cmd)
}

// finishSetup goes on to the start once hypruse is installed and checks
// out, or settles the card with why not.
func (m Model) finishSetup(msg deskSetupDoneMsg) (tea.Model, tea.Cmd) {
	if m.desk.installing == msg.itemID {
		m.desk.installing = ""
	}
	var it *inbox.Item
	for _, x := range m.store.Items() {
		if x.ID == msg.itemID {
			it = x
		}
	}
	if it == nil || it.State != inbox.Waiting {
		return m, nil
	}
	err := msg.err
	if err == nil {
		err = deskVerify()
	}
	if err != nil {
		why := "installing hypruse failed: " + sanitize.Line(err.Error())
		m.store.Settle(it, false, "approved, but "+why+"; nothing started")
		m.refresh()
		return m.say("The desktop mission did not start: "+why, true)
	}
	m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: "Installed " + sanitize.Line(msg.got) + ".", at: time.Now()}))
	offer := msg.offer
	offer.setup = nil
	m.store.Start(it, "starting…", 10*time.Minute)
	if offer.task != "" {
		return m.startTask(msg.itemID, offer)
	}
	id := msg.itemID
	model, say := m.say("Installed hypruse. Preparing the desktop mission…", false)
	return model, tea.Batch(say, func() tea.Msg { return prepareMission(id, offer) })
}
