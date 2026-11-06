package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/api"
	perm "github.com/GlacierHubAB/den/internal/grants"
	"github.com/GlacierHubAB/den/internal/inbox"
)

// fakePanel stands in for den-api's panel methods: the same rules on a
// perm.State (SetDenLevel never raises a cap; a raise and the confirm hold
// the read's seq).
type fakePanel struct {
	st      perm.State
	seq     uint64
	raises  int
	removed []string
	lines   []string // the powers card's lines until the confirm
}

func (f *fakePanel) GrantsStart() (bool, []string) {
	if f.st.Confirmed {
		return f.st.Held, nil
	}
	return f.st.Held, f.lines
}

func (f *fakePanel) GrantsRead() perm.Grants {
	g := perm.Read(f.st)
	g.Seq = f.seq
	return g
}

func (f *fakePanel) SetDenLevel(class string, level int) error {
	if !perm.Valid(class, level) || level > perm.LastStep(class) {
		return api.ErrNoClass
	}
	for _, c := range perm.Read(f.st).Classes {
		if c.Class == class && level > c.DenLevel {
			return api.ErrRaise
		}
	}
	after := f.st
	after.DenLevels = map[string]int{}
	for k, v := range f.st.DenLevels {
		after.DenLevels[k] = v
	}
	after.DenLevels[class] = level
	after.Sessions = map[string]perm.Session{}
	for k, v := range f.st.Sessions {
		if k != class || v.Level < level {
			after.Sessions[k] = v
		}
	}
	if after.Cap(class) > f.st.Cap(class) {
		return api.ErrRaise
	}
	f.st, f.seq = after, f.seq+1
	return nil
}

func (f *fakePanel) RaiseDenLevel(class string, level int, seq uint64) error {
	if seq != f.seq {
		return api.ErrMoved
	}
	if f.st.DenLevels == nil {
		f.st.DenLevels = map[string]int{}
	}
	f.st.DenLevels[class] = level
	delete(f.st.Sessions, class)
	f.seq++
	f.raises++
	return nil
}

func (f *fakePanel) ConfirmLevels(seq uint64) error {
	if f.st.Held {
		return api.ErrHeld
	}
	if seq != f.seq {
		return api.ErrMoved
	}
	f.st.Confirmed, f.st.Held = true, false
	f.seq++
	return nil
}

func (f *fakePanel) RemoveKept(id string) error {
	var left []string
	for _, k := range f.st.Kept {
		if id != "*" && k != id {
			left = append(left, k)
		}
	}
	if len(left) == len(f.st.Kept) {
		return api.ErrNotKept
	}
	f.removed = append(f.removed, id)
	f.st.Kept = left
	f.seq++
	return nil
}

// panelModel is a model with the panel open on f.
func panelModel(t *testing.T, f *fakePanel, w int) Model {
	t.Helper()
	m := testModel(w, 50)
	m.perms.levels = f
	mm, _ := m.permissionsCommand()
	return mm.(Model)
}

func panelPress(m Model, keys ...string) Model {
	for _, k := range keys {
		var msg tea.KeyPressMsg
		switch k {
		case "left":
			msg = tea.KeyPressMsg{Code: tea.KeyLeft}
		case "right":
			msg = tea.KeyPressMsg{Code: tea.KeyRight}
		case "up":
			msg = tea.KeyPressMsg{Code: tea.KeyUp}
		case "down":
			msg = tea.KeyPressMsg{Code: tea.KeyDown}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "ctrl+y":
			msg = tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl}
		case "ctrl+n":
			msg = tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl}
		default:
			msg = tea.KeyPressMsg{Code: rune(k[0]), Text: k}
		}
		mm, _ := m.handleKey(msg)
		m = mm.(Model)
	}
	return m
}

func levelOf(f *fakePanel, class string) int { return f.st.Cap(class) }

// /permissions is Den's window only: from a console it is needs_den_screen,
// refused with its reason, and no words come back to run.
func TestPermissionsIsDensWindowOnly(t *testing.T) {
	words, v, why := dockLine("/permissions")
	if mode, _ := v.verdict(nil); words != nil || why == "" || mode != api.DockDenScreen {
		t.Errorf("cmd.run /permissions: %v %q %q", words, mode, why)
	}
}

// ← lowers at once; → asks, and only the question's ^Y raises; ^N, Esc and
// any other key keep the level.
func TestPanelRaiseAsks(t *testing.T) {
	f := &fakePanel{st: perm.State{Powers: true, Confirmed: true}, seq: 1}
	m := panelModel(t, f, 140)
	m = panelPress(m, "left") // routine 4 → 3, at once
	if levelOf(f, perm.Routine) != 3 || m.perms.ask != nil {
		t.Fatalf("after ←: %d, ask %+v", levelOf(f, perm.Routine), m.perms.ask)
	}
	for _, keep := range []string{"ctrl+n", "esc", "x"} {
		m = panelPress(m, "right")
		if m.perms.ask == nil || m.perms.ask.kind != "raise" {
			t.Fatalf("→ did not ask: %+v", m.perms.ask)
		}
		if !strings.Contains(ansi.Strip(strings.Join(m.permLines(140), "\n")), "Raise Everyday answers to 4 of 5 (Answer in your words)? ^Y to raise, ^N to keep") {
			t.Errorf("the question's words:\n%s", ansi.Strip(strings.Join(m.permLines(140), "\n")))
		}
		m = panelPress(m, keep)
		if levelOf(f, perm.Routine) != 3 || !m.perms.open {
			t.Errorf("%s raised or closed: %d", keep, levelOf(f, perm.Routine))
		}
	}
	m = panelPress(m, "right", "ctrl+y")
	if levelOf(f, perm.Routine) != 4 || f.raises != 1 {
		t.Errorf("^Y: %d, raises %d", levelOf(f, perm.Routine), f.raises)
	}
}

// P1: with a console's session below Den's level, ← never lifts the cap,
// and r to the default asks.
func TestPanelLoweringNeverLiftsASession(t *testing.T) {
	f := &fakePanel{st: perm.State{Powers: true, Confirmed: true, Sessions: map[string]perm.Session{perm.Routine: {Level: 1, Console: "Drey"}}}, seq: 1}
	m := panelModel(t, f, 140)
	m = panelPress(m, "left")
	if levelOf(f, perm.Routine) != 1 || m.perms.ask != nil {
		t.Errorf("← with a session at 1: cap %d, ask %+v", levelOf(f, perm.Routine), m.perms.ask)
	}
	m = panelPress(m, "r")
	if m.perms.ask == nil || levelOf(f, perm.Routine) != 1 {
		t.Errorf("r to the default above a session did not ask: %+v, cap %d", m.perms.ask, levelOf(f, perm.Routine))
	}
}

// P3: a question holds the read's seq; a change under it drops it and
// applies nothing.
func TestPanelQuestionDroppedWhenTheReadMoves(t *testing.T) {
	f := &fakePanel{st: perm.State{Powers: true, Confirmed: true, DenLevels: map[string]int{perm.Routine: 2}}, seq: 1}
	m := panelModel(t, f, 140)
	m = panelPress(m, "right")
	f.seq++ // a console's lowering, the lock, God mode…
	m = panelPress(m, "ctrl+y")
	if levelOf(f, perm.Routine) != 2 || m.perms.note != permMoved {
		t.Errorf("after a move: cap %d, note %q", levelOf(f, perm.Routine), m.perms.note)
	}
}

// P3: while a question is open, ^Y goes to it only, never to a card.
func TestPanelTakesTheKeysFromCards(t *testing.T) {
	f := &fakePanel{st: perm.State{Powers: true, Confirmed: true, DenLevels: map[string]int{perm.Routine: 2}}, seq: 1}
	m := panelModel(t, f, 140)
	m = panelPress(m, "right")
	it := m.store.AddMerge("api:console", "Merge a branch?", []string{"a line"})
	m.refresh()
	m = panelPress(m, "ctrl+y")
	if levelOf(f, perm.Routine) != 3 || it.State == inbox.Resolved {
		t.Errorf("^Y: routine %d, card %v", levelOf(f, perm.Routine), it.State)
	}
}

// Option 1: while a desktop agent can act, nothing raises or confirms; a
// paused mission lets them again. Lowering always works.
func TestPanelNoRaiseWhileAnAgentHasTheDesktop(t *testing.T) {
	f := &fakePanel{st: perm.State{Powers: true, Confirmed: true, DenLevels: map[string]int{perm.Routine: 2}}, seq: 1}
	m := panelModel(t, f, 140)
	dm := &deskMission{}
	m.desk.missions = map[string]*deskMission{"d1": dm}
	m = panelPress(m, "right")
	if m.perms.ask != nil || m.perms.note != permDeskHeld {
		t.Errorf("a raise while an agent has the desktop: %+v %q", m.perms.ask, m.perms.note)
	}
	m = panelPress(m, "left")
	if levelOf(f, perm.Routine) != 1 {
		t.Errorf("lowering under a mission: %d", levelOf(f, perm.Routine))
	}
	dm.Paused, dm.frozen = true, true
	m = panelPress(m, "right")
	if m.perms.ask == nil {
		t.Error("a paused mission still holds the raise")
	}
	dm.Paused = false
	m = panelPress(m, "ctrl+y")
	if levelOf(f, perm.Routine) != 1 || m.perms.note != permDeskHeld {
		t.Errorf("a ^Y after the mission resumed: %d, %q", levelOf(f, perm.Routine), m.perms.note)
	}
}

// P4: c asks with the powers card's lines, and only ^Y confirms.
func TestPanelConfirmAsks(t *testing.T) {
	f := &fakePanel{st: perm.State{Powers: true, DenLevels: map[string]int{perm.Destructive: 2}, Kept: []string{perm.KeptClose}}, seq: 1,
		lines: []string{"Den did not stop cleanly last time.", "Ctrl+Y also confirms the levels you set above Den's defaults: Stop and close 2 of 5 (default 1). Until then they are at the defaults."}}
	m := panelModel(t, f, 140)
	m = panelPress(m, "esc") // past the upgrade screen, keeping kept
	mm, _ := m.permissionsCommand()
	m = panelPress(mm.(Model), "c")
	if m.perms.ask == nil || m.perms.ask.kind != "confirm" || f.st.Confirmed {
		t.Fatalf("c: %+v, confirmed %v", m.perms.ask, f.st.Confirmed)
	}
	// The powers card's lines, what Den found at start first.
	if q := permQuestion(f.GrantsRead(), *m.perms.ask); q != "Confirm your levels? "+f.lines[0]+" "+f.lines[1]+" ^Y to confirm, ^N not now" {
		t.Errorf("the question: %q", q)
	}
	m = panelPress(m, "ctrl+y")
	if !f.st.Confirmed {
		t.Error("^Y did not confirm")
	}
}

// Kept: Enter asks, ^Y removes for good; the upgrade screen's Enter
// removes every entry and raises nothing.
func TestPanelKept(t *testing.T) {
	f := &fakePanel{st: perm.State{Powers: true, Confirmed: true, Kept: []string{perm.KeptClose, perm.KeptMerge}}, seq: 1}
	m := panelModel(t, f, 140)
	if !m.perms.upgrade {
		t.Fatal("no upgrade screen")
	}
	m = panelPress(m, "esc")
	mm, _ := m.permissionsCommand()
	m = panelPress(mm.(Model), "down", "down", "down", "down", "down", "enter")
	if m.perms.ask == nil || m.perms.ask.kind != "remove" || m.perms.ask.kept != perm.KeptClose {
		t.Fatalf("Enter on a kept row: %+v", m.perms.ask)
	}
	m = panelPress(m, "ctrl+y")
	if len(f.st.Kept) != 1 || f.st.Kept[0] != perm.KeptMerge {
		t.Errorf("after ^Y: %v", f.st.Kept)
	}
	g := &fakePanel{st: perm.State{Powers: true, Confirmed: true, Kept: perm.KeptAll()}, seq: 1}
	n := panelModel(t, g, 140)
	n = panelPress(n, "enter")
	if len(g.st.Kept) != 0 || g.raises != 0 {
		t.Errorf("the upgrade screen: kept %v, raises %d", g.st.Kept, g.raises)
	}
}

// The panel in each state, as the human reads it (plain text), at 140 and
// 80 columns: nothing cut, the lines wrap.
func TestPanelGoldens(t *testing.T) {
	top := map[string]int{}
	for _, id := range perm.Classes() {
		top[id] = perm.LastStep(id)
	}
	states := map[string]perm.State{
		"defaults":  {Powers: true, Confirmed: true},
		"lowered":   {Powers: true, Confirmed: true, Sessions: map[string]perm.Session{perm.Routine: {Level: 1, Console: "Drey"}}},
		"locked":    {Powers: true, Locked: true, Confirmed: true},
		"god":       {Powers: true, God: true, Confirmed: true, DenLevels: map[string]int{perm.Desktop: 2}, Kept: []string{perm.KeptClose, perm.KeptMerge}},
		"nopowers":  {Confirmed: true},
		"held":      {Powers: true, Held: true, DenLevels: map[string]int{perm.Destructive: 2}},
		"unconfirm": {Powers: true, DenLevels: map[string]int{perm.Destructive: 2}},
	}
	for _, w := range []int{140, 80} {
		for name, st := range states {
			f := &fakePanel{st: st, seq: 1}
			m := panelModel(t, f, w)
			m.perms.upgrade = false
			check(t, "permissions_"+name+"_"+itoa(w), ansi.Strip(strings.Join(m.permLines(w), "\n")))
		}
		f := &fakePanel{st: perm.State{Powers: true, Confirmed: true, DenLevels: map[string]int{perm.Outward: 2}}, seq: 1}
		m := panelModel(t, f, w)
		m = panelPress(m, "down", "down", "down", "down", "right")
		check(t, "permissions_raise_"+itoa(w), ansi.Strip(strings.Join(m.permLines(w), "\n")))
		u := panelModel(t, &fakePanel{st: perm.State{Powers: true, Confirmed: true, Kept: perm.KeptAll()}, seq: 1}, w)
		check(t, "permissions_upgrade_"+itoa(w), ansi.Strip(strings.Join(u.permLines(w), "\n")))
		for _, l := range strings.Split(ansi.Strip(strings.Join(m.permLines(w), "\n")), "\n") {
			if ansi.StringWidth(l) > w {
				t.Errorf("a line past %d columns: %q", w, l)
			}
		}
	}
}

func check(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *updateGolden {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v (run with -update-golden)", name, err)
	}
	if got != string(want) {
		t.Errorf("%s changed\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

// G1: after another program changed the stored levels, the panel says so,
// every bar is at 0, and c is refused: that hold ends only on the powers
// card.
func TestPanelHeldAfterATamper(t *testing.T) {
	f := &fakePanel{st: perm.State{Powers: true, Held: true, DenLevels: map[string]int{perm.Destructive: 2}}, seq: 1,
		lines: []string{"Ctrl+Y also confirms the levels you set above Den's defaults: Stop and close 2 of 5 (default 1). Until then they are at the defaults."}}
	m := panelModel(t, f, 140)
	out := ansi.Strip(strings.Join(m.permLines(140), "\n"))
	if !strings.Contains(out, permHeld) || strings.Contains(out, "█") || strings.Contains(out, "press c") {
		t.Errorf("the held panel:\n%s", out)
	}
	// c is refused while held, so neither the panel nor its keys offer it.
	if k := m.permFooter(f.GrantsRead()); strings.Contains(k, "c confirm") {
		t.Errorf("the held key line offers c: %q", k)
	}
	m = panelPress(m, "c")
	if m.perms.ask != nil || m.perms.note != permHeldC || f.st.Confirmed {
		t.Errorf("c while held: %+v %q", m.perms.ask, m.perms.note)
	}
	// A question asked before the hold is refused at ^Y, too.
	f.st.Held = false
	m = panelPress(m, "c")
	f.st.Held = true
	m = panelPress(m, "ctrl+y")
	if f.st.Confirmed || m.perms.note != permHeldC {
		t.Errorf("^Y after the hold: confirmed %v, %q", f.st.Confirmed, m.perms.note)
	}
}

// G2: on a short terminal the body scrolls to the selected row, and an
// open question or note is drawn above the footer, never cut while ^Y can
// answer it.
func TestPanelQuestionDrawnOnAShortTerminal(t *testing.T) {
	view := func(m Model) string {
		m.width, m.height = 80, 24
		return ansi.Strip(m.View().Content)
	}
	f := &fakePanel{st: perm.State{Powers: true, Confirmed: true, DenLevels: map[string]int{perm.Outward: 2}}, seq: 1}
	m := panelModel(t, f, 80)
	m = panelPress(m, "down", "down", "down", "down", "right")
	if v := view(m); !strings.Contains(v, "Raise Send out") {
		t.Errorf("the raise question at 80x24:\n%s", v)
	}
	u := &fakePanel{st: perm.State{Powers: true, DenLevels: map[string]int{perm.Destructive: 2}, Kept: perm.KeptAll()}, seq: 1,
		lines: []string{"Ctrl+Y also confirms the levels you set above Den's defaults: Stop and close 2 of 5 (default 1). Until then they are at the defaults."}}
	m = panelModel(t, u, 80)
	m = panelPress(m, "esc")
	mm, _ := m.permissionsCommand()
	m = panelPress(mm.(Model), "c")
	if v := view(m); !strings.Contains(v, "^Y to confirm, ^N not now") {
		t.Errorf("the confirm question at 80x24:\n%s", v)
	}
	m = panelPress(m, "ctrl+n")
	m.desk.missions = map[string]*deskMission{"d1": {}}
	m = panelPress(m, "c")
	// The note, in the rows above the footer (the state line at the top
	// says it too, and may be scrolled away).
	if v := strings.Split(view(m), "\n"); !strings.Contains(strings.Join(v[len(v)-4:], "\n"), "pause or end it to raise levels") {
		t.Errorf("the desk-held note at 80x24:\n%s", strings.Join(v, "\n"))
	}
	m.desk.missions = nil
	for i := 0; i < len(perm.Classes())+len(perm.KeptAll()); i++ {
		m = panelPress(m, "down")
	}
	m = panelPress(m, "enter")
	v := view(m)
	last := perm.KeptLabel(perm.KeptAll()[len(perm.KeptAll())-1])
	if !strings.Contains(v, "It cannot come back.") || !strings.Contains(v, "▎ "+last) {
		t.Errorf("the last kept row and its question at 80x24:\n%s", v)
	}
	if n := strings.Count(v, "\n") + 1; n > 24 {
		t.Errorf("%d lines at 24 rows", n)
	}
	// A terminal with no room for the panel at all draws without a panic.
	for _, rows := range []int{-3, 0, 1, 2} {
		m.permFit(80, rows)
	}
}
