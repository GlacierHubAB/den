package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/remote"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// Installing den on a remote host is one inbox question (decision 3 in
// docs/REMOTE-DESIGN.md). `den remote install` asks over den.sock; Den
// makes the plan itself, shows it in full, and copies the binary only on
// ^Y. A background connection never installs.

// installPlan and installRun reach the host; tests swap them.
var (
	installPlan = func(ctx context.Context, h remote.Host) (remote.Plan, error) {
		return remote.PlanInstall(ctx, remote.DefaultSSH(), h, remote.LocalVersion)
	}
	installRun = func(ctx context.Context, p remote.Plan) (string, error) {
		return remote.DefaultSSH().Install(ctx, p)
	}
	installHosts = remote.Load
)

// installPlanMsg is a plan made for an install request.
type installPlanMsg struct {
	req  *bridge.Request
	in   bridge.InstallInput
	plan remote.Plan
	err  error
}

// installDoneMsg is the end of an approved install.
type installDoneMsg struct {
	itemID string
	got    string
	err    error
}

// handleInstallRequest checks the request and makes the plan off the
// update loop: reading the host takes an ssh round trip.
func (m *Model) handleInstallRequest(r *bridge.Request) tea.Cmd {
	var in bridge.InstallInput
	_ = json.Unmarshal(r.Payload, &in)
	deny := func(msg string) { r.Reply(bridge.Decision{Behavior: "deny", Message: msg}) }
	if r.Host != "" {
		// Only a command run on this machine may ask (see bridge.InstallEvent).
		deny("an install can only be asked for from this machine")
		return nil
	}
	cfg, err := installHosts()
	if err != nil {
		deny(err.Error())
		return nil
	}
	h, ok := cfg.Find(in.Host)
	if !ok {
		deny(fmt.Sprintf("no host labelled %q in %s", in.Host, remote.Path()))
		return nil
	}
	for _, it := range m.store.Items() {
		if it.Kind == inbox.Install && it.State == inbox.Waiting && it.SessionID == inbox.HostPrefix+h.Label {
			deny("an install on " + h.Label + " is already waiting in Den's inbox")
			return nil
		}
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		plan, err := installPlan(ctx, h)
		return installPlanMsg{req: r, in: in, plan: plan, err: err}
	}
}

// offerInstall puts the plan in the inbox, or answers at once when there
// is nothing to approve.
func (m *Model) offerInstall(msg installPlanMsg) {
	deny := func(s string) { msg.req.Reply(bridge.Decision{Behavior: "deny", Message: s}) }
	switch {
	case msg.req.HungUp():
		return // the asker gave up while the host was read
	case msg.err != nil:
		deny(sanitize.Line(msg.err.Error()))
		return
	case !msg.plan.Copy:
		deny(msg.plan.Text) // the steps to install by hand; nothing for Den to do
		return
	}
	h := msg.plan.Host
	title := fmt.Sprintf("Install den %s on %s?", msg.plan.Version, h.Label)
	body := strings.Split(sanitize.Text(msg.plan.Text), "\n")
	if msg.in.AskedBy != "" {
		body = append(body, "asked by "+sanitize.Label(msg.in.AskedBy, 120))
	}
	it := m.store.AddInstall(h.Label, title, body, msg.req)
	if m.installs == nil {
		m.installs = map[string]remote.Plan{}
	}
	m.installs[it.ID] = msg.plan
	m.flash, m.flashErr = title+" ^Y installs, ^N declines.", false
	m.refresh()
}

// decideInstall is ^Y or ^N on an install question.
func (m Model) decideInstall(it *inbox.Item, verb string) (tea.Model, tea.Cmd) {
	plan, ok := m.installs[it.ID]
	if !ok {
		m.store.Settle(it, false, "the plan was lost; nothing installed")
		return m.say("That install is stale; nothing was installed.", true)
	}
	delete(m.installs, it.ID)
	if verb != "allow" {
		m.store.Settle(it, false, "declined in Den; nothing installed")
		m.refresh()
		return m.say("Declined the install.", false)
	}
	it.Resolved = "installing…"
	id := it.ID
	cmd := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		got, err := installRun(ctx, plan)
		return installDoneMsg{itemID: id, got: got, err: err}
	}
	model, say := m.say("Installing den on "+plan.Host.Label+"…", false)
	return model, tea.Batch(say, cmd)
}

// finishInstall settles the question with what happened, which also
// answers the command that asked.
func (m *Model) finishInstall(msg installDoneMsg) tea.Cmd {
	outcome := "installed: " + sanitize.Line(msg.got)
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
	return tea.Tick(4*time.Second, func(time.Time) tea.Msg { return flashClearMsg{} })
}
