package ui

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/doctor"
	"github.com/GlacierHubAB/den/internal/hooks"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/mods"
	"github.com/GlacierHubAB/den/internal/remote"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// The setup card: at start, Den reads whether Claude Code has its hooks
// and its mods (doctor.MakePlan, read-only) and, when it lacks them, asks
// once on a card. ^Y there, in Den's own window, is the only way Den writes
// them: no console, no God mode, no API verb, and no agent's or console's
// text, ever makes or answers it. ^N hides it until a newer Den.
// den-sec-review's conditions D4 to D8 are in doctor-design.md.

// setupState is the open card and what it showed.
type setupState struct {
	askedID string
	plan    doctor.Plan // what the open card says ^Y writes
	listed  []string    // its body as den-api.sock lists it, paste-safe
	// env reads this machine; install writes. Tests replace both.
	env     func() doctor.Env
	install setupInstaller
}

// setupInstaller is what ^Y calls: the same calls den install-hooks and
// den install-mods make.
type setupInstaller struct {
	hooks func(exe string) (backup string, err error)
	mod   func(name string) error
}

var liveInstaller = setupInstaller{
	hooks: hooks.Install,
	mod: func(name string) error {
		list, err := mods.List()
		if err != nil {
			return err
		}
		for _, m := range list {
			if m.Name == name {
				return mods.Install(m)
			}
		}
		return os.ErrNotExist
	},
}

func (m *Model) setupEnv() doctor.Env {
	if m.setup.env == nil {
		return doctor.System()
	}
	return m.setup.env()
}

func (m *Model) setupInstall() setupInstaller {
	if m.setup.install.hooks == nil {
		return liveInstaller
	}
	return m.setup.install
}

const setupTitle = "Install Den's hooks and mods into Claude Code?"

// startSetup is the start-up check: a card when Claude Code lacks Den's
// hooks or mods, unless ^N hid it for this Den version.
func (m *Model) startSetup() {
	e := m.setupEnv()
	if raw, err := e.ReadFile(doctor.Dismissed(e)); err == nil && strings.TrimSpace(string(raw)) == remote.LocalVersion {
		return
	}
	m.askSetup(e, doctor.MakePlan(e))
}

// askSetup puts plan on a card, in place of the one open.
func (m *Model) askSetup(e doctor.Env, plan doctor.Plan) {
	m.dropSetupCard("replaced by a newer reading")
	if !plan.Work() {
		return
	}
	it := m.store.AddSetup(setupTitle, plan.Lines(e, true))
	if it == nil {
		return
	}
	m.setup.askedID, m.setup.plan, m.setup.listed = it.ID, plan, plan.Lines(e, false)
}

func (m *Model) dropSetupCard(why string) {
	if m.setup.askedID == "" {
		return
	}
	for _, it := range m.store.Items() {
		if it.ID == m.setup.askedID && it.State != inbox.Resolved {
			m.store.Settle(it, false, why)
		}
	}
	m.setup.askedID, m.setup.listed = "", nil
}

// setupListed is the card's body as den-api.sock lists it: the paste-safe
// copy, never the real paths Den's window shows.
func (m Model) setupListed(it *inbox.Item) []string {
	if it.ID != m.setup.askedID {
		return []string{"(Den's setup card: read it in Den's window)"}
	}
	return m.setup.listed
}

// setupActs is the card's keys: no ^Y when the plan offers none.
func (m Model) setupActs(it *inbox.Item, key func(k, label string) string, sep string) string {
	if it.ID == m.setup.askedID && m.setup.plan.NoY == "" {
		return key("^Y", "install them") + sep + key("^N", "hide until a newer Den")
	}
	return key("^N", "hide until a newer Den")
}

// decideSetup is ^Y or ^N on the card, in Den's own window only.
func (m Model) decideSetup(it *inbox.Item, verb string) (tea.Model, tea.Cmd) {
	e := m.setupEnv()
	switch {
	case it.ID != m.setup.askedID:
		m.store.Settle(it, false, "the card is gone")
		m.refresh()
		return m.say("That card is gone.", true)
	case verb != "allow":
		path := doctor.Dismissed(e)
		os.MkdirAll(filepath.Dir(path), 0o700)
		if err := os.WriteFile(path, []byte(remote.LocalVersion+"\n"), 0o600); err != nil {
			return m.say("Den could not note your ^N ("+sanitize.Label(err.Error(), 160)+"): the card stays.", true)
		}
		m.setup.askedID, m.setup.listed = "", nil
		m.store.Settle(it, false, "hidden until a newer Den")
		m.refresh()
		return m.say("Hidden until a newer Den. den doctor still reports it; den install-hooks and den install-mods do it by hand.", false)
	case m.setup.plan.NoY != "":
		return m.say("This card offers no ^Y: "+m.setup.plan.NoY, true)
	}
	// D5: read it all again; anything changed since the card was shown
	// writes nothing, and the new reading goes on a card of its own.
	now := doctor.MakePlan(e)
	if !now.Same(m.setup.plan) {
		m.askSetup(e, now)
		m.refresh()
		return m.say("Something changed since that card was shown: nothing was written. Read the new card.", true)
	}
	plan, ins := m.setup.plan, m.setupInstall()
	m.setup.askedID, m.setup.listed = "", nil
	var done []string
	if plan.Hooks() {
		backup, err := ins.hooks(plan.Exe)
		if err != nil {
			m.store.Settle(it, false, "the hooks were not written")
			m.refresh()
			return m.say("Den's hooks were not written: "+sanitize.Label(err.Error(), 200), true)
		}
		m.hooksReady = true
		done = append(done, "hooks into "+plan.Settings)
		if backup != "" {
			done[len(done)-1] += " (backup: " + backup + ")"
		}
	}
	for _, md := range plan.Mods {
		if err := ins.mod(md.Name); err != nil {
			m.store.Settle(it, false, "a mod was not installed")
			m.refresh()
			return m.say(md.Name+" was not installed: "+sanitize.Label(err.Error(), 200), true)
		}
		done = append(done, md.Name+" into "+md.Target)
	}
	m.store.Settle(it, true, "installed")
	m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: "Installed, on your ^Y in Den: " + strings.Join(done, "; ") + ". Agents started from now on report here; restart the ones running now.", at: time.Now()}))
	m.refresh()
	return m.say("Installed. Agents started from now on report here.", false)
}
