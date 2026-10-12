package remote

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/question"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// What a held request's body may carry. The rule: the human never approves
// something they cannot see. A decisive field (a command, a path, a URL, a
// question) must arrive whole and unchanged by sanitising, or the request
// is not relayed and the terminal asks instead. A content field (a diff, a
// file, a plan, a prompt) is shown cut, as the local inbox shows it, so it
// is capped and sanitised. A flag that changes what the call does
// (replace_all, dangerouslyDisableSandbox, a subagent type) travels as it
// is and the inbox lists it. A key Den does not know refuses the request,
// unless it is one of the few that change nothing worth asking about.
//
// heldBody applies this on the host; checkBody holds a body to the same
// line on Den's side, since a hostile host skips heldBody.
const (
	exactMax     = 16 << 10
	contentMax   = 32 << 10
	contentLines = 400
	flagMax      = 2 << 10
	maxEdits     = 50
	argsMax      = 16 << 10 // all arguments of a tool Den has no layout for
	argsLines    = 40       // as many as the inbox shows of them (inbox.bodyLimit)
	maxQuestions = 8
	maxOptions   = 16
	headerMax    = 200
)

// cutMark ends a content field cut on the host.
const cutMark = "\n… cut on the host"

// shape is what Den shows of one tool's input.
type shape struct {
	exact   []string // decisive, required, shown whole
	content []string // optional, shown cut
	flags   []string // optional, shown as they are
	edits   bool     // MultiEdit's edits: each a content diff and a replace_all flag
}

// shapes are the tools the inbox lays out (inbox.describeTool).
var shapes = map[string]shape{
	"Bash":         {exact: []string{"command"}, content: []string{"description"}, flags: []string{"dangerouslyDisableSandbox"}},
	"Edit":         {exact: []string{"file_path"}, content: []string{"old_string", "new_string"}, flags: []string{"replace_all"}},
	"MultiEdit":    {exact: []string{"file_path"}, edits: true},
	"Write":        {exact: []string{"file_path"}, content: []string{"content"}},
	"NotebookEdit": {exact: []string{"notebook_path"}, content: []string{"new_source"}, flags: []string{"cell_id", "cell_type", "edit_mode"}},
	"Read":         {exact: []string{"file_path"}},
	"WebFetch":     {exact: []string{"url"}, content: []string{"prompt"}},
	"WebSearch":    {exact: []string{"query"}, flags: []string{"allowed_domains", "blocked_domains"}},
	"Agent":        {content: []string{"description", "prompt"}, flags: []string{"subagent_type", "model", "isolation", "name", "team_name", "mode"}},
	"Task":         {content: []string{"description", "prompt"}, flags: []string{"subagent_type", "model", "isolation", "name", "team_name", "mode"}},
	"SendMessage":  {exact: []string{"to"}, content: []string{"message", "summary"}},
	"ExitPlanMode": {content: []string{"plan"}, flags: []string{"planFilePath", "allowedPrompts"}},
}

// quiet keys change nothing worth asking about; they are dropped, never
// shown or relayed.
var quiet = map[string]bool{"timeout": true, "run_in_background": true, "offset": true, "limit": true, "pages": true}

// heldBody is the body serve relays for a held request: the tool input as
// its shape allows, capped and sanitised. For AskUserQuestion, keys maps
// each question's text as shown to its text as asked, so the answers can
// go back keyed as Claude Code expects. ok is false when the request
// cannot be shown faithfully; it is then not relayed.
func heldBody(in bridge.HookInput) (body json.RawMessage, keys map[string]string, ok bool) {
	var out map[string]any
	if in.ToolName == question.Tool {
		out, keys, ok = questionBody(in.ToolInput)
	} else {
		var input map[string]any
		if json.Unmarshal(in.ToolInput, &input) != nil || input == nil {
			return nil, nil, false
		}
		if sh, known := shapes[in.ToolName]; known {
			out, ok = shapeBody(sh, input)
		} else {
			out, ok = argsBody(input)
		}
	}
	if !ok {
		return nil, nil, false
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, nil, false
	}
	return raw, keys, true
}

// shapeBody keeps what sh shows of input; ok is false when a decisive
// field is missing, too long or changed by sanitising, or when input has a
// key that is neither shown nor quiet.
func shapeBody(sh shape, input map[string]any) (map[string]any, bool) {
	out := map[string]any{}
	for _, k := range sh.exact {
		v, isStr := input[k].(string)
		if !isStr || !exactOK(v) {
			return nil, false
		}
		out[k] = v
	}
	for k, v := range input {
		switch {
		case slicesHas(sh.exact, k), quiet[k]:
		case slicesHas(sh.content, k):
			str, isStr := v.(string)
			if !isStr {
				return nil, false
			}
			out[k] = capContent(str)
		case slicesHas(sh.flags, k):
			if !flagOK(v) {
				return nil, false
			}
			out[k] = v
		case k == "edits" && sh.edits:
			edits, ok := editsBody(v)
			if !ok {
				return nil, false
			}
			out[k] = edits
		default:
			return nil, false // a key Den would not show
		}
	}
	return out, true
}

// editsBody is MultiEdit's edits, each diff cut as content.
func editsBody(v any) ([]any, bool) {
	list, ok := v.([]any)
	if !ok || len(list) == 0 || len(list) > maxEdits {
		return nil, false
	}
	out := make([]any, 0, len(list))
	for _, e := range list {
		em, ok := e.(map[string]any)
		if !ok {
			return nil, false
		}
		edit, ok := shapeBody(shape{content: []string{"old_string", "new_string"}, flags: []string{"replace_all"}}, em)
		if !ok {
			return nil, false
		}
		out = append(out, edit)
	}
	return out, true
}

// argsBody is the arguments of a tool Den has no layout for (an MCP tool,
// say), all of them, shown as JSON: they must all fit on the inbox item.
func argsBody(input map[string]any) (map[string]any, bool) {
	out, ok := cleanArgs(input, 0).(map[string]any)
	if !ok {
		return nil, false
	}
	return out, argsFit(out)
}

// argsFit reports whether arguments show whole: within argsMax and within
// the lines the inbox shows.
func argsFit(args map[string]any) bool {
	if len(args) == 0 {
		return true
	}
	raw, err := json.Marshal(args)
	if err != nil || len(raw) > argsMax {
		return false
	}
	ind, err := json.MarshalIndent(args, "", "  ")
	return err == nil && bytes.Count(ind, []byte("\n"))+1 <= argsLines
}

func exactOK(v string) bool { return len(v) <= exactMax && sanitize.Text(v) == v }

func flagOK(v any) bool {
	c := cleanArgs(v, 0)
	if c == nil && v != nil {
		return false
	}
	raw, err := json.Marshal(c)
	return err == nil && len(raw) <= flagMax
}

func slicesHas(xs []string, x string) bool {
	for _, s := range xs {
		if s == x {
			return true
		}
	}
	return false
}

// capContent sanitises a shown-but-not-decisive text and cuts it to
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
		s += cutMark
	}
	return s
}

// contentOK reports whether s is what capContent could have made.
func contentOK(s string) bool {
	return len(s) <= contentMax+len(cutMark) && strings.Count(s, "\n") <= contentLines && sanitize.Text(s) == s
}

// cleanArgs checks a tool's arguments for a tool Den has no layout for,
// and a flag's value. Strings must survive sanitising unchanged and
// nesting is bounded; anything else fails it (nil).
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
		if !exactOK(v) {
			return nil
		}
		return v
	default:
		return v // numbers, booleans, null
	}
}

// questionBody is an AskUserQuestion call as Den shows it: each question's
// text, header, multiSelect and options. The texts are sanitised; keys maps
// a shown question text back to the asked one.
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
			if sanitize.Line(o.Label) != o.Label || len(o.Label) > exactMax {
				return nil, nil, false // an answer is the label itself
			}
			opt := map[string]any{"label": o.Label}
			if o.Description != "" {
				opt["description"] = capContent(o.Description)
			}
			opts = append(opts, opt)
		}
		qm := map[string]any{"question": text, "options": opts}
		if q.Header != "" {
			qm["header"] = sanitize.Label(q.Header, headerMax)
		}
		if q.Multi {
			qm["multiSelect"] = true
		}
		out = append(out, qm)
	}
	return map[string]any{"questions": out}, keys, true
}

// checkBody is Den's half of the body filter: a held body must be one
// heldBody could have made for its tool, every decisive field whole and
// within its cap, every content field within its caps, and nothing else.
func checkBody(tool string, body json.RawMessage) bool {
	if len(body) > MaxLine {
		return false
	}
	var m map[string]any
	if json.Unmarshal(body, &m) != nil || m == nil {
		return false
	}
	if tool == question.Tool {
		return checkQuestions(body, m)
	}
	sh, known := shapes[tool]
	if !known {
		return cleanArgs(m, 0) != nil && argsFit(m)
	}
	return checkShape(sh, m)
}

func checkShape(sh shape, m map[string]any) bool {
	for _, k := range sh.exact {
		if v, isStr := m[k].(string); !isStr || !exactOK(v) {
			return false
		}
	}
	for k, v := range m {
		switch {
		case slicesHas(sh.exact, k):
		case slicesHas(sh.content, k):
			if str, isStr := v.(string); !isStr || !contentOK(str) {
				return false
			}
		case slicesHas(sh.flags, k):
			if !flagOK(v) {
				return false
			}
		case k == "edits" && sh.edits:
			list, ok := v.([]any)
			if !ok || len(list) == 0 || len(list) > maxEdits {
				return false
			}
			for _, e := range list {
				em, ok := e.(map[string]any)
				if !ok || !checkShape(shape{content: []string{"old_string", "new_string"}, flags: []string{"replace_all"}}, em) {
					return false
				}
			}
		default:
			return false
		}
	}
	return true
}

func checkQuestions(body json.RawMessage, m map[string]any) bool {
	qs, ok := question.Parse(body)
	if !ok || len(qs) > maxQuestions || len(m) != 1 {
		return false
	}
	for _, q := range qs {
		if !exactOK(q.Text) || len(q.Options) > maxOptions || sanitize.Label(q.Header, headerMax) != q.Header {
			return false
		}
		for _, o := range q.Options {
			if sanitize.Line(o.Label) != o.Label || len(o.Label) > exactMax || !contentOK(o.Description) {
				return false
			}
		}
	}
	return true
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
