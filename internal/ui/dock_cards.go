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
	if it.Kind == inbox.RulesPick {
		body = withoutNotes(body)
	}
	if it.Kind == inbox.Setup {
		// Den's window shows the real paths (D6); a console gets the
		// paste-safe copy, as den doctor prints them.
		body = m.setupListed(it)
	}
	class, step := inbox.Classify(it, m.cardFacts(it))
	return api.Card{
		ID:          it.ID,
		Kind:        it.Kind.String(),
		Agent:       m.dockName(itemKey(it)),
		AgentID:     m.dockAgentID(itemKey(it)),
		Tool:        it.Tool,
		Title:       it.Title,
		Note:        m.twinNote(itemKey(it)),
		Class:       string(class),
		Step:        step,
		Body:        strings.Join(body, "\n"),
		Verbs:       verbs,
		DockAnswers: dockMayAnswer(it) && !m.godTTYOnly(it),
		TTYOnly:     m.godTTYOnly(it),
	}
}

// withoutNotes is a rules card's lines for a console: each note, fenced
// from its "┌" line to its "└", becomes one line saying it is in Den's
// window. The console cannot answer the card, and agents' notes reach a
// console nowhere else; the rules and the project's rules stay.
func withoutNotes(lines []string) []string {
	var out []string
	in := false
	for _, l := range lines {
		switch {
		case !in && strings.HasPrefix(l, noteFenceTop):
			in = true
			out = append(out, "    (the agent's note is shown in Den's window)")
		case in && strings.HasPrefix(l, noteFenceBottom):
			in = false
		case !in:
			out = append(out, l)
		}
	}
	return out
}

// todayDock are the kinds a console may answer: exactly the set it could
// answer before the permission manager, and no other. A kind added to
// inbox is not in it, so a console never answers it until a design says
// so. Every other kind is the human's in Den: what makes a console or an
// app pairing, what turns the API on, what loads guidance; an app's grant
// (the console is an app itself) and skills (every later agent loads
// them); a question (it needs the human's words); desktop control; a
// mission's done criteria; the console's own automatic powers, which would
// be the console granting itself more; Den's notices, which no one answers.
//
// A kind here may still be den_only in inbox.Classify (Budget, UsageResume,
// UsageSet, ModeSet, ControlAsk): levels gate nothing yet, so the console
// answers them as before. The table never puts a kind outside todayDock
// in a class a console could reach (TestClassesNeverWidenTheDock).
var todayDock = map[inbox.Kind]bool{
	inbox.Permission: true, inbox.Plan: true, inbox.Close: true, inbox.Budget: true, inbox.Restore: true,
	inbox.Merge: true, inbox.Cleanup: true, inbox.MissionUpdate: true, inbox.UsageResume: true,
	inbox.MissionMove: true, inbox.ControlAsk: true, inbox.UsageSet: true, inbox.ModeSet: true,
}

// dockMayAnswer reports whether a console may answer it: a kind in
// todayDock, and never a remote host's request or Den's own card about a
// host (installing there, a message to an agent there).
func dockMayAnswer(it *inbox.Item) bool {
	if hostOf(itemKey(it)) != "" || strings.HasPrefix(it.SessionID, inbox.HostPrefix) {
		return false
	}
	return todayDock[it.Kind]
}

// cardFacts is what Den's own records add to an item for inbox.Classify,
// read now: the Cleanup's plan, as ^Y would carry it out.
func (m Model) cardFacts(it *inbox.Item) inbox.Facts {
	var f inbox.Facts
	if o, ok := m.wt[it.ID]; ok && o.cleanup != nil {
		f.CleanupKnown, f.CleanupIgnored = true, len(o.cleanup.Ignored)
	}
	return f
}

// dockName is how a card names its agent for the console: Den's name for
// it without the "@" Den's window puts before a name it gave (the console
// styles names itself); an item from a host keeps its "@host".
func (m Model) dockName(key string) string {
	n := m.nameFor(key)
	if hostOf(key) == "" && !strings.HasPrefix(key, inbox.HostPrefix) {
		n = strings.TrimPrefix(n, "@")
	}
	return n
}

// dockAgentID is the id of the agent a card is for, "" for a card from a
// host or from Den.
func (m Model) dockAgentID(key string) string {
	if a, ok := m.agentFor(key); ok && a.Host == "" {
		return a.Session.SessionID
	}
	return ""
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
	case card.HashIn(a.Version) != a.Hash:
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
	if it.Started && it.State != inbox.Resolved {
		// The answer started the card's action: accepted, and Den settles
		// the card with it when the action ends.
		note := "Allowed from " + sanitize.Label(a.Console, 40) + ": " + m.nameFor(itemKey(it)) + " · " + sanitize.Label(it.Title, 80) + " · started; Den settles it when it ends"
		m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: note, at: time.Now()}))
		return tea.Batch(cmd, reply(api.CardResult{Result: "allowed", Note: note}))
	}
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
