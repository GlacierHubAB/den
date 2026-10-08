package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/review"
	"github.com/GlacierHubAB/den/internal/theme"
	"github.com/GlacierHubAB/den/internal/transcript"
)

func reviewFiles() []review.File {
	files := review.ParseUnified("diff --git a/cmd/list.go b/cmd/list.go\n--- a/cmd/list.go\n+++ b/cmd/list.go\n" +
		"@@ -18,3 +18,5 @@\n \tcmd := &cobra.Command{\n-\t\tRunE:  runList,\n+\t\tRunE: run,\n+\t\tArgs: cobra.NoArgs,\n+\t\tHidden: false,\n \t}\n" +
		"diff --git a/internal/store/filter.go b/internal/store/filter.go\nnew file mode 100644\n--- /dev/null\n+++ b/internal/store/filter.go\n" +
		"@@ -0,0 +1,2 @@\n+package store\n+func Pending() {}\n" +
		"diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-old\n+new by hand\n")
	// list.go: mossy wrote RunE and Args, maple wrote Hidden. filter.go:
	// all maple. README: nobody.
	files[0].Owner = [][]string{{"", "mossy", "mossy", "mossy", "maple", ""}}
	files[1].Owner = [][]string{{"maple", "maple"}}
	files[2].Owner = [][]string{{"", ""}}
	return files
}

func TestSplitWorks(t *testing.T) {
	at := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	sources := []review.Source{
		{SessionID: "mossy", Changes: []transcript.TimedChange{{At: at}}},
		{SessionID: "maple", Changes: []transcript.TimedChange{{At: at.Add(time.Hour)}}},
		{SessionID: "idle"}, // changed nothing that is still on disk
	}
	works := splitWorks("/r/tasklog", reviewFiles(), sources)
	if len(works) != 2 {
		t.Fatalf("works = %+v", works)
	}
	got := fmt.Sprintf("%s %d+%d- %d files | %s %d+%d- %d files",
		works[0].SessionID, works[0].Added, works[0].Removed, len(works[0].Files),
		works[1].SessionID, works[1].Added, works[1].Removed, len(works[1].Files))
	if want := "mossy 2+1- 1 files | maple 3+0- 2 files"; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if works[1].Repo != "tasklog" || !works[1].Last.Equal(at.Add(time.Hour)) { // not a checkout: the folder name
		t.Errorf("maple work = %+v", works[1])
	}
}

func TestReviewViewFitsWidth(t *testing.T) {
	agents := sample()
	files := reviewFiles()
	for i := range files[0].Owner[0] {
		if files[0].Owner[0][i] == "mossy" {
			files[0].Owner[0][i] = agents[0].Session.SessionID
		}
	}
	works := []work{{SessionID: agents[0].Session.SessionID, Root: "/home/x/tasklog", Repo: "tasklog", Files: files, Added: 2, Removed: 1, Last: time.Now().Add(-14 * time.Minute)}}
	for _, size := range [][2]int{{160, 45}, {120, 40}, {80, 24}, {60, 20}, {120, 16}, {100, 12}} {
		m := Model{st: newStyles(theme.Deepharness), agents: agents, store: inbox.New(), tab: tabReview, width: size[0], height: size[1]}
		m.input = newInput(m.st)
		m.rev = reviewState{works: works, loadedAt: time.Now(), focus: revDiff}
		out := m.View().Content
		lines := strings.Split(out, "\n")
		if size[1] < 20 {
			continue // tiny terminals: rendering without a crash is the test
		}
		if len(lines) != size[1] {
			t.Errorf("%dx%d: %d lines", size[0], size[1], len(lines))
		}
		for i, l := range lines {
			if w := ansi.StringWidth(l); w > size[0] {
				t.Errorf("%dx%d: line %d is %d wide: %q", size[0], size[1], i, w, ansi.Strip(l))
			}
		}
		plain := ansi.Strip(out)
		for _, want := range []string{"cmd/list.go", "Args: cobra.NoArgs", "@@ -18 +18 @@"} {
			if !strings.Contains(plain, want) {
				t.Errorf("%dx%d: %q missing", size[0], size[1], want)
			}
		}
		if size[0] >= 72 && !strings.Contains(plain, "Mossy Lemur") {
			t.Errorf("%dx%d: work row missing", size[0], size[1])
		}
		if os.Getenv("DEN_SHOW") != "" && size[0] == 120 {
			fmt.Println(plain)
		}
	}
}

func TestReviewKeys(t *testing.T) {
	m := Model{st: newStyles(theme.Deepharness), agents: sample(), store: inbox.New(), tab: tabReview}
	m.input = newInput(m.st)
	m.rev = reviewState{works: []work{
		{SessionID: "a", Files: reviewFiles()},
		{SessionID: "b", Files: reviewFiles()[:1]},
	}, loadedAt: time.Now()}
	m.moveReview(1)
	if m.rev.cur != 1 || m.rev.fileCur != 0 {
		t.Fatalf("work move: %+v", m.rev)
	}
	m.rev.cur, m.rev.focus = 0, revFiles
	m.moveReview(5)
	if m.rev.fileCur != 2 {
		t.Errorf("file move clamps to the last file: %d", m.rev.fileCur)
	}
	// A reload keeps the same work and file selected.
	m.applyReview(reviewLoadedMsg{works: []work{{SessionID: "b"}, {SessionID: "a", Files: reviewFiles()}}})
	if m.rev.cur != 1 || m.rev.fileCur != 2 {
		t.Errorf("after reload cur=%d file=%d", m.rev.cur, m.rev.fileCur)
	}
}

// reviewModel is a Review view on one work with list.go selected, with
// comments saved to a temporary file.
func reviewModel(t *testing.T) Model {
	t.Helper()
	agents := sample()
	files := reviewFiles()
	sid := agents[0].Session.SessionID
	for i := range files[0].Owner[0] {
		if files[0].Owner[0][i] != "" {
			files[0].Owner[0][i] = sid
		}
	}
	m := Model{st: newStyles(theme.Deepharness), agents: agents, store: inbox.New(), tab: tabReview, width: 160, height: 45}
	m.input = newInput(m.st)
	m.input.Focus()
	m.rev = reviewState{
		works:        []work{{SessionID: sid, Root: "/home/x/tasklog", Repo: "tasklog", Files: files}},
		loadedAt:     time.Now(),
		focus:        revDiff,
		commentsPath: filepath.Join(t.TempDir(), "reviews.json"),
	}
	m.resetCursor()
	return m
}

func press(m Model, keys ...string) Model {
	for _, k := range keys {
		var msg tea.KeyPressMsg
		switch k {
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		case "down":
			msg = tea.KeyPressMsg{Code: tea.KeyDown}
		case "up":
			msg = tea.KeyPressMsg{Code: tea.KeyUp}
		case "shift+down":
			msg = tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModShift}
		default:
			mm, _ := m.Update(tea.PasteMsg{Content: k})
			m = mm.(Model)
			continue
		}
		mm, _ := m.Update(msg)
		m = mm.(Model)
	}
	return m
}

func TestReviewCommentLifecycle(t *testing.T) {
	m := reviewModel(t)
	// The cursor starts on the first changed line: the removed RunE (old 19).
	if l := diffLines(*m.curFile())[m.rev.line]; l.kind != '-' || l.num != 19 {
		t.Fatalf("cursor starts on %+v", l)
	}
	if got := m.selectionLabel(); got != "list.go L19" {
		t.Errorf("label = %q", got)
	}

	// Select the three added lines (new 19-21) and comment on them.
	m = press(m, "down", "shift+down", "shift+down")
	if got := m.selectionLabel(); got != "list.go L19–21" {
		t.Errorf("range label = %q", got)
	}
	m = press(m, "Handle the error from GetBool", "enter")
	if len(m.rev.comments) != 1 {
		t.Fatalf("comments = %+v", m.rev.comments)
	}
	c := m.rev.comments[0]
	if c.Side != "new" || c.Start != 19 || c.End != 21 || len(c.Quote) != 3 || c.Quote[1] != "+\t\tArgs: cobra.NoArgs," || c.Body != "Handle the error from GetBool" {
		t.Errorf("comment = %+v", c)
	}
	// Lines 19-20 are Mossy's, 21 is Maple's: the majority gets it.
	if c.SessionID != m.rev.works[0].SessionID {
		t.Errorf("owner = %q", c.SessionID)
	}
	if m.input.Value() != "" || m.rev.ranged {
		t.Errorf("after adding: input %q ranged %v", m.input.Value(), m.rev.ranged)
	}
	if saved := review.LoadComments(m.rev.commentsPath); len(saved) != 1 {
		t.Errorf("saved = %+v", saved)
	}
	plain := ansi.Strip(m.View().Content)
	for _, want := range []string{"you · draft · L19–21", "Handle the error from GetBool", "Review · 1 draft"} {
		if !strings.Contains(plain, want) {
			t.Errorf("view is missing %q", want)
		}
	}

	// Enter on an empty line edits the comment under the cursor.
	m = press(m, "enter")
	if m.rev.editing != c.ID || m.input.Value() != c.Body {
		t.Fatalf("edit: editing=%q input=%q", m.rev.editing, m.input.Value())
	}
	m.input.SetValue("Handle the GetBool error")
	m.rev.focus = revFiles // moving off the diff must not turn the edit into a message
	m = press(m, "enter")
	m.rev.focus = revDiff

	// Leaving the view mid-edit drops the edit and its text.
	m = press(m, "enter")
	if m.rev.editing == "" {
		t.Fatal("expected an open edit")
	}
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = mm.(Model)
	if m.rev.editing != "" || m.input.Value() != "" {
		t.Errorf("after Tab: editing=%q input=%q", m.rev.editing, m.input.Value())
	}
	m.tab = tabReview
	if len(m.rev.comments) != 1 || m.rev.comments[0].Body != "Handle the GetBool error" || m.rev.editing != "" {
		t.Errorf("after edit = %+v", m.rev.comments)
	}

	// @Name and /commands are not comments.
	m = press(m, "/rescan", "enter")
	if len(m.rev.comments) != 1 {
		t.Errorf("a slash command became a comment")
	}

	m = press(m, "/delete", "enter")
	if len(m.rev.comments) != 0 {
		t.Errorf("after /delete = %+v", m.rev.comments)
	}
	if saved := review.LoadComments(m.rev.commentsPath); len(saved) != 0 {
		t.Errorf("delete not saved: %+v", saved)
	}
}

func TestReviewMouse(t *testing.T) {
	m := reviewModel(t)
	lay := m.reviewLayout()
	rows := m.diffRows(lay.diffW - 2)
	// Find the screen row of the "Args" line and click it.
	target := -1
	for i, r := range rows {
		if strings.Contains(ansi.Strip(r.text), "Args: cobra.NoArgs") {
			target = i
		}
	}
	if target < 0 {
		t.Fatal("Args line not rendered")
	}
	y := lay.top + 1 + target - m.rev.scroll
	mm, _ := m.Update(tea.MouseClickMsg{X: lay.diffX + 5, Y: y, Button: tea.MouseLeft})
	m = mm.(Model)
	if d := diffLines(*m.curFile())[m.rev.line]; !strings.Contains(d.text, "Args") || m.rev.ranged {
		t.Fatalf("click put the cursor on %+v", d)
	}
	// Drag up one row: a two-line range.
	mm, _ = m.Update(tea.MouseMotionMsg{X: lay.diffX + 5, Y: y - 1, Button: tea.MouseLeft})
	m = mm.(Model)
	mm, _ = m.Update(tea.MouseReleaseMsg{X: lay.diffX + 5, Y: y - 1, Button: tea.MouseLeft})
	m = mm.(Model)
	if got := m.selectionLabel(); got != "list.go L19–20" || m.rev.dragging {
		t.Errorf("after drag: %q dragging=%v", got, m.rev.dragging)
	}
	// Clicking the work pane moves focus there.
	mm, _ = m.Update(tea.MouseClickMsg{X: 3, Y: lay.top + 1, Button: tea.MouseLeft})
	m = mm.(Model)
	if m.rev.focus != revWork {
		t.Errorf("focus after clicking work = %v", m.rev.focus)
	}
	if m.View().MouseMode != tea.MouseModeCellMotion {
		t.Error("mouse not enabled in Review")
	}
	m.tab = tabFleet
	if m.View().MouseMode != tea.MouseModeNone {
		t.Error("mouse must stay off outside Review")
	}
}
