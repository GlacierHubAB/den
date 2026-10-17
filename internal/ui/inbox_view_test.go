package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/theme"
)

func req(event, session string, in map[string]any) *bridge.Request {
	in["session_id"] = session
	in["hook_event_name"] = event
	p, _ := json.Marshal(in)
	r := &bridge.Request{Envelope: bridge.Envelope{Event: event, SessionID: session, Payload: p}}
	json.Unmarshal(p, &r.Input)
	return r
}

func TestInboxRendersAllKinds(t *testing.T) {
	store := inbox.New()
	agents := sample()
	s1 := agents[0].Session.SessionID
	store.Apply(req("Notification", s1, map[string]any{"notification_type": "permission_prompt", "message": "Claude needs your permission to use Bash"}))
	store.Apply(req("PreToolUse", s1, map[string]any{"tool_name": "AskUserQuestion", "tool_input": map[string]any{"questions": []map[string]any{{"question": "Which key should trigger the menu?", "options": []map[string]string{{"label": "Super+Space"}, {"label": "Super+M"}}}}}}))
	store.Apply(req("Stop", s1, map[string]any{"last_assistant_message": "Pushed GlacierHubAB/omarchy-super-hints.\n\nOpened issue #10463."}))
	store.Apply(req("Notification", s1, map[string]any{"notification_type": "idle_prompt", "message": "Claude is waiting for your input"}))

	for _, size := range [][2]int{{140, 40}, {90, 30}} {
		for _, tb := range []tab{tabFleet, tabInbox} {
			m := Model{st: newStyles(theme.Deepharness), agents: agents, store: store, tab: tb, width: size[0], height: size[1]}
			m.input = newInput(m.st)
			out := m.View().Content
			for i, l := range strings.Split(out, "\n") {
				if w := ansi.StringWidth(l); w > size[0] {
					t.Errorf("tab %d %dx%d line %d too wide (%d): %q", tb, size[0], size[1], i, w, ansi.Strip(l))
				}
			}
			plain := ansi.Strip(out)
			if tb == tabFleet {
				continue // the Fleet view no longer shows the inbox
			}
			for _, want := range []string{"PERMISSION", "QUESTION", "DONE", "IDLE", "Super+M"} {
				if !strings.Contains(plain, want) {
					t.Errorf("tab %d: missing %q", tb, want)
				}
			}
			if os.Getenv("DEN_SHOW") != "" && size[0] == 140 && tb == tabFleet {
				fmt.Println(plain)
			}
		}
	}
}

func TestInboxKeepsSelectionVisibleAndNewestOnTop(t *testing.T) {
	m := Model{st: newStyles(theme.Deepharness), agents: sample(), store: inbox.New(), tab: tabInbox, width: 100, height: 24}
	m.input = newInput(m.st)
	for i := 0; i < 20; i++ {
		m.store.Apply(req("Stop", "0123456789abcdef", map[string]any{"last_assistant_message": fmt.Sprintf("done number %02d", i)}))
	}
	out := ansi.Strip(m.View().Content)
	if !strings.Contains(out, "done number 19") {
		t.Errorf("newest notice should be on top:\n%s", out)
	}
	m.inboxCur = 19
	out = ansi.Strip(m.View().Content)
	if !strings.Contains(out, "done number 00") {
		t.Errorf("selected (oldest) notice should scroll into view:\n%s", out)
	}
}

// Inbox prose wraps at word boundaries to the pane instead of being cut:
// a long summary shows whole, word for word, no "…", no line too wide.
func TestInboxWrapsInsteadOfCutting(t *testing.T) {
	store := inbox.New()
	agents := sample()
	s1 := agents[0].Session.SessionID
	long := "I've asked the designer for the look on the two queued items from drey-lead and the strip's wording, and I'll come back with screenshots once both are signed off by the human."
	store.Apply(req("Stop", s1, map[string]any{"last_assistant_message": long}))
	m := Model{st: newStyles(theme.Deepharness), agents: agents, store: store, tab: tabInbox, width: 70, height: 40}
	m.input = newInput(m.st)
	var it *inbox.Item
	for _, x := range store.Items() {
		it = x
	}
	const w = 60
	lines := m.inboxBlock(it, false, w)
	plain := ansi.Strip(strings.Join(lines, "\n"))
	if strings.Contains(plain, "…") {
		t.Errorf("prose was cut:\n%s", plain)
	}
	words := " " + strings.Join(strings.Fields(strings.NewReplacer("▌", " ", "▏", " ").Replace(plain)), " ") + " "
	for _, word := range strings.Fields(long) {
		if !strings.Contains(words, " "+word+" ") {
			t.Errorf("word %q missing or split:\n%s", word, plain)
		}
	}
	for i, l := range lines {
		if ansi.StringWidth(l) > w {
			t.Errorf("line %d too wide (%d): %q", i, ansi.StringWidth(l), ansi.Strip(l))
		}
	}
}
