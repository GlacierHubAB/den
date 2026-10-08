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
	Kind string // "you", "text", "tool"
	Text string
}

// Tail returns the last n human prompts, assistant texts and tool calls,
// oldest first. Tool results, system notices and slash-command echoes are
// skipped.
func Tail(path string, n int) []Entry {
	var rev []Entry
	scanBackwards(path, 4<<20, func(line []byte) bool {
		var rec struct {
			Type    string `json:"type"`
			IsMeta  bool   `json:"isMeta"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
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
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(rec.Message.Content, &blocks) == nil {
				for _, b := range blocks {
					if b.Type == "text" {
						if t := cleanPrompt(b.Text); t != "" {
							rev = append(rev, Entry{Kind: "you", Text: t})
						}
					}
				}
			}
		case "assistant":
			var blocks []struct {
				Type  string          `json:"type"`
				Text  string          `json:"text"`
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
					rev = append(rev, Entry{Kind: "tool", Text: b.Name + " " + toolBrief(b.Input)})
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

// excerptLines is how much of a prompt or reply the tail keeps.
const excerptLines = 8

func cleanPrompt(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "<") {
		return "" // command echoes, caveats, reminders
	}
	return excerpt(s, excerptLines)
}

// excerpt keeps the first n non-empty lines of s, marking a cut with an
// ellipsis, so the detail pane can show the gist of a turn rather than
// only its opening line.
func excerpt(s string, n int) string {
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		l = strings.TrimRight(l, " \t")
		if strings.TrimSpace(l) == "" {
			continue
		}
		if len(out) == n {
			out = append(out, "…")
			break
		}
		out = append(out, l)
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

func toolBrief(raw json.RawMessage) string {
	var in map[string]any
	if json.Unmarshal(raw, &in) != nil {
		return ""
	}
	// The description says what a command is for; the command itself follows
	// so the pane shows both.
	var parts []string
	if v, ok := in["description"].(string); ok && strings.TrimSpace(v) != "" {
		parts = append(parts, firstLine(v))
	}
	for _, k := range []string{"command", "file_path", "pattern", "url", "query", "prompt", "to", "skill"} {
		if v, ok := in[k].(string); ok && strings.TrimSpace(v) != "" {
			parts = append(parts, excerpt(v, 3))
			break
		}
	}
	return strings.Join(parts, "\n")
}
