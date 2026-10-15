package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/theme"
)

func TestChatLogRoundTripAndReconcile(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "orchestrator.jsonl")
	m := Model{st: newStyles(theme.Deepharness), store: inbox.New(), chatLog: log, orch: orchState{cardCur: -1}}
	m.base = sample()
	m.refresh()
	sid := m.agents[0].Session.SessionID

	base := time.Now()
	you := chatEntry{role: "you", text: "review the notes", at: base, mission: "m1"}
	m.orch.chat = append(m.orch.chat, you)
	m.logEntry(you)
	d := &dispatch{ID: "t1", SessionID: sid, Agent: "Mossy Lemur", Brief: "Review the notes.", SentAt: base.Add(2 * time.Second)}
	m.orch.chat = append(m.orch.chat, chatEntry{role: "card", card: d, at: d.SentAt})
	m.logCard(d)
	d.State, d.StartedAt, d.Calls = cardWorking, time.Now(), 3
	m.logCard(d)
	// The orchestrator's text started streaming before the dispatch, but is
	// logged after it; restore must put it back in front of the card.
	reply := chatEntry{role: "orchestrator", text: "Mossy is on it.", at: base.Add(time.Second)}
	m.orch.chat = append(m.orch.chat, reply)
	m.logEntry(reply)
	m.logSession("sess-123")

	chat, session := loadChatLog(log, maxChatLog)
	if session != "sess-123" {
		t.Errorf("session = %q", session)
	}
	// restored note + you + card + orchestrator; the card appears once, in its last state.
	if len(chat) != 4 || chat[0].role != "note" || chat[1].mission != "m1" || chat[2].text != "Mossy is on it." || chat[3].card == nil || chat[3].card.State != cardWorking || chat[3].card.Calls != 3 {
		t.Fatalf("chat = %+v", chat)
	}

	// Reconcile: the agent's transcript has a reply after the brief, so the
	// card finishes with that reply; a card for a vanished session fails.
	tr := filepath.Join(dir, "agent.jsonl")
	later := time.Now().Add(30 * time.Second).UTC().Format(time.RFC3339Nano)
	os.WriteFile(tr, []byte(`{"type":"assistant","timestamp":"`+later+`","message":{"content":[{"type":"text","text":"Done: fixed the install path."}]}}`+"\n"), 0o644)
	m2 := Model{st: newStyles(theme.Deepharness), store: inbox.New(), chatLog: log, orch: orchState{cardCur: -1, chat: chat, reconcile: true}}
	m2.base = sample()
	m2.base[0].Path = tr
	m2.base[0].Status = fleet.Idle
	gone := &dispatch{ID: "t2", SessionID: "nope", Agent: "Comet Newt", Brief: "x", SentAt: time.Now(), State: cardWorking, StartedAt: time.Now()}
	m2.orch.chat = append(m2.orch.chat, chatEntry{role: "card", card: gone, at: gone.SentAt})
	m2.refresh()
	restored := chat[3].card
	if restored.State != cardDone || !strings.Contains(restored.Result, "fixed the install path") {
		t.Errorf("restored card should finish from the transcript: %+v", restored)
	}
	if gone.State != cardFailed {
		t.Errorf("card for a vanished session should fail: %+v", gone)
	}
	if m2.orch.reconcile {
		t.Error("reconcile should run once")
	}
	_ = discover.Session{}
}

// TestChatLogRestoreSanitises: an edited log cannot put escapes or bidi
// controls on screen, and a card written again keeps its brief as logged.
func TestChatLogRestoreSanitises(t *testing.T) {
	log := filepath.Join(t.TempDir(), "orchestrator.jsonl")
	m := Model{st: newStyles(theme.Deepharness), store: inbox.New(), chatLog: log, orch: orchState{cardCur: -1}}
	at := time.Date(2026, 10, 13, 5, 0, 0, 0, time.UTC)
	hostile := "a\x1b]52;c;aGk=\x07b\u202ec\x1b[2Jd"
	appendChatRecord(log, chatRecord{Kind: "orchestrator", At: at, Text: hostile})
	appendChatRecord(log, chatRecord{Kind: "fan", At: at, Text: "x", To: hostile, Status: hostile})
	appendChatRecord(log, chatRecord{Kind: "card", At: at, Card: &cardRecord{ID: "t1", SessionID: "s", Agent: hostile, Brief: hostile, Reason: hostile, Ticker: hostile, Result: hostile, SentAt: at}})
	before, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	chat, _ := loadChatLog(log, maxChatLog)
	for _, e := range chat {
		fields := []string{e.text, e.to, e.status}
		if d := e.card; d != nil {
			fields = append(fields, d.Agent, d.Brief, d.Reason, d.Ticker, d.Result)
		}
		for _, f := range fields {
			if strings.ContainsAny(f, "\x1b\x07\u202e") {
				t.Errorf("restored %s entry keeps %q", e.role, f)
			}
		}
	}
	after, _ := os.ReadFile(log)
	if string(after) != string(before) {
		t.Error("loading the log changed it")
	}
	// Written again (a state change), the card logs the brief it was given.
	for _, e := range chat {
		if e.card != nil {
			m.logCard(e.card)
		}
	}
	again, _ := os.ReadFile(log)
	last := strings.TrimSpace(string(again[len(before):]))
	raw, _ := json.Marshal(hostile)
	if !strings.Contains(last, `"brief":`+string(raw)) {
		t.Errorf("the brief should be logged as it came: %s", last)
	}
}

// TestChatLogRestoreIsCapped: a log with huge records comes back cut to
// size, a line too long to read is skipped without losing the lines after
// it, and laying the restored chat out again on a resize stays quick.
func TestChatLogRestoreIsCapped(t *testing.T) {
	log := filepath.Join(t.TempDir(), "orchestrator.jsonl")
	at := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	appendChatRecord(log, chatRecord{Kind: "session", At: at, Session: "s1"})
	appendChatRecord(log, chatRecord{Kind: "orchestrator", At: at, Text: strings.Repeat("x", maxChatLine)})
	// About 100 KB of markdown, and a text where the cut falls inside a character.
	para := strings.Repeat("Some **bold** words, a `code` span and a [link](https://example.com).\n", 1500)
	wide := "a" + strings.Repeat("é", 60000)
	for i := range 40 {
		text := para
		if i%2 == 1 {
			text = wide
		}
		appendChatRecord(log, chatRecord{Kind: "orchestrator", At: at.Add(time.Duration(i) * time.Minute), Text: text})
	}
	appendChatRecord(log, chatRecord{Kind: "card", At: at.Add(time.Hour), Card: &cardRecord{ID: "t1", SessionID: "s", Agent: "Mossy Lemur", Brief: para, Result: para, SentAt: at.Add(time.Hour), State: cardDone}})
	appendChatRecord(log, chatRecord{Kind: "session", At: at, Session: "s2"})

	chat, session := loadChatLog(log, maxChatLog)
	if session != "s2" {
		t.Errorf("session %q: the lines after the oversized one were not read", session)
	}
	total := 0
	for _, e := range chat[1:] {
		texts := []string{e.text}
		if e.card != nil {
			texts = []string{e.card.Brief, e.card.Result}
		}
		for _, s := range texts {
			if len(s) > maxRestoredText+len(cutMark) || !strings.HasSuffix(s, cutMark) || !utf8.ValidString(s) {
				t.Fatalf("a %s entry came back at %d bytes, not cut at %d", e.role, len(s), maxRestoredText)
			}
			total += len(s)
		}
	}
	if total > restoreBudget+len(chat)*len(cutMark) {
		t.Errorf("restored %d bytes of text, over the %d budget", total, restoreBudget)
	}
	if e := chat[len(chat)-1]; e.card == nil {
		t.Error("the newest record, the card, was not restored")
	}
	if !strings.Contains(chat[0].text, "older records stay in the log") {
		t.Errorf("the note does not say older records were left: %q", chat[0].text)
	}

	m := testModel(120, 40)
	m.orch.chat = chat
	budget := 2 * time.Second
	if raceDetector {
		budget = 30 * time.Second // only a hang fails here
	}
	for _, w := range []int{120, 80} { // the first layout, then a resize
		start := time.Now()
		m.chatLines(w, func(chatEntry) bool { return true })
		if d := time.Since(start); d > budget {
			t.Errorf("laying out the restored chat at width %d took %v", w, d)
		}
	}
}
