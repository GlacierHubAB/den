package desktop

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/GlacierHubAB/den/internal/sanitize"
)

// View turns a desktop agent's stream-json (claude -p --output-format
// stream-json --verbose) into lines for its terminal, so the human can
// follow it there: its words, each tool call and what came of it. Every
// string is sanitised and cut; nothing it prints is ever read back.
func View(in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64<<10), MaxMessage)
	for sc.Scan() {
		for _, l := range viewLines(sc.Bytes()) {
			fmt.Fprintln(out, l)
		}
	}
	return sc.Err()
}

// viewLines is what one stream-json event shows, none for the rest.
func viewLines(raw []byte) []string {
	var ev struct {
		Type    string   `json:"type"`
		Subtype string   `json:"subtype"`
		Tools   []string `json:"tools"`
		Result  string   `json:"result"`
		IsError bool     `json:"is_error"`
		Message struct {
			Content []struct {
				Type    string          `json:"type"`
				Text    string          `json:"text"`
				Name    string          `json:"name"`
				Input   json.RawMessage `json:"input"`
				Content json.RawMessage `json:"content"`
				IsError bool            `json:"is_error"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &ev) != nil {
		return nil
	}
	var out []string
	switch ev.Type {
	case "system":
		if ev.Subtype == "init" {
			out = append(out, fmt.Sprintf("── desktop agent started by Den · %d tools · Den's gate decides every action ──", len(ev.Tools)))
		}
	case "assistant":
		for _, c := range ev.Message.Content {
			switch c.Type {
			case "text":
				for _, l := range strings.Split(sanitize.Text(strings.TrimSpace(c.Text)), "\n") {
					out = append(out, "  "+cut(l, 200))
				}
			case "tool_use":
				out = append(out, "▶ "+callLine(c.Name, c.Input))
			}
		}
	case "user":
		for _, c := range ev.Message.Content {
			if c.Type != "tool_result" {
				continue
			}
			mark := "  ✓ "
			if c.IsError {
				mark = "  ✗ "
			}
			out = append(out, mark+cut(sanitize.Line(viewResult(c.Content)), 160))
		}
	case "result":
		word := "done"
		if ev.IsError || ev.Subtype != "success" {
			word = "ended: " + sanitize.Label(ev.Subtype, 40)
		}
		out = append(out, "── "+word+" ──")
	}
	return out
}

// callLine is a tool call in a few words: the tool, and what matters of
// its input.
func callLine(name string, input json.RawMessage) string {
	tool := strings.TrimPrefix(name, "mcp__"+MCPServer+"__")
	var a map[string]any
	json.Unmarshal(input, &a)
	s := func(k string) string { v, _ := a[k].(string); return v }
	switch tool {
	case "keyboard":
		if s("action") == "type" {
			return fmt.Sprintf("keyboard: type %q", cut(sanitize.Line(Describe(s("text"), 60)), 64))
		}
		return "keyboard: press " + sanitize.Label(s("keys"), 40)
	case "pointer":
		return fmt.Sprintf("pointer: %s at %v,%v", sanitize.Label(s("action"), 12), a["x"], a["y"])
	case "click_ui":
		if n := s("name"); n != "" {
			return fmt.Sprintf("click_ui: %q", sanitize.Label(n, 60))
		}
		return fmt.Sprintf("click_ui: mark %v", a["mark"])
	case "request_approval":
		return "asks you: " + sanitize.Label(s("intent"), 120)
	case "your_turn":
		return "your turn: " + sanitize.Label(s("step"), 120)
	}
	return sanitize.Label(tool, 40)
}

func viewResult(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	json.Unmarshal(raw, &blocks)
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		} else if b.Type == "image" {
			parts = append(parts, "[screenshot]")
		}
	}
	return strings.Join(parts, " ")
}

func cut(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
