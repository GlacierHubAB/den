package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/sanitize"
	"github.com/GlacierHubAB/den/internal/skills"
	"github.com/GlacierHubAB/den/internal/spawn"
)

// Skills offers. The orchestrator (propose_skills, or spawn_agent with a
// role) or a lead proposes; Den matches the role against its own vetted
// catalogue again and puts the offer in the inbox, each skill with its
// repository, pinned commit and license. ^Y downloads, verifies and
// installs them and records their provenance; ^N declines. The proposer's
// request waits on that answer.

// skillsDoneMsg is the end of an install.
type skillsDoneMsg struct {
	itemID   string
	outcomes []skills.Outcome
}

// skillsInstall carries out an approval; tests replace it.
var skillsInstall = func(ctx context.Context, a skills.Approval) []skills.Outcome {
	return skills.Install(ctx, a, skills.ProvenancePath())
}

// approver is who pressed ^Y, for the provenance record.
func approver() string {
	who := os.Getenv("USER")
	if who == "" {
		who = "the human"
	}
	return who + " in Den (^Y)"
}

// skillsTarget checks where an offer would install. A project must be an
// existing folder; an agent's folder must be a new one inside Den's own
// state, so a request can never point Den at another place.
func skillsTarget(in bridge.SkillsInput) (skills.Target, error) {
	switch skills.Scope(in.Scope) {
	case skills.Project:
		dir, err := spawn.ExpandDir(in.Dir)
		if err != nil {
			return skills.Target{}, err
		}
		return skills.Target{Scope: skills.Project, Dir: dir, Root: dir}, nil
	case skills.Agent:
		base := filepath.Join(skills.StateDir(), "skills", "agents")
		root := filepath.Clean(in.Root)
		if in.Peer == "" || filepath.Dir(root) != base || !strings.HasPrefix(filepath.Base(root), in.Peer+"-") {
			return skills.Target{}, fmt.Errorf("an agent's skills folder must be a new folder for it in %s", base)
		}
		if _, err := os.Lstat(root); err == nil {
			return skills.Target{}, fmt.Errorf("%s exists already", root)
		}
		return skills.Target{Scope: skills.Agent, Dir: in.Dir, Peer: in.Peer, Root: root}, nil
	}
	return skills.Target{}, fmt.Errorf("unknown scope %q", in.Scope)
}

// handleSkillsRequest turns a proposal into an inbox question, or refuses
// it at once when nothing in the catalogue fits.
func (m *Model) handleSkillsRequest(r *bridge.Request) {
	var in bridge.SkillsInput
	_ = json.Unmarshal(r.Payload, &in)
	deny := func(msg string) { r.Reply(bridge.Decision{Behavior: "deny", Message: msg}) }
	cat, err := skills.Load()
	if err != nil {
		deny(err.Error())
		return
	}
	ms, err := cat.Pick(in.Role, in.Skills)
	if err != nil {
		deny(err.Error())
		return
	}
	if len(ms) == 0 {
		deny(fmt.Sprintf("no catalogue skill fits the role %q", in.Role))
		return
	}
	tgt, err := skillsTarget(in)
	if err != nil {
		deny(err.Error())
		return
	}
	n := fmt.Sprintf("%d skill", len(ms))
	if len(ms) > 1 {
		n += "s"
	}
	title := fmt.Sprintf("Install %s for a %s in %s?", n, in.Role, shortPath(tgt.Dir, ""))
	if tgt.Scope == skills.Agent {
		title = fmt.Sprintf("Start %s (%s) with %s?", in.Peer, in.Role, n)
		if c := m.critterOf(in.Peer); c != "" {
			title = fmt.Sprintf("Start %s (%s, %s) with %s?", c, sanitize.Label(in.Peer, 60), in.Role, n)
		}
	}
	body := skills.Lines(ms, tgt)
	asked := "asked by " + in.AskedBy
	if in.Reason != "" {
		asked += " · " + in.Reason
	}
	body = append(body, asked)
	sid := ""
	if a, ok := m.agentByPeer(in.AskedBy); ok {
		sid = a.Session.SessionID
	} else if m.orch.proc != nil {
		sid = m.orch.proc.SessionID()
	}
	it := m.store.AddSkills(sid, title, body, r)
	it.Role = in.Role
	if m.offers == nil {
		m.offers = map[string]skills.Approval{}
	}
	m.offers[it.ID] = skills.Approval{Target: tgt, Role: in.Role, Skills: ms, AskedBy: in.AskedBy}
	m.flash, m.flashErr = "Skills offer: "+title+" ^Y installs, ^N declines.", false
	m.refresh()
}

// pendingOrchSkills is the orchestrator's skills offer still waiting on the
// human, nil when there is none.
func (m Model) pendingOrchSkills() *inbox.Item {
	for _, it := range m.store.Items() {
		if it.Kind == inbox.Skills && it.Answerable() && m.offers[it.ID].AskedBy == "orchestrator" {
			return it
		}
	}
	return nil
}

// decideSkills is ^Y or ^N on a skills offer.
func (m Model) decideSkills(it *inbox.Item, verb string) (tea.Model, tea.Cmd) {
	a, ok := m.offers[it.ID]
	if !ok {
		m.store.Settle(it, false, "the offer was lost; nothing installed")
		return m.say("That skills offer is stale; nothing was installed.", true)
	}
	delete(m.offers, it.ID)
	if verb != "allow" {
		m.store.Settle(it, false, "declined in Den; nothing installed")
		m.refresh()
		return m.say("Declined the skills.", false)
	}
	a.ApprovedBy = approver()
	it.Resolved = "installing…"
	id := it.ID
	cmd := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		return skillsDoneMsg{itemID: id, outcomes: skillsInstall(ctx, a)}
	}
	model, say := m.say("Installing skills…", false)
	return model, tea.Batch(say, cmd)
}

// finishSkills settles the offer with what happened and tells the asker.
func (m *Model) finishSkills(msg skillsDoneMsg) tea.Cmd {
	summary := skills.Summary(msg.outcomes)
	installed := false
	for _, o := range msg.outcomes {
		if o.Err == nil {
			installed = true
		}
	}
	for _, it := range m.store.Items() {
		if it.ID == msg.itemID {
			if installed {
				m.store.Settle(it, true, summary)
			} else {
				m.store.Settle(it, false, "approved, but "+summary)
			}
		}
	}
	m.flash, m.flashErr = strings.ToUpper(summary[:1])+summary[1:], !installed
	m.refresh()
	return flashTick(4*time.Second, m.flash)
}
