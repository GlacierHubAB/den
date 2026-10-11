package ui

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/badge"
	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/theme"
	"github.com/GlacierHubAB/den/internal/transcript"
)

func sample() []fleet.Agent {
	mk := func(name, role, task, cwd string, st fleet.Status, hist ...string) fleet.Agent {
		return fleet.Agent{
			Session: discover.Session{PID: 4242, SessionID: "0123456789abcdef", CWD: cwd, Version: "2.1.289", Name: "user-aa", Alive: st != fleet.Gone},
			Name:    name, Labels: badge.Labels{Role: role, Task: task, History: hist}, Status: st, Model: "claude-fable-5-1",
			Usage: transcript.Usage{Tokens: 373961, Window: 1_000_000}, HasUsage: true,
			Tail: []transcript.Entry{{Kind: "you", Text: "create a mockup"}, {Kind: "tool", Tool: "Bash", Text: "Run the tests", Code: "go test ./..."}, {Kind: "text", Text: "Done, all tests pass."}},
		}
	}
	return []fleet.Agent{
		mk("Mossy Lemur", "coding agent", "implement --pending flag for list command", "/home/x/tasklog", fleet.NeedsYou),
		mk("Snug Capybara", "designer", "design TUI mockup for multi-agent orchestrator with a very long task label that wraps", "/home/x", fleet.Working, "earlier thing one", "earlier thing two"),
		mk("Fuzzy Sprite", "", "", "/home/x/herdr-nvim-test", fleet.Idle),
		mk("Maple Owl", "coding agent", "match theme borders", "/home/x/agent-badge", fleet.Unknown),
		mk("Comet Newt", "coding agent", "gone away", "/home/x", fleet.Gone),
	}
}

func TestViewFitsWidthAtSeveralSizes(t *testing.T) {
	for _, size := range [][2]int{{160, 45}, {120, 40}, {100, 30}, {80, 24}, {60, 20}} {
		m := Model{st: newStyles(theme.Deepharness), agents: sample(), store: inbox.New(), cursor: 1, width: size[0], height: size[1], tab: tabFleet}
		m.input = newInput(m.st)
		out := m.View().Content
		lines := strings.Split(out, "\n")
		if len(lines) != size[1] {
			t.Errorf("%dx%d: %d lines", size[0], size[1], len(lines))
		}
		for i, l := range lines {
			if w := ansi.StringWidth(l); w > size[0] {
				t.Errorf("%dx%d: line %d is %d wide: %q", size[0], size[1], i, w, ansi.Strip(l))
			}
		}
		if size[0] >= 72 && !strings.Contains(ansi.Strip(out), "37%") {
			t.Errorf("%dx%d: context gauge missing", size[0], size[1])
		}
		if os.Getenv("DEN_SHOW") != "" && size[0] == 120 {
			fmt.Println(ansi.Strip(out))
		}
	}
}
