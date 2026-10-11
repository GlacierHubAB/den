package inbox

import (
	"encoding/json"
	"testing"

	"github.com/GlacierHubAB/den/internal/bridge"
)

const sid = "11111111-2222-4333-8444-555555555555"

var askInput = map[string]any{"questions": []map[string]any{
	{"question": "Which colour scheme?", "header": "Colours", "options": []map[string]string{{"label": "Warm", "description": "Oranges"}, {"label": "Cool, calm", "description": "Blues"}}},
	{"question": "Which extras?", "header": "Extras", "multiSelect": true, "options": []map[string]string{{"label": "Icons"}, {"label": "Sounds"}, {"label": `Say "hi"`}}},
}}

func askHook(event string, in map[string]any) *bridge.Request {
	in["session_id"] = sid
	in["hook_event_name"] = event
	p, _ := json.Marshal(in)
	env := bridge.Envelope{Event: event, SessionID: sid, Payload: p}
	if event == "PermissionRequest" {
		return bridge.TestRequest(env)
	}
	r := &bridge.Request{Envelope: env}
	json.Unmarshal(p, &r.Input)
	return r
}

// ask is what Claude Code sends for one AskUserQuestion call: PreToolUse,
// then PermissionRequest while its dialog is already on screen (2.1.289).
func ask(store *Store) (*Item, *bridge.Request) {
	pre := store.Apply(askHook("PreToolUse", map[string]any{"tool_name": "AskUserQuestion", "tool_input": askInput}))
	r := askHook("PermissionRequest", map[string]any{"tool_name": "AskUserQuestion", "tool_input": askInput})
	it := store.Apply(r)
	// Claude Code then notifies about its dialog (2.1.289, mock run).
	store.Apply(askHook("Notification", map[string]any{"notification_type": "permission_prompt", "message": "Claude needs your permission"}))
	if store.asks && pre != nil && it != nil && pre != it {
		panic("the PreToolUse item should become the held question")
	}
	return it, r
}

func TestQuestionsOffStayInTheTerminal(t *testing.T) {
	store := New()
	it, r := ask(store)
	// Den does not hold the call: a bare allow would be ignored by Claude
	// Code, so its dialog answers it and the PreToolUse notice is the entry.
	if it != nil {
		t.Fatalf("held as %+v", it)
	}
	if d, decided := r.Decided(); !decided || d.Behavior != "" {
		t.Errorf("released with %+v %v, want no decision at once", d, decided)
	}
	items := store.Items()
	if len(items) != 1 || items[0].Kind != Question || items[0].State != InTerminal || items[0].Answerable() || items[0].Title != "Which colour scheme? (+1 more)" {
		t.Fatalf("items = %+v", items)
	}
	if store.Answer(items[0], "allow") {
		t.Error("allowed a question in den")
	}
	if done, ok := store.AnswerQuestion(items[0], "1"); done || ok {
		t.Error("answered a question with the option off")
	}
	store.Apply(askHook("PostToolUse", map[string]any{"tool_name": "AskUserQuestion"}))
	if items[0].State != Resolved || store.Waiting() != 0 {
		t.Errorf("after the terminal answer: %+v", items[0])
	}
}

func TestQuestionAnsweredStepByStep(t *testing.T) {
	store := New()
	store.AnswerQuestions(true)
	it, r := ask(store)
	if it == nil || !it.AsksInDen() || it.Kind != Question || it.State != Waiting {
		t.Fatalf("held question = %+v", it)
	}
	if len(store.Items()) != 1 || store.Waiting() != 1 {
		t.Errorf("items = %d, waiting = %d", len(store.Items()), store.Waiting())
	}
	if it.Title != "Which colour scheme?" || it.Intent != "Question 1 of 2 · Colours" || it.Options[1] != "Cool, calm — Blues" {
		t.Errorf("step 1 = %q %q %q", it.Title, it.Intent, it.Options)
	}
	if store.Answer(it, "allow") {
		t.Error("allow must not answer a question")
	}
	if _, ok := store.AnswerQuestion(it, "3"); ok {
		t.Error("option 3 does not exist")
	}
	if done, ok := store.AnswerQuestion(it, "2"); done || !ok {
		t.Fatalf("first answer: done %v ok %v", done, ok)
	}
	if _, decided := r.Decided(); decided {
		t.Fatal("replied before every question was answered")
	}
	if it.Title != "Which extras?" || it.Intent != "Question 2 of 2 · Extras · choose one or more" || len(it.Body) != 1 || it.Body[0] != "✓ Which colour scheme? → Cool, calm" {
		t.Errorf("step 2 = %q %q %q", it.Title, it.Intent, it.Body)
	}
	if done, ok := store.AnswerQuestion(it, "1 3"); !done || !ok {
		t.Fatalf("second answer: done %v ok %v", done, ok)
	}
	d, decided := r.Decided()
	want := map[string]string{"Which colour scheme?": "Cool, calm", "Which extras?": `Icons, "Say \"hi\""`}
	if !decided || d.Behavior != "allow" || len(d.Answers) != 2 || d.Answers["Which colour scheme?"] != want["Which colour scheme?"] || d.Answers["Which extras?"] != want["Which extras?"] {
		t.Errorf("decision = %+v", d)
	}
	if it.State != Resolved || store.Waiting() != 0 || it.Answerable() {
		t.Errorf("after the answers: %+v", it)
	}
}

func TestQuestionFreeText(t *testing.T) {
	store := New()
	store.AnswerQuestions(true)
	it, r := ask(store)
	store.AnswerQuestion(it, "Something else, typed")
	store.AnswerQuestion(it, `"2"`)
	d, _ := r.Decided()
	if d.Answers["Which colour scheme?"] != "Something else, typed" || d.Answers["Which extras?"] != "2" {
		t.Errorf("answers = %v", d.Answers)
	}
}

func TestQuestionAnsweredInTheTerminal(t *testing.T) {
	store := New()
	store.AnswerQuestions(true)
	it, r := ask(store)
	store.AnswerQuestion(it, "1") // half-way in Den
	store.Apply(askHook("PostToolUse", map[string]any{"tool_name": "AskUserQuestion"}))
	d, decided := r.Decided()
	if !decided || d.Behavior != "" || it.State != Resolved || it.AsksInDen() {
		t.Errorf("after a terminal answer: decision %+v %v, item %+v", d, decided, it)
	}
}

func TestQuestionHookHungUp(t *testing.T) {
	store := New()
	store.AnswerQuestions(true)
	it, r := ask(store)
	if store.LetGo() {
		t.Error("let go of a question still held")
	}
	bridge.TestHangUp(r) // the dialog was dismissed, or the wait ran out
	if !store.LetGo() || it.State != InTerminal || it.AsksInDen() {
		t.Errorf("after hang-up: %+v", it)
	}
	if _, ok := store.AnswerQuestion(it, "1"); ok {
		t.Error("answered a question nobody waits for")
	}
}

func TestQuestionReleasedToTheTerminal(t *testing.T) {
	store := New()
	store.AnswerQuestions(true)
	it, r := ask(store)
	store.Release(it, "you were at that terminal")
	d, decided := r.Decided()
	if !decided || d.Behavior != "" || it.State != InTerminal || it.AsksInDen() {
		t.Errorf("released: %+v %v %+v", d, decided, it)
	}
}

func TestUnreadableQuestionGoesToTheDialog(t *testing.T) {
	store := New()
	store.AnswerQuestions(true)
	r := askHook("PermissionRequest", map[string]any{"tool_name": "AskUserQuestion", "tool_input": map[string]any{"questions": []any{}}})
	if it := store.Apply(r); it != nil {
		t.Errorf("item = %+v", it)
	}
	if d, decided := r.Decided(); !decided || d.Behavior != "" {
		t.Errorf("an unreadable call must go straight to the dialog: %+v %v", d, decided)
	}
}

func TestNewQuestionSupersedesAReleasedOne(t *testing.T) {
	store := New()
	store.AnswerQuestions(true)
	old, r := ask(store)
	bridge.TestHangUp(r)
	store.LetGo()
	it, _ := ask(store)
	if it == old || !it.AsksInDen() || old.State != Resolved || store.Waiting() != 1 {
		t.Errorf("old %+v, new %+v, waiting %d", old, it, store.Waiting())
	}
}
