package ui

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/desktop"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/permmode"
)

// holdModel is Den's window with a desktop mission live and unpaused, and
// a close card selected in the inbox.
func holdModel(t *testing.T) (Model, *deskMission, *inbox.Item) {
	t.Helper()
	m := testModel(140, 40)
	m.base = append(m.base, m.agents...)
	dm := &deskMission{}
	m.desk.missions = map[string]*deskMission{"d1": dm}
	it := m.store.AddClose(m.agents[0].Session.SessionID, "Close Lemur?", []string{"Lemur"}, nil)
	m.tab = tabInbox
	m.refresh()
	m.selectItem(it)
	m.shown = shownTarget{id: it.ID}
	return m, dm, it
}

func holdKey(m Model, k string) Model {
	var msg tea.KeyPressMsg
	switch k {
	case "enter":
		msg = tea.KeyPressMsg{Code: tea.KeyEnter}
	case "down":
		msg = tea.KeyPressMsg{Code: tea.KeyDown}
	case "ctrl+y":
		msg = tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl}
	case "ctrl+n":
		msg = tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl}
	case "ctrl+x":
		msg = tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl}
	default:
		panic("holdKey: " + k)
	}
	mm, _ := m.Update(msg)
	return mm.(Model)
}

// holdLine types text into the line and presses Enter, as the human does.
func holdLine(m Model, text string) Model {
	m = typeText(m, text)
	return holdKey(m, "enter")
}

// Every key handleKey has a case for is placed in the hold's lists, so a
// key added there later waits until someone decides it may run.
func TestDeskHoldKnowsEveryKey(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "app.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "handleKey" {
			return true
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			sw, ok := n.(*ast.SwitchStmt)
			if !ok {
				return true
			}
			call, ok := sw.Tag.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); !ok || sel.Sel.Name != "String" {
				return true
			}
			for _, s := range sw.Body.List {
				for _, e := range s.(*ast.CaseClause).List {
					if lit, ok := e.(*ast.BasicLit); ok {
						k, _ := strconv.Unquote(lit.Value)
						keys = append(keys, k)
					}
				}
			}
			return false
		})
		return false
	})
	if len(keys) < 20 {
		t.Fatalf("found only %d of handleKey's keys: %v", len(keys), keys)
	}
	for _, k := range keys {
		if !deskHoldMoves[k] && !deskHoldTypes[k] && !deskHoldRuns[k] && !deskHoldWaits[k] && k != "enter" {
			t.Errorf("handleKey's key %q is in none of the hold's lists", k)
		}
	}
}

// The commands that run while held, judged for every command Den has (the
// dock's list, every alias) with the arguments each may take: only the
// ones listed here run. A command added later waits.
func TestDeskHoldCommands(t *testing.T) {
	runs := map[string]bool{
		"/deny": true, "/pause": true, "/stop": true, "/quit": true, "/help": true, "/permissions": true,
		"/inbox": true, "/overview": true, "/review": true, "/map": true, "/orchestrator": true,
		"/desktop": true, "/desktop stop": true, "/desktop pause": true,
		"/god off": true,
		"/api":     true, "/api status": true, "/api list": true, "/api off": true, "/api disconnect": true, "/api unpair": true, "/api lock": true, "/api control off": true,
		"/usage": true, "/usage stop": true,
		"/mode": true, "/mode plan": true, "/mode default": true, "/mode dontask": true, "/mode acceptedits": true,
		"/fleet": true, "/fleet project": true, "/fleet projects": true, "/fleet flat": true,
	}
	args := [][]string{nil, {"stop"}, {"pause"}, {"resume"}, {"keep"}, {"on"}, {"off"}, {"unlock"}, {"status"}, {"list"},
		{"disconnect"}, {"unpair"}, {"lock"}, {"disallow", "1"}, {"control", "off"}, {"control", "on"}, {"all"},
		{"set", "50"}, {"clear"}, {"auto"}, {"plan"}, {"default"}, {"dontask"}, {"acceptedits"}, {"bypasspermissions"},
		{"new"}, {"restart"}, {"project"}, {"projects"}, {"flat"}, {"rm", "1"}, {"rules"}, {"retry", "h"}, {"move", "p"},
		{"save", "t"}, {"rescue"}, {"lemur"}, {"idle"}, {"x"}}
	m := testModel(140, 40)
	m.mode.global = permmode.Auto
	for _, v := range dockVerbs {
		for _, word := range append([]string{v.name}, v.aliases...) {
			for _, a := range args {
				line := strings.Join(append([]string{word}, a...), " ")
				words, ok := m.deskHeldLine(line)
				want := runs[strings.Join(append([]string{v.name}, a...), " ")]
				if ok != want {
					t.Errorf("%q while held: runs %v, want %v", line, ok, want)
				}
				if ok && words[0] != v.name {
					t.Errorf("%q runs as %v", line, words)
				}
			}
		}
	}
	// From plan, only a lowering of plan runs.
	m.mode.global = permmode.Plan
	for mode, want := range map[string]bool{"auto": false, "acceptedits": false, "plan": false, "default": true, "dontask": true} {
		if _, ok := m.deskHeldLine("/mode " + mode); ok != want {
			t.Errorf("/mode %s from plan while held: %v", mode, ok)
		}
	}
	// Any form but one plain command waits, and so does every line that
	// is not a command.
	for _, line := range []string{"", "  ", "hello", "@Lemur stop", "/deny; /allow", "/desktop stop && /allow", "/deny /allow", "/deny\u200b", "/nosuch", "/allow", "/y", "/always", "/answer yes"} {
		if _, ok := m.deskHeldLine(line); ok {
			t.Errorf("%q runs while held", line)
		}
	}
	// NFKC: a fullwidth form is the command it looks like.
	if words, ok := m.deskHeldLine("／ｄｅｎｙ"); !ok || words[0] != "/deny" {
		t.Errorf("fullwidth /deny: %v %v", words, ok)
	}
}

// ^Y, ^X and Enter on a card wait while an agent has the desktop; ^N
// denies; a pause lets ^Y answer, and resuming holds it again.
func TestDeskHoldCards(t *testing.T) {
	m, dm, it := holdModel(t)
	for _, k := range []string{"ctrl+y", "ctrl+x"} {
		m = holdKey(m, k)
		if it.State == inbox.Resolved || m.flash != deskHeldNote {
			t.Errorf("%s while held: state %v, %q", k, it.State, m.flash)
		}
	}
	m = holdLine(m, "/allow")
	if it.State == inbox.Resolved || m.flash != deskHeldNote || m.input.Value() != "/allow" {
		t.Errorf("/allow while held: state %v, %q, line %q", it.State, m.flash, m.input.Value())
	}
	m.input.SetValue("")
	m = holdLine(m, "/desktop pause")
	if !dm.humanPaused || m.deskHolds() {
		t.Fatalf("/desktop pause while held: paused %v, %q", dm.humanPaused, m.flash)
	}
	m = holdKey(m, "ctrl+y")
	if it.State != inbox.Resolved {
		t.Errorf("^Y after a pause: state %v, %q", it.State, m.flash)
	}
	typeLine(&m, "/desktop resume")
	if dm.humanPaused || !m.deskHolds() {
		t.Error("/desktop resume did not hold Den's window again")
	}
	other := m.store.AddClose(m.agents[0].Session.SessionID, "Close Otter?", []string{"Otter"}, nil)
	m.refresh()
	m.selectItem(other)
	m.shown = shownTarget{id: other.ID}
	m = holdKey(m, "ctrl+y")
	if other.State == inbox.Resolved {
		t.Error("^Y after resume answered")
	}
	m = holdKey(m, "ctrl+n")
	if other.State != inbox.Resolved {
		t.Errorf("^N while held: state %v, %q", other.State, m.flash)
	}
}

// Enter while held: a message, an empty line (focus a terminal) and a
// command that widens wait, with the line kept; a lowering runs.
func TestDeskHoldEnter(t *testing.T) {
	m, _, _ := holdModel(t)
	m.mode.global = permmode.Auto
	m.tab = tabFleet
	for _, line := range []string{"hello there", "/god on", "/mode auto", "/close Lemur", "/api on"} {
		m.input.SetValue("")
		m = holdLine(m, line)
		if m.flash != deskHeldNote || m.input.Value() != line {
			t.Errorf("%q while held: %q, line %q", line, m.flash, m.input.Value())
		}
	}
	m.input.SetValue("")
	m = holdKey(m, "enter")
	if m.flash != deskHeldNote {
		t.Errorf("an empty Enter while held: %q", m.flash)
	}
	m = holdLine(m, "/mode plan")
	if m.mode.global != permmode.Plan || m.input.Value() != "" {
		t.Errorf("/mode plan while held: mode %s, line %q, %q", m.mode.global, m.input.Value(), m.flash)
	}
}

// A line edited while held asks before it is sent once the hold is over;
// one typed before the hold and left alone does not.
func TestDeskHoldLineAsksAgain(t *testing.T) {
	// /unread runs on the selected item and says so: a line that ran.
	ran := func(m Model) bool {
		return strings.HasPrefix(m.flash, "Kept unread") || strings.HasPrefix(m.flash, "Marked read")
	}
	m, dm, _ := holdModel(t)
	m = typeText(m, "/unread")
	dm.humanPaused = true
	m = holdKey(m, "enter")
	if m.flash != deskHeldAsk || m.input.Value() != "/unread" {
		t.Fatalf("first Enter after the hold: %q, line %q", m.flash, m.input.Value())
	}
	m = holdKey(m, "enter")
	if !ran(m) {
		t.Errorf("second Enter: %q", m.flash)
	}

	m, dm, _ = holdModel(t)
	dm.Ended = "test"
	m = typeText(m, "/unread")
	dm.Ended = ""
	if !m.deskHolds() {
		t.Fatal("not held")
	}
	dm.humanPaused = true
	m = holdKey(m, "enter")
	if !ran(m) {
		t.Errorf("a line typed before the hold asked: %q", m.flash)
	}
}

// A selection moved while held holds the next answer once, ^Y or a typed
// /allow; a move the human makes after the hold settles it.
func TestDeskHoldMovedSelection(t *testing.T) {
	m, dm, it := holdModel(t)
	m = holdKey(m, "down")
	dm.humanPaused = true
	m.selectItem(it)
	m.shown = shownTarget{id: it.ID}
	m = holdKey(m, "ctrl+y")
	if it.State == inbox.Resolved || m.flash != deskHeldMoved {
		t.Fatalf("first ^Y after a moved hold: state %v, %q", it.State, m.flash)
	}
	m = holdKey(m, "ctrl+y")
	if it.State != inbox.Resolved {
		t.Errorf("second ^Y: state %v, %q", it.State, m.flash)
	}

	m, dm, it = holdModel(t)
	m = holdKey(m, "down")
	dm.humanPaused = true
	m.input.SetValue("")
	m = holdLine(m, "/allow")
	if it.State == inbox.Resolved || m.flash != deskHeldMoved {
		t.Errorf("/allow after a moved hold: state %v, %q", it.State, m.flash)
	}

	// A deny while held stands, but moves the selection on to the next
	// card: the first ^Y after the hold is held once.
	for _, deny := range []string{"ctrl+n", "/deny"} {
		m, dm, it = holdModel(t)
		next := m.store.AddClose(m.agents[0].Session.SessionID, "Close Otter?", []string{"Otter"}, nil)
		m.refresh()
		m.selectItem(it)
		if deny == "ctrl+n" {
			m = holdKey(m, "ctrl+n")
		} else {
			m = holdLine(m, deny)
		}
		if it.State != inbox.Resolved {
			t.Fatalf("%s while held did not deny: %q", deny, m.flash)
		}
		dm.humanPaused = true
		m.selectItem(next)
		m.shown = shownTarget{id: next.ID}
		m = holdKey(m, "ctrl+y")
		if next.State == inbox.Resolved || m.flash != deskHeldMoved {
			t.Errorf("first ^Y after %s while held: state %v, %q", deny, next.State, m.flash)
		}
	}

	// A view command typed while held is a move too (sec's S1-1); the
	// pause itself is not (TestDeskHoldCards: ^Y right after it answers).
	for _, view := range []string{"/inbox", "/overview", "/fleet flat", "/orchestrator", "/map"} {
		m, dm, it = holdModel(t)
		m = holdLine(m, view)
		dm.humanPaused = true
		m.tab = tabInbox
		m.selectItem(it)
		m.shown = shownTarget{id: it.ID}
		m = holdKey(m, "ctrl+y")
		if it.State == inbox.Resolved || m.flash != deskHeldMoved {
			t.Errorf("first ^Y after %s while held: state %v, %q", view, it.State, m.flash)
		}
	}

	m, dm, it = holdModel(t)
	m = holdKey(m, "down")
	dm.humanPaused = true
	m = holdKey(m, "down") // the human's own move, after the hold
	m.selectItem(it)
	m.shown = shownTarget{id: it.ID}
	m = holdKey(m, "ctrl+y")
	if it.State != inbox.Resolved {
		t.Errorf("^Y after the human moved it again: state %v, %q", it.State, m.flash)
	}
}

// /desktop resume ends only the human's own pause: a your-turn pause ends
// on its card's answer, an overlay pause when the overlay is back.
func TestDeskResumeEndsOnlyItsOwnPause(t *testing.T) {
	m, dm, _ := holdModel(t)
	for _, other := range []func(on bool){
		func(on bool) { dm.Paused = on },
		func(on bool) { dm.overlayPaused = on },
	} {
		other(true)
		typeLine(&m, "/desktop pause")
		typeLine(&m, "/desktop resume")
		if dm.humanPaused || m.deskHolds() {
			t.Errorf("resume ended another pause: paused %v, overlay %v, held %v", dm.Paused, dm.overlayPaused, m.deskHolds())
		}
		typeLine(&m, "/desktop resume")
		if m.deskHolds() {
			t.Error("a second resume ended another pause")
		}
		other(false)
		if !m.deskHolds() {
			t.Error("not held once the other pause ended")
		}
	}
}

// A pause is not a hold: a line typed during it is not marked. A line
// typed while held stays marked through a pause and a resume.
func TestDeskPauseIsNotAHold(t *testing.T) {
	m, dm, _ := holdModel(t)
	dm.humanPaused = true
	m = typeText(m, "/unread")
	if m.hold.edited {
		t.Error("a line typed during a pause is marked")
	}
	m.input.SetValue("")
	dm.humanPaused = false
	m = typeText(m, "/unread") // typed while held: marked
	dm.humanPaused = true      // a pause,
	m = holdKey(m, "down")
	dm.humanPaused = false // a resume,
	m = holdKey(m, "down")
	dm.humanPaused = true // and a pause again, to send it
	if !m.hold.edited || m.input.Value() != "/unread" {
		t.Fatalf("the mark did not last through a pause and resume: %v %q", m.hold.edited, m.input.Value())
	}
	m = holdKey(m, "enter")
	if m.flash != deskHeldAsk {
		t.Errorf("a line typed while held, after a pause and resume: %q", m.flash)
	}
}

// A card approved during a pause doesn't run its action then: it runs
// once the mission goes on, decided again on a fresh read, exactly once.
func TestDeskApprovedDuringAPauseRunsOnResume(t *testing.T) {
	m := deskRig(t)
	deskStarted(t, &m)
	r := deskReq(600, bridge.DesktopInput{Kind: "call", Tool: "keyboard", Args: json.RawMessage(`{"action":"type","text":"hello\nthere","window":"0xb"}`)})
	deskDo(&m, r)
	it := desktopCard(m, inbox.DesktopAct)
	if it == nil {
		t.Fatal("no card")
	}
	dm := m.deskLive()
	dm.humanPaused = true
	m = answerDeskCard(t, m, it, "allow")
	if _, done := r.Decided(); done {
		t.Fatal("an action approved during a pause ran during it")
	}
	if it.State != inbox.Resolved || len(dm.queued) != 1 {
		t.Fatalf("the card after ^Y in a pause: state %v, %d queued", it.State, len(dm.queued))
	}
	if cmd := m.deskFlush(); cmd != nil {
		t.Error("a flush started while still paused")
	}
	dm.humanPaused = false
	for _, msg := range runBatch(m.deskFlush()) {
		if f, ok := msg.(deskFlushMsg); ok {
			m.deskFlushed(f)
		}
	}
	d, done := r.Decided()
	var a desktop.Answer
	json.Unmarshal(d.Data, &a)
	if !done || d.Behavior != "allow" || a.Token == "" || len(dm.queued) != 0 {
		t.Fatalf("after resume: %+v, %d queued", d, len(dm.queued))
	}
	if cmd := m.deskFlush(); cmd != nil {
		t.Error("a second flush for an action that already ran")
	}

	// Its window gone by the resume: refused, never run.
	r = deskReq(600, bridge.DesktopInput{Kind: "call", Tool: "keyboard", Args: json.RawMessage(`{"action":"type","text":"hello\nthere","window":"0xb"}`)})
	deskDo(&m, r)
	dm.humanPaused = true
	m = answerDeskCard(t, m, desktopCard(m, inbox.DesktopAct), "allow")
	dm.humanPaused = false
	for _, msg := range runBatch(m.deskFlush()) {
		if f, ok := msg.(deskFlushMsg); ok {
			var kept []desktop.Window
			for _, w := range f.snap.Windows {
				if w.Address != "0xb" {
					kept = append(kept, w)
				}
			}
			f.snap.Windows = kept
			m.deskFlushed(f)
		}
	}
	if d, done := r.Decided(); !done || d.Behavior != "deny" {
		t.Errorf("a queued action whose window went: %+v", d)
	}

	// The mission ended while paused: refused.
	r = deskReq(600, bridge.DesktopInput{Kind: "call", Tool: "keyboard", Args: json.RawMessage(`{"action":"type","text":"hello\nthere","window":"0xb"}`)})
	deskDo(&m, r)
	dm.humanPaused = true
	m = answerDeskCard(t, m, desktopCard(m, inbox.DesktopAct), "allow")
	dm.Ended = "test"
	m.deskFlush()
	if d, done := r.Decided(); !done || d.Behavior != "deny" {
		t.Errorf("a queued action of an ended mission: %+v", d)
	}
}

// /desktop pause makes every call of the agent but a look wait; resume
// lets them through again.
func TestDeskPauseWaitsCalls(t *testing.T) {
	m, _, _ := taskMission(t)
	m.deskMission("m1").limitActions = 100
	typeLine(m, "/desktop pause")
	if !m.deskMission("m1").humanPaused {
		t.Fatalf("/desktop pause: %q", m.flash)
	}
	for _, c := range []struct{ tool, args string }{
		{"pointer", `{"action":"click","x":1200,"y":700}`},
		{"keyboard", `{"action":"type","text":"hi","window":"0xa"}`},
		{"launch", `{"command":"foot"}`},
	} {
		if dv, _ := deskCall(m, c.tool, c.args); dv.Behavior != "deny" || !strings.Contains(dv.Message, deskHumanPaused) {
			t.Errorf("%s while paused: %+v", c.tool, dv)
		}
	}
	if dv, _ := deskCall(m, "screenshot", `{}`); dv.Behavior != "allow" {
		t.Errorf("a screenshot while paused: %+v", dv)
	}
	typeLine(m, "/desktop resume")
	if dv, _ := deskCall(m, "pointer", `{"action":"click","x":1200,"y":700}`); dv.Behavior == "deny" && strings.Contains(dv.Message, deskHumanPaused) {
		t.Errorf("a click after resume: %+v", dv)
	}
	// The dock may pause, never resume.
	if words, _, why := dockLine("/desktop pause"); words == nil {
		t.Errorf("/desktop pause from the dock: %q", why)
	}
	if words, _, why := dockLine("/desktop resume"); words != nil || why == "" {
		t.Errorf("/desktop resume from the dock: %v", words)
	}
}
