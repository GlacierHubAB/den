package ui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/api"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// Cards from the dock (internal/api's card.go): Den hands den-api.sock the
// cards it would show, and carries out a console's answer through the same
// path as the human's ^Y (answerItem), once den-api.sock has checked that
// the console read the card whole and that it is unchanged.

// dockCards are the cards waiting for an answer, as a console may read them.
func (m Model) dockCards() []api.Card {
	var out []api.Card
	for _, it := range m.store.Items() {
		if it.State == inbox.Resolved || !it.Answerable() {
			continue
		}
		out = append(out, m.dockCard(it))
	}
	return out
}

// dockCard is one card: its kind, who it is for, its title and the lines
// Den shows, and the verbs it takes.
func (m Model) dockCard(it *inbox.Item) api.Card {
	verbs := []string{"allow", "deny"}
	if it.Kind == inbox.Permission && inbox.AlwaysRefusal(it) == "" {
		verbs = append(verbs, "always")
	}
	return api.Card{
		ID:          it.ID,
		Kind:        it.Kind.String(),
		Agent:       m.nameFor(itemKey(it)),
		Title:       it.Title,
		Body:        strings.Join(it.Body, "\n"),
		Verbs:       verbs,
		DockAnswers: dockMayAnswer(it),
	}
}

// dockMayAnswer reports whether a console may answer it. Never: what makes
// a console or an app pairing, what turns the API on, what loads guidance
// or installs on a host (each stays a decision made in Den), a question
// (it needs the human's words), and a remote host's request.
func dockMayAnswer(it *inbox.Item) bool {
	if hostOf(itemKey(it)) != "" {
		return false
	}
	switch it.Kind {
	case inbox.Console, inbox.Pair, inbox.APIOn, inbox.Guidance, inbox.Install, inbox.Question:
		return false
	}
	return true
}

// answerFromDock carries out a console's answer, or says why not.
func (m *Model) answerFromDock(a *api.CardAnswer) tea.Cmd {
	reply := func(r api.CardResult) tea.Cmd {
		a.Reply <- r
		return nil
	}
	var it *inbox.Item
	for _, x := range m.store.Items() {
		if x.ID == a.ID {
			it = x
		}
	}
	if it == nil || it.State == inbox.Resolved || !it.Answerable() {
		return reply(api.CardResult{Code: api.CodeNotFound, Note: "no such card, or it was answered"})
	}
	card := api.ProjectCard(m.dockCard(it))
	switch {
	case card.Hash != a.Hash:
		return reply(api.CardResult{Code: api.CodeCardChanged, Note: "the card changed: fetch it again"})
	case !card.DockAnswers || !containsVerb(card.Verbs, a.Verb):
		return reply(api.CardResult{Code: api.CodeRefusedFromDock, Note: "the console may not answer this card that way"})
	case m.tab == tabInbox && m.inboxSel == it.ID:
		// The human has it in front of them in Den: theirs to answer, so
		// the two never race.
		return reply(api.CardResult{Code: api.CodeUnavailable, Note: "Den has this card open: it waits for an answer there"})
	}
	mm, cmd := m.answerItem(it, a.Verb)
	*m = mm.(Model)
	if it.State != inbox.Resolved && m.flashErr {
		return tea.Batch(cmd, reply(api.CardResult{Code: api.CodeUnavailable, Note: sanitize.Label(m.flash, 120)}))
	}
	result := map[string]string{"allow": "allowed", "deny": "denied", "always": "always"}[a.Verb]
	word := map[string]string{"allow": "Allowed", "deny": "Denied", "always": "Allowed, and from now on,"}[a.Verb]
	note := word + " from " + sanitize.Label(a.Console, 40) + ": " + m.nameFor(itemKey(it)) + " · " + sanitize.Label(it.Title, 80)
	m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: note, at: time.Now()}))
	return tea.Batch(cmd, reply(api.CardResult{Result: result, Note: note}))
}

func containsVerb(verbs []string, v string) bool {
	for _, x := range verbs {
		if x == v {
			return true
		}
	}
	return false
}
