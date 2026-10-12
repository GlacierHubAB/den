package inbox

import (
	"fmt"
	"time"

	"github.com/GlacierHubAB/den/internal/events"
)

// ApplyRemote folds an event from a remote host's stream into the store,
// keyed by its host and session (Item.Host and Item.SessionID), so a
// session id seen on two hosts never mixes. The event is metadata only:
// a kind, a request kind and a tool name, never the command or the
// question. It returns the item it created, if any.
//
// Nothing a remote event makes can be answered here (Item.Answerable is
// false for a remote item): a request waiting on the human is shown as
// waiting in the terminal on its host. Every title says which host.
func (s *Store) ApplyRemote(ev events.Event) *Item {
	host, sid := ev.Host, ev.Session
	if host == "" || sid == "" {
		return nil
	}
	on := " on " + host
	add := func(it *Item) *Item {
		it.SessionID, it.Host = sid, host
		if it.At.IsZero() {
			it.At = ev.Time
		}
		return s.add(it)
	}
	// Any sign of life ends a stall, as a local hook does.
	s.resolveSession(host, sid, "active again", func(it *Item) bool { return it.Kind == Stalled })
	switch ev.Kind {
	case events.NeedsYou:
		kind, title := Permission, "Waiting for your permission"+on
		switch ev.Request {
		case "plan":
			kind, title = Plan, "Plan ready for review"+on
		case "question":
			kind, title = Question, "Has a question for you"+on
		default:
			if ev.Tool != "" {
				title = "Wants to use " + ev.Tool + on
			}
		}
		if s.hasOpen(host, sid, kind) {
			return nil // the same wait, reported twice (a PreToolUse, then its PermissionRequest)
		}
		return add(&Item{Kind: kind, State: InTerminal, Tool: ev.Tool, Title: title})
	case events.ToolFailed:
		// The tool ran, so whatever it asked for was answered there.
		s.resolveSession(host, sid, "answered"+on, func(it *Item) bool { return it.Kind <= Question })
		s.streak[agentKey(host, sid)]++
		n := s.streak[agentKey(host, sid)]
		if s.failing <= 0 || n < s.failing {
			return nil
		}
		title := fmt.Sprintf("%d tool calls failed in a row%s", n, on)
		for _, it := range s.items {
			if it.is(host, sid) && it.Kind == Failed && it.Tool != "" && it.State != Resolved {
				it.Title, it.Tool = title, ev.Tool
				it.clean()
				return nil
			}
		}
		return add(&Item{Kind: Failed, Tool: ev.Tool, Title: title})
	case events.TurnFailed:
		delete(s.streak, agentKey(host, sid))
		s.resolveSession(host, sid, "turn failed", func(it *Item) bool { return it.Kind != Done && it.Kind != Failed })
		return add(&Item{Kind: Failed, Title: FailureTitle(ev.Error) + on})
	case events.Idle:
		if s.hasOpen(host, sid, Idle) {
			return nil
		}
		return add(&Item{Kind: Idle, Title: "Waiting for your input" + on})
	case events.TurnStart:
		delete(s.streak, agentKey(host, sid))
		s.resolveSession(host, sid, "a new turn started"+on, func(it *Item) bool { return it.Kind != Done })
	case events.Done:
		delete(s.streak, agentKey(host, sid))
		s.resolveSession(host, sid, "turn finished", func(it *Item) bool { return it.Kind != Done })
		return add(&Item{Kind: Done, State: Resolved, Title: "Turn finished" + on + "."})
	case events.SessionEnd:
		delete(s.streak, agentKey(host, sid))
		s.resolveSession(host, sid, "session ended", func(it *Item) bool { return true })
	}
	return nil
}

// ReleaseHost lets go of everything open for agents on a host whose link
// is down: nothing from it may look answerable or still waiting here.
func (s *Store) ReleaseHost(host, why string) bool {
	changed := false
	for _, it := range s.items {
		if it.Host == host && it.State != Resolved {
			it.State, it.Resolved = Resolved, why
			changed = true
		}
	}
	return changed
}

// AddStalledOn is AddStalled for an agent on host ("" for this machine).
func (s *Store) AddStalledOn(host, sessionID string, quiet time.Duration) *Item {
	if host == "" {
		return s.AddStalled(sessionID, quiet)
	}
	if s.hasOpen(host, sessionID, Stalled) {
		return nil
	}
	unit := "minutes"
	if mins := int(quiet.Minutes()); mins == 1 {
		unit = "minute"
	}
	return s.add(&Item{SessionID: sessionID, Host: host, Kind: Stalled, Title: fmt.Sprintf("Working with no sign of progress for %d %s on %s", int(quiet.Minutes()), unit, host)})
}
