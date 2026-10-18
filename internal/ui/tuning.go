package ui

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/config"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/sanitize"
	"github.com/GlacierHubAB/den/internal/spawn"
	"github.com/GlacierHubAB/den/internal/templates"
	"github.com/GlacierHubAB/den/internal/tuning"
)

// The orchestrator's tuning: a project's process numbers and template
// defaults, changed through tune_process within bounds the tuning package
// keeps, each change a notice card the human may undo in Den. A value the
// human set in config.toml always wins.

// tuneState keeps each project's tuning, read once per Den run, and what
// each card changed.
type tuneState struct {
	files    map[string]*tuning.File // by repo id
	humanSet map[string]bool         // the [team] keys config.toml sets
	cards    map[string]tuneCard     // by inbox item id
}

type tuneCard struct {
	repo    string
	changes []tuning.Change
}

// tuneReasonMax caps the orchestrator's reason on a card.
const tuneReasonMax = 200

// tuneStateDir is Den's state folder; tests point it elsewhere.
var tuneStateDir = tuning.StateDir

// tuneHumanSet is the [team] keys the human set; tests replace it.
var tuneHumanSet = config.TeamKeysSet

// tuneFile is the project's tuning, read the first time Den needs it; a
// note in Den's chat says what it left out.
func (m *Model) tuneFile(repo string) *tuning.File {
	if m.tune.files == nil {
		m.tune.files = map[string]*tuning.File{}
	}
	if m.tune.humanSet == nil {
		m.tune.humanSet = tuneHumanSet()
	}
	if f := m.tune.files[repo]; f != nil {
		return f
	}
	f, notes := tuning.Load(tuning.Path(tuneStateDir(), repo))
	for _, n := range notes {
		m.logEntry(m.addUnthreaded(chatEntry{role: "note", at: time.Now(), text: "Den: " + n + " (" + sanitize.Label(repo, 60) + ")"}))
	}
	m.tune.files[repo] = &f
	return &f
}

// teamFor is the [team] settings for a repository: the config's, with the
// orchestrator's tuning for each key the human did not set. It reads only
// what tuneFile already loaded (the team read and the tools load it).
func (m Model) teamFor(repo string) (max, branchHours, behind, idle int) {
	t := m.cfg.Team
	max, branchHours, behind, idle = t.MaxAgents, t.BranchHours, t.BehindMax, t.IdleMinutes
	f := m.tune.files[repo]
	if repo == "" || f == nil {
		return
	}
	for key, dst := range map[string]*int{"team.max_agents": &max, "team.branch_hours": &branchHours, "team.behind_max": &behind, "team.idle_minutes": &idle} {
		if v, ok := f.Team[key]; ok && !m.tune.humanSet[key] {
			*dst = v
		}
	}
	return
}

type tuneMsg struct {
	r          *bridge.Request
	in         bridge.TuneInput
	repo, name string
	err        error
}

// handleTune reads the project's repository off the loop, then applies.
// Only the orchestrator's den mcp reaches here (app.go checks its pid).
func (m *Model) handleTune(r *bridge.Request) tea.Cmd {
	var in bridge.TuneInput
	if json.Unmarshal(r.Envelope.Payload, &in) != nil || len(in.Changes) == 0 {
		r.Reply(bridge.Decision{Behavior: "deny", Message: "give dir, changes and reason"})
		return nil
	}
	dir, err := spawn.ExpandDir(in.Dir)
	if err != nil {
		r.Reply(bridge.Decision{Behavior: "deny", Message: "no such directory"})
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), teamScanTimeout)
		defer cancel()
		repo, name, err := teamRepo(ctx, dir)
		return tuneMsg{r: r, in: in, repo: repo, name: name, err: err}
	}
}

// applyTune checks every change against its bounds, applies all or none,
// saves, and puts the card to the human.
func (m *Model) applyTune(msg tuneMsg, now time.Time) {
	r := msg.r
	deny := func(why string) { r.Reply(bridge.Decision{Behavior: "deny", Message: why + "; nothing was changed"}) }
	if msg.err != nil || msg.repo == "" {
		deny("the directory is not in a git repository")
		return
	}
	project := strconv.Quote(sanitize.Label(msg.name, 40))
	reason := sanitize.Label(strings.TrimSpace(msg.in.Reason), tuneReasonMax)
	if reason == "" {
		deny("give reason, one line on why")
		return
	}
	session := "tune:" + msg.repo
	for _, c := range msg.in.Changes {
		if c.Key != tuning.ProposalKey {
			continue
		}
		if len(msg.in.Changes) != 1 {
			deny("send prompt.proposal alone")
			return
		}
		text, err := tuning.ProposalText(c.Value)
		if err != nil {
			deny(err.Error())
			return
		}
		body := []string{"The orchestrator proposes this change to its own instructions. Den applies nothing from it, and it reaches no prompt: if you want it, make the change yourself.",
			"Why, in its words: " + strconv.Quote(reason), "──── the proposal, as sent ────"}
		for _, l := range strings.Split(text, "\n") {
			body = append(body, sanitize.Line(l))
		}
		body = append(body, "──── end ────")
		m.store.AddPromptProposal(session, "The orchestrator proposes a change to its instructions", body)
		m.refresh()
		r.Reply(bridge.Decision{Behavior: "allow", Message: "shown to the human in Den; Den applies nothing from it"})
		return
	}
	cur := m.tuneFile(msg.repo)
	f := cloneTuning(*cur)
	max, _, _, _ := m.teamFor(msg.repo)
	list, _ := templateList()
	ctx := tuning.Context{MaxAgents: max, Template: func(name string) (templates.Template, bool) { return templates.Find(list, name) }}
	var changes []tuning.Change
	for _, c := range msg.in.Changes {
		if strings.HasPrefix(c.Key, "team.") && m.humanSetKey(c.Key) {
			deny(c.Key + " is set in the human's config, which always wins")
			return
		}
		ch, err := f.Apply(c.Key, c.Value, ctx, now)
		if err != nil {
			deny(err.Error())
			return
		}
		changes = append(changes, ch)
	}
	if err := tuning.Save(tuning.Path(tuneStateDir(), msg.repo), f); err != nil {
		deny("Den could not save it: " + sanitize.Label(err.Error(), 120))
		return
	}
	*cur = f
	body := []string{"Already in force. ^Y keeps it, ^N undoes it.", "Why, in the orchestrator's words: " + strconv.Quote(reason)}
	var said []string
	for _, ch := range changes {
		line := changeLine(ch)
		body = append(body, line)
		said = append(said, line)
	}
	it := m.store.AddProcessTuned(session, "The orchestrator tuned "+project, body)
	if m.tune.cards == nil {
		m.tune.cards = map[string]tuneCard{}
	}
	m.tune.cards[it.ID] = tuneCard{repo: msg.repo, changes: changes}
	m.logEntry(m.addUnthreaded(chatEntry{role: "note", at: now, text: "The orchestrator tuned " + project + ": " + strings.Join(said, "; ") + ". It is in force; keep or undo it in the Inbox."}))
	m.refresh()
	r.Reply(bridge.Decision{Behavior: "allow", Message: "in force: " + strings.Join(said, "; ") + ". The human sees it in Den and may undo it."})
}

func (m Model) humanSetKey(key string) bool { return m.tune.humanSet[key] }

// changeLine is one change as the card and the chat say it: whole, its
// values quoted, a model change with the way its cost moves.
func changeLine(ch tuning.Change) string {
	key := sanitize.Label(ch.Key, 80)
	switch {
	case strings.HasSuffix(ch.Key, ".add_criterion"), strings.HasSuffix(ch.Key, ".add_constraint"), strings.HasSuffix(ch.Key, ".add_role"):
		return key + ": adds " + strconv.Quote(ch.New)
	case strings.Contains(ch.Key, ".model."):
		return key + ": " + tuning.CostWord(ch.Old, ch.New)
	}
	old := ch.Old
	if !ch.Had {
		old = "the config's"
	}
	return key + ": " + strconv.Quote(old) + " → " + strconv.Quote(ch.New)
}

// decideTune is the human's ^Y (keep) or ^N (undo) on a tuning card, in
// Den only. Undo restores each value as it was, once.
func (m Model) decideTune(it *inbox.Item, verb string) (tea.Model, tea.Cmd) {
	c, ok := m.tune.cards[it.ID]
	if !ok {
		m.store.Settle(it, verb == "allow", "noted")
		m.refresh()
		return m.say("That change is from before Den restarted; Den no longer holds what it was.", true)
	}
	delete(m.tune.cards, it.ID)
	if verb == "allow" {
		m.store.Settle(it, true, "kept")
		m.refresh()
		return m, nil
	}
	cur := m.tuneFile(c.repo)
	f := cloneTuning(*cur)
	for i := len(c.changes) - 1; i >= 0; i-- {
		f.Undo(c.changes[i])
	}
	if err := tuning.Save(tuning.Path(tuneStateDir(), c.repo), f); err != nil {
		m.tune.cards[it.ID] = c
		return m.say("Den could not undo it: "+err.Error(), true)
	}
	*cur = f
	m.store.Settle(it, false, "undone")
	m.refresh()
	return m.say("Undone: the values are as they were before.", false)
}

// decideProposal is the human's answer to a prompt proposal: it only
// leaves the inbox.
func (m Model) decideProposal(it *inbox.Item, verb string) (tea.Model, tea.Cmd) {
	m.store.Settle(it, verb == "allow", "read")
	m.refresh()
	return m, nil
}

func cloneTuning(f tuning.File) tuning.File {
	raw, _ := json.Marshal(f)
	var out tuning.File
	json.Unmarshal(raw, &out)
	return out
}
