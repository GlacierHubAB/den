package ui

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/orch"
	"github.com/GlacierHubAB/den/internal/theme"
)

// chatGoldenReply is an orchestrator reply with the markdown it writes:
// the bold-with-code lines of the reported bug, lists, a quote, a table,
// a link and a fenced block.
const chatGoldenReply = "drey-lead has routed around the stalled agent, so you don't need to top up drey-hatch.\n\n" +
	"**On `dev` (all green):** the HTML pages with the signed golden set (resize, animation, uninstall-as-command), the host with its bridge, the review report, and the old QML removed.\n\n" +
	"**On `origin/host-fixes`:** the lead pushed drey-hatch's committed work, and nothing was uncommitted.\n\n" +
	"**Still missing before the security re-review:**\n" +
	"- The input region re-applied after hide and show.\n" +
	"- The `drey doctor` rule for the catcher layers.\n" +
	"  - nested: see [the notes](https://example.test/notes)\n\n" +
	"### What follows, in order\n" +
	"1. drey-secreview's re-review.\n" +
	"2. The bar item rebuilt to open the HTML drop-down.\n\n" +
	"> The lead's estimate is 2 to 3 hours, *if* the re-review passes.\n\n" +
	"| Branch | State |\n|---|:---:|\n| `dev` | green |\n| host-fixes-2 | open |\n\n" +
	"```sh\ngo test ./internal/ui\n```\n" +
	"---\n" +
	"snake_case_names stay, and \\*escaped\\* stars too."

// TestChatGolden pins the Orchestrator tab with a markdown reply at a wide
// and a narrow width: the reply's markup is drawn, never shown as
// asterisks, and the chat keeps its inner margin.
func TestChatGolden(t *testing.T) {
	volatile := regexp.MustCompile(`\d{2}:(\d{2}|…)|(\b|m)\d+[smhd]\b`)
	for _, c := range []struct {
		name string
		w, h int
	}{
		{"wide", 100, 46},
		{"narrow", 44, 64},
	} {
		st := newStyles(theme.Deepharness)
		m := Model{st: st, agents: sample(), store: inbox.New(), tab: tabOrch, width: c.w, height: c.h}
		m.input = newInput(st)
		m.colors = map[string]string{}
		at := time.Date(2026, 10, 13, 5, 15, 0, 0, time.UTC)
		m.orch.chat = append(m.orch.chat, chatEntry{role: "you", text: "where are we with the review pack?", at: at})
		m.handleOrchEvent(orch.Event{Kind: orch.Text, Text: chatGoldenReply})
		got := volatile.ReplaceAllStringFunc(m.View().Content, func(v string) string {
			if strings.HasPrefix(v, "m") {
				return "m#"
			}
			return "#"
		})
		plain := ansi.Strip(got)
		for _, bad := range []string{"**", "`dev`", "### "} {
			if strings.Contains(plain, bad) {
				t.Errorf("%s: markup %q shown literally:\n%s", c.name, bad, plain)
			}
		}
		for i, l := range strings.Split(got, "\n") {
			if w := ansi.StringWidth(l); w > c.w {
				t.Errorf("%s: line %d is %d wide", c.name, i, w)
			}
		}
		path := filepath.Join("testdata", "chat_"+c.name+".golden")
		if *updateGolden {
			if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if got != string(want) {
			t.Errorf("%s: the chat frame changed\n--- got\n%s\n--- want\n%s", c.name, plain, ansi.Strip(string(want)))
		}
	}
}
