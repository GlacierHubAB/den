package inbox

import (
	"fmt"
	"strings"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
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
		return s.addRemote(it)
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
			// A question held for Den lately is this question; the event
			// can come after it was answered, and says no more. One that
			// comes later is another question, maybe one the host could
			// not hold, and shows.
			if at, ok := s.heldQ[agentKey(host, sid)]; ok && time.Since(at) < heldQuiet {
				return nil
			}
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
		delete(s.heldQ, agentKey(host, sid))
		s.resolveSession(host, sid, "turn failed", func(it *Item) bool { return it.Kind != Done && it.Kind != Failed })
		return add(&Item{Kind: Failed, Title: FailureTitle(ev.Error) + on})
	case events.Idle:
		if s.hasOpen(host, sid, Idle) {
			return nil
		}
		return add(&Item{Kind: Idle, Title: "Waiting for your input" + on})
	case events.TurnStart:
		delete(s.streak, agentKey(host, sid))
		delete(s.heldQ, agentKey(host, sid))
		s.resolveSession(host, sid, "a new turn started"+on, func(it *Item) bool { return it.Kind != Done })
	case events.Done:
		delete(s.streak, agentKey(host, sid))
		delete(s.heldQ, agentKey(host, sid))
		s.resolveSession(host, sid, "turn finished", func(it *Item) bool { return it.Kind != Done })
		return add(&Item{Kind: Done, State: Resolved, Title: "Turn finished" + on + "."})
	case events.SessionEnd:
		delete(s.streak, agentKey(host, sid))
		delete(s.heldQ, agentKey(host, sid))
		s.cancelMessages(host, sid)
		s.resolveSession(host, sid, "session ended", func(it *Item) bool { return true })
	}
	return nil
}

// heldQuiet is how long after a question was held for Den the host's own
// notice of a question from that agent is taken to be the same one.
var heldQuiet = 30 * time.Second

// ReleaseHost lets go of everything open for agents on a host whose link
// is down: nothing from it may look answerable or still waiting here.
func (s *Store) ReleaseHost(host, why string) bool {
	for k := range s.heldQ {
		if strings.HasPrefix(k, host+"\x00") {
			delete(s.heldQ, k)
		}
	}
	changed := false
	for _, it := range s.items {
		if it.Host != host || it.State == Resolved || it.Kind == Sent {
			continue // a message on its way reports its own outcome
		}
		it.State, it.Resolved = Resolved, why
		if it.Kind == Close {
			it.Resolved = "the link to " + host + " was lost; nothing was closed"
		}
		if it.Kind == Message {
			it.Resolved = "the link to " + host + " was lost; nothing was sent"
			if it.req != nil {
				it.req.Reply(bridge.Decision{Behavior: bridge.SendCancelled, Message: "the request was cancelled: " + it.Resolved})
			}
		}
		changed = true
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
	return s.addRemote(&Item{SessionID: sessionID, Host: host, Kind: Stalled, Title: fmt.Sprintf("Working with no sign of progress for %d %s on %s", int(quiet.Minutes()), unit, host)})
}

// Caps on what a host's events may put in the inbox. A host chooses its
// events, so without them it could fill the inbox (and history.jsonl)
// faster than anyone can read. Requests a host holds for an answer have
// caps of their own (heldFits), besides MaxHeld where they arrive.
const (
	MaxRemoteOpen    = 32 // open items from one host's events
	MaxRemoteOpenAll = 128
	MaxRemoteNew     = 60 // new items, finished turns included, from one host a minute
	// MaxRemoteHeld caps the items of one host's held requests that are
	// still waiting or in its terminal, and MaxRemoteHeldNew how many it
	// may add a minute.
	MaxRemoteHeld    = 64
	MaxRemoteHeldNew = 60
)

type remoteCaps struct {
	window  map[string]time.Time // host -> when its minute began
	added   map[string]int       // host -> items added in that minute
	dropped map[string]int       // host -> items not added for a cap
	heldAt  map[string]time.Time // host -> when its held requests' minute began
	held    map[string]int       // host -> held requests added in that minute
}

// heldFits reports whether a held request from host may become an item,
// and counts it if so; one that may not goes to its terminal there.
func (s *Store) heldFits(host string) bool {
	c := &s.remote
	if c.held == nil {
		c.heldAt, c.held = map[string]time.Time{}, map[string]int{}
	}
	if c.dropped == nil {
		c.dropped = map[string]int{}
	}
	now := time.Now()
	if now.Sub(c.heldAt[host]) >= time.Minute {
		c.heldAt[host], c.held[host] = now, 0
	}
	open := 0
	for _, o := range s.items {
		if o.Host == host && o.req != nil && o.State != Resolved {
			open++
		}
	}
	if c.held[host] >= MaxRemoteHeldNew || open >= MaxRemoteHeld {
		c.dropped[host]++
		return false
	}
	c.held[host]++
	return true
}

// addRemote adds an item made by a host's event, unless a cap is reached;
// then it counts the drop and returns nil.
func (s *Store) addRemote(it *Item) *Item {
	c := &s.remote
	if c.window == nil {
		c.window, c.added, c.dropped = map[string]time.Time{}, map[string]int{}, map[string]int{}
	}
	now := time.Now()
	if now.Sub(c.window[it.Host]) >= time.Minute {
		c.window[it.Host], c.added[it.Host] = now, 0
	}
	open, all := 0, 0
	for _, o := range s.items {
		if o.fromHost && o.State != Resolved {
			all++
			if o.Host == it.Host {
				open++
			}
		}
	}
	opens := it.State != Resolved
	if c.added[it.Host] >= MaxRemoteNew || opens && (open >= MaxRemoteOpen || all >= MaxRemoteOpenAll) {
		c.dropped[it.Host]++
		return nil
	}
	c.added[it.Host]++
	it.fromHost = true
	return s.add(it)
}

// RemoteDropped is how many items a host's events would have added past
// the caps since Den started.
func (s *Store) RemoteDropped(host string) int { return s.remote.dropped[host] }
