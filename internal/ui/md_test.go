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
		got, fence := m.mdLine(c.in, false, 80)
		if ansi.Strip(got) != c.want || fence {
			t.Errorf("mdLine(%q) = %q fence=%v, want %q", c.in, ansi.Strip(got), fence, c.want)
		}
	}
	if out, fence := m.mdLine("```go", false, 80); out != "" || !fence {
		t.Errorf("opening fence: %q %v", out, fence)
	}
	if out, fence := m.mdLine("x := 1", true, 80); !fence || !strings.Contains(ansi.Strip(out), "x := 1") {
		t.Errorf("code line: %q %v", out, fence)
	}
}
