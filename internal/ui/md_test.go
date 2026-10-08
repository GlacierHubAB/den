package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/theme"
)

func TestMarkdownLines(t *testing.T) {
	m := Model{st: newStyles(theme.Deepharness)}
	cases := []struct{ in, want string }{
		{"## Feedback", "Feedback"},
		{"> quoted **bold** text", "│ quoted bold text"},
		{"- item with `code`", "• item with code"},
		{"---", "────────────────────────"},
		{"plain **bold** and `code` here", "plain bold and code here"},
		{"unmatched `tick", "unmatched `tick"},
	}
	for _, c := range cases {
		got, _, fence := m.mdLine(c.in, false, 80)
		if ansi.Strip(got) != c.want || fence {
			t.Errorf("mdLine(%q) = %q fence=%v, want %q", c.in, ansi.Strip(got), fence, c.want)
		}
	}
	if out, _, fence := m.mdLine("```go", false, 80); out != "" || !fence {
		t.Errorf("opening fence: %q %v", out, fence)
	}
	if out, code, fence := m.mdLine("x := 1", true, 80); !fence || !code || out != "x := 1" {
		t.Errorf("code line: %q code=%v fence=%v", out, code, fence)
	}
	block := m.codeBlock("go test ./...\nlonger line that will need to wrap around the block width", 30)
	if len(block) < 3 || !strings.HasPrefix(ansi.Strip(block[0]), "┃ go test") {
		t.Errorf("codeBlock = %q", block)
	}
	for _, l := range block {
		if w := ansi.StringWidth(l); w > 30 {
			t.Errorf("block line width %d: %q", w, ansi.Strip(l))
		}
	}
}
