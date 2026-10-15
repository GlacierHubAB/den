package ui

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/sanitize"
	"github.com/GlacierHubAB/den/internal/transcript"
)

// chatRecord is one line of the orchestrator log on disk. A card is written
// again on every state change; on load the last record for an id wins and
// the card keeps the position of its first record.
type chatRecord struct {
	Kind    string      `json:"kind"` // you, orchestrator, note, fan, card, session
	At      time.Time   `json:"at"`
	Text    string      `json:"text,omitempty"`
	To      string      `json:"to,omitempty"`
	Status  string      `json:"status,omitempty"`
	Session string      `json:"session,omitempty"` // kind "session": the orchestrator's session id
	Mission string      `json:"mission,omitempty"` // the thread: the mission it was about
	Card    *cardRecord `json:"card,omitempty"`
	App     bool        `json:"app,omitempty"` // written in a paired app's turn
}

type cardRecord struct {
	ID        string    `json:"id"`
	SessionID string    `json:"session"`
	Agent     string    `json:"agent"`
	Brief     string    `json:"brief"`
	Summary   string    `json:"summary,omitempty"`
	SentAt    time.Time `json:"sent"`
	StartedAt time.Time `json:"started,omitempty"`
	EndedAt   time.Time `json:"ended,omitempty"`
	State     cardState `json:"state"`
	Reason    string    `json:"reason,omitempty"`
	Calls     int       `json:"calls,omitempty"`
	Ticker    string    `json:"ticker,omitempty"`
	Result    string    `json:"result,omitempty"`
	Files     int       `json:"files,omitempty"`
	MissionID string    `json:"mission,omitempty"`
	Thread    string    `json:"thread,omitempty"`
	Stopped   time.Time `json:"stopped,omitempty"`
	App       bool      `json:"app,omitempty"` // made in a paired app's turn
}

// maxChatLog is how many records come back on launch.
const maxChatLog = 500

// What comes back is capped too: the chat is laid out whole on the first
// frame and on every resize, at about 0.45 µs a byte, and the log has no
// limit of its own.
const (
	maxRestoredText = 32 << 10  // per text of a record; the rest stays in the log
	restoreBudget   = 512 << 10 // all restored text, newest first; older records stay in the log
	maxChatLine     = 4 << 20   // a longer line is skipped, and the lines after it still read
	maxRestoredWord = 1 << 10   // the short fields (names, states, ids), which Den writes short
)

// cutMark ends a text that was cut on restore.
const cutMark = "\n\n(cut: the rest is in the orchestrator log)"

// cutText keeps at most max bytes of s, ending on a whole character.
func cutText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	i := max
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i] + cutMark
}

// cutShort cuts the record's short fields: only an edited log has long
// ones, and they are drawn whole.
func cutShort(r *chatRecord) {
	for _, f := range []*string{&r.To, &r.Status, &r.Session, &r.Mission} {
		*f = cutWord(*f)
	}
	if c := r.Card; c != nil {
		for _, f := range []*string{&c.ID, &c.SessionID, &c.Agent, &c.Summary, &c.Reason, &c.Ticker, &c.MissionID, &c.Thread} {
			*f = cutWord(*f)
		}
	}
}

// cutWord keeps at most maxRestoredWord bytes of s, ending on a whole
// character.
func cutWord(s string) string {
	if len(s) <= maxRestoredWord {
		return s
	}
	i := maxRestoredWord
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i]
}

// recordText is how much text a record brings back.
func recordText(r chatRecord) int {
	n := len(r.Text)
	if r.Card != nil {
		n += len(r.Card.Brief) + len(r.Card.Result)
	}
	return n
}

// eachLine calls fn for each line of r, without its newline. A line longer
// than max is skipped whole, so one oversized record does not end the read.
func eachLine(r io.Reader, max int, fn func([]byte)) {
	br := bufio.NewReaderSize(r, 64<<10)
	var line []byte
	skip := false
	for {
		chunk, err := br.ReadSlice('\n')
		if !skip {
			if len(line)+len(chunk) > max {
				line, skip = line[:0], true
			} else {
				line = append(line, chunk...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if !skip && len(line) > 0 {
			fn(bytes.TrimSuffix(line, []byte("\n")))
		}
		line, skip = line[:0], false
		if err != nil {
			return
		}
	}
}

// ChatLogPath is the orchestrator log: one JSON record per line.
func ChatLogPath() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "den", "orchestrator.jsonl")
}

func appendChatRecord(path string, r chatRecord) {
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	raw, err := json.Marshal(r)
	if err != nil {
		return
	}
	f.Write(append(raw, '\n'))
}

// logEntry persists a chat entry that is final (streamed text is logged
// once the full block lands).
func (m *Model) logEntry(e chatEntry) {
	if e.card != nil {
		m.logCard(e.card)
		return
	}
	appendChatRecord(m.chatLog, chatRecord{Kind: e.role, At: e.at, Text: e.text, To: e.to, Status: e.status, Mission: e.mission, App: e.app})
}

// logCard persists the card's current state.
func (m *Model) logCard(d *dispatch) {
	if d == nil {
		return
	}
	appendChatRecord(m.chatLog, chatRecord{Kind: "card", At: d.SentAt, Card: &cardRecord{
		ID: d.ID, SessionID: d.SessionID, Agent: d.Agent, Brief: firstNonEmptyS(d.rawBrief, d.Brief), Summary: d.Summary,
		SentAt: d.SentAt, StartedAt: d.StartedAt, EndedAt: d.EndedAt,
		State: d.State, Reason: d.Reason, Calls: d.Calls, Ticker: d.Ticker, Result: d.Result, Files: d.Files, MissionID: d.MissionID, Thread: d.Thread, Stopped: d.Stopped, App: d.App,
	}})
}

// logSession persists which orchestrator session to resume next time.
func (m *Model) logSession(id string) {
	appendChatRecord(m.chatLog, chatRecord{Kind: "session", At: time.Now(), Session: id})
}

// loadChatLog rebuilds the chat and the orchestrator session id from the
// log. It returns the entries, the session id, and how many were read.
func loadChatLog(path string, n int) (chat []chatEntry, session string) {
	f, err := os.Open(path)
	if err != nil {
		return nil, ""
	}
	defer f.Close()
	var records []chatRecord
	older := 0
	eachLine(f, maxChatLine, func(line []byte) {
		var r chatRecord
		if json.Unmarshal(line, &r) != nil {
			return
		}
		cutShort(&r)
		if r.Kind == "session" {
			session = r.Session
			return
		}
		r.Text = cutText(r.Text, maxRestoredText)
		if r.Card != nil {
			r.Card.Brief, r.Card.Result = cutText(r.Card.Brief, maxRestoredText), cutText(r.Card.Result, maxRestoredText)
		}
		records = append(records, r)
		// Only the newest n are kept, so a long log holds no more than
		// 2n records in memory while it is read.
		if len(records) > 2*n {
			older += len(records) - n
			records = append(records[:0], records[len(records)-n:]...)
		}
	})
	if len(records) > n {
		older += len(records) - n
		records = records[len(records)-n:]
	}
	// Newest first, until the budget is spent.
	left := restoreBudget
	for i := len(records) - 1; i >= 0; i-- {
		if left -= recordText(records[i]); left < 0 {
			older, records = older+i+1, records[i+1:]
			break
		}
	}
	cards := map[string]*dispatch{}
	for _, r := range records {
		if r.Kind == "card" && r.Card != nil {
			c := r.Card
			// Cards logged before threads were kept fall back to their run's
			// mission.
			thread := c.Thread
			if thread == "" {
				thread = c.MissionID
			}
			// The log is Den's own, but a file can be edited: what comes
			// back is sanitised like any outside text. The brief keeps its
			// raw form for the log, so records written again are unchanged.
			nd := dispatch{ID: c.ID, SessionID: c.SessionID, Agent: sanitize.Line(c.Agent), Brief: sanitize.Text(c.Brief), rawBrief: c.Brief, Summary: sanitize.Line(c.Summary), SentAt: c.SentAt, StartedAt: c.StartedAt, EndedAt: c.EndedAt, State: c.State, Reason: sanitize.Line(c.Reason), Calls: c.Calls, Ticker: sanitize.Line(c.Ticker), Result: sanitize.Text(c.Result), Files: c.Files, MissionID: c.MissionID, Thread: thread, Stopped: c.Stopped, App: c.App}
			if d, ok := cards[c.ID]; ok {
				*d = nd
				continue
			}
			d := &nd
			cards[c.ID] = d
			chat = append(chat, chatEntry{role: "card", card: d, toolUseID: c.ID, at: r.At, mission: thread, app: c.App})
			continue
		}
		chat = append(chat, chatEntry{role: r.Kind, text: sanitize.Text(r.Text), to: sanitize.Line(r.To), status: sanitize.Line(r.Status), at: r.At, mission: r.Mission, app: r.App || r.Kind == "app", memo: &mdMemo{}})
	}
	// Streamed text is logged when its final block lands, which can be after
	// the card for a dispatch made in the same turn; the screen order is by
	// time, so restore that.
	sort.SliceStable(chat, func(i, j int) bool { return chat[i].at.Before(chat[j].at) })
	if len(chat) > 0 {
		first, last := chat[0].at, chat[len(chat)-1].at
		note := fmt.Sprintf("restored %d entries · %s to %s", len(chat), first.Format("Jan 2 15:04"), last.Format("15:04"))
		if older > 0 {
			note += fmt.Sprintf(" · %d older records stay in the log", older)
		}
		chat = append([]chatEntry{{role: "note", text: note, at: time.Now()}}, chat...)
	}
	return chat, session
}

// reconcileCards settles cards that were live when Den last quit, once the
// fleet is known: a session that is gone fails them; a transcript with a
// reply after the brief finishes them with that reply.
func (m *Model) reconcileCards() {
	for _, d := range m.liveCards() {
		a, ok := m.agentFor(d.SessionID)
		if !ok {
			d.State, d.Reason, d.EndedAt = cardFailed, "gone · session ended", time.Now()
			m.logCard(d)
			continue
		}
		from := d.StartedAt
		if from.IsZero() {
			from = d.SentAt
		}
		turn := transcript.Turn(a.Path, from, time.Time{}, 200)
		var last *transcript.Entry
		for i := range turn {
			if turn[i].Kind == "text" {
				last = &turn[i]
			}
		}
		if last != nil && a.Status != fleet.Working {
			d.State, d.EndedAt, d.Result = cardDone, last.At, last.Text
			if d.StartedAt.IsZero() {
				d.StartedAt = d.SentAt
			}
			m.loadTurn(d)
			m.logCard(d)
			continue
		}
		if d.State == cardQueued && !a.Session.Alive {
			d.State, d.Reason, d.EndedAt = cardFailed, "gone · session ended", time.Now()
			m.logCard(d)
		}
	}
}
