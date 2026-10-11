package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/mission"
	"github.com/GlacierHubAB/den/internal/theme"
)

// copyModel is an Overview with an open mission whose thread holds a
// command block, beside an agents pane with text of its own.
func copyModel(t *testing.T) Model {
	t.Helper()
	m := Model{st: newStyles(theme.Deepharness), store: inbox.New(), width: 120, height: 40, tree: newTreeState(), tab: tabOrch}
	m.base = sample()
	for i := range m.base {
		m.base[i].Session.SessionID = strings.ToLower(strings.ReplaceAll(m.base[i].Name, " ", "-"))
	}
	m.missions = mission.New("")
	m.input = newInput(m.st)
	m.refresh()
	m.orch.chat = []chatEntry{
		{role: "you", text: "how do I run the tests?", at: time.Now()},
		{role: "orchestrator", text: "Run this in the repository:\n```\ngo test ./...\nmake install\n```\nThen restart den.", at: time.Now()},
	}
	return m
}

func recordCopies(t *testing.T) *[]string {
	t.Helper()
	var got []string
	old, oldFlash := copyViaWlCopy, copyFlashFor
	copyViaWlCopy, copyFlashFor = func(s string) { got = append(got, s) }, time.Millisecond
	t.Cleanup(func() { copyViaWlCopy, copyFlashFor = old, oldFlash })
	return &got
}

// runCmds runs a command tree and returns the messages it produced.
func runCmds(c tea.Cmd) []tea.Msg {
	var out []tea.Msg
	var walk func(tea.Cmd)
	walk = func(c tea.Cmd) {
		if c == nil {
			return
		}
		switch v := c().(type) {
		case tea.BatchMsg:
			for _, x := range v {
				walk(x)
			}
		case nil:
		default:
			out = append(out, v)
		}
	}
	walk(c)
	return out
}

func rowOf(frame []string, want string) int {
	for i, l := range frame {
		if strings.Contains(ansi.Strip(l), want) {
			return i
		}
	}
	return -1
}

func TestCopyPaneIsTheChatOnOrchAndTheMissionPaneOnOverview(t *testing.T) {
	m := copyModel(t)
	top, h := m.paneArea()
	r, ok := m.copyPane()
	if !ok || r.x0 != 1 || r.x1 != 118 || r.y0 != top+1 || r.y1 != top+h-2 {
		t.Errorf("orch pane = %+v %v (top %d h %d)", r, ok, top, h)
	}
	m.tab = tabOverview
	r, ok = m.copyPane()
	lw := clamp(120*42/100, 36, 64)
	if !ok || r.x0 != lw+1 || r.x1 != 118 || r.y0 != top+stripRows+1 {
		t.Errorf("overview pane = %+v %v (lw %d)", r, ok, lw)
	}
	m.tab = tabFleet
	if _, ok := m.copyPane(); ok {
		t.Error("the Fleet view must not own the mouse")
	}
	if v := m.View(); v.MouseMode != tea.MouseModeNone {
		t.Errorf("Fleet view mouse mode = %v", v.MouseMode)
	}
	m.tab = tabOrch
	if v := m.View(); v.MouseMode != tea.MouseModeCellMotion {
		t.Errorf("Orchestrator view mouse mode = %v", v.MouseMode)
	}
}

func TestClickOnACommandBlockCopiesTheWholeBlock(t *testing.T) {
	m := copyModel(t)
	got := recordCopies(t)
	frame := m.frameLines()
	y := rowOf(frame, "make install")
	if y < 0 {
		t.Fatalf("no command block drawn:\n%s", ansi.Strip(strings.Join(frame, "\n")))
	}
	pane, _ := m.copyPane()
	x := pane.x0 + 5
	mm, _ := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	mm, cmd := mm.(Model).Update(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
	m = mm.(Model)
	msgs := runCmds(cmd)
	if m.sel.copied != "go test ./...\nmake install" || m.flash != "Copied command (2 lines)" {
		t.Errorf("copied %q flash %q", m.sel.copied, m.flash)
	}
	if len(*got) != 1 || (*got)[0] != "go test ./...\nmake install" {
		t.Errorf("wl-copy got %q", *got)
	}
	if len(msgs) == 0 {
		t.Error("no clipboard message was produced")
	}
	// /copy finds the same block from the keyboard.
	m.sel.copied = ""
	mm, _ = m.command("/copy")
	if mm.(Model).sel.copied != "go test ./...\nmake install" {
		t.Errorf("/copy copied %q", mm.(Model).sel.copied)
	}
	// A click on prose copies nothing and leaves no selection.
	y = rowOf(frame, "Then restart den")
	mm, _ = m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	mm, cmd = mm.(Model).Update(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
	if cmd != nil || mm.(Model).sel.active {
		t.Error("a click on prose should not copy or select")
	}
}

func TestDragSelectsOnlyTheMissionPane(t *testing.T) {
	m := copyModel(t)
	recordCopies(t)
	// An open mission whose thread shows the chat, next to the agents pane.
	ms := &mission.Mission{ID: "m1", Project: "/home/x/tasklog", Name: "Tests", Mission: "Keep tests green", Shape: mission.Once, State: mission.Running, Created: time.Now()}
	m.missions.Save(ms)
	m.proj.list = []project{{Root: "/home/x/tasklog", Name: "tasklog", Agents: []string{"mossy-lemur"}}}
	m.proj.cur = "/home/x/tasklog"
	m.tab, m.ov.open = tabOverview, "m1"
	frame := m.frameLines()
	pane, _ := m.copyPane()
	y0 := rowOf(frame, "Keep tests green")
	if y0 < 0 || !pane.has(pane.x0, y0) {
		t.Fatalf("mission text not in the pane: row %d pane %+v\n%s", y0, pane, ansi.Strip(strings.Join(frame, "\n")))
	}
	left := ansi.Strip(ansi.Cut(frame[y0], 0, pane.x0-1))
	y1 := y0 + 2
	mm, _ := m.Update(tea.MouseClickMsg{X: pane.x0, Y: y0, Button: tea.MouseLeft})
	mm, _ = mm.(Model).Update(tea.MouseMotionMsg{X: pane.x1, Y: y1, Button: tea.MouseLeft})
	m = mm.(Model)
	if !m.sel.active {
		t.Fatal("drag did not select")
	}
	// The highlight stays inside the pane: the agents pane's columns are untouched.
	hl := strings.Split(m.View().Content, "\n")
	if got := ansi.Strip(ansi.Cut(hl[y0], 0, pane.x0-1)); got != left {
		t.Errorf("left pane changed under the highlight: %q vs %q", got, left)
	}
	if !strings.Contains(hl[y0], "\x1b[7m") {
		t.Error("no reverse-video highlight drawn")
	}
	mm, cmd := m.Update(tea.MouseReleaseMsg{X: pane.x1, Y: y1, Button: tea.MouseLeft})
	m = mm.(Model)
	runCmds(cmd)
	if !strings.Contains(m.sel.copied, "Keep tests green") || !strings.Contains(m.sel.copied, "shape") {
		t.Errorf("copied %q", m.sel.copied)
	}
	for _, l := range strings.Split(m.sel.copied, "\n") {
		if strings.Contains(l, "│") || strings.Contains(l, "Mossy") || strings.Contains(l, "ORCHESTRATOR") {
			t.Errorf("copied text carries the neighbouring pane or a border: %q", l)
		}
	}
	// Esc drops the highlight.
	mm, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if mm.(Model).sel.active {
		t.Error("Esc should clear the selection")
	}
}

func TestSelectedTextUsesPaneColumnsAndStripsTheBar(t *testing.T) {
	frame := []string{
		"│ left pane text   │ ┃ go test ./...     │",
		"│ more on the left │ ┃ make install      │",
		"│ still left       │ plain words here  │",
	}
	pane := rect{x0: 21, y0: 0, x1: 38, y1: 2}
	s := selState{pane: pane, ax: 23, ay: 0, cx: 30, cy: 2}
	got := selectedText(frame, s)
	if got != "go test ./...\nmake install\nplain word" {
		t.Errorf("selectedText = %q", got)
	}
	// Backwards drags read the same.
	s = selState{pane: pane, ax: 30, ay: 2, cx: 23, cy: 0}
	if selectedText(frame, s) != got {
		t.Error("a backwards drag should give the same text")
	}
	if b := codeBlockAt(frame, pane, 1); b != "go test ./...\nmake install" {
		t.Errorf("codeBlockAt = %q", b)
	}
	if b := codeBlockAt(frame, pane, 2); b != "" {
		t.Errorf("prose is not a block: %q", b)
	}
}
