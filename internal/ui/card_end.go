package ui

import (
	"time"

	"github.com/GlacierHubAB/den/internal/api"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// cardEnd says how the action of a started card a console answered ended:
// a line in Den's chat, red unless it all went, and Den's line in the
// mirror (origin card), which carries the agents' names only; den-api
// writes its words. action is "close" or "reopen"; done and failed are
// the agents it did and did not close or reopen; summary is Den's own
// account, errors included, for Den's chat only. A lost action's chat
// line is the tick's (loseStarted), so lost adds only the mirror's.
func (m *Model) cardEnd(it *inbox.Item, action string, done, failed []string, lost bool, summary string) {
	if !it.Started || it.AnsweredBy != "console" {
		return
	}
	if !lost {
		what := "The close"
		if action == "reopen" {
			what = "The reopen"
		}
		e := chatEntry{role: "note", text: what + " the console answered ended: " + sanitize.Line(summary), at: time.Now()}
		if len(failed) > 0 {
			e.status = "failed: not all of it went"
		}
		m.logEntry(m.addUnthreaded(e))
	}
	m.converse(api.Converse{Origin: api.OriginCard, Speaker: api.SpeakerDen, Card: it.ID, End: &api.CardEnd{Action: action, Done: done, Failed: failed, Lost: lost}})
}

// loseStartedEnd is cardEnd for a close or reopen Den lost track of: its
// agents, from its plan, which it then drops.
func (m *Model) loseStartedEnd(it *inbox.Item) {
	var names []string
	switch it.Kind {
	case inbox.Close:
		for _, t := range m.closing[it.ID].Targets {
			names = append(names, t.Name)
		}
		delete(m.closing, it.ID)
		m.cardEnd(it, "close", names, nil, true, "")
	case inbox.Restore:
		m.ensureRestore()
		for _, a := range m.restore.plans[it.ID].agents {
			names = append(names, a.Name())
		}
		delete(m.restore.plans, it.ID)
		m.cardEnd(it, "reopen", names, nil, true, "")
	}
}
