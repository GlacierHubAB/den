package remote

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/question"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// What a held request's body may carry. An exact field is what the human
// approves (a command, a path, a URL, a question): it must arrive whole and
// unchanged by sanitising, or the request is not relayed and the terminal
// asks instead. A content field (a diff, a file, a plan) is shown cut, as
// the local inbox shows it, so it is capped and sanitised.
const (
	exactMax     = 16 << 10
	contentMax   = 32 << 10
	contentLines = 400
	argsMax      = 16 << 10 // all arguments of a tool Den has no layout for
	maxQuestions = 8
	maxOptions   = 16
)

type fieldKind int

const (
	exact fieldKind = iota
	content
)

type field struct {
	key  string
	kind fieldKind
}

// heldFields are, per tool, the keys of its input the local inbox shows
// (inbox.describeTool and planExcerpt); nothing else of the input leaves
// the host.
var heldFields = map[string][]field{
	"Bash":         {{"command", exact}, {"description", content}},
	"Edit":         {{"file_path", exact}, {"old_string", content}, {"new_string", content}},
	"MultiEdit":    {{"file_path", exact}, {"old_string", content}, {"new_string", content}},
	"Write":        {{"file_path", exact}, {"content", content}},
	"NotebookEdit": {{"notebook_path", exact}, {"new_source", content}},
	"Read":         {{"file_path", exact}},
	"WebFetch":     {{"url", exact}, {"prompt", content}},
	"WebSearch":    {{"query", exact}},
	"Agent":        {{"description", content}, {"prompt", content}},
	"Task":         {{"description", content}, {"prompt", content}},
	"SendMessage":  {{"to", exact}, {"message", content}},
	"ExitPlanMode": {{"plan", content}},
}

// heldBody is the body serve relays for a held request: the tool input
// filtered to what Den shows, capped and sanitised. For AskUserQuestion,
// keys maps each question's text as shown to its text as asked, so the
// answers can go back keyed as Claude Code expects. ok is false when the
// request cannot be shown faithfully, and is then not relayed.
func heldBody(in bridge.HookInput) (body json.RawMessage, keys map[string]string, ok bool) {
	var input map[string]any
	if json.Unmarshal(in.ToolInput, &input) != nil {
		input = map[string]any{}
	}
	var out map[string]any
	switch fields, known := heldFields[in.ToolName]; {
	case in.ToolName == question.Tool:
		out, keys, ok = questionBody(in.ToolInput)
	case known:
		out, ok = map[string]any{}, true
		for _, f := range fields {
			v, isStr := input[f.key].(string)
			if !isStr {
				continue
			}
			if f.kind == exact {
				if len(v) > exactMax || sanitize.Text(v) != v {
					return nil, nil, false
				}
				out[f.key] = v
			} else {
				out[f.key] = capContent(v)
			}
		}
	default:
		out, ok = cleanArgs(input, 0).(map[string]any)
	}
	if !ok {
		return nil, nil, false
	}
	raw, err := json.Marshal(out)
	if err != nil || (heldFields[in.ToolName] == nil && in.ToolName != question.Tool && len(raw) > argsMax) {
		return nil, nil, false
	}
	return raw, keys, true
}

// capContent sanitises a shown-but-not-approved text and cuts it to
// contentLines lines and contentMax bytes, saying so.
func capContent(s string) string {
	s = sanitize.Text(s)
	lines := strings.Split(s, "\n")
	cut := false
	if len(lines) > contentLines {
		lines, cut = lines[:contentLines], true
	}
	s = strings.Join(lines, "\n")
	if len(s) > contentMax {
		s = strings.ToValidUTF8(s[:contentMax], "")
		cut = true
	}
	if cut {
		s += "\n… cut on the host"
	}
	return s
}

// cleanArgs sanitises a tool's arguments for a tool Den has no layout for
// (an MCP tool, say), which Den shows as JSON. Strings must survive
// sanitising unchanged and nesting is bounded; anything else fails it.
func cleanArgs(v any, depth int) any {
	if depth > 6 {
		return nil
	}
	switch v := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, x := range v {
			c := cleanArgs(x, depth+1)
			if sanitize.Line(k) != k || (c == nil && x != nil) {
				return nil
			}
			out[k] = c
		}
		return out
	case []any:
		if len(v) > 100 {
			return nil
		}
		out := make([]any, len(v))
		for i, x := range v {
			c := cleanArgs(x, depth+1)
			if c == nil && x != nil {
				return nil
			}
			out[i] = c
		}
		return out
	case string:
		if sanitize.Text(v) != v {
			return nil
		}
		return v
	default:
		return v // numbers, booleans, null
	}
}

// questionBody is an AskUserQuestion call as Den shows it: each question's
// text, header, multiSelect and options. The texts and labels are
// sanitised; keys maps a shown question text back to the asked one.
func questionBody(input json.RawMessage) (map[string]any, map[string]string, bool) {
	qs, ok := question.Parse(input)
	if !ok || len(qs) > maxQuestions {
		return nil, nil, false
	}
	keys := map[string]string{}
	var out []map[string]any
	for _, q := range qs {
		text := sanitize.Text(q.Text)
		if text == "" || len(text) > exactMax || keys[text] != "" || len(q.Options) > maxOptions {
			return nil, nil, false
		}
		keys[text] = q.Text
		var opts []map[string]any
		for _, o := range q.Options {
			label := sanitize.Line(o.Label)
			if label != o.Label || len(label) > exactMax {
				return nil, nil, false // an answer is the label itself
			}
			opt := map[string]any{"label": label}
			if o.Description != "" {
				opt["description"] = capContent(o.Description)
			}
			opts = append(opts, opt)
		}
		qm := map[string]any{"question": text, "options": opts}
		if q.Header != "" {
			qm["header"] = sanitize.Label(q.Header, 200)
		}
		if q.Multi {
			qm["multiSelect"] = true
		}
		out = append(out, qm)
	}
	return map[string]any{"questions": out}, keys, true
}

// answersBack turns answers keyed by the questions as shown back into
// answers keyed as asked. ok is false when one names no question.
func answersBack(answers map[string]string, keys map[string]string) (map[string]string, bool) {
	out := make(map[string]string, len(answers))
	for shown, a := range answers {
		asked, ok := keys[shown]
		if !ok || len(a) > exactMax {
			return nil, false
		}
		out[asked] = sanitize.Text(a)
	}
	return out, true
}

// heldSummary names a request in serve's log: metadata only.
func heldSummary(id string, in bridge.HookInput) string {
	sid := in.SessionID
	if len(sid) > 8 {
		sid = sid[:8]
	}
	short := id
	if len(short) > 8 {
		short = short[:8]
	}
	return fmt.Sprintf("%s %s %s", short, sanitize.Line(sid), sanitize.Label(in.ToolName, 60))
}
