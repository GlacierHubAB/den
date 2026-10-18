package api

import (
	"encoding/json"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/GlacierHubAB/den/internal/sanitize"
)

// The conversation mirror: the human's orchestrator conversation, as Den
// shows it, on the paired console. Each entry is the human's words, Den's
// own fixed line, or the orchestrator's completed reply: never an agent's
// or a host's raw text. It goes only to the paired console, only while it
// has its powers and the human let it mirror (Den's mirror_conversation),
// and only entries since then. It is not part of console.events' snapshot:
// a console backfills with conversation.history.

// EventConversation is one entry of the mirrored conversation.
const EventConversation = "conversation"

// Origins of a conversation entry: whose turn it belongs to.
const (
	OriginYou     = "you"     // the human typed it in Den
	OriginConsole = "console" // a console sent the turn
	OriginReport  = "report"  // an agent reported
	OriginMission = "mission" // Den drives a mission
	OriginDesktop = "desktop" // a desktop mission's result
)

// Speakers of a conversation entry.
const (
	SpeakerYou          = "you"
	SpeakerDen          = "den"
	SpeakerOrchestrator = "orchestrator"
)

var (
	convOrigins  = map[string]bool{OriginYou: true, OriginConsole: true, OriginReport: true, OriginMission: true, OriginDesktop: true}
	convSpeakers = map[string]bool{SpeakerYou: true, SpeakerDen: true, SpeakerOrchestrator: true}
	// reservedNames never name an agent in the mirror.
	reservedNames = map[string]bool{"den": true, "drey": true, "you": true, "orchestrator": true, "the orchestrator": true, "human": true, "the human": true, "system": true, "console": true, "dock": true, "den-api": true}
)

// Caps on the mirror.
const (
	// An entry is the whole message: a console wraps it. Only one past
	// convMaxBytes, a safety cap, is cut, and its JSON (at most six bytes a
	// byte, for an escaped "<") stays well inside MaxLine.
	convMaxBytes     = 64 << 10
	convCut          = "(cut: the rest is in Den)"
	convKeep         = 500       // entries kept in memory
	convKeepBytes    = 8 << 20   // and their text, in all
	convHistoryMax   = 200       // entries one conversation.history returns
	convHistoryBytes = 768 << 10 // the entries' JSON in one reply, inside MaxLine
	convPerAgent     = 6         // report entries a minute per agent
	convReportsTotal = 30        // report entries a minute across all agents
)

// ConvConsole names the console that sent a turn: its label, and Self
// when the receiving connection is that console's.
type ConvConsole struct {
	Label string `json:"label"`
	Self  bool   `json:"self"`
}

// ConvEntry is one entry of the mirrored conversation, as a console
// receives it.
type ConvEntry struct {
	ID      uint64       `json:"id"`
	At      time.Time    `json:"at"`
	Turn    string       `json:"turn,omitempty"`
	Origin  string       `json:"origin"`
	Speaker string       `json:"speaker"`
	Agent   string       `json:"agent,omitempty"`
	Console *ConvConsole `json:"console,omitempty"`
	Text    string       `json:"text"`
}

// Converse is what Den hands the mirror: ConsoleID names the console that
// sent the turn (for Console), ConsoleLabel how Den shows it.
type Converse struct {
	Turn, Origin, Speaker, Agent, Text string
	ConsoleID, ConsoleLabel            string
}

// convState is the mirror's state, under its own lock.
type convState struct {
	mu      sync.Mutex
	seq     uint64
	entries []convStored
	// textBytes is the entries' text, in all, for convKeepBytes.
	textBytes int
	// on, for and since: the human let console for mirror, at since.
	on    bool
	forID string
	since time.Time
	// reports are when each agent's report entries went out, and all
	// reports' together, for the rate limits; told when each agent last
	// had the "more reports" line.
	reports map[string][]time.Time
	all     []time.Time
	told    map[string]time.Time
	// askingID is the console Den's ask card is for, "" for none; heard
	// what the console last heard of the mirror's state.
	askingID string
	heard    mirrorTold
	// pushMu orders every mirror state a console is sent, live or in the
	// snapshot, so that the newest always arrives last. It is taken before
	// con.mu and mu, never while either is held.
	pushMu sync.Mutex
}

type convStored struct {
	ConvEntry
	consoleID string
}

// SetMirror turns the mirror on for console id (the human's choice, made
// in Den) or off. On, entries from now on reach that console; a new
// pairing starts with it off. The console hears the change (EventMirror).
func (s *Server) SetMirror(id string, on bool) {
	st := &s.conv
	st.mu.Lock()
	st.on, st.forID = on && id != "", id
	if st.on {
		st.since = time.Now()
		st.askingID = ""
	}
	st.mu.Unlock()
	s.pushMirror()
}

// SetMirrorAsking says whether Den's card asking the human to show the
// conversation in console id waits in Den's own window ("" for none).
func (s *Server) SetMirrorAsking(id string) {
	st := &s.conv
	st.mu.Lock()
	st.askingID = id
	st.mu.Unlock()
	s.pushMirror()
}

// EventMirror says whether the conversation is shown in this console:
// MirrorState, sent on every change and in console.events' snapshot.
const EventMirror = "mirror"

// Mirror states, a closed set.
const (
	MirrorOff    = "off"
	MirrorAsking = "asking" // Den's card for it waits in Den's own window
	MirrorOn     = "on"
)

// MirrorState is the mirror as the console sees it; Since only when on,
// the moment conversation.history reaches back to.
type MirrorState struct {
	State string     `json:"state"`
	Since *time.Time `json:"since,omitempty"`
}

// mirrorFor is the mirror's state for the console with proof id now.
func (s *Server) mirrorFor(id string) MirrorState {
	s.con.mu.Lock()
	current := id != "" && s.con.cur != nil && s.con.cur.ID == id
	powered := current && s.con.cur.powers
	s.con.mu.Unlock()
	st := &s.conv
	st.mu.Lock()
	defer st.mu.Unlock()
	switch {
	case powered && st.on && st.forID == id:
		since := st.since.UTC()
		return MirrorState{State: MirrorOn, Since: &since}
	case current && st.askingID == id:
		return MirrorState{State: MirrorAsking}
	}
	return MirrorState{State: MirrorOff}
}

// pushMirror sends the current console's connections the mirror's state
// when it changed since they last heard it.
func (s *Server) pushMirror() {
	s.conv.pushMu.Lock()
	defer s.conv.pushMu.Unlock()
	s.con.mu.Lock()
	id := ""
	if s.con.cur != nil {
		id = s.con.cur.ID
	}
	s.con.mu.Unlock()
	if id == "" {
		return
	}
	now := s.mirrorFor(id)
	st := &s.conv
	st.mu.Lock()
	changed := st.heard != (mirrorTold{id, now.State, timeOf(now.Since)})
	st.heard = mirrorTold{id, now.State, timeOf(now.Since)}
	st.mu.Unlock()
	if !changed {
		return
	}
	for _, c := range s.consoleConns(id) {
		if c.subscribed(EventConsole) {
			c.push(Event{Event: EventMirror, Data: now}, true)
		}
	}
}

// mirrorTold is what the console last heard of the mirror.
type mirrorTold struct {
	id, state string
	since     time.Time
}

func timeOf(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// Mirroring reports whether the mirror is on now, and for which console.
func (s *Server) Mirroring() (id string, on bool) {
	st := &s.conv
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.forID, st.on
}

// Converse adds one entry to the conversation and sends it to the console
// it may reach. An unknown origin or speaker is refused; it reports
// whether the entry was kept.
func (s *Server) Converse(in Converse) bool {
	if !convOrigins[in.Origin] || !convSpeakers[in.Speaker] {
		return false
	}
	e := ConvEntry{Turn: sanitize.Label(in.Turn, 80), Origin: in.Origin, Speaker: in.Speaker, Text: convText(in.Text)}
	if in.Origin == OriginReport || in.Origin == OriginDesktop {
		e.Agent = agentLabel(in.Agent)
		if e.Speaker == SpeakerDen {
			// Den's line is built here, from the checked name, whatever
			// the caller sent: an agent's chosen name never writes Den's
			// words.
			e.Text = "Report from " + e.Agent + "."
		}
	}
	if in.Origin == OriginConsole {
		e.Console = &ConvConsole{Label: sanitize.Label(in.ConsoleLabel, 60)}
	}
	if strings.TrimSpace(e.Text) == "" {
		return false
	}
	st := &s.conv
	st.mu.Lock()
	now := time.Now()
	if (in.Origin == OriginReport || in.Origin == OriginDesktop) && in.Speaker != SpeakerOrchestrator {
		ok, line := st.allowReport(e.Agent, now)
		if !ok {
			if line == "" {
				st.mu.Unlock()
				return false
			}
			e.Speaker, e.Text = SpeakerDen, line
		}
	}
	st.seq++
	e.ID, e.At = st.seq, now.UTC()
	stored := convStored{ConvEntry: e, consoleID: in.ConsoleID}
	st.entries = append(st.entries, stored)
	st.textBytes += len(e.Text)
	n := 0
	for n < len(st.entries)-1 && (len(st.entries)-n > convKeep || st.textBytes > convKeepBytes) {
		st.textBytes -= len(st.entries[n].Text)
		n++
	}
	if n > 0 {
		st.entries = append([]convStored(nil), st.entries[n:]...)
	}
	on, forID := st.on, st.forID
	st.mu.Unlock()
	if !on {
		return true
	}
	for _, c := range s.consoleConns(forID) {
		if c.subscribed(EventConsole) && s.mayMirror(c) {
			c.push(Event{Event: EventConversation, Data: stored.forConn(c)}, true)
		}
	}
	return true
}

// allowReport applies the report limits: convPerAgent a minute per agent
// and convReportsTotal across all. Past them, the first entry dropped in a
// minute becomes Den's line saying more are in Den; the rest are dropped.
func (st *convState) allowReport(agent string, now time.Time) (bool, string) {
	if st.reports == nil {
		st.reports, st.told = map[string][]time.Time{}, map[string]time.Time{}
	}
	recent := func(ts []time.Time) []time.Time {
		var out []time.Time
		for _, t := range ts {
			if now.Sub(t) < time.Minute {
				out = append(out, t)
			}
		}
		return out
	}
	st.reports[agent], st.all = recent(st.reports[agent]), recent(st.all)
	if len(st.reports[agent]) < convPerAgent && len(st.all) < convReportsTotal {
		st.reports[agent] = append(st.reports[agent], now)
		st.all = append(st.all, now)
		return true, ""
	}
	if now.Sub(st.told[agent]) < time.Minute {
		return false, ""
	}
	st.told[agent] = now
	return false, "More reports from " + agent + " are in Den."
}

// forConn is the entry as connection c receives it: Self set only on the
// connection of the console that sent the turn.
func (e convStored) forConn(c *conn) ConvEntry {
	out := e.ConvEntry
	if out.Console != nil {
		cc := *out.Console
		c.mu.Lock()
		cc.Self = e.consoleID != "" && c.console == e.consoleID
		c.mu.Unlock()
		out.Console = &cc
	}
	return out
}

// mayMirror reports whether c may receive the mirror: the paired console,
// by its proof, with its powers, while the human lets it mirror. Every
// console is local: den-api.sock is a socket on this machine, open to this
// user only.
func (s *Server) mayMirror(c *conn) bool {
	c.mu.Lock()
	id := c.console
	c.mu.Unlock()
	if id == "" {
		return false
	}
	s.con.mu.Lock()
	powered := s.con.cur != nil && s.con.cur.ID == id && s.con.cur.powers
	s.con.mu.Unlock()
	st := &s.conv
	st.mu.Lock()
	on := st.on && st.forID == id
	st.mu.Unlock()
	return powered && on
}

// conversationHistory is conversation.history: the newest entries first,
// at most limit (convHistoryMax) and as many as fit in one reply line
// (convHistoryBytes), only those since the mirror was turned
// on for this console (and since Den started), only for a console that
// may mirror.
func (s *Server) conversationHistory(c *conn, req Request) {
	if !s.mayMirror(c) {
		c.fail(req.ID, CodeForbidden, "the conversation is not shown in this console: the human lets it in Den")
		return
	}
	var p struct {
		Limit int `json:"limit"`
	}
	json.Unmarshal(req.Params, &p)
	if p.Limit <= 0 || p.Limit > convHistoryMax {
		p.Limit = convHistoryMax
	}
	st := &s.conv
	st.mu.Lock()
	since := st.since
	var out []ConvEntry
	for i := len(st.entries) - 1; i >= 0 && len(out) < p.Limit; i-- {
		e := st.entries[i]
		if e.At.Before(since.UTC()) {
			break
		}
		out = append(out, e.forConn(c))
	}
	st.mu.Unlock()
	// The reply is one line: the newest entries that fit convHistoryBytes,
	// at least one.
	size := 0
	for i, e := range out {
		raw, _ := json.Marshal(e)
		if size += len(raw) + 1; i > 0 && size > convHistoryBytes {
			out = out[:i]
			break
		}
	}
	if out == nil {
		out = []ConvEntry{}
	}
	c.reply(req.ID, map[string]any{"entries": out})
}

// convText is an entry's text: escapes, controls, bidi and other format
// characters (zero-width ones too) removed, whole up to convMaxBytes, and
// past that cut on a character with convCut as its last line: never cut
// silently.
func convText(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, sanitize.Text(s))
	s = strings.TrimSpace(s)
	if len(s) <= convMaxBytes {
		return s
	}
	room := convMaxBytes - len(convCut) - 1
	for room > 0 && !utf8.RuneStart(s[room]) {
		room--
	}
	return strings.TrimRight(s[:room], "\n ") + "\n" + convCut
}

// agentName is a name the mirror shows as it is: a short, plain name, no
// sentence in it.
var agentName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,39}$`)

// agentLabel is an agent's name in the mirror: a plain name, never one that
// could pass for Den, Drey, the human, the orchestrator or the console, and
// never a sentence; anything else is "an agent".
func agentLabel(name string) string {
	n := strings.TrimSpace(sanitize.Label(name, 60))
	if !agentName.MatchString(n) || strings.Contains(n, ". ") || strings.HasSuffix(n, ".") || reservedNames[strings.ToLower(n)] {
		return "an agent"
	}
	return n
}
