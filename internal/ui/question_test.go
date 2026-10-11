package ui

import (
	"encoding/json"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/inbox"
)

var twoAsks = map[string]any{"tool_name": "AskUserQuestion", "tool_input": map[string]any{"questions": []map[string]any{
	{"question": "Which colour scheme?", "header": "Colours", "options": []map[string]string{{"label": "Warm", "description": "Oranges"}, {"label": "Cool, calm", "description": "Blues"}}},
	{"question": "Which extras?", "header": "Extras", "multiSelect": true, "options": []map[string]string{{"label": "Icons"}, {"label": "Sounds"}, {"label": "Badges"}}},
}}}

// heldQuestion is a model on the Inbox with an agent's two-question call
// held for Den, and the request its hook waits on.
func heldQuestion(t *testing.T, on bool) (Model, *bridge.Request) {
	m := testModel(140, 40)
	m.tab = tabInbox
	m.store.AnswerQuestions(on)
	sid := m.agents[0].Session.SessionID
	m.store.Apply(req("PreToolUse", sid, map[string]any{"tool_name": twoAsks["tool_name"], "tool_input": twoAsks["tool_input"]}))
	in := map[string]any{"tool_name": twoAsks["tool_name"], "tool_input": twoAsks["tool_input"], "session_id": sid, "hook_event_name": "PermissionRequest"}
	p, _ := json.Marshal(in)
	r := bridge.TestRequest(bridge.Envelope{Event: "PermissionRequest", SessionID: sid, Payload: p})
	m.store.Apply(r)
	return m, r
}

func enter(m Model, text string) Model {
	m = typeText(m, text)
	mm, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	return mm.(Model)
}

func TestQuestionAnsweredFromTheInbox(t *testing.T) {
	m, r := heldQuestion(t, true)
	out := ansi.Strip(m.View().Content)
	for _, want := range []string{"QUESTION", "Which colour scheme?", "Question 1 of 2 · Colours", "2 Cool, calm — Blues", "1-2 or your own answer, Enter", "^N answer in the terminal"} {
		if !strings.Contains(out, want) {
			t.Errorf("inbox lacks %q:\n%s", want, out)
		}
	}
	m = enter(m, "5")
	if !m.flashErr || !strings.Contains(m.flash, "1-2") || m.input.Value() != "5" {
		t.Errorf("a missing option: flash %q, input %q", m.flash, m.input.Value())
	}
	m.input.Reset()
	m = enter(m, "2")
	if m.flashErr || !strings.Contains(m.flash, "Next question") || m.input.Value() != "" {
		t.Errorf("first answer: flash %q", m.flash)
	}
	out = ansi.Strip(m.View().Content)
	for _, want := range []string{"Which extras?", "choose one or more", "✓ Which colour scheme? → Cool, calm", "1-3 … or your own answer"} {
		if !strings.Contains(out, want) {
			t.Errorf("step 2 lacks %q:\n%s", want, out)
		}
	}
	if _, decided := r.Decided(); decided {
		t.Fatal("replied before the second answer")
	}
	m = enter(m, "1 3")
	d, decided := r.Decided()
	if !decided || d.Behavior != "allow" || d.Answers["Which colour scheme?"] != "Cool, calm" || d.Answers["Which extras?"] != "Icons, Badges" {
		t.Errorf("decision = %+v", d)
	}
	if !strings.Contains(m.flash, "Answered") {
		t.Errorf("flash = %q", m.flash)
	}
}

func TestQuestionKeys(t *testing.T) {
	m, r := heldQuestion(t, true)
	mm, _ := m.handleKey(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	m = mm.(Model)
	if _, decided := r.Decided(); decided || !m.flashErr || !strings.Contains(m.flash, "Type a number") {
		t.Errorf("^Y must not answer: flash %q", m.flash)
	}
	mm, _ = m.command("/answer Something of my own")
	m = mm.(Model)
	it := m.store.Items()[0]
	if it.Step != 1 || it.Answers["Which colour scheme?"] != "Something of my own" {
		t.Errorf("/answer: %+v", it)
	}
	mm, _ = m.handleKey(tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	m = mm.(Model)
	d, decided := r.Decided()
	if !decided || d.Behavior != "" || it.State != inbox.InTerminal || it.AsksInDen() {
		t.Errorf("^N should hand it to the terminal: %+v %v %+v", d, decided, it)
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "you chose the terminal · answer in the terminal") {
		t.Errorf("released item:\n%s", ansi.Strip(m.View().Content))
	}
}

func TestQuestionLetGoWhenTheHookHangsUp(t *testing.T) {
	t.Setenv("HYPRLAND_INSTANCE_SIGNATURE", "")
	m, r := heldQuestion(t, true)
	bridge.TestHangUp(r)
	if m.releaseIfAtTerminal() {
		t.Error("still held after the hook hung up")
	}
	if it := m.store.Items()[0]; it.State != inbox.InTerminal || !strings.Contains(ansi.Strip(m.View().Content), "no longer held · answer in the terminal") {
		t.Errorf("item = %+v", it)
	}
}

func TestQuestionOptionOff(t *testing.T) {
	m, r := heldQuestion(t, false)
	for _, it := range m.store.Items() {
		if it.AsksInDen() {
			t.Fatalf("held a question with the option off: %+v", it)
		}
	}
	mm, _ := m.command("/answer 1")
	m = mm.(Model)
	if _, decided := r.Decided(); decided || !strings.Contains(m.flash, "No question") {
		t.Errorf("/answer with the option off: flash %q", m.flash)
	}
}
