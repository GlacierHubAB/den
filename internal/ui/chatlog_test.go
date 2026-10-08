package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

	you := chatEntry{role: "you", text: "review the notes", at: time.Now()}
	m.orch.chat = append(m.orch.chat, you)
	m.logEntry(you)
	d := &dispatch{ID: "t1", SessionID: sid, Agent: "Mossy Lemur", Brief: "Review the notes.", SentAt: time.Now()}
	m.orch.chat = append(m.orch.chat, chatEntry{role: "card", card: d, at: d.SentAt})
	m.logCard(d)
	d.State, d.StartedAt, d.Calls = cardWorking, time.Now(), 3
	m.logCard(d)
	reply := chatEntry{role: "orchestrator", text: "Mossy is on it.", at: time.Now()}
	m.orch.chat = append(m.orch.chat, reply)
	m.logEntry(reply)
	m.logSession("sess-123")

	chat, session := loadChatLog(log, maxChatLog)
	if session != "sess-123" {
		t.Errorf("session = %q", session)
	}
	// restored note + you + card + orchestrator; the card appears once, in its last state.
	if len(chat) != 4 || chat[0].role != "note" || chat[2].card == nil || chat[2].card.State != cardWorking || chat[2].card.Calls != 3 || chat[3].text != "Mossy is on it." {
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
	restored := chat[2].card
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
