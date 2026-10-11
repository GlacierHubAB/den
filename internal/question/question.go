// Package question reads an AskUserQuestion tool call and builds the
// answer a PermissionRequest hook gives back, so a question an agent asks
// can be answered from Den's inbox (hooks.answer_questions).
//
// Answering through a hook is undocumented Claude Code behaviour. What is
// written here was checked against Claude Code 2.1.289 in two ways, and the
// comments say which:
//   - the bundle: strings of the installed claude binary (the hook output
//     schema, the AskUserQuestion input and output schemas, and the
//     function that joins multi-select answers);
//   - the real binary in a scratch config directory, driven by a
//     throwaway localhost mock of the Messages API (no login, test only).
package question

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Tool is the tool name Claude Code gives the question dialog.
const Tool = "AskUserQuestion"

// Question is one question of a call. Text is also its key in the answers.
type Question struct {
	Text    string
	Header  string
	Options []Option
	Multi   bool // multiSelect: the human may choose several options
}

// Option is one choice.
type Option struct {
	Label       string
	Description string
}

// Parse reads a call's tool_input. ok is false when there is no question,
// a question has no text, or two share a text (answers are keyed by it).
func Parse(input json.RawMessage) (qs []Question, ok bool) {
	var in struct {
		Questions []struct {
			Question    string `json:"question"`
			Header      string `json:"header"`
			MultiSelect bool   `json:"multiSelect"`
			Options     []struct {
				Label       string `json:"label"`
				Description string `json:"description"`
			} `json:"options"`
		} `json:"questions"`
	}
	if json.Unmarshal(input, &in) != nil || len(in.Questions) == 0 {
		return nil, false
	}
	seen := map[string]bool{}
	for _, q := range in.Questions {
		if q.Question == "" || seen[q.Question] {
			return nil, false
		}
		seen[q.Question] = true
		out := Question{Text: q.Question, Header: q.Header, Multi: q.MultiSelect}
		for _, o := range q.Options {
			out.Options = append(out.Options, Option{Label: o.Label, Description: o.Description})
		}
		qs = append(qs, out)
	}
	return qs, true
}

// Pick turns what the human typed into the answer for q. A number picks
// that option; for a multi-select question several numbers, separated by
// spaces or commas, pick several. Anything else is the human's own answer,
// as the dialog's "Type something" row takes it; a number meant as text
// goes in quotes ("3"). ok is false for an empty answer or a number with no
// option.
func Pick(q Question, typed string) (answer string, ok bool) {
	typed = strings.TrimSpace(typed)
	if typed == "" {
		return "", false
	}
	if len(typed) >= 2 && typed[0] == '"' && typed[len(typed)-1] == '"' {
		lit := strings.TrimSpace(typed[1 : len(typed)-1])
		return lit, lit != ""
	}
	fields := strings.FieldsFunc(typed, func(r rune) bool { return r == ' ' || r == ',' })
	var nums []int
	for _, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil {
			return typed, true // words: the human's own answer
		}
		nums = append(nums, n)
	}
	if len(nums) > 1 && !q.Multi {
		return "", false
	}
	var labels []string
	chosen := map[int]bool{}
	for _, n := range nums {
		if n < 1 || n > len(q.Options) {
			return "", false
		}
		if !chosen[n] {
			chosen[n] = true
			labels = append(labels, q.Options[n-1].Label)
		}
	}
	if !q.Multi {
		return labels[0], true // the label as it is, as the dialog sends it
	}
	return Join(labels), true
}

// Join encodes several chosen labels as one answer string the way Claude
// Code's own dialog does: joined with ", ", and a label that itself holds
// ", " or a double quote written as a JSON string.
//
// Bundle: the AskUserQuestion module joins multi-select answers with
// e.map(t => t.includes(", ") || t.includes('"') ? JSON-quote(t) : t).join(", "),
// and its output schema says "multi-select answers are comma-separated".
// Real binary (mock): choosing Icons and Say "hi" in the terminal gave
// `Icons, "Say \"hi\""`. A hook answer must already be joined: an array sent
// through a PermissionRequest hook reached the model as `Icons,Say "hi"`.
func Join(labels []string) string {
	parts := make([]string, len(labels))
	for i, l := range labels {
		if strings.Contains(l, ", ") || strings.Contains(l, `"`) {
			b, _ := json.Marshal(l)
			l = string(b)
		}
		parts[i] = l
	}
	return strings.Join(parts, ", ")
}

// Output is what `den hook` prints on a PermissionRequest for the question
// call input once every question has an answer: allow, with the call's own
// input plus the answers as updatedInput. Claude Code then skips its dialog
// (or closes it, when it is on screen) and hands the model the answers.
// ok is false, and nothing must be printed, when an answer is missing or
// unknown, or the input cannot carry answers; the dialog then stays.
//
// Bundle: PermissionRequest hookSpecificOutput is {hookEventName,
// decision: {behavior, updatedInput, ...}}; Claude Code refuses an answer
// whose input changes a field the dialog showed, or that already held
// "answers", so the input goes back unchanged. Real binary (mock):
// accepted in default, acceptEdits, plan, auto and bypassPermissions
// modes; the terminal showed "Allowed by PermissionRequest hook" and the
// model got "The user answered: ...". A bare allow without answers is
// ignored and the dialog stays.
func Output(input json.RawMessage, answers map[string]string) ([]byte, bool) {
	qs, ok := Parse(input)
	if !ok || len(answers) != len(qs) {
		return nil, false
	}
	for _, q := range qs {
		if a, ok := answers[q.Text]; !ok || strings.TrimSpace(a) == "" {
			return nil, false
		}
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(input, &fields) != nil {
		return nil, false
	}
	if _, has := fields["answers"]; has {
		return nil, false
	}
	a, err := json.Marshal(answers)
	if err != nil {
		return nil, false
	}
	fields["answers"] = a
	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName": "PermissionRequest",
			"decision":      map[string]any{"behavior": "allow", "updatedInput": fields},
		},
	}
	b, err := json.Marshal(out)
	return b, err == nil
}
