package ui

import (
	"fmt"
	"slices"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/hooks"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// A request that carries a session id Den makes up for its own cards (the
// prefixes of inbox.MadeUpSession) is not from an agent: a Claude session's
// id is a UUID. Den refuses it before anything keys on that id, so no card,
// notice or timer can read as Den's, a mission's, a project's or a host's
// on a sender's say.

// madeUpNoteEvery is how seldom the chat notes such a refusal, per prefix
// and host, so a sender cannot fill the log with them.
const madeUpNoteEvery = time.Minute

// refusals remembers when the chat last noted a refusal.
type refusals struct{ noted map[string]time.Time }

// madeUpRequest reports whether a request names a session id Den makes up,
// in its envelope or in the hook's own payload (which is what a card keys
// on).
func madeUpRequest(r *bridge.Request) bool {
	return inbox.MadeUpSession(r.SessionID) || inbox.MadeUpSession(r.Input.SessionID)
}

// namedEvent says what a request is for a note Den writes: its event name
// when Den knows it (a hook event it subscribes to, or one of its own bridge
// events), else "an unknown event". The sender chooses the name, and none of
// the sender's words goes into a note.
func namedEvent(event string) string {
	if bridge.Held(event) || slices.Contains(hooks.Events(), event) {
		return "a " + event + " event"
	}
	return "an unknown event"
}

// refuseMadeUp answers such a request with a deny and notes it in the chat
// log, at most once a minute per prefix and host. The note names the
// prefix Den matched and an event name Den knows, never the sender's text.
func (m *Model) refuseMadeUp(r *bridge.Request, now time.Time) {
	r.Reply(bridge.Decision{Behavior: "deny", Message: "den: that session id is one Den makes up for its own cards"})
	prefix := inbox.MadeUpPrefix(r.SessionID)
	if prefix == "" {
		prefix = inbox.MadeUpPrefix(r.Input.SessionID)
	}
	key := prefix + "@" + r.Host
	if last, ok := m.refused.noted[key]; ok && now.Sub(last) < madeUpNoteEvery {
		return
	}
	if m.refused.noted == nil {
		m.refused.noted = map[string]time.Time{}
	}
	m.refused.noted[key] = now
	from := ""
	switch {
	case r.Host != "":
		from = " from host " + sanitize.Label(r.Host, 40)
	case r.PeerPID > 0:
		from = fmt.Sprintf(" from pid %d", r.PeerPID)
	}
	m.logEntry(m.addUnthreaded(chatEntry{role: "note", at: now, text: fmt.Sprintf(
		"REFUSED: %s%s used a session id starting with %q, which Den makes up for its own cards. No card was made.",
		namedEvent(r.Event), from, prefix)}))
}
