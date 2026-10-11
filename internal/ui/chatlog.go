package ui

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/GlacierHubAB/den/internal/fleet"
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
}

type cardRecord struct {
	ID        string    `json:"id"`
	SessionID string    `json:"session"`
	Agent     string    `json:"agent"`
	Brief     string    `json:"brief"`
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
}

// maxChatLog is how many records come back on launch.
const maxChatLog = 500

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
	appendChatRecord(m.chatLog, chatRecord{Kind: e.role, At: e.at, Text: e.text, To: e.to, Status: e.status, Mission: e.mission})
}

// logCard persists the card's current state.
func (m *Model) logCard(d *dispatch) {
	if d == nil {
		return
	}
	appendChatRecord(m.chatLog, chatRecord{Kind: "card", At: d.SentAt, Card: &cardRecord{
		ID: d.ID, SessionID: d.SessionID, Agent: d.Agent, Brief: d.Brief,
		SentAt: d.SentAt, StartedAt: d.StartedAt, EndedAt: d.EndedAt,
		State: d.State, Reason: d.Reason, Calls: d.Calls, Ticker: d.Ticker, Result: d.Result, Files: d.Files, MissionID: d.MissionID, Thread: d.Thread, Stopped: d.Stopped,
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
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var r chatRecord
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			continue
		}
		if r.Kind == "session" {
			session = r.Session
			continue
		}
		records = append(records, r)
	}
	if len(records) > n {
		records = records[len(records)-n:]
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
			nd := dispatch{ID: c.ID, SessionID: c.SessionID, Agent: c.Agent, Brief: c.Brief, SentAt: c.SentAt, StartedAt: c.StartedAt, EndedAt: c.EndedAt, State: c.State, Reason: c.Reason, Calls: c.Calls, Ticker: c.Ticker, Result: c.Result, Files: c.Files, MissionID: c.MissionID, Thread: thread, Stopped: c.Stopped}
			if d, ok := cards[c.ID]; ok {
				*d = nd
				continue
			}
			d := &nd
			cards[c.ID] = d
			chat = append(chat, chatEntry{role: "card", card: d, toolUseID: c.ID, at: r.At, mission: thread})
			continue
		}
		chat = append(chat, chatEntry{role: r.Kind, text: r.Text, to: r.To, status: r.Status, at: r.At, mission: r.Mission})
	}
	// Streamed text is logged when its final block lands, which can be after
	// the card for a dispatch made in the same turn; the screen order is by
	// time, so restore that.
	sort.SliceStable(chat, func(i, j int) bool { return chat[i].at.Before(chat[j].at) })
	if len(chat) > 0 {
		first, last := chat[0].at, chat[len(chat)-1].at
		note := fmt.Sprintf("restored %d entries · %s to %s", len(chat), first.Format("Jan 2 15:04"), last.Format("15:04"))
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
