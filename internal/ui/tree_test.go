package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/theme"
	"github.com/GlacierHubAB/den/internal/tree"
)

func treeModel() Model {
	m := Model{st: newStyles(theme.Deepharness), store: inbox.New(), width: 120, height: 40, tree: newTreeState()}
	m.base = sample()
	for i := range m.base {
		m.base[i].Session.SessionID = strings.ToLower(strings.ReplaceAll(m.base[i].Name, " ", "-"))
	}
	m.input = newInput(m.st)
	m.refresh()
	now := time.Now()
	m.learnEdge(tree.Edge{Parent: tree.Orchestrator, Child: "snug-capybara", Brief: "lead the release", At: now.Add(-time.Hour)})
	m.learnEdge(tree.Edge{Parent: "snug-capybara", Child: "mossy-lemur", Brief: "check the notes", At: now.Add(-30 * time.Minute)})
	m.learnEdge(tree.Edge{Parent: "snug-capybara", Child: "fuzzy-sprite", Brief: "check the installer", At: now.Add(-20 * time.Minute)})
	m.refresh()
	return m
}

func names(m Model) []string {
	var out []string
	for _, a := range m.agents {
		out = append(out, a.Name)
	}
	return out
}

func TestTreeOrdersChildrenUnderParents(t *testing.T) {
	m := treeModel()
	got := strings.Join(names(m), ",")
	want := "Snug Capybara,Mossy Lemur,Fuzzy Sprite,Maple Owl,Comet Newt"
	if got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
	r := m.rowFor("snug-capybara")
	if r.directs != 2 || r.depth != 1 || r.section != "orchestrator" || r.guide != "└─ " {
		t.Errorf("snug row = %+v", r)
	}
	if r := m.rowFor("fuzzy-sprite"); r.guide != "   └─ " || r.cont != "      " {
		t.Errorf("fuzzy row = %+v", r)
	}
	if r := m.rowFor("mossy-lemur"); r.guide != "   ├─ " || r.cont != "   │  " {
		t.Errorf("mossy row = %+v", r)
	}
	if roll, via := m.rollupOf("snug-capybara"); roll.needsYou != 1 || via != "Mossy Lemur" {
		t.Errorf("rollup = %+v via %q", roll, via)
	}
	if ch := ansi.Strip(m.chainLine("fuzzy-sprite", true)); ch != "you › orchestrator › Snug Capybara › Fuzzy Sprite" {
		t.Errorf("chain = %q", ch)
	}
	out := ansi.Strip(m.agentsPane(60, 40))
	if !strings.Contains(out, "ORCHESTRATOR · 3") || !strings.Contains(out, "directs 2 · 1 needs you") || !strings.Contains(out, "├─ ◐ Mossy Lemur") {
		t.Errorf("agents pane:\n%s", out)
	}
}

func TestFoldHidesTheBranchAndTheCursorSkipsIt(t *testing.T) {
	m := treeModel()
	m.cursor = 0
	if !m.toggleFold(true) {
		t.Fatal("fold refused")
	}
	if !m.rowFor("mossy-lemur").hidden || !m.rowFor("fuzzy-sprite").hidden {
		t.Error("children not hidden")
	}
	m.move(1)
	if a, _ := m.selected(); a.Name != "Maple Owl" {
		t.Errorf("cursor landed on %s, want Maple Owl", a.Name)
	}
	out := ansi.Strip(m.agentsPane(60, 40))
	if strings.Contains(out, "Mossy Lemur") || !strings.Contains(out, "folded") {
		t.Errorf("folded pane still shows the branch:\n%s", out)
	}
	m.cursor = 0
	if !m.toggleFold(false) || m.rowFor("mossy-lemur").hidden {
		t.Error("unfold failed")
	}
}

func TestMapDrawsEveryNodeWithinWidth(t *testing.T) {
	m := treeModel()
	m.cursor = 2 // Fuzzy Sprite, the deepest node
	out := m.mapPane(116, 40)
	plain := ansi.Strip(out)
	for _, want := range []string{"you", "orchestrator", "Snug Capybara", "Mossy Lemur", "Fuzzy Sprite", "Maple Owl", "┌", "└"} {
		if !strings.Contains(plain, want) {
			t.Errorf("map lacks %q:\n%s", want, plain)
		}
	}
	for i, l := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(l); w > 116 {
			t.Errorf("line %d is %d wide", i, w)
		}
	}
	// A deep selection in a narrow pane scrolls sideways to it.
	narrow := ansi.Strip(m.mapPane(60, 40))
	if !strings.Contains(narrow, "Fuzzy Sprite") {
		t.Errorf("narrow map lost the selected node:\n%s", narrow)
	}
}

func TestPeerMessageMakesAnEdge(t *testing.T) {
	m := treeModel()
	m.base[1].Session.PID = 777 // Snug
	m.refresh()
	m.learnPeer("maple-owl", `<cross-session-message from="uds:/run/user/1000/cc-socks/777.sock" from-name="den-x" from-mode="prompting">
please run the tests</cross-session-message>`)
	m.refresh()
	if e := m.edgeFor("maple-owl"); e.Parent != "snug-capybara" || e.Brief != "please run the tests" {
		t.Errorf("edge = %+v", e)
	}
	// Maple replying to Snug does not put Snug under Maple.
	m.base[3].Session.PID = 778
	m.refresh()
	m.learnPeer("snug-capybara", `<cross-session-message from="uds:/run/user/1000/cc-socks/778.sock" from-name="den-y" from-mode="prompting">
done</cross-session-message>`)
	if e := m.edgeFor("snug-capybara"); e.Parent != tree.Orchestrator {
		t.Errorf("reply inverted the tree: %+v", e)
	}
}
