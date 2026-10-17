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
	convMaxRunes     = 2000
	convMaxLines     = 20
	convCut          = "(cut: the rest is in Den)"
	convKeep         = 500 // entries kept in memory
	convHistoryMax   = 200 // entries one conversation.history returns
	convPerAgent     = 6   // report entries a minute per agent
	convReportsTotal = 30  // report entries a minute across all agents
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
}

type convStored struct {
	ConvEntry
	consoleID string
}

// SetMirror turns the mirror on for console id (the human's choice, made
// in Den) or off. On, entries from now on reach that console; a new
// pairing starts with it off.
func (s *Server) SetMirror(id string, on bool) {
	st := &s.conv
	st.mu.Lock()
	st.on, st.forID = on && id != "", id
	if st.on {
		st.since = time.Now()
	}
	st.mu.Unlock()
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
	if n := len(st.entries) - convKeep; n > 0 {
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
// at most limit (convHistoryMax), only those since the mirror was turned
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
	if out == nil {
		out = []ConvEntry{}
	}
	c.reply(req.ID, map[string]any{"entries": out})
}

// convText is an entry's text: escapes, controls, bidi and other format
// characters (zero-width ones too) removed, at most convMaxLines lines and
// convMaxRunes runes, with convCut when it was longer: never cut silently.
func convText(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, sanitize.Text(s))
	s = strings.TrimSpace(s)
	cut := false
	if lines := strings.Split(s, "\n"); len(lines) > convMaxLines {
		s, cut = strings.Join(lines[:convMaxLines-1], "\n"), true // the cut note is the last line
	}
	if utf8.RuneCountInString(s) > convMaxRunes {
		cut = true
	}
	if !cut {
		return s
	}
	if room := convMaxRunes - utf8.RuneCountInString(convCut) - 1; utf8.RuneCountInString(s) > room {
		s = string([]rune(s)[:room])
	}
	return strings.TrimRight(s, "\n ") + "\n" + convCut
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
