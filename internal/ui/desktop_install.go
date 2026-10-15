package ui

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/desktop"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// Installing hypruse for desktop control is one inbox question.
// `den install-hypruse` asks over den.sock; Den makes the plan itself,
// checks the clone against the compiled pin, shows the plan in full and
// installs only on ^Y. Nothing an agent runs may ask.

// desktopPlan and desktopInstall reach the disk and the network; tests
// swap them.
var (
	desktopPlan = func(in bridge.DesktopInstallInput) (desktop.Plan, error) {
		return desktop.MakePlan(desktop.DefaultPaths(), in.Clone, in.Wheels)
	}
	desktopInstall = func(ctx context.Context, p desktop.Plan) (string, error) {
		return desktop.Install(ctx, p, desktop.Run)
	}
)

// desktopInstallDoneMsg is the end of an approved install.
type desktopInstallDoneMsg struct {
	itemID string
	got    string
	err    error
}

// handleDesktopInstallRequest refuses a caller under Claude Code, Den's
// own tools or hooks, checks the plan and puts it in the inbox.
func (m *Model) handleDesktopInstallRequest(r *bridge.Request) tea.Cmd {
	deny := func(msg string) { r.Reply(bridge.Decision{Behavior: "deny", Message: msg}) }
	if r.Host != "" {
		deny("hypruse can only be installed from this machine")
		return nil
	}
	if why := pairCaller(*m, r.PeerPID); why != "" {
		deny("refused: " + why + ". Run den install-hypruse in a terminal of your own.")
		return nil
	}
	var in bridge.DesktopInstallInput
	if json.Unmarshal(r.Payload, &in) != nil {
		deny("unreadable request")
		return nil
	}
	for _, it := range m.store.Items() {
		if it.Kind == inbox.DesktopInstall && it.State == inbox.Waiting {
			deny("a hypruse install is already waiting in Den's inbox")
			return nil
		}
	}
	plan, err := desktopPlan(in)
	if err != nil {
		deny(sanitize.Line(err.Error()))
		return nil
	}
	title := "Install hypruse " + desktop.Pinned.Version + " for desktop control?"
	body := strings.Split(sanitize.Text(plan.Text()), "\n")
	if in.AskedBy != "" {
		body = append(body, "asked by "+sanitize.Label(in.AskedBy, 120))
	}
	it := m.store.AddDesktopInstall(title, body, r)
	if m.desktopInstalls == nil {
		m.desktopInstalls = map[string]desktop.Plan{}
	}
	m.desktopInstalls[it.ID] = plan
	m.flash, m.flashErr = title+" ^Y installs, ^N declines.", false
	m.refresh()
	return nil
}

// decideDesktopInstall is ^Y or ^N on the install question.
func (m Model) decideDesktopInstall(it *inbox.Item, verb string) (tea.Model, tea.Cmd) {
	if why := deskTTYOnly(it, verb); why != "" {
		return m.say(why, true)
	}
	plan, ok := m.desktopInstalls[it.ID]
	if !ok {
		m.store.Settle(it, false, "the plan was lost; nothing installed")
		return m.say("That install is stale; nothing was installed.", true)
	}
	delete(m.desktopInstalls, it.ID)
	if verb != "allow" {
		m.store.Settle(it, false, "declined in Den; nothing installed")
		m.refresh()
		return m.say("Declined the hypruse install.", false)
	}
	it.Resolved = "installing…"
	id := it.ID
	cmd := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		got, err := desktopInstall(ctx, plan)
		return desktopInstallDoneMsg{itemID: id, got: got, err: err}
	}
	model, say := m.say("Installing hypruse…", false)
	return model, tea.Batch(say, cmd)
}

// finishDesktopInstall settles the question with what happened, which also
// answers the command that asked.
func (m *Model) finishDesktopInstall(msg desktopInstallDoneMsg) tea.Cmd {
	outcome := "installed " + sanitize.Line(msg.got)
	if msg.err != nil {
		outcome = "approved, but the install failed: " + sanitize.Line(msg.err.Error())
	}
	for _, it := range m.store.Items() {
		if it.ID == msg.itemID {
			m.store.Settle(it, msg.err == nil, outcome)
		}
	}
	m.flash, m.flashErr = strings.ToUpper(outcome[:1])+outcome[1:], msg.err != nil
	m.refresh()
	return flashTick(4*time.Second, m.flash)
}
