package denconformance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
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
		"you":                   `"origin":"you","speaker":"you","text":"hi"`,
		"reply":                 `"origin":"you","speaker":"orchestrator","text":"hello"`,
		"console":               `"origin":"console","speaker":"you","turn":"t-0123456789abcdef0123456789abcdef","console":{"label":"phone","self":false},"text":"hi"`,
		"report":                `"origin":"report","speaker":"den","agent":"Mossy Lemur","text":"Report from Mossy Lemur."`,
		"mission":               `"origin":"mission","speaker":"den","text":"Mission run."`,
		"desktop":               `"origin":"desktop","speaker":"orchestrator","agent":"Otter","text":"done"`,
		"five thousand lines":   `"origin":"you","speaker":"you","text":"` + strings.Repeat("l\\n", 5000) + `l"`,
		"cut":                   `"origin":"you","speaker":"you","text":"the start\n(cut: the rest is in Den)"`,
		"the words mid-text":    `"origin":"you","speaker":"you","text":"a (cut: the rest is in Den) b"`, // an agent can write them: not Den's cut
		"65536 bytes":           `"origin":"you","speaker":"you","text":"` + strings.Repeat("x", 65536) + `"`,
		"a cut at 64 KiB":       `"origin":"you","speaker":"you","text":"` + strings.Repeat("x", 65536) + `\n(cut: the rest is in Den)"`,
		"32768 two-byte":        `"origin":"you","speaker":"you","text":"` + strings.Repeat("é", 32768) + `"`,
		"an agent as name":      `"origin":"report","speaker":"den","agent":"an agent","text":"Report from an agent."`,
		"a report with its id":  `"origin":"report","speaker":"den","agent":"backend-engineer2","agent_id":"0b5e1c1e-7c1e-4a41-9d3a-5f3c1b2a9e10","text":"Report."`,
		"a desktop with its id": `"origin":"desktop","speaker":"orchestrator","agent":"Otter","agent_id":"a1:b2.c3_d4-5","text":"done"`,
		"an id of 64":           `"origin":"report","speaker":"den","agent":"Otter","agent_id":"` + strings.Repeat("a", 64) + `","text":"hi"`,
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
		"agent_id without agent":       `"origin":"report","speaker":"den","agent_id":"abc","text":"hi"`,
		"agent_id on origin you":       `"origin":"you","speaker":"you","agent_id":"abc","text":"hi"`,
		"agent_id on origin mission":   `"origin":"mission","speaker":"den","agent_id":"abc","text":"hi"`,
		"an empty agent_id":            `"origin":"report","speaker":"den","agent":"Otter","agent_id":"","text":"hi"`,
		"an agent_id of 65":            `"origin":"report","speaker":"den","agent":"Otter","agent_id":"` + strings.Repeat("a", 65) + `","text":"hi"`,
		"an agent_id with a space":     `"origin":"report","speaker":"den","agent":"Otter","agent_id":"a b","text":"hi"`,
		"an agent_id with a slash":     `"origin":"report","speaker":"den","agent":"Otter","agent_id":"a/b","text":"hi"`,
		"an agent_id with a newline":   `"origin":"report","speaker":"den","agent":"Otter","agent_id":"a\nb","text":"hi"`,
		"an agent_id that is a number": `"origin":"report","speaker":"den","agent":"Otter","agent_id":7,"text":"hi"`,
		"an agent_id with a NUL":       `"origin":"report","speaker":"den","agent":"Otter","agent_id":"nul\u0000l","text":"hi"`,
		"console label of only spaces": `"origin":"console","speaker":"you","console":{"label":"  ","self":false},"text":"hi"`,
		"console without console":      `"origin":"console","speaker":"you","text":"hi"`,
		"console with unknown field":   `"origin":"console","speaker":"you","console":{"label":"p","self":true,"x":1},"text":"hi"`,
		"console on origin you":        `"origin":"you","speaker":"you","console":{"label":"p","self":true},"text":"hi"`,
		"turn on origin you":           `"origin":"you","speaker":"you","turn":"t-0123456789abcdef0123456789abcdef","text":"hi"`,
		"a bad turn":                   `"origin":"console","speaker":"you","turn":"x","console":{"label":"p","self":true},"text":"hi"`,
		"65537 bytes":                  `"origin":"you","speaker":"you","text":"` + strings.Repeat("x", 65537) + `"`,
		"32769 two-byte runes":         `"origin":"you","speaker":"you","text":"` + strings.Repeat("é", 32769) + `"`,
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

// What the connection holds every event line to: the conversation lines' bounds, ids that strictly increase
// (a gap is legal), a closed conversation_gap, and the joined text chunk bound.
func TestEventLineChecks(t *testing.T) {
	ev := func(kind, data string, size int) *line {
		return &line{Event: kind, Data: json.RawMessage(data), size: size}
	}
	c := &conn{}
	good := []*line{
		ev("conversation", `{"id":3}`, 100),
		ev("conversation", `{"id":9}`, maxConversationEventLine), // a gap in the ids is legal; the bound is inclusive
		ev("conversation_gap", `{"from":4}`, 30),
		ev("orch", `{"delta":"`+strings.Repeat("x", maxStreamChunk)+`"}`, 70000),
		ev("chat", `{"delta":"hi"}`, 30),
		ev("fleet", `{}`, 5),
	}
	for i, l := range good {
		if err := c.checkEventLine(l); err != nil {
			t.Errorf("good line %d (%s): %v", i, l.Event, err)
		}
	}
	for name, l := range map[string]*line{
		"an event line over its bound": ev("conversation", `{"id":20}`, maxConversationEventLine+1),
		"an id that does not rise":     ev("conversation", `{"id":9}`, 10),
		"an id that falls":             ev("conversation", `{"id":2}`, 10),
		"a gap without from":           ev("conversation_gap", `{}`, 10),
		"a gap from 0":                 ev("conversation_gap", `{"from":0}`, 10),
		"a gap from a string":          ev("conversation_gap", `{"from":"x"}`, 10),
		"a gap with another field":     ev("conversation_gap", `{"from":3,"x":1}`, 10),
		"a joined chunk over 64 KiB":   ev("orch", `{"delta":"`+strings.Repeat("x", maxStreamChunk+1)+`"}`, 70000),
		"a chat chunk over 64 KiB":     ev("chat", `{"delta":"`+strings.Repeat("x", maxStreamChunk+1)+`"}`, 70000),
	} {
		if err := c.checkEventLine(l); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if maxConversationEventLine != 400*1024 || maxHistoryReplyLine != 800*1024 {
		t.Error("the line bounds are not Den's stated 400 KiB and 800 KiB")
	}
}

// Den's fixed text_sha256 vectors (recomputed independently): the mission is the seventh field, last, for the three mission kinds only.
func TestCardTextHashVectors(t *testing.T) {
	const body = "Stop the mission and its run. No agent is touched."
	// the vectors are for kind "mission stop" with m1 and m12 and, six fields only, the same text under a kind that is not a mission one
	if got := CardTextSHA256("mission stop", "", "", "Stop the mission?", "", body, "m1", nil); got != "ebcd19504c06e36065a619f828fcebd544f9eb7ac5fdf1f1757e01121ef50e7b" {
		t.Errorf("m1: %s", got)
	}
	if got := CardTextSHA256("mission stop", "", "", "Stop the mission?", "", body, "m12", nil); got != "cddac4ea934efdc421d8ed683df7f3a7347213d71e6530be247522989b0b77bf" {
		t.Errorf("m12: %s", got)
	}
	const six = "dc79822212cf160275753d859032fcccdd07f91b226577e6cf2d3a06df9c5ded"
	// the six-field form: the same kind and text, hashed without a mission (what every other kind does)
	sum := sha256.Sum256([]byte("mission stop\n\n\nStop the mission?\n\n" + body))
	if got := hex.EncodeToString(sum[:]); got != six {
		t.Errorf("six fields only: %s", got)
	}
	for _, kind := range []string{"permission", "plan", "question", "mission pause", "mission", "Mission stop", ""} {
		if CardTextSHA256(kind, "", "", "Stop the mission?", "", body, "m1", nil) != CardTextSHA256(kind, "", "", "Stop the mission?", "", body, "", nil) {
			t.Errorf("kind %q: the mission changed the hash of a card that is not a mission card", kind)
		}
	}
	for _, kind := range []string{"mission stop", "mission restart", "mission done"} {
		a, b := CardTextSHA256(kind, "den", "", "t", "", body, "m1", nil), CardTextSHA256(kind, "den", "", "t", "", body, "m2", nil)
		if a == b {
			t.Errorf("%s: the same body under another mission hashes the same", kind)
		}
		if a == CardTextSHA256(kind, "den", "", "t", "", body, "", nil) {
			t.Errorf("%s: a missing mission hashes like a present one", kind)
		}
	}
}

// Den's fixed vectors for "mission done": the eighth field is the criteria, id US state US text US evidence, joined by RS.
func TestMissionDoneHashVectors(t *testing.T) {
	const body = "Confirm the mission is done."
	full := []Criterion{
		{ID: "c1", State: "met", Text: "Tests pass", Evidence: "PR #97 merged"},
		{ID: "c2", State: "claimed", Text: "Docs updated", Evidence: "README section added"},
		{ID: "c3", State: "open", Text: "Released"},
	}
	bare := []Criterion{{ID: "c1", State: "met"}, {ID: "c2", State: "claimed"}, {ID: "c3", State: "open"}}
	if got := CardTextSHA256("mission done", "", "", "Is it done?", "", body, "m1", full); got != "2274768428161c7f2c78b89f885bca7663a48fc20888ddd76a7b3711637128a5" {
		t.Errorf("with texts: %s", got)
	}
	if got := CardTextSHA256("mission done", "", "", "Is it done?", "", body, "m1", bare); got != "ecc54dae1673c025b0b8fcafa33d324b5af1b43da185c5d591ebdab9d9479357" {
		t.Errorf("ids and states only: %s", got)
	}
	base := CardTextSHA256("mission done", "", "", "Is it done?", "", body, "m1", full)
	for name, mut := range map[string]func(c []Criterion){
		"a text":     func(c []Criterion) { c[0].Text += "!" },
		"a state":    func(c []Criterion) { c[1].State = "met" },
		"evidence":   func(c []Criterion) { c[1].Evidence = "x" },
		"an id":      func(c []Criterion) { c[2].ID = "c9" },
		"the order":  func(c []Criterion) { c[0], c[1] = c[1], c[0] },
		"one fewer":  func(c []Criterion) { c[2] = Criterion{} },
		"a boundary": func(c []Criterion) { c[0].Text, c[0].Evidence = "Tests passPR #97 merged", "" },
	} {
		c := append([]Criterion(nil), full...)
		mut(c)
		if name == "one fewer" {
			c = c[:2]
		}
		if CardTextSHA256("mission done", "", "", "Is it done?", "", body, "m1", c) == base {
			t.Errorf("changing %s leaves the hash alone", name)
		}
	}
	// stop and restart stay seven fields: criteria are not part of them; every other kind stays six
	for _, kind := range []string{"mission stop", "mission restart", "permission"} {
		if CardTextSHA256(kind, "den", "", "t", "", body, "m1", full) != CardTextSHA256(kind, "den", "", "t", "", body, "m1", nil) {
			t.Errorf("%s: criteria changed the hash of a card that is not mission done", kind)
		}
	}
}

func TestCheckCriteria(t *testing.T) {
	ok := []Criterion{{ID: "c1", State: "met", Text: "a", Evidence: "b"}, {ID: "c2", State: "open"}}
	if err := checkCriteria(ok); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("x", 301)
	var nine []Criterion
	for i := 1; i <= 9; i++ {
		nine = append(nine, Criterion{ID: "c" + strconv.Itoa(i), State: "open"})
	}
	for name, bad := range map[string][]Criterion{
		"nine":             nine,
		"a bad id":         {{ID: "x1", State: "open"}},
		"a duplicate":      {{ID: "c1", State: "open"}, {ID: "c1", State: "open"}},
		"an unknown state": {{ID: "c1", State: "done"}},
		"open evidence":    {{ID: "c1", State: "open", Evidence: "e"}},
		"a long text":      {{ID: "c1", State: "met", Text: long}},
		"a separator":      {{ID: "c1", State: "met", Text: "a\x1fb"}},
		"a record sep":     {{ID: "c1", State: "met", Evidence: "a\x1eb"}},
		"a newline":        {{ID: "c1", State: "met", Text: "a\nb"}},
		"a bidi mark":      {{ID: "c1", State: "met", Text: "a\u202eb"}},
	} {
		if checkCriteria(bad) == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
