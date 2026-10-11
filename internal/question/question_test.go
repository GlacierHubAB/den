package question

import (
	"encoding/json"
	"reflect"
	"testing"
)

// twoQuestions is the call the mock used against the real binary: one
// single-select question and one multi-select with awkward labels.
const twoQuestions = `{"questions":[
 {"question":"Which colour scheme should the demo use?","header":"Colours","options":[{"label":"Warm","description":"Oranges and reds"},{"label":"Cool, calm","description":"Blues"}],"multiSelect":false},
 {"question":"Which extras should ship?","header":"Extras","options":[{"label":"Icons","description":"An icon set"},{"label":"Sounds","description":"Short sounds"},{"label":"Say \"hi\"","description":"A greeting"}],"multiSelect":true}
],"metadata":{"source":"test"}}`

func TestParse(t *testing.T) {
	qs, ok := Parse(json.RawMessage(twoQuestions))
	if !ok || len(qs) != 2 {
		t.Fatalf("Parse = %+v, %v", qs, ok)
	}
	if qs[0].Header != "Colours" || qs[0].Multi || qs[0].Options[1] != (Option{"Cool, calm", "Blues"}) {
		t.Errorf("first = %+v", qs[0])
	}
	if !qs[1].Multi || len(qs[1].Options) != 3 {
		t.Errorf("second = %+v", qs[1])
	}
	for _, bad := range []string{``, `{}`, `{"questions":[]}`, `{"questions":[{"question":""}]}`, `{"questions":[{"question":"A?"},{"question":"A?"}]}`} {
		if _, ok := Parse(json.RawMessage(bad)); ok {
			t.Errorf("Parse(%s) should fail", bad)
		}
	}
}

func TestPick(t *testing.T) {
	qs, _ := Parse(json.RawMessage(twoQuestions))
	single, multi := qs[0], qs[1]
	cases := []struct {
		q     Question
		typed string
		want  string
		ok    bool
	}{
		{single, "2", "Cool, calm", true},
		{single, " 1 ", "Warm", true},
		{single, "1 2", "", false}, // one choice only
		{single, "3", "", false},   // no such option
		{single, "0", "", false},
		{single, "", "", false},
		{single, "Something else, typed", "Something else, typed", true},
		{single, `"2"`, "2", true}, // a number meant as text
		{single, `""`, "", false},
		{multi, "1 3", `Icons, "Say \"hi\""`, true},
		{multi, "3,1", `"Say \"hi\"", Icons`, true},
		{multi, "1, 1", "Icons", true},
		{multi, "2", "Sounds", true},
		{multi, "1 4", "", false},
		{multi, "1 and 3", "1 and 3", true}, // words are free text
	}
	for _, c := range cases {
		got, ok := Pick(c.q, c.typed)
		if got != c.want || ok != c.ok {
			t.Errorf("Pick(%q, %q) = %q, %v; want %q, %v", c.q.Text, c.typed, got, ok, c.want, c.ok)
		}
	}
}

func TestJoinMatchesTheDialog(t *testing.T) {
	// What the real 2.1.289 dialog wrote for Icons + Say "hi" (mock run).
	if got := Join([]string{"Icons", `Say "hi"`}); got != `Icons, "Say \"hi\""` {
		t.Errorf("Join = %s", got)
	}
	if got := Join([]string{"Cool, calm", "Warm"}); got != `"Cool, calm", Warm` {
		t.Errorf("Join = %s", got)
	}
	if got := Join([]string{"a,b"}); got != "a,b" {
		t.Errorf("a comma without a space is not quoted: %s", got)
	}
}

func TestOutput(t *testing.T) {
	in := json.RawMessage(twoQuestions)
	answers := map[string]string{
		"Which colour scheme should the demo use?": "Something else, typed",
		"Which extras should ship?":                `Icons, "Say \"hi\""`,
	}
	b, ok := Output(in, answers)
	if !ok {
		t.Fatal("Output refused a complete answer")
	}
	var out struct {
		HookSpecificOutput struct {
			HookEventName string `json:"hookEventName"`
			Decision      struct {
				Behavior     string                     `json:"behavior"`
				UpdatedInput map[string]json.RawMessage `json:"updatedInput"`
			} `json:"decision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	h := out.HookSpecificOutput
	if h.HookEventName != "PermissionRequest" || h.Decision.Behavior != "allow" {
		t.Errorf("output = %s", b)
	}
	// Every field the call had goes back unchanged: Claude Code refuses an
	// answer that changes what the dialog showed.
	var orig map[string]json.RawMessage
	json.Unmarshal(in, &orig)
	for k, v := range orig {
		var a, b any
		json.Unmarshal(v, &a)
		json.Unmarshal(h.Decision.UpdatedInput[k], &b)
		if !reflect.DeepEqual(a, b) {
			t.Errorf("field %s changed: %s", k, h.Decision.UpdatedInput[k])
		}
	}
	var got map[string]string
	json.Unmarshal(h.Decision.UpdatedInput["answers"], &got)
	if !reflect.DeepEqual(got, answers) {
		t.Errorf("answers = %v", got)
	}
}

func TestOutputRefusesIncompleteAnswers(t *testing.T) {
	in := json.RawMessage(twoQuestions)
	for name, answers := range map[string]map[string]string{
		"none":     nil,
		"one of 2": {"Which colour scheme should the demo use?": "Warm"},
		"blank":    {"Which colour scheme should the demo use?": "Warm", "Which extras should ship?": " "},
		"unknown":  {"Which colour scheme should the demo use?": "Warm", "Another question?": "Icons"},
	} {
		if b, ok := Output(in, answers); ok || b != nil {
			t.Errorf("%s: Output = %s, %v", name, b, ok)
		}
	}
	answered := json.RawMessage(`{"questions":[{"question":"A?","options":[]}],"answers":{"A?":"x"}}`)
	if _, ok := Output(answered, map[string]string{"A?": "y"}); ok {
		t.Error("an input that already holds answers must not be answered again")
	}
}
