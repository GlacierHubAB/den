package transcript

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Usage is the context the last assistant turn consumed.
type Usage struct {
	Tokens int    // input + cache read + cache creation of the last assistant message
	Window int    // the model's context window, best effort
	Model  string // model id of that message
}

// Percent is how full the context window is, 0-100.
func (u Usage) Percent() int {
	if u.Window == 0 {
		return 0
	}
	p := u.Tokens * 100 / u.Window
	if p > 100 {
		p = 100
	}
	return p
}

// ReadUsage returns the last assistant message's usage, or ok=false.
func ReadUsage(path string) (Usage, bool) {
	var u Usage
	found := false
	scanBackwards(path, 4<<20, func(line []byte) bool {
		if !bytes.Contains(line, []byte(`"type":"assistant"`)) || !bytes.Contains(line, []byte(`"usage"`)) {
			return false
		}
		var rec struct {
			Message struct {
				Model string `json:"model"`
				Usage struct {
					Input         int `json:"input_tokens"`
					CacheRead     int `json:"cache_read_input_tokens"`
					CacheCreation int `json:"cache_creation_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &rec) != nil {
			return false
		}
		u.Model = rec.Message.Model
		u.Tokens = rec.Message.Usage.Input + rec.Message.Usage.CacheRead + rec.Message.Usage.CacheCreation
		u.Window = windowFor(u.Model, u.Tokens)
		found = u.Tokens > 0
		return found
	})
	return u, found
}

// windowFor guesses the context window: 1M for models that say so or that
// are already past 200k, 200k otherwise.
func windowFor(model string, tokens int) int {
	if strings.Contains(model, "[1m]") || strings.Contains(model, "-1m") || tokens > 200_000 {
		return 1_000_000
	}
	return 200_000
}

// Entry is one line of recent activity.
type Entry struct {
	Kind   string // "you", "text", "tool"
	Text   string // the prompt, the reply, or a tool call's description
	Tool   string // tool name, for Kind "tool"
	Code   string // the command, path or other main argument of a tool call
	File   string // the file an Edit or Write touched
	Patch  []Hunk // what an Edit changed, as Claude Code recorded it
	Output string // the first lines a command printed
}

// Hunk is one changed region of a file.
type Hunk struct {
	OldStart, NewStart int
	Lines              []string // each prefixed " ", "-" or "+"
}

// Added and Removed count the changed lines across the patch.
func (e Entry) Added() (added, removed int) {
	for _, h := range e.Patch {
		for _, l := range h.Lines {
			switch {
			case strings.HasPrefix(l, "+"):
				added++
			case strings.HasPrefix(l, "-"):
				removed++
			}
		}
	}
	return
}

// Tail returns the last n human prompts, assistant texts and tool calls,
// oldest first. Tool results, system notices and slash-command echoes are
// skipped.
// outputLines is how much of a command's output the tail keeps.
const outputLines = 5

// toolResult is what Claude Code records next to a tool's result.
type toolResult struct {
	FilePath        string `json:"filePath"`
	StructuredPatch []struct {
		OldStart int      `json:"oldStart"`
		NewStart int      `json:"newStart"`
		Lines    []string `json:"lines"`
	} `json:"structuredPatch"`
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
}

func Tail(path string, n int) []Entry {
	var rev []Entry
	// Results follow their calls in the file, so scanning backwards we meet
	// each result first and attach it when its call turns up.
	results := map[string]toolResult{}
	scanBackwards(path, 4<<20, func(line []byte) bool {
		var rec struct {
			Type    string `json:"type"`
			IsMeta  bool   `json:"isMeta"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
			ToolUseResult json.RawMessage `json:"toolUseResult"`
		}
		if json.Unmarshal(line, &rec) != nil || rec.IsMeta {
			return false
		}
		switch rec.Type {
		case "user":
			var s string
			if json.Unmarshal(rec.Message.Content, &s) == nil {
				if t := cleanPrompt(s); t != "" {
					rev = append(rev, Entry{Kind: "you", Text: t})
				}
				break
			}
			var blocks []struct {
				Type      string `json:"type"`
				Text      string `json:"text"`
				ToolUseID string `json:"tool_use_id"`
			}
			if json.Unmarshal(rec.Message.Content, &blocks) == nil {
				for _, b := range blocks {
					switch b.Type {
					case "text":
						if t := cleanPrompt(b.Text); t != "" {
							rev = append(rev, Entry{Kind: "you", Text: t})
						}
					case "tool_result":
						var r toolResult
						if b.ToolUseID != "" && len(rec.ToolUseResult) > 0 && json.Unmarshal(rec.ToolUseResult, &r) == nil {
							results[b.ToolUseID] = r
						}
					}
				}
			}
		case "assistant":
			var blocks []struct {
				Type  string          `json:"type"`
				Text  string          `json:"text"`
				ID    string          `json:"id"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			}
			if json.Unmarshal(rec.Message.Content, &blocks) != nil {
				break
			}
			// Blocks are in order; we collect in reverse so walk backwards.
			for i := len(blocks) - 1; i >= 0; i-- {
				b := blocks[i]
				switch b.Type {
				case "text":
					if t := excerpt(b.Text, excerptLines); t != "" {
						rev = append(rev, Entry{Kind: "text", Text: t})
					}
				case "tool_use":
					desc, code := toolBrief(b.Input)
					e := Entry{Kind: "tool", Tool: b.Name, Text: desc, Code: code}
					if r, ok := results[b.ID]; ok {
						e.File = r.FilePath
						for _, h := range r.StructuredPatch {
							e.Patch = append(e.Patch, Hunk{OldStart: h.OldStart, NewStart: h.NewStart, Lines: h.Lines})
						}
						if out := strings.TrimSpace(r.Stdout); out != "" {
							e.Output = excerpt(out, outputLines)
						} else if out := strings.TrimSpace(r.Stderr); out != "" {
							e.Output = excerpt(out, outputLines)
						}
					}
					rev = append(rev, e)
				}
			}
		}
		return len(rev) >= n
	})
	if len(rev) > n {
		rev = rev[:n]
	}
	out := make([]Entry, len(rev))
	for i, e := range rev {
		out[len(rev)-1-i] = e
	}
	return out
}

// excerptLines is how much of a prompt or reply the tail keeps. The pane
// scrolls, so this is generous; it only guards against pathological turns.
const excerptLines = 40

func cleanPrompt(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "<") {
		return "" // command echoes, caveats, reminders
	}
	return excerpt(s, excerptLines)
}

// excerpt keeps the first n non-empty lines of s and the paragraph breaks
// between them (runs of blank lines collapse to one), marking a cut with an
// ellipsis.
func excerpt(s string, n int) string {
	var out []string
	kept := 0
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		l = strings.TrimRight(l, " \t")
		if strings.TrimSpace(l) == "" {
			if len(out) > 0 && out[len(out)-1] != "" {
				out = append(out, "")
			}
			continue
		}
		if kept == n {
			out = append(out, "…")
			break
		}
		out = append(out, l)
		kept++
	}
	return strings.Join(out, "\n")
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// toolBrief returns what a tool call was for (its description, when the
// model gave one) and the call itself: the command, path or other main
// argument, up to six lines.
func toolBrief(raw json.RawMessage) (desc, code string) {
	var in map[string]any
	if json.Unmarshal(raw, &in) != nil {
		return "", ""
	}
	if v, ok := in["description"].(string); ok && strings.TrimSpace(v) != "" {
		desc = firstLine(v)
	}
	for _, k := range []string{"command", "file_path", "pattern", "url", "query", "prompt", "to", "skill"} {
		if v, ok := in[k].(string); ok && strings.TrimSpace(v) != "" {
			code = excerpt(v, 6)
			break
		}
	}
	return desc, code
}
