package ui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/inbox"
)

// answerQuestion answers the question an agent is asking now with what the
// human typed (hooks.answer_questions). A call with several questions takes
// them one at a time; the agent hears back once all are answered.
func (m Model) answerQuestion(it *inbox.Item, text string) (tea.Model, tea.Cmd) {
	if why := m.readGate(it); why != "" {
		return m.say(why, true) // the typed text stays on the line
	}
	done, ok := m.store.AnswerQuestion(it, text)
	if !ok {
		return m.say(questionHint(it), true)
	}
	cleared := m.clear()
	m.refresh()
	msg := "Next question for " + m.nameFor(itemKey(it))
	if done {
		msg = "Answered " + m.nameFor(itemKey(it)) + "'s question"
		if len(it.Questions) > 1 {
			msg += "s"
		}
	}
	mm, cmd := m.say(msg, false)
	return mm, tea.Batch(cleared, cmd)
}

// questionHint says how to answer the question being asked now.
func questionHint(it *inbox.Item) string {
	if !it.AsksInDen() {
		return "Answer that question in its terminal."
	}
	q := it.Questions[it.Step]
	switch {
	case len(q.Options) == 0:
		return "Type your answer, then Enter."
	case q.Multi:
		return fmt.Sprintf("Type the numbers to choose (1-%d, e.g. 1 3) or your own answer, then Enter.", len(q.Options))
	}
	return fmt.Sprintf("Type a number (1-%d) or your own answer, then Enter.", len(q.Options))
}

// optionKeys names what to type for the question being asked now: "1-3",
// "1-3 …" when several may be chosen, or "type" when it has no options.
func optionKeys(it *inbox.Item) string {
	if !it.AsksInDen() || len(it.Questions[it.Step].Options) == 0 {
		return "type"
	}
	q := it.Questions[it.Step]
	k := fmt.Sprintf("1-%d", len(q.Options))
	if len(q.Options) == 1 {
		k = "1"
	}
	if q.Multi {
		k += " …"
	}
	return k
}
