package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/review"
)

// Sending review comments: /send delivers each agent's drafts as one
// message through its session socket, held by the typing guard like any
// @Name message. A single command is enough: a message is plain text, it
// cannot approve anything, and the comments stay visible as sent.

// reviewGroup is one recipient's drafts.
type reviewGroup struct {
	sessionID string
	comments  []review.Comment
}

// draftGroups gathers the drafts not already on their way, one group per
// recipient, sorted by name.
func (m Model) draftGroups() []reviewGroup {
	by := map[string]int{}
	var out []reviewGroup
	for _, c := range m.rev.comments {
		if !c.Draft() || m.rev.sending[c.ID] {
			continue
		}
		i, ok := by[c.SessionID]
		if !ok {
			i = len(out)
			by[c.SessionID] = i
			out = append(out, reviewGroup{sessionID: c.SessionID})
		}
		out[i].comments = append(out[i].comments, c)
	}
	// Lines no agent wrote last.
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].sessionID == "") != (out[j].sessionID == "") {
			return out[j].sessionID == ""
		}
		return m.nameFor(out[i].sessionID) < m.nameFor(out[j].sessionID)
	})
	return out
}

// reviewRecipient is the live session that receives comments written for
// sessionID, false when none can (the session ended or has no socket).
// Comments for an ended session could later go to its resumed session;
// this is the one place that would map it.
func (m Model) reviewRecipient(sessionID string) (fleet.Agent, bool) {
	if sessionID == "" {
		return fleet.Agent{}, false
	}
	a, ok := m.agentFor(sessionID)
	if !ok || a.Status == fleet.Gone || a.Session.Socket == "" {
		return fleet.Agent{}, false
	}
	return a, true
}

// sendReview runs /send: every reachable recipient's drafts, or one
// recipient's when a name is given; "/send orchestrator" hands the drafts
// no agent can receive to the orchestrator.
func (m Model) sendReview(args []string) (tea.Model, tea.Cmd) {
	arg := strings.TrimSpace(strings.Join(args, " "))
	switch strings.ToLower(arg) {
	case "orchestrator", "orch":
		return m.sendReviewToOrchestrator()
	}
	groups := m.draftGroups()
	if arg != "" {
		var hit []reviewGroup
		for _, g := range groups {
			if g.sessionID != "" && strings.HasPrefix(strings.ToLower(m.nameFor(g.sessionID)), strings.ToLower(arg)) {
				hit = append(hit, g)
			}
		}
		if len(hit) != 1 {
			return m.say(fmt.Sprintf("No drafts for one agent called %q.", arg), true)
		}
		groups = hit
	}
	if len(groups) == 0 {
		return m.say("No draft comments to send.", true)
	}
	var cmds []tea.Cmd
	var sent, held, kept []string
	for _, g := range groups {
		a, ok := m.reviewRecipient(g.sessionID)
		if !ok {
			kept = append(kept, m.recipientName(g.sessionID)+fmt.Sprintf(" (%d)", len(g.comments)))
			continue
		}
		text := review.Message(g.comments)
		var ids []string
		if m.rev.sending == nil {
			m.rev.sending = map[string]bool{}
		}
		for _, c := range g.comments {
			ids = append(ids, c.ID)
			m.rev.sending[c.ID] = true
		}
		m.noteCopies(ids)
		if m.atTerminal(a.Session.SessionID) {
			it := m.store.Sent(a.Session.SessionID, text, "waiting: you are at that terminal")
			m.queue = append(m.queue, outgoing{agent: a, text: text, item: it, since: time.Now(), review: ids})
			held = append(held, a.Name)
			continue
		}
		it := m.store.Sent(a.Session.SessionID, text, "sending…")
		cmds = append(cmds, deliverReview(a, text, it, ids))
		sent = append(sent, a.Name)
	}
	if len(held) > 0 {
		cmds = append(cmds, holdTick())
	}
	m.refresh()
	var note []string
	if len(sent) > 0 {
		note = append(note, "Sending review to "+strings.Join(sent, ", ")+".")
	}
	if len(held) > 0 {
		note = append(note, "Holding the review for "+strings.Join(held, ", ")+" while you are at that terminal.")
	}
	if len(kept) > 0 {
		note = append(note, "Kept as drafts, no live session: "+strings.Join(kept, ", ")+". /send orchestrator hands them on.")
	}
	mm, flash := m.say(strings.Join(note, " "), len(sent)+len(held) == 0)
	return mm, tea.Batch(append(cmds, flash)...)
}

// sendReviewToOrchestrator hands every draft no live session can receive
// to the orchestrator in one turn, so it can pass them to an agent that
// can act on them. They count as sent once handed over.
func (m Model) sendReviewToOrchestrator() (tea.Model, tea.Cmd) {
	var parts []string
	var ids []string
	for _, g := range m.draftGroups() {
		if _, ok := m.reviewRecipient(g.sessionID); ok {
			continue
		}
		name := ""
		if g.sessionID != "" {
			name = m.nameFor(g.sessionID)
		}
		parts = append(parts, review.OrchestratorMessage(name, g.comments))
		for _, c := range g.comments {
			ids = append(ids, c.ID)
		}
	}
	if len(parts) == 0 {
		return m.say("No drafts are waiting for an ended session; /send delivers to live agents.", true)
	}
	text := strings.Join(parts, "\n\n")
	m.noteCopies(ids)
	now := time.Now()
	for i := range m.rev.comments {
		for _, id := range ids {
			if m.rev.comments[i].ID == id {
				m.rev.comments[i].Sent, m.rev.comments[i].Via = now, "orchestrator"
			}
		}
	}
	m.saveComments()
	cmd := m.tell("", text, text)
	mm, flash := m.say(fmt.Sprintf("Handed %s to the orchestrator.", commentCount(len(ids))), false)
	return mm, tea.Batch(cmd, flash)
}

// deliverReview is deliver for review comments: the reply carries their
// ids, so they are marked sent only once the message is through.
func deliverReview(a fleet.Agent, text string, it *inbox.Item, ids []string) tea.Cmd {
	return func() tea.Msg {
		err := sendPeer(a.Session.Socket, text)
		return sentMsg{item: it, name: a.Name, err: err, review: ids}
	}
}

// applyReviewSent marks delivered comments sent; failed ones stay drafts.
func (m *Model) applyReviewSent(msg sentMsg) {
	for _, id := range msg.review {
		delete(m.rev.sending, id)
	}
	if msg.err != nil {
		return
	}
	now := time.Now()
	for i := range m.rev.comments {
		for _, id := range msg.review {
			if m.rev.comments[i].ID == id {
				m.rev.comments[i].Sent = now
			}
		}
	}
	m.saveComments()
}

// noteCopies records, as the comments go out, how often their quoted lines
// are in the file, the baseline for telling later that they changed.
func (m *Model) noteCopies(ids []string) {
	for i := range m.rev.comments {
		for _, id := range ids {
			if m.rev.comments[i].ID == id {
				m.rev.comments[i].Copies = review.FileCopies(m.rev.comments[i])
			}
		}
	}
}

// markAddressed records comments whose quoted lines changed on disk.
func (m *Model) markAddressed(ids []string) {
	if len(ids) == 0 {
		return
	}
	now := time.Now()
	for i := range m.rev.comments {
		for _, id := range ids {
			if m.rev.comments[i].ID == id && m.rev.comments[i].Open() {
				m.rev.comments[i].Addressed = now
			}
		}
	}
	m.saveComments()
}

// addressedComments returns the ids of open comments whose quoted lines
// have changed on disk. It reads files, so it runs with the Review load.
func addressedComments(open []review.Comment) []string {
	var ids []string
	for _, c := range open {
		if review.FileChanged(c) {
			ids = append(ids, c.ID)
		}
	}
	return ids
}

// recipientName names a recipient for the drafts column and notes.
func (m Model) recipientName(sessionID string) string {
	if sessionID == "" {
		return "no agent"
	}
	return m.nameFor(sessionID)
}

func commentCount(n int) string {
	if n == 1 {
		return "1 comment"
	}
	return fmt.Sprintf("%d comments", n)
}
