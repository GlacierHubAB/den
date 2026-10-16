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

// dockSettled is how the answered cards Den still keeps were settled.
func (m Model) dockSettled() []api.Settled {
	var out []api.Settled
	for _, it := range m.store.Items() {
		if it.State == inbox.Resolved && it.Answer != "" {
			out = append(out, api.Settled{ID: it.ID, Verb: it.Answer, By: it.AnsweredBy})
		}
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
	// What requestLines shows: the kind, the tool, the title, a twin's
	// warning, the intent, then the body.
	var body []string
	if it.Intent != "" {
		body = append(body, strings.Split(it.Intent, "\n")...)
	}
	body = append(body, it.Body...)
	return api.Card{
		ID:          it.ID,
		Kind:        it.Kind.String(),
		Agent:       m.nameFor(itemKey(it)),
		Tool:        it.Tool,
		Title:       it.Title,
		Note:        m.twinNote(itemKey(it)),
		Body:        strings.Join(body, "\n"),
		Verbs:       verbs,
		DockAnswers: dockMayAnswer(it) && !m.godTTYOnly(it),
		TTYOnly:     m.godTTYOnly(it),
	}
}

// dockMayAnswer reports whether a console may answer it. Never: what makes
// a console or an app pairing, what turns the API on, what loads guidance;
// an app's grant (the console is an app itself) and skills (every later
// agent loads them), until the dock confirms in its own native window; a
// question (it needs the human's words); a remote host's request, and Den's
// own cards about a host (installing there, a message to an agent there).
func dockMayAnswer(it *inbox.Item) bool {
	if hostOf(itemKey(it)) != "" || strings.HasPrefix(it.SessionID, inbox.HostPrefix) {
		return false
	}
	switch it.Kind {
	case inbox.Console, inbox.Pair, inbox.APIOn, inbox.Guidance, inbox.Install, inbox.Question, inbox.Grant, inbox.Skills, inbox.Message:
		return false
	case inbox.DesktopInstall, inbox.DesktopMission, inbox.DesktopAct, inbox.YourTurn:
		// Desktop control: the human's own window only.
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
	case m.godTTYOnly(it) || m.god.on && a.Verb == "always":
		// A standing allowance outlives the turn: in God mode, Den's
		// window only.
		return reply(api.CardResult{Code: api.CodeTTYOnly, Note: "while God mode is on, only Den's own window answers this"})
	case !card.DockAnswers || !containsVerb(card.Verbs, a.Verb):
		return reply(api.CardResult{Code: api.CodeRefusedFromDock, Note: "the console may not answer this card that way"})
	case m.tab == tabInbox && m.inboxSel == it.ID:
		// The human has it in front of them in Den: theirs to answer, so
		// the two never race.
		return reply(api.CardResult{Code: api.CodeUnavailable, Note: "Den has this card open: it waits for an answer there"})
	}
	it.AnsweredBy = "console"
	mm, cmd := m.answerItem(it, a.Verb)
	*m = mm.(Model)
	if it.State != inbox.Resolved {
		it.AnsweredBy = ""
		// Refused, or a step of several (a plan, a question): Den shows the
		// rest, and the console is not told it is done.
		why := "answered in part; Den shows the rest"
		if m.flashErr {
			why = sanitize.Label(m.flash, 120)
		}
		return tea.Batch(cmd, reply(api.CardResult{Code: api.CodeUnavailable, Note: why}))
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
