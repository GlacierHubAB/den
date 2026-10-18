package denconformance

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// The wire checks the console subtests use are strict: each case below is one thing a server must not send
// (or, for the good ones, one it may).

func entryJSON(fields string) json.RawMessage {
	return json.RawMessage(`{"id":7,"at":"2026-10-17T20:00:00Z",` + fields + `}`)
}

func TestConversationEntryChecks(t *testing.T) {
	good := map[string]string{
		"you":              `"origin":"you","speaker":"you","text":"hi"`,
		"reply":            `"origin":"you","speaker":"orchestrator","text":"hello"`,
		"console":          `"origin":"console","speaker":"you","turn":"t-0123456789abcdef0123456789abcdef","console":{"label":"phone","self":false},"text":"hi"`,
		"report":           `"origin":"report","speaker":"den","agent":"Mossy Lemur","text":"Report from Mossy Lemur."`,
		"mission":          `"origin":"mission","speaker":"den","text":"Mission run."`,
		"desktop":          `"origin":"desktop","speaker":"orchestrator","agent":"Otter","text":"done"`,
		"twenty lines":     `"origin":"you","speaker":"you","text":"` + strings.Repeat("l\\n", 19) + `l"`,
		"cut":              `"origin":"you","speaker":"you","text":"the start\\n(cut: the rest is in Den)"`,
		"2000 runes":       `"origin":"you","speaker":"you","text":"` + strings.Repeat("é", 2000) + `"`,
		"an agent as name": `"origin":"report","speaker":"den","agent":"an agent","text":"Report from an agent."`,
	}
	for name, f := range good {
		if _, err := checkConversationEntry(entryJSON(f)); err != nil {
			t.Errorf("%s refused: %v", name, err)
		}
	}
	bad := map[string]string{
		"an unknown field":             `"origin":"you","speaker":"you","text":"hi","extra":1`,
		"den on origin you":            `"origin":"you","speaker":"den","text":"hi"`,
		"den on origin console":        `"origin":"console","speaker":"den","console":{"label":"p","self":true},"text":"hi"`,
		"you on origin report":         `"origin":"report","speaker":"you","agent":"Otter","text":"hi"`,
		"you on origin mission":        `"origin":"mission","speaker":"you","text":"hi"`,
		"an unknown origin":            `"origin":"robot","speaker":"you","text":"hi"`,
		"an unknown speaker":           `"origin":"you","speaker":"agent","text":"hi"`,
		"report without agent":         `"origin":"report","speaker":"den","text":"hi"`,
		"desktop without agent":        `"origin":"desktop","speaker":"den","text":"hi"`,
		"agent on origin you":          `"origin":"you","speaker":"you","agent":"Otter","text":"hi"`,
		"agent Den":                    `"origin":"report","speaker":"den","agent":"Den","text":"hi"`,
		"agent drey":                   `"origin":"report","speaker":"den","agent":"drey","text":"hi"`,
		"agent YOU":                    `"origin":"report","speaker":"den","agent":"YOU","text":"hi"`,
		"agent Orchestrator":           `"origin":"desktop","speaker":"den","agent":"Orchestrator","text":"hi"`,
		"agent of 65 runes":            `"origin":"report","speaker":"den","agent":"` + strings.Repeat("a", 65) + `","text":"hi"`,
		"agent with a newline":         `"origin":"report","speaker":"den","agent":"a\nb","text":"hi"`,
		"agent with a bidi override":   `"origin":"report","speaker":"den","agent":"a\u202eb","text":"hi"`,
		"agent of only spaces":         `"origin":"report","speaker":"den","agent":"   ","text":"hi"`,
		"console label of only spaces": `"origin":"console","speaker":"you","console":{"label":"  ","self":false},"text":"hi"`,
		"console without console":      `"origin":"console","speaker":"you","text":"hi"`,
		"console with unknown field":   `"origin":"console","speaker":"you","console":{"label":"p","self":true,"x":1},"text":"hi"`,
		"console on origin you":        `"origin":"you","speaker":"you","console":{"label":"p","self":true},"text":"hi"`,
		"turn on origin you":           `"origin":"you","speaker":"you","turn":"t-0123456789abcdef0123456789abcdef","text":"hi"`,
		"a bad turn":                   `"origin":"console","speaker":"you","turn":"x","console":{"label":"p","self":true},"text":"hi"`,
		"21 lines":                     `"origin":"you","speaker":"you","text":"` + strings.Repeat("l\\n", 20) + `l"`,
		"20 newlines":                  `"origin":"you","speaker":"you","text":"` + strings.Repeat("l\\n", 20) + `"`,
		"2001 runes":                   `"origin":"you","speaker":"you","text":"` + strings.Repeat("é", 2001) + `"`,
		"empty text":                   `"origin":"you","speaker":"you","text":""`,
		"an escape":                    `"origin":"you","speaker":"you","text":"a\u001b[31mb"`,
		"a bidi override":              `"origin":"you","speaker":"you","text":"a\u202eb"`,
		"a zero-width space":           `"origin":"you","speaker":"you","text":"a\u200bb"`,
		"a line separator":             `"origin":"you","speaker":"you","text":"a\u2028b"`,
		"a paragraph separator":        `"origin":"you","speaker":"you","text":"a\u2029b"`,
		"an Arabic letter mark":        `"origin":"you","speaker":"you","text":"a\u061cb"`,
		"an interlinear annotation":    `"origin":"you","speaker":"you","text":"a\ufff9b"`,
		"a tag character":              `"origin":"you","speaker":"you","text":"a\U000e0041b"`,
		"a C1 control":                 `"origin":"you","speaker":"you","text":"a\u0085b"`,
		"time not UTC":                 `"origin":"you","speaker":"you","text":"hi","at":"x"`,
	}
	for name, f := range bad {
		raw := entryJSON(f)
		if name == "time not UTC" {
			raw = json.RawMessage(`{"id":7,"origin":"you","speaker":"you","text":"hi","at":"2026-10-17T22:00:00+02:00"}`)
		}
		if _, err := checkConversationEntry(raw); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := checkConversationEntry(json.RawMessage(`{"id":0,"at":"2026-10-17T20:00:00Z","origin":"you","speaker":"you","text":"hi"}`)); err == nil {
		t.Error("id 0 accepted")
	}
}

func TestDoingChecks(t *testing.T) {
	at := `,"at":"2026-10-17T20:00:00Z"}`
	for _, d := range []string{
		`{"text":"","at":"0001-01-01T00:00:00Z"}`,
		`{"text":"Idle"` + at, `{"text":"Stuck, needs a look"` + at,
		`{"text":"Waiting for you: permission"` + at, `{"text":"Waiting for you: question"` + at, `{"text":"Waiting for you: plan"` + at,
		`{"text":"Working\u2026"` + at, `{"text":"Running the tests\u2026"` + at,
		`{"text":"Editing week.go\u2026"` + at, `{"text":"Reading a file\u2026"` + at, `{"text":"Writing files\u2026"` + at,
		`{"text":"Working on fix login bug\u2026"` + at, `{"text":"Working on\u2026"` + at,
	} {
		if _, err := checkDoing(json.RawMessage(d)); err != nil {
			t.Errorf("%s refused: %v", d, err)
		}
	}
	for _, l := range doingLeadWords {
		if _, err := checkDoing(json.RawMessage(`{"text":"` + l + `"` + at)); err != nil {
			t.Errorf("the lead %q alone is refused: %v", l, err)
		}
	}
	if len(doingLeadWords) != 25 {
		t.Errorf("%d lead phrases, Den's set has 25", len(doingLeadWords))
	}
	for name, d := range map[string]string{
		"an unknown field":  `{"text":"Idle","at":"2026-10-17T20:00:00Z","x":1}`,
		"an unknown lead":   `{"text":"Dancing"` + at,
		"the old lowercase": `{"text":"editing week.go"` + at,
		"a lead run on":     `{"text":"Editingweek.go"` + at,
		"81 runes":          `{"text":"Idle ` + strings.Repeat("a", 76) + `"` + at,
		"a newline":         `{"text":"Editing a\nb"` + at,
		"an escape":         `{"text":"Editing a\u001bb"` + at,
		"a bidi override":   `{"text":"Editing a\u202eb"` + at,
		"a tag character":   `{"text":"Editing a\U000e0041"` + at,
		"no time":           `{"text":"Idle"}`,
	} {
		if _, err := checkDoing(json.RawMessage(d)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestMirrorEventChecks(t *testing.T) {
	for _, d := range []string{`{"state":"off"}`, `{"state":"asking"}`, `{"state":"on","since":"2026-10-17T20:00:00Z"}`} {
		if err := checkMirrorEvent(json.RawMessage(d)); err != nil {
			t.Errorf("%s refused: %v", d, err)
		}
	}
	for name, d := range map[string]string{
		"an unknown field": `{"state":"off","x":1}`,
		"an unknown state": `{"state":"maybe"}`,
		"on without since": `{"state":"on"}`,
		"since with off":   `{"state":"off","since":"2026-10-17T20:00:00Z"}`,
		"since not UTC":    `{"state":"on","since":"2026-10-17T22:00:00+02:00"}`,
	} {
		if err := checkMirrorEvent(json.RawMessage(d)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestEveryBadRuneIsRefused(t *testing.T) {
	for _, r := range []rune{0, 0x1b, 0x7f, 0x85, 0x9f, 0x061c, 0x200b, 0x200f, 0x2028, 0x2029, 0x202a, 0x202e, 0x2060, 0x2069, 0xfeff, 0xfff9, 0xfffb, 0xe0000, 0xe007f} {
		if !badRune(r, true) {
			t.Errorf("U+%04X is allowed in a text", r)
		}
	}
	if badRune('\n', true) || !badRune('\n', false) {
		t.Error("a newline is for a text only")
	}
	for _, r := range []rune{'a', 'é', '世', ' ', ' ', '\U0001f600'} {
		if badRune(r, false) {
			t.Errorf("%q is refused", fmt.Sprint(string(r)))
		}
	}
}
