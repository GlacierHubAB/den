package ui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/api"
	perm "github.com/GlacierHubAB/den/internal/grants"
)

// /permissions: what Drey may do, set in Den's own window only. A row per
// class, its bar of five segments, the kept items, and what only Den does.
// Lowering applies at once; a raise, removing a kept item and the start
// confirm each ask, and only the question's ^Y applies them. A question
// holds the read's seq: any change under it drops it. While a desktop
// agent has the desktop, nothing here raises or confirms.

// panelLevels is what the panel asks of den-api (api.Server).
type panelLevels interface {
	GrantsRead() perm.Grants
	SetDenLevel(class string, level int) error
	RaiseDenLevel(class string, level int, seq uint64) error
	ConfirmLevels(seq uint64) error
	RemoveKept(id string) error
}

// permState is the panel as the human has it.
type permState struct {
	open bool
	sel  int  // the row: classes, then kept items
	seen bool // the intro was shown whole once; it folds to one line
	// upgrade: the one-press screen for kept items, shown at the first
	// /permissions while kept has entries.
	upgrade  bool
	upgraded bool
	ask      *permAsk
	note     string // why a question was dropped or refused, on the panel
	// levels stands in for den-api in a test with no den-api.
	levels panelLevels
}

// permAsk is a question the panel holds until ^Y or ^N.
type permAsk struct {
	kind  string // "raise", "remove", "confirm"
	class string
	level int
	kept  string
	seq   uint64
}

// The panel's fixed words.
const (
	permTitle      = "What Drey may do"
	permIntro      = "Drey is the dock on your desktop. Each bar says how much Drey may do on its own for one kind of action; everything else it can only show you. Less is to the left. Drey can lower a bar itself, never raise one. Saying no, pausing and stopping are always allowed from Drey."
	permIntroShort = "Less is to the left · Drey can lower a bar itself, never raise one · deny, pause and stop are always allowed"
	permFixed1     = "Saying no, pausing and stopping are always allowed from Drey."
	permFixed2     = "Raising permissions, \"always allow\" and pairing are done in Den."
	permDeskHeld   = "An agent has the desktop: pause or end it to raise levels."
	permMoved      = "The levels changed while you were asked: nothing was raised. Ask again."
)

func (m Model) panel() panelLevels {
	if m.api.srv != nil {
		return m.api.srv
	}
	return m.perms.levels
}

// deskHolds reports whether a desktop agent can act now: a mission live
// and not paused (a your-turn card open, or the dock's overlay down).
func (m Model) deskHolds() bool {
	for _, dm := range m.desk.missions {
		if dm.Ended == "" && !dm.Paused && !dm.overlayPaused {
			return true
		}
	}
	return false
}

// permissionsCommand opens the panel.
func (m Model) permissionsCommand() (tea.Model, tea.Cmd) {
	p := m.panel()
	if p == nil {
		return m.say("The API is off: there is nothing to set yet. /api on starts it.", true)
	}
	g := p.GrantsRead()
	m.perms.open, m.perms.ask, m.perms.note = true, nil, ""
	m.perms.upgrade = len(g.Kept) > 0 && !m.perms.upgraded
	if m.perms.sel >= len(g.Classes)+len(g.Kept) {
		m.perms.sel = 0
	}
	return m, nil
}

// permKey is a key while the panel is open: it takes every key, so a
// card's ^Y never reaches a card meanwhile and the panel's question never
// takes a card's.
func (m Model) permKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := m.panel()
	if p == nil {
		m.perms.open = false
		return m, nil
	}
	g := p.GrantsRead()
	key := msg.String()
	if a := m.perms.ask; a != nil {
		switch key {
		case "ctrl+y":
			m.perms.ask = nil
			m.perms.note = m.permApply(p, g, *a)
		case "ctrl+n", "esc":
			m.perms.ask, m.perms.note = nil, ""
		default:
			// Any other key keeps everything as it is.
			m.perms.ask, m.perms.note = nil, ""
		}
		return m, nil
	}
	if m.perms.upgrade {
		switch key {
		case "enter":
			p.RemoveKept("*")
			m.perms.upgrade, m.perms.upgraded = false, true
			m.perms.note = "The kept items are removed. Nothing was raised."
		case "esc":
			m.perms.upgrade, m.perms.upgraded = false, true
		}
		return m, nil
	}
	rows := len(g.Classes) + len(g.Kept)
	switch key {
	case "esc":
		m.perms.open, m.perms.seen, m.perms.note = false, true, ""
	case "up", "k":
		if m.perms.sel > 0 {
			m.perms.sel--
		}
		m.perms.note = ""
	case "down", "j":
		if m.perms.sel < rows-1 {
			m.perms.sel++
		}
		m.perms.note = ""
	case "left", "right", "r":
		if m.perms.sel >= len(g.Classes) {
			return m, nil
		}
		c := g.Classes[m.perms.sel]
		to := c.DenLevel
		switch key {
		case "left":
			to--
		case "right":
			to++
		case "r":
			to = c.Default
		}
		if to < 0 || to > perm.LastStep(c.Class) || to == c.DenLevel && key != "r" {
			return m, nil
		}
		m.perms.note = m.permSet(p, g, c, to)
	case "c":
		if g.StartConfirmed || !permUnconfirmed(g) {
			return m, nil
		}
		if m.deskHolds() {
			m.perms.note = permDeskHeld
			return m, nil
		}
		m.perms.ask = &permAsk{kind: "confirm", seq: g.Seq}
	case "enter":
		i := m.perms.sel - len(g.Classes)
		if i < 0 || i >= len(g.Kept) {
			return m, nil
		}
		m.perms.ask = &permAsk{kind: "remove", kept: g.Kept[i].ID, seq: g.Seq}
	}
	return m, nil
}

// permSet sets class c to level to: at once when it does not raise c's
// cap (SetDenLevel refuses a raise), else on a question, never while a
// desktop agent can act.
func (m *Model) permSet(p panelLevels, g perm.Grants, c perm.Class, to int) string {
	err := p.SetDenLevel(c.Class, to)
	if err == nil {
		return ""
	}
	if err != api.ErrRaise {
		return err.Error()
	}
	if m.deskHolds() {
		return permDeskHeld
	}
	m.perms.ask = &permAsk{kind: "raise", class: c.Class, level: to, seq: g.Seq}
	return ""
}

// permApply carries out a question's ^Y, only on the read it was asked
// on, and says why when it cannot.
func (m Model) permApply(p panelLevels, g perm.Grants, a permAsk) string {
	if (a.kind == "raise" || a.kind == "confirm") && m.deskHolds() {
		return permDeskHeld
	}
	var err error
	switch a.kind {
	case "raise":
		err = p.RaiseDenLevel(a.class, a.level, a.seq)
	case "confirm":
		err = p.ConfirmLevels(a.seq)
	case "remove":
		err = p.RemoveKept(a.kept)
	}
	switch {
	case err == api.ErrMoved:
		return permMoved
	case err != nil:
		return err.Error()
	}
	return ""
}

// permUnconfirmed reports whether g has levels above the defaults or kept
// items waiting for the start confirm.
func permUnconfirmed(g perm.Grants) bool {
	if len(g.Kept) > 0 {
		return true
	}
	for _, c := range g.Classes {
		if c.DenLevel > c.Default {
			return true
		}
	}
	return false
}

// permQuestion is an open question's words on the panel.
func permQuestion(g perm.Grants, a permAsk) string {
	switch a.kind {
	case "raise":
		for _, c := range g.Classes {
			if c.Class != a.class {
				continue
			}
			name := "Only in Den"
			if a.level > 0 && a.level <= len(c.Steps) {
				name = c.Steps[a.level-1].Name
			}
			more := ""
			if a.level > c.Default {
				more = ", more than recommended"
			}
			return "Raise " + c.Label + " to " + strconv.Itoa(a.level) + " of 5 (" + name + ")" + more + "? ^Y to raise, ^N to keep"
		}
	case "remove":
		return "Remove \"" + perm.KeptLabel(a.kept) + "\" from Drey? It cannot come back. ^Y to remove, ^N to keep"
	case "confirm":
		var raised, kept []string
		for _, c := range g.Classes {
			if c.DenLevel > c.Default {
				raised = append(raised, c.Label+" "+strconv.Itoa(c.DenLevel)+" of 5")
			}
		}
		for _, k := range g.Kept {
			kept = append(kept, k.Label)
		}
		what := strings.Join(append(raised, kept...), ", ")
		return "Confirm your levels: " + what + "? ^Y to confirm, ^N not now"
	}
	return ""
}

// permBar is a class's five segments: on to its level, a ghost up to the
// asked or Den level, off where it has a step, a dot where it has none.
func (m Model) permBar(c perm.Class, level, ghost int) string {
	s := m.st
	var b strings.Builder
	for i := 1; i <= perm.Steps; i++ {
		switch {
		case i <= level:
			b.WriteString(s.bright.Render("█"))
		case i <= ghost:
			b.WriteString(s.dim.Render("▒"))
		case i <= len(c.Steps) && c.Steps[i-1].Available:
			b.WriteString(s.muted.Render("░"))
		default:
			b.WriteString(s.muted.Render("·"))
		}
		if i < perm.Steps {
			b.WriteString(" ")
		}
	}
	return b.String()
}

// stepName is a level's short name in class c.
func stepName(c perm.Class, level int) string {
	if level <= 0 || level > len(c.Steps) {
		return "Only in Den"
	}
	return c.Steps[level-1].Name
}

// stepMeaning is a level's sentence in class c.
func stepMeaning(c perm.Class, level int) string {
	if level <= 0 || level > len(c.Steps) {
		return "Drey shows it; you act in Den's window."
	}
	return c.Steps[level-1].Label
}

// godHeld are the classes God mode holds at 0 below their cap.
func godHeld(g perm.Grants) []string {
	var out []string
	for _, c := range g.Classes {
		cp := c.DenLevel
		if c.Session != nil && c.Session.Level < cp {
			cp = c.Session.Level
		}
		if g.God && g.Powers && !g.Locked && c.Level < cp {
			out = append(out, c.Label)
		}
	}
	return out
}

// listAnd is "A", "A and B", "A, B and C".
func listAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

// permLines is the panel, wrapped at w, never cut.
func (m Model) permLines(w int) []string {
	s := m.st
	p := m.panel()
	if p == nil {
		return nil
	}
	g := p.GrantsRead()
	in := w - 4
	if in < 20 {
		in = 20
	}
	var out []string
	add := func(st func(...string) string, indent, text string) {
		for _, l := range wrapLines(text, in-len(indent)) {
			out = append(out, indent+st(l))
		}
	}
	title := s.bright.Render(permTitle)
	back := s.muted.Render("Esc back")
	pad := w - len([]rune(permTitle)) - len("Esc back") - 2
	if pad < 1 {
		pad = 1
	}
	out = append(out, " "+title+strings.Repeat(" ", pad)+back, "")
	if m.perms.upgrade {
		add(s.fg.Render, "  ", permIntro)
		out = append(out, "", "  "+s.muted.Render("KEPT FROM BEFORE"))
		for _, k := range g.Kept {
			add(s.fg.Render, "    ", k.Label)
		}
		out = append(out, "")
		add(s.fg.Render, "  ", "Enter  Use the recommended settings (removes the kept items; raises nothing)   Esc  keep them for now")
		return out
	}
	if m.perms.seen {
		add(s.dim.Render, "  ", permIntroShort)
	} else {
		add(s.dim.Render, "  ", permIntro)
	}
	out = append(out, "")
	// The state lines.
	switch {
	case g.Locked:
		add(s.attn.Render, "  ", "Locked: every level is 0 until you unlock (^Y on the powers card)")
	case !g.Powers:
		add(s.muted.Render, "  ", "No console has its powers: Drey is at 0 in every class until you press ^Y on its powers card. Below is what it gets then.")
	}
	if g.God {
		held, paused := godHeld(g), false
		for _, k := range g.Kept {
			paused = paused || perm.KeptGodTTY(k.ID)
		}
		switch {
		case len(held) == 1 && paused:
			add(s.attn.Render, "  ", "God mode: "+held[0]+" is at 0 and kept items are paused while it is on")
		case len(held) == 1:
			add(s.attn.Render, "  ", "God mode: "+held[0]+" is at 0 while it is on")
		case len(held) > 1 && paused:
			add(s.attn.Render, "  ", "God mode: "+listAnd(held)+" are at 0 and kept items are paused while it is on")
		case len(held) > 1:
			add(s.attn.Render, "  ", "God mode: "+listAnd(held)+" are at 0 while it is on")
		case paused:
			add(s.attn.Render, "  ", "God mode: kept items are paused while it is on")
		}
	}
	if !g.StartConfirmed && permUnconfirmed(g) {
		add(s.attn.Render, "  ", "Your levels above the defaults and kept items wait for your Ctrl+Y on the powers card, or press c here to confirm them now.")
	}
	if m.deskHolds() {
		add(s.attn.Render, "  ", permDeskHeld)
	}
	out = append(out, "")
	for i, c := range g.Classes {
		sel := i == m.perms.sel
		mark := "  "
		if sel {
			mark = s.attn.Render("▎") + " "
		}
		level, ghost := c.DenLevel, c.DenLevel
		if c.Session != nil && c.Session.Level < level {
			level = c.Session.Level
		}
		if !g.StartConfirmed && level > c.Default {
			level = c.Default // above the default waits for the confirm
		}
		if g.Locked || g.God && len(godHeld(g)) > 0 && c.Level < level {
			ghost, level = level, 0
		}
		if a := m.perms.ask; a != nil && a.kind == "raise" && a.class == c.Class {
			ghost = a.level
		}
		label := c.Label + strings.Repeat(" ", max(1, 22-len([]rune(c.Label))))
		out = append(out, mark+s.bright.Render(label)+"  "+m.permBar(c, level, ghost)+"   "+s.fg.Render(stepName(c, level)))
		second := stepMeaning(c, level)
		if c.Session != nil && c.Session.Level < c.DenLevel {
			second = "Lowered from " + c.Session.Console + " for this session · back to " + strconv.Itoa(c.DenLevel) + " of 5 when Den restarts"
		}
		if a := m.perms.ask; sel && a != nil && a.kind == "raise" {
			add(s.attn.Render, "    ", permQuestion(g, *a))
		} else {
			add(s.dim.Render, "    ", second)
		}
		if c.DenLevel != c.Default && level != c.Default {
			add(s.dim.Render, "    ", "▴ recommended: "+stepName(c, c.Default))
		}
		out = append(out, "")
	}
	add(s.fg.Render, "  ", permFixed1)
	add(s.fg.Render, "  ", permFixed2)
	if len(g.Kept) > 0 {
		out = append(out, "", "  "+s.muted.Render("KEPT FROM BEFORE"))
		for i, k := range g.Kept {
			row := len(g.Classes) + i
			mark := "  "
			if row == m.perms.sel {
				mark = s.attn.Render("▎") + " "
			}
			line := "  " + mark + s.fg.Render(k.Label)
			if !k.Active {
				line += "  " + s.muted.Render("paused")
			}
			out = append(out, line)
		}
	}
	out = append(out, "", "  "+s.muted.Render("DEN ONLY"))
	for _, d := range g.DenOnly {
		add(s.dim.Render, "    ", d.Label)
	}
	if a := m.perms.ask; a != nil && a.kind != "raise" {
		out = append(out, "")
		add(s.attn.Render, "  ", permQuestion(g, *a))
	}
	if m.perms.note != "" {
		out = append(out, "")
		add(s.attn.Render, "  ", m.perms.note)
	}
	return out
}

// permFooter is the panel's key line.
func (m Model) permFooter(g perm.Grants) string {
	keys := "↑ ↓ select   ← → change   r recommended"
	if len(g.Kept) > 0 {
		keys += "   Enter remove (kept)"
	}
	if !g.StartConfirmed && permUnconfirmed(g) {
		keys += "   c confirm"
	}
	return keys + "   Esc back"
}
