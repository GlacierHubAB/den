package ui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/api"
	perm "github.com/GlacierHubAB/den/internal/grants"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// Cards from the dock (internal/api's card.go): Den hands den-api.sock the
// cards it would show, and carries out a console's answer through the same
// path as the human's ^Y (answerItem), once den-api.sock has checked that
// the console read the card whole and that it is unchanged.

// dockCards are the cards waiting for an answer, as a console may read
// them, and the started ones whose action runs, as they were when they
// started.
func (m Model) dockCards() []api.Card {
	var out []api.Card
	for _, it := range m.store.Items() {
		if c, ok := m.api.started[it.ID]; ok && it.Started && it.State != inbox.Resolved {
			out = append(out, c)
			continue
		}
		if it.State == inbox.Resolved || !it.Answerable() {
			continue
		}
		out = append(out, m.dockCard(it))
	}
	return out
}

// freezeStarted keeps the card of each item an answer started, as it was
// when it started, until its action ends: it stays listed, started, with
// no verbs, and nothing changes its text meanwhile. Only a card a console
// may answer is kept so (dockMayAnswer): a host's card, or one only Den
// answers, leaves the list when it starts, as before. It runs on Den's own
// loop (publishAPI), and only there is the map read or written.
func (m *Model) freezeStarted() {
	live := map[string]bool{}
	for _, it := range m.store.Items() {
		if !it.Started || it.State == inbox.Resolved || !dockMayAnswer(it) {
			continue
		}
		live[it.ID] = true
		if _, ok := m.api.started[it.ID]; ok {
			continue
		}
		if m.api.started == nil {
			m.api.started = map[string]api.Card{}
		}
		c := m.dockCard(it)
		c.Started, c.StartedBy, c.DockAnswers, c.TTYOnly, c.Verbs = true, it.AnsweredBy, false, false, nil
		m.api.started[it.ID] = c
	}
	for id := range m.api.started {
		if !live[id] {
			delete(m.api.started, id)
		}
	}
}

// dockSettled is how the answered cards Den still keeps were settled,
// with how a started one's action ended. A started card a console never
// sees started (freezeStarted) is settled by its answer when it starts.
func (m Model) dockSettled() []api.Settled {
	var out []api.Settled
	for _, it := range m.store.Items() {
		switch {
		case it.Answer == "":
		case it.State == inbox.Resolved:
			out = append(out, api.Settled{ID: it.ID, Verb: it.Answer, By: it.AnsweredBy, Outcome: it.Outcome})
		case it.Started && !dockMayAnswer(it):
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
	if lv := m.cardLevels(); dockMayAnswer(it) && lv != nil {
		// A console is offered only what it may give now: allow and
		// "always" by the levels and kept, deny always (the human's rule:
		// saying no is always allowed from the console). The lock, the
		// powers and the class limit are refused at answer time only.
		var listed []string
		for _, v := range verbs {
			if v == "deny" || lv.AnswerListed(permAnswer(it, class, step, v)) {
				listed = append(listed, v)
			}
		}
		verbs = listed
	}
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
		DockAnswers: dockMayAnswer(it) && !m.godTTYOnly(it) && len(verbs) > 0,
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

// keptIDs are the kept entries a kind's allow may be covered by: what a
// pairing that existed when the levels came kept answering (upgrade only).
var keptIDs = map[inbox.Kind]string{
	inbox.Permission: perm.KeptPermission, inbox.Plan: perm.KeptPlan, inbox.Close: perm.KeptClose,
	inbox.Budget: perm.KeptBudget, inbox.Restore: perm.KeptRestore, inbox.Merge: perm.KeptMerge,
	inbox.Cleanup: perm.KeptCleanup, inbox.MissionUpdate: perm.KeptMissionUpdate,
}

// selfRaised are the kinds a console can raise itself (a request, or its
// own turn) whose allow would make Den less careful: a looser mode, the
// usage figure, going past the usage stop, a tool for the console's own
// turn, and a mission's project (#63: only the human moves a mission).
// A console never allows them, by level or by kept; it may deny them.
var selfRaised = map[inbox.Kind]bool{
	inbox.ModeSet: true, inbox.UsageSet: true, inbox.UsageResume: true, inbox.ControlAsk: true, inbox.MissionMove: true,
}

// cardLevels is the answer check and the listing den-api decides
// (api.Server's AnswerAllowed and AnswerListed).
type cardLevels interface {
	AnswerAllowed(perm.Answer) (code, class string)
	AnswerListed(perm.Answer) bool
}

// cardLevels is den-api's answer check, nil when den-api is off.
func (m Model) cardLevels() cardLevels {
	if m.api.srv != nil {
		return m.api.srv
	}
	return m.api.levels
}

// permAnswer is a console's verb on it, as Den's record has the card: the
// class and step the card's hash holds, never anything the console sent
// but the verb.
func permAnswer(it *inbox.Item, class inbox.Class, step int, verb string) perm.Answer {
	return perm.Answer{Kept: keptIDs[it.Kind], Class: string(class), Step: step, Verb: verb,
		GodTTY: godTTYKinds[it.Kind], SelfRaised: selfRaised[it.Kind]}
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
	// The levels, after every check above and never instead of them (the
	// outer gate, todayDock, stays), with the class and step of the card
	// whose hash the console matched. A listing the console read before a
	// lowering (the verbs are outside the hash) can only show a button
	// that is refused: by the listing now (refused_from_dock) or here
	// (below_level), never taken; both decide on Den's loop at answer time
	// from Den's own record. answerItem runs right after, in this same
	// step of Den's loop, with nothing between that yields: a lowering, the
	// lock or a tamper that lands after this call is after the answer.
	lv := m.cardLevels()
	if lv == nil {
		// An answer comes only from den-api; without it, none is taken.
		return reply(api.CardResult{Code: api.CodeUnavailable, Note: "den-api is off"})
	}
	if code, class := lv.AnswerAllowed(permAnswer(it, inbox.Class(card.Class), card.Step, a.Verb)); code != "" {
		return reply(api.CardResult{Code: code, Class: class, Note: answerRefusal(code, class)})
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

// answerRefusal is the answer check's refusal in Den's words, for the
// console.
func answerRefusal(code, class string) string {
	switch code {
	case api.CodePowersLocked:
		return api.PowersLocked
	case api.CodeTTYOnly:
		return "while God mode is on, only Den's own window answers this"
	case api.CodeRateLimited:
		return "too many answers above everyday ones: wait a few seconds"
	}
	if l := perm.Label(class); l != "" {
		return l + " is set below this in Den: Den's window answers it"
	}
	return "only Den's window allows this"
}
