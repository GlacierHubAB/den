package ui

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/theme"
)

func testModel(w, h int) Model {
	m := Model{st: newStyles(theme.Deepharness), agents: sample(), store: inbox.New(), width: w, height: h, tab: tabFleet}
	m.input = newInput(m.st)
	m.input.Focus()
	m.fitInput()
	return m
}

func TestLongPromptWrapsAndStaysVisible(t *testing.T) {
	m := testModel(100, 30)
	m.input.SetValue(strings.Repeat("word ", 60) + "END")
	m.fitInput()
	out := ansi.Strip(m.View().Content)
	if !strings.Contains(out, "END") {
		t.Errorf("end of the prompt is not visible:\n%s", out)
	}
	if n := len(strings.Split(m.View().Content, "\n")); n != 30 {
		t.Errorf("height = %d, want 30: the panes must shrink, not the screen grow", n)
	}
	if m.input.Height() < 3 {
		t.Errorf("input height = %d for 300 columns of text", m.input.Height())
	}
}

func TestNewlineGrowsInputUpToCap(t *testing.T) {
	m := testModel(100, 30)
	type_ := func(text string) {
		mm, _ := m.Update(tea.PasteMsg{Content: text})
		m = mm.(Model)
	}
	for i := 0; i < 12; i++ {
		mm, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt})
		m = mm.(Model)
		type_("line")
	}
	type_("last")
	if m.input.Height() != inputMaxRows {
		t.Errorf("height = %d, want the cap %d", m.input.Height(), inputMaxRows)
	}
	if lines := strings.Count(m.input.Value(), "\n"); lines != 12 {
		t.Errorf("newlines = %d, want 12", lines)
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "last") {
		t.Errorf("cursor line scrolled out of view")
	}
	if n := len(strings.Split(m.View().Content, "\n")); n != 30 {
		t.Errorf("screen height = %d, want 30", n)
	}
}

func TestPastedImagePathBecomesChip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "shot one.png")
	f, _ := os.Create(p)
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(1, 1, color.RGBA{255, 0, 0, 255})
	png.Encode(f, img)
	f.Close()

	m := testModel(100, 30)
	m.input.SetValue("look at ")
	m.input.CursorEnd()
	mm, _ := m.Update(tea.PasteMsg{Content: "file://" + strings.ReplaceAll(p, " ", "%20") + "\n"})
	m = mm.(Model)
	if got := m.input.Value(); got != "look at [Image #1] " {
		t.Errorf("value = %q", got)
	}
	if len(m.attachments) != 1 || m.attachments[0].Path != p || m.attachments[0].ImageID != minImageID {
		t.Errorf("attachments = %+v", m.attachments)
	}
	if got := m.expandChips(m.input.Value()); got != "look at [Image #1: "+p+"] " {
		t.Errorf("expanded = %q", got)
	}
	out := ansi.Strip(m.View().Content)
	if !strings.Contains(out, "[Image #1] shot one.png") {
		t.Errorf("chip row missing:\n%s", out)
	}
	// Plain text pastes stay text, even when they mention a file.
	mm, _ = m.Update(tea.PasteMsg{Content: "see /etc/hostname"})
	m = mm.(Model)
	if !strings.HasSuffix(m.input.Value(), "see /etc/hostname") {
		t.Errorf("text paste altered: %q", m.input.Value())
	}
	// Deleting a chip drops its image from the send.
	m.input.SetValue("no image here")
	if list := m.referenced(m.input.Value()); len(list) != 0 {
		t.Errorf("unreferenced attachment still counted: %+v", list)
	}
	if cmd := m.clear(); cmd != nil || len(m.attachments) != 0 {
		t.Errorf("clear without kitty should forget attachments silently")
	}
}

func TestImagePathsRejectsProse(t *testing.T) {
	if got := imagePaths("hello world\n/nonexistent/x.png"); got != nil {
		t.Errorf("got %v", got)
	}
	if got := imagePaths(""); got != nil {
		t.Errorf("empty: %v", got)
	}
}

func TestThumbPlaceholdersCarryTheImageID(t *testing.T) {
	rows := thumbRowsFor(attachment{Num: 2, ImageID: 17, Path: "x.png"})
	if len(rows) != thumbRows {
		t.Fatalf("rows = %d", len(rows))
	}
	if w := ansi.StringWidth(rows[0]); w != thumbCols {
		t.Errorf("placeholder row width = %d, want %d", w, thumbCols)
	}
	if !strings.Contains(rows[0], "38;5;17") {
		t.Errorf("foreground must carry the image id: %q", rows[0])
	}
}
