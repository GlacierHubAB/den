package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The header's API chip is short: the names Den read, and a count of the
// apps it cannot identify; never the full sentence, which stays in /api
// and on cards.
func TestAPIChipShortForm(t *testing.T) {
	for _, c := range []struct {
		names []string
		n     int
		want  string
	}{
		{nil, 1, "⇄ 1 app connected · unidentified"},
		{nil, 2, "⇄ 2 apps connected · unidentified"},
		{[]string{"drey-host"}, 0, "⇄ drey-host connected"},
		{[]string{"a", "b"}, 1, "⇄ a, b, 1 unidentified connected"},
	} {
		if got := connectedChip(c.names, c.n); got != c.want {
			t.Errorf("%v, %d: %q, want %q", c.names, c.n, got, c.want)
		}
	}
}

// The header's third line leaves out whole parts when it is too long,
// the least needed first, and never cuts one; the parts that must stay
// always do.
func TestHeaderPartsFitWhole(t *testing.T) {
	parts := []headerPart{
		{"hooks on", 2},
		{"⇄ 1 app connected · unidentified", 0},
		{"asking", 3},
		{"lane ready", 3},
		{"Pairing code (2m): alpha bravo", 0},
	}
	all := fitParts(parts, 1000)
	for w := 10; w <= 160; w++ {
		got := fitParts(parts, w)
		var kept []string
		for _, p := range parts {
			if strings.Contains(got, p.text) {
				kept = append(kept, p.text)
			} else if p.rank == 0 {
				t.Errorf("w=%d: %q left out: %q", w, p.text, got)
			}
		}
		if strings.Join(kept, " · ") != got {
			t.Errorf("w=%d: a part was cut or reordered: %q", w, got)
		}
		if ansi.StringWidth(all) <= w && got != all {
			t.Errorf("w=%d: left out parts that fit: %q", w, got)
		}
	}
}

// A live pairing code is never cut: at every width with the system line,
// with a connected app's chip and the bridge's error beside it, the code's
// words are on the line whole whenever its part fits the room at all.
func TestHeaderPairingCodeNeverCut(t *testing.T) {
	apiEnv(t)
	m := testModel(160, 40)
	apiOn(t, &m)
	dialAPI(t, &m)
	for end := time.Now().Add(2 * time.Second); len(m.api.srv.Clients()) == 0 && time.Now().Before(end); {
		time.Sleep(5 * time.Millisecond)
	}
	mm, _ := m.apiCommand([]string{"pair"})
	m = mm.(Model)
	if m.codeLine() == "" {
		t.Fatalf("no pairing code: %q", m.flash)
	}
	m.srvErr = errors.New("a long error from the bridge, to crowd the line")
	words := strings.Join(m.ctl.code, " ")
	// The narrow header has one line: the code or the note is on it.
	for w := 40; w < 72; w++ {
		line := ansi.Strip(m.header(w)[0])
		// The note may be cut at the narrowest widths; the code never is.
		if !strings.Contains(line, words) && !strings.Contains(line, "Pairing code live") {
			t.Errorf("w=%d: a live code is not shown in %q", w, line)
		}
		if lipgloss.Width(m.header(w)[0]) > w {
			t.Errorf("w=%d: the line is wider than the window", w)
		}
	}
	for w := 72; w <= 200; w++ {
		line := ansi.Strip(m.header(w)[3])
		if strings.Contains(line, words) {
			continue
		}
		if room := headerRoom(m.logoRows()[2]+"      ", "  ", w); room >= ansi.StringWidth(m.codeLine()) {
			t.Errorf("w=%d: the code is not whole in %q", w, line)
		} else if !strings.Contains(line, codeHidden) {
			t.Errorf("w=%d: neither the code nor the note in %q", w, line)
		}
		for _, word := range m.ctl.code {
			if strings.Contains(line, word+"…") {
				t.Errorf("w=%d: a word of the code is cut in %q", w, line)
			}
		}
	}
}

// At every width the header draws with its logo, a connected app's chip
// is shown whole, and the line it is on fits the width.
func TestHeaderChipNeverCut(t *testing.T) {
	apiEnv(t)
	m := testModel(160, 40)
	apiOn(t, &m)
	dialAPI(t, &m)
	for end := time.Now().Add(2 * time.Second); len(m.api.srv.Clients()) == 0 && time.Now().Before(end); {
		time.Sleep(5 * time.Millisecond)
	}
	parts := m.apiChipParts()
	if len(parts) == 0 {
		t.Fatal("no chip")
	}
	chip := ansi.Strip(parts[0])
	if !strings.HasSuffix(chip, " connected") {
		t.Fatalf("chip %q", chip)
	}
	for w := 60; w <= 160; w++ {
		rows := m.header(w)
		if w < 72 {
			continue // the narrow header has no system line
		}
		line := rows[3]
		if !strings.Contains(ansi.Strip(line), chip) {
			t.Errorf("w=%d: chip %q not whole in %q", w, chip, ansi.Strip(line))
		}
		if lipgloss.Width(line) > w {
			t.Errorf("w=%d: the line is %d wide", w, lipgloss.Width(line))
		}
	}
}
