package transcript

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/GlacierHubAB/den/internal/sanitize"
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
	At      time.Time    // when it happened
	Kind    string       // "you", "text", "tool"
	Text    string       // the prompt, the reply, or a tool call's description
	Tool    string       // tool name, for Kind "tool"
	Code    string       // the command, path or other main argument of a tool call
	Changes []FileChange // files the call changed, as Claude Code recorded them
	Output  string       // the first lines a command printed
}

// FileChange is one file an Edit, Write or Bash call changed.
type FileChange struct {
	Path             string // safe to print
	Hunks            []Hunk
	Created, Deleted bool
	// Raw is the path as the transcript gave it, when cleaning Path for
	// printing changed it: what a change is matched against files by,
	// never shown.
	Raw string `json:"-"`
	// Unsafe marks a change a host sent for such a file: by its cleaned
	// name only, with no content.
	Unsafe bool `json:"-"`
}

// Hidden says the file's name has control characters, so its content is
// not shown (or sent to Den) as Review does not show it either.
func (c FileChange) Hidden() bool { return c.Raw != "" || c.Unsafe }

// Real is the path the change was made to, to match it against files.
func (c FileChange) Real() string {
	if c.Raw != "" {
		return c.Raw
	}
	return c.Path
}

// Hunk is one changed region of a file.
type Hunk struct {
	OldStart, NewStart int
	Lines              []string // each prefixed " ", "-" or "+"
}

// Counts returns the added and removed lines of the change.
func (c FileChange) Counts() (added, removed int) {
	for _, h := range c.Hunks {
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

// toolResult is what Claude Code records next to a tool's result: a patch
// for Edit and Write, output and any files a command changed for Bash.
type toolResult struct {
	Type            string    `json:"type"` // Write: "create" or "update"
	FilePath        string    `json:"filePath"`
	Content         string    `json:"content"`
	StructuredPatch []rawHunk `json:"structuredPatch"`
	Stdout          string    `json:"stdout"`
	Stderr          string    `json:"stderr"`
	BashEditDiff    struct {
		Files []struct {
			FilePath string    `json:"filePath"`
			Hunks    []rawHunk `json:"hunks"`
			Created  bool      `json:"created"`
			Deleted  bool      `json:"deleted"`
		} `json:"files"`
	} `json:"bashEditDiff"`
}

type rawHunk struct {
	OldStart int      `json:"oldStart"`
	NewStart int      `json:"newStart"`
	Lines    []string `json:"lines"`
}

func hunks(raw []rawHunk) []Hunk {
	out := make([]Hunk, 0, len(raw))
	for _, h := range raw {
		out = append(out, Hunk{OldStart: h.OldStart, NewStart: h.NewStart, Lines: diffLines(h.Lines)})
	}
	return out
}

// diffLines makes each hunk line safe to print, one line each.
func diffLines(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = sanitize.Code(l)
	}
	return out
}

// filePath makes a changed file's path safe to print. A path with control
// characters, bidi controls or invalid UTF-8 in it loses them; rawPath
// keeps the path it was, to match.
func filePath(p string) string {
	return sanitize.Code(p)
}

// rawPath is p when filePath changes it, "" otherwise.
func rawPath(p string) string {
	if filePath(p) == p {
		return ""
	}
	return p
}

// changes turns a result into the files it changed.
func (r toolResult) changes(tool string) []FileChange {
	switch tool {
	case "Edit", "MultiEdit", "NotebookEdit":
		if r.FilePath != "" && len(r.StructuredPatch) > 0 {
			return []FileChange{{Path: filePath(r.FilePath), Raw: rawPath(r.FilePath), Hunks: hunks(r.StructuredPatch)}}
		}
	case "Write":
		if r.FilePath == "" {
			return nil
		}
		if r.Type == "create" || len(r.StructuredPatch) == 0 {
			// A new file: every line is an addition.
			var lines []string
			for _, l := range strings.Split(strings.TrimRight(sanitize.Text(r.Content), "\n"), "\n") {
				lines = append(lines, "+"+l)
			}
			return []FileChange{{Path: filePath(r.FilePath), Raw: rawPath(r.FilePath), Created: true, Hunks: []Hunk{{OldStart: 0, NewStart: 1, Lines: lines}}}}
		}
		return []FileChange{{Path: filePath(r.FilePath), Raw: rawPath(r.FilePath), Hunks: hunks(r.StructuredPatch)}}
	case "Bash":
		var out []FileChange
		for _, f := range r.BashEditDiff.Files {
			out = append(out, FileChange{Path: filePath(f.FilePath), Raw: rawPath(f.FilePath), Hunks: hunks(f.Hunks), Created: f.Created, Deleted: f.Deleted})
		}
		return out
	}
	return nil
}

func Tail(path string, n int) []Entry {
	return Turn(path, time.Time{}, time.Time{}, n)
}

// Turn returns the newest n entries whose timestamps fall between from and
// to; a zero bound is open. It is how a dispatch card shows one agent turn.
func Turn(path string, from, to time.Time, n int) []Entry {
	var rev []Entry
	// Results follow their calls in the file, so scanning backwards we meet
	// each result first and attach it when its call turns up.
	results := map[string]toolResult{}
	scanBackwards(path, 8<<20, func(line []byte) bool {
		var rec struct {
			Type      string `json:"type"`
			IsMeta    bool   `json:"isMeta"`
			Timestamp string `json:"timestamp"`
			Message   struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
			ToolUseResult json.RawMessage `json:"toolUseResult"`
		}
		if json.Unmarshal(line, &rec) != nil || rec.IsMeta {
			return false
		}
		at, _ := time.Parse(time.RFC3339Nano, rec.Timestamp)
		if !to.IsZero() && !at.IsZero() && at.After(to) {
			return false // newer than the window: keep scanning back
		}
		if !from.IsZero() && !at.IsZero() && at.Before(from) {
			return true // older than the window: done
		}
		stamp := func(e Entry) Entry { e.At = at; return e }
		switch rec.Type {
		case "user":
			var s string
			if json.Unmarshal(rec.Message.Content, &s) == nil {
				if t := cleanPrompt(s); t != "" {
					rev = append(rev, stamp(Entry{Kind: "you", Text: t}))
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
							rev = append(rev, stamp(Entry{Kind: "you", Text: t}))
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
						rev = append(rev, stamp(Entry{Kind: "text", Text: t}))
					}
				case "tool_use":
					desc, code := ToolBrief(b.Input)
					e := stamp(Entry{Kind: "tool", Tool: sanitize.Line(b.Name), Text: desc, Code: code})
					if r, ok := results[b.ID]; ok {
						e.Changes = r.changes(b.Name)
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
// ellipsis. It sanitises s first: everything a transcript entry shows passes
// through here or firstLine.
func excerpt(s string, n int) string {
	var out []string
	kept := 0
	for _, l := range strings.Split(strings.TrimSpace(sanitize.Text(s)), "\n") {
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
	s = strings.TrimSpace(sanitize.Text(s))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// ToolBrief returns what a tool call was for (its description, when the
// model gave one) and the call itself: the command, path or other main
// argument, up to six lines. Hook payloads carry the same input shape.
func ToolBrief(raw json.RawMessage) (desc, code string) {
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
