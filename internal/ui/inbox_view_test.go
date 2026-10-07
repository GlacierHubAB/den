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
