package ui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/names"
	"github.com/GlacierHubAB/den/internal/transcript"
	"github.com/GlacierHubAB/den/internal/worktree"
)

// An agent that works in a git worktree has a branch to merge. Merging is
// hard to reverse, so /merge never merges: it checks everything and puts
// one question in the inbox (the branch, the branch it goes into, the
// commits, the files, and whether it fast-forwards or conflicts). ^Y does
// the merge after checking again; ^N drops it. Removing the worktree
// afterwards is a second, separate question (/cleanup). "Send back" is the
// review /send that already exists. Den never merges or removes anything
// on its own, and never removes a worktree with uncommitted work.

// wtOffer is the plan behind a Merge or Cleanup question.
type wtOffer struct {
	merge   *worktree.MergePlan
	cleanup *worktree.CleanupPlan
}

// gitTimeout bounds one planning or merging step.
const gitTimeout = 20 * time.Second

// workFor is the work a worktree command acts on: the selected row in
// Review, or the agent named in args.
func (m Model) workFor(args []string) (*work, string) {
	name := strings.TrimSpace(strings.Join(args, " "))
	if name == "" {
		if m.tab != tabReview {
			return nil, "Open Review and select the agent first, or name it: /merge <agent>."
		}
		if w := m.curWork(); w != nil {
			return w, ""
		}
		return nil, "No agent is selected in Review."
	}
	// The agents with work in Review, named the way every command names
	// one (names.Resolve): two that fit is neither.
	var agents []fleet.Agent
	for _, w := range m.rev.works {
		if a, ok := m.agentFor(w.SessionID); ok {
			agents = append(agents, a)
		}
	}
	a, err := m.resolveAgent(name, agents, names.Opts{})
	if err != nil && !errors.Is(err, names.ErrNoMatch) {
		return nil, err.Error() + "."
	}
	if err == nil {
		for i := range m.rev.works {
			if w := &m.rev.works[i]; w.SessionID == a.Session.SessionID {
				return w, ""
			}
		}
	}
	return nil, "No agent called " + name + " has anything in Review."
}

// remoteOnly is why a worktree command refuses remote work: Den merges and
// removes worktrees on this machine only.
func remoteOnly(cmd, name, host string) string {
	return cmd + " works on this machine only, and " + name + "'s checkout is on " + host + ". Ask the agent to do it there: /send it a comment."
}

// worktreeLive reports whether a live agent works inside dir: its folder
// is there, or its session recorded dir as its worktree.
func (m Model) worktreeLive(dir string) bool {
	for _, a := range m.base {
		if a.Status == fleet.Gone {
			continue
		}
		if cwd := a.Session.CWD; cwd != "" && (samePath(cwd, dir) || strings.HasPrefix(cwd, dir+string(filepath.Separator))) {
			return true
		}
		if a.Path != "" {
			if st, ok := transcript.ReadWorktree(a.Path); ok && samePath(st.Path, dir) {
				return true
			}
		}
	}
	return false
}

// openWtItem is the open question of a kind about a worktree, if any.
func (m Model) openWtItem(kind inbox.Kind, dir string) *inbox.Item {
	for _, it := range m.store.Items() {
		if it.Kind != kind || it.State != inbox.Waiting {
			continue
		}
		o := m.wt[it.ID]
		switch {
		case kind == inbox.Merge && o.merge != nil && o.merge.Worktree == dir:
			return it
		case kind == inbox.Cleanup && o.cleanup != nil && o.cleanup.Worktree == dir:
			return it
		}
	}
	return nil
}

func (m *Model) rememberWt(it *inbox.Item, o wtOffer) {
	if m.wt == nil {
		m.wt = map[string]wtOffer{}
	}
	m.wt[it.ID] = o
}

// showQuestion puts the inbox on screen with the question selected, so ^Y
// answers that question and not another one waiting elsewhere.
func (m *Model) showQuestion(it *inbox.Item) {
	m.refresh()
	m.selectItem(it)
	m.tab = tabInbox
}

// mergeCommand is /merge: ask whether to merge the selected agent's branch.
func (m Model) mergeCommand(args []string) (tea.Model, tea.Cmd) {
	w, why := m.workFor(args)
	if w == nil {
		return m.say(why, true)
	}
	if w.Host != "" {
		return m.say(remoteOnly("/merge", m.nameFor(w.SessionID), w.Host), true)
	}
	if !w.Worktree {
		return m.say(m.nameFor(w.SessionID)+" does not work in a git worktree, so there is no branch of its own to merge.", true)
	}
	if it := m.openWtItem(inbox.Merge, w.Root); it != nil {
		m.showQuestion(it)
		return m.say("That question is already waiting: ^Y merges, ^N drops it.", false)
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	plan, err := worktree.Plan(ctx, w.Root)
	if err != nil {
		return m.say("Not merging "+w.Branch+": "+err.Error(), true)
	}
	it := m.store.AddMerge(w.SessionID, "Merge "+plan.Branch+" into "+plan.Target+"?", plan.Describe())
	m.rememberWt(it, wtOffer{merge: &plan})
	m.showQuestion(it)
	if len(plan.Conflicts) > 0 {
		return m.say("It would conflict, so ^Y will refuse. /send the agent a comment to resolve it, or ^N.", true)
	}
	return m.say("^Y merges "+plan.Branch+" into "+plan.Target+", ^N drops the question.", false)
}

// decideMerge is ^Y or ^N on a merge question.
func (m Model) decideMerge(it *inbox.Item, verb string) (tea.Model, tea.Cmd) {
	o, ok := m.wt[it.ID]
	if !ok || o.merge == nil {
		m.store.Settle(it, false, "the plan was lost; nothing merged")
		m.refresh()
		return m.say("That merge question is stale; nothing was merged. Ask again with /merge.", true)
	}
	plan := *o.merge
	if verb != "allow" {
		m.store.Settle(it, false, "declined; nothing merged")
		delete(m.wt, it.ID)
		m.refresh()
		return m.say("Not merging "+plan.Branch+".", false)
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	if err := worktree.Merge(ctx, plan); err != nil {
		if worktree.IsMoved(err) {
			m.store.Settle(it, false, "not merged: "+err.Error())
			delete(m.wt, it.ID)
			m.refresh()
			return m.say(err.Error()+" (/merge asks again)", true)
		}
		// The question stays: the human can fix what is in the way and
		// press ^Y again, or drop it with ^N.
		return m.say("Not merged: "+err.Error(), true)
	}
	how := "merged"
	if plan.FastForward {
		how = "fast-forwarded"
	}
	m.store.Settle(it, true, how+" "+plan.Branch+" into "+plan.Target)
	delete(m.wt, it.ID)
	m.refresh()
	reload := m.loadReview()
	// The follow-up is a question, never an action.
	note := "Merged " + plan.Branch + " into " + plan.Target + "."
	cctx, ccancel := context.WithTimeout(context.Background(), gitTimeout)
	defer ccancel()
	cp, err := worktree.CleanupPlanFor(cctx, plan.Worktree, m.worktreeLive)
	if err != nil {
		mm, cmd := m.say(note+" The worktree stays: "+err.Error()+". /cleanup asks again.", false)
		return mm, tea.Batch(cmd, reload)
	}
	cit := m.store.AddCleanup(it.SessionID, "Remove the worktree of "+plan.Branch+"?", cp.Describe())
	m.rememberWt(cit, wtOffer{cleanup: &cp})
	m.showQuestion(cit)
	mm, cmd := m.say(note+" ^Y removes its worktree, ^N keeps it.", false)
	return mm, tea.Batch(cmd, reload)
}

// cleanupCommand is /cleanup: ask whether to remove the selected agent's
// worktree.
func (m Model) cleanupCommand(args []string) (tea.Model, tea.Cmd) {
	w, why := m.workFor(args)
	if w == nil {
		return m.say(why, true)
	}
	if w.Host != "" {
		return m.say(remoteOnly("/cleanup", m.nameFor(w.SessionID), w.Host), true)
	}
	if !w.Worktree {
		return m.say(m.nameFor(w.SessionID)+" does not work in a git worktree.", true)
	}
	if it := m.openWtItem(inbox.Cleanup, w.Root); it != nil {
		m.showQuestion(it)
		return m.say("That question is already waiting: ^Y removes it, ^N keeps it.", false)
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cp, err := worktree.CleanupPlanFor(ctx, w.Root, m.worktreeLive)
	if err != nil {
		return m.say("Not removing it: "+err.Error(), true)
	}
	it := m.store.AddCleanup(w.SessionID, "Remove the worktree of "+firstNonEmptyS(cp.Branch, filepath.Base(cp.Worktree))+"?", cp.Describe())
	m.rememberWt(it, wtOffer{cleanup: &cp})
	m.showQuestion(it)
	return m.say("^Y removes the worktree, ^N keeps it.", false)
}

// decideCleanup is ^Y or ^N on a cleanup question.
func (m Model) decideCleanup(it *inbox.Item, verb string) (tea.Model, tea.Cmd) {
	o, ok := m.wt[it.ID]
	if !ok || o.cleanup == nil {
		m.store.Settle(it, false, "the plan was lost; nothing removed")
		m.refresh()
		return m.say("That question is stale; nothing was removed. Ask again with /cleanup.", true)
	}
	plan := *o.cleanup
	if verb != "allow" {
		m.store.Settle(it, false, "kept the worktree")
		delete(m.wt, it.ID)
		m.refresh()
		return m.say("Keeping "+plan.Worktree+".", false)
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	if err := worktree.Cleanup(ctx, plan, m.worktreeLive); err != nil {
		if worktree.IsMoved(err) {
			m.store.Settle(it, false, "not removed: "+err.Error())
			delete(m.wt, it.ID)
			m.refresh()
			return m.say(err.Error()+" (/cleanup asks again)", true)
		}
		return m.say("Not removed: "+err.Error(), true)
	}
	out := "removed the worktree"
	if plan.Branch != "" && plan.Merged {
		out += " and deleted " + plan.Branch
	}
	m.store.Settle(it, true, out)
	delete(m.wt, it.ID)
	m.refresh()
	reload := m.loadReview()
	mm, cmd := m.say(strings.ToUpper(out[:1])+out[1:]+".", false)
	return mm, tea.Batch(cmd, reload)
}
