package ui

import (
	"testing"

	"github.com/GlacierHubAB/den/internal/api"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/permmode"
)

// dockHeldModel is a model with a console's card waiting and a live
// desktop mission holding Den's window.
func dockHeldModel(t *testing.T) (Model, *deskMission) {
	t.Helper()
	m := testModel(140, 40)
	m.base = append(m.base, m.agents...)
	dm := &deskMission{}
	m.desk.missions = map[string]*deskMission{"d1": dm}
	if !m.deskHolds() {
		t.Fatal("the mission doesn't hold")
	}
	return m, dm
}

// dockAct sends a console's card answer through applyAPIAct, the one gate.
func dockAct(m *Model, it *inbox.Item, verb string) api.CardResult {
	a := &api.CardAnswer{ID: it.ID, Hash: api.ProjectCard(m.dockCard(it)).Hash, Verb: verb, Console: "app-host", Reply: make(chan api.CardResult, 1)}
	m.applyAPIAct(api.Act{CardAnswer: a})
	return <-a.Reply
}

// While an agent has the desktop, a console's allow and always wait as
// Den's ^Y does, and its deny is taken as ^N is. Once the mission is
// paused and frozen, or has ended with its slice gone, the same answers
// are taken again.
func TestDockHeldCardAnswers(t *testing.T) {
	m, dm := dockHeldModel(t)
	for _, verb := range []string{"allow", "always"} {
		it := permissionCard(t, &m)
		if r := dockAct(&m, it, verb); r.Code != api.CodeUnavailable || r.Note != deskHeldDock || it.State == inbox.Resolved {
			t.Fatalf("%s while held: %+v, state %v", verb, r, it.State)
		}
		if r := dockAct(&m, it, "deny"); r.Code != "" || it.State != inbox.Resolved {
			t.Fatalf("deny while held: %+v, state %v", r, it.State)
		}
	}
	pauseNow(dm)
	if m.deskHolds() {
		t.Fatal("still held after a frozen pause")
	}
	it := permissionCard(t, &m)
	if r := dockAct(&m, it, "allow"); r.Code != "" || r.Result != "allowed" {
		t.Fatalf("allow after a frozen pause: %+v", r)
	}
	dm.humanPaused, dm.frozen = false, false
	dm.Ended = "stopped"
	it = permissionCard(t, &m)
	if r := dockAct(&m, it, "allow"); r.Code != "" || r.Result != "allowed" {
		t.Fatalf("allow after the end: %+v", r)
	}
	// An ended mission whose slice isn't gone yet still holds.
	dm.launched = true
	it = permissionCard(t, &m)
	if r := dockAct(&m, it, "allow"); r.Code != api.CodeUnavailable {
		t.Fatalf("allow with the ended mission's slice still there: %+v", r)
	}
}

// The gate is the same for every kind a console may answer: only deny
// passes it while held, as only ^N does in Den's window.
func TestDockHeldVerbsPerKind(t *testing.T) {
	m, _ := dockHeldModel(t)
	if len(deskHeldVerbs) != 1 || !deskHeldVerbs["deny"] || !deskHoldRuns["ctrl+n"] || !deskHoldWaits["ctrl+y"] {
		t.Fatalf("the held answers differ from Den's window's: %v", deskHeldVerbs)
	}
	for k := range todayDock {
		for _, verb := range []string{"allow", "always", "deny"} {
			a := &api.CardAnswer{ID: "x", Verb: verb, Reply: make(chan api.CardResult, 1)}
			refused := m.deskHeldAct(api.Act{CardAnswer: a})
			if refused != (verb != "deny") {
				t.Errorf("%v %s while held: refused %v", k, verb, refused)
			}
			if refused {
				if r := <-a.Reply; r.Code != api.CodeUnavailable || r.Note != deskHeldDock {
					t.Errorf("%v %s: %+v", k, verb, r)
				}
			}
		}
	}
}

// While held, a console's mode.set takes only a lowering.
func TestDockHeldModeSet(t *testing.T) {
	m, dm := dockHeldModel(t)
	m.mode.global = permmode.AcceptEdits
	for _, c := range []struct {
		to    string
		taken bool
	}{{permmode.Default, true}, {permmode.DontAsk, true}, {permmode.AcceptEdits, false}, {permmode.Plan, false}, {permmode.Auto, false}, {"bypassPermissions", false}, {"nonsense", false}} {
		r := &api.ModeSet{Mode: c.to, Console: "app-host", Reply: make(chan api.ModeSetResult, 1)}
		refused := m.deskHeldAct(api.Act{ModeSet: r})
		if refused == c.taken {
			t.Errorf("mode.set %s while held: refused %v", c.to, refused)
		}
		if refused {
			if res := <-r.Reply; res.Code != api.CodeUnavailable || res.Note != deskHeldDock {
				t.Errorf("mode.set %s: %+v", c.to, res)
			}
		}
	}
	// End to end: a raise through applyAPIAct asks for no card while held,
	// and is a card again once paused.
	r := &api.ModeSet{Mode: permmode.Auto, Console: "app-host", Reply: make(chan api.ModeSetResult, 1)}
	m.applyAPIAct(api.Act{ModeSet: r})
	if res := <-r.Reply; res.Code != api.CodeUnavailable || m.mode.askedID != "" {
		t.Fatalf("a raise while held: %+v, card %q", res, m.mode.askedID)
	}
	pauseNow(dm)
	r = &api.ModeSet{Mode: permmode.Auto, Console: "app-host", Reply: make(chan api.ModeSetResult, 1)}
	m.applyAPIAct(api.Act{ModeSet: r})
	if res := <-r.Reply; res.Code != "" || res.Card == "" {
		t.Fatalf("a raise after a frozen pause: %+v", res)
	}
}

// Every command line the dock accepts, held: it runs only if Den's own
// window would run the same line then (the dock's held set is a subset of
// the window's), and anything else waits with the dock's note. Lines the
// dock refuses anyway stay refused by the dock's own rules.
func TestDockHeldCommands(t *testing.T) {
	m, _ := dockHeldModel(t)
	m.mode.global = permmode.Auto
	args := []string{"", "stop", "pause", "resume", "keep", "off", "on", "status", "list", "lock", "unpair", "disconnect", "control off",
		"project", "flat", "default", "plan", "dontAsk", "acceptEdits", "auto", "set 50", "clear", "all", "x"}
	taken, waited := 0, 0
	var waits string
	for _, v := range dockVerbs {
		for _, a := range args {
			line := v.name
			if a != "" {
				line += " " + a
			}
			words, _, why := dockLine(line)
			r := &api.CommandRun{Text: line, Console: "app-host", Reply: make(chan api.CommandResult, 1)}
			refused := m.deskHeldAct(api.Act{Command: r})
			switch {
			case why != "":
				if refused {
					t.Errorf("%q: the dock refuses it, yet the hold answered it", line)
				}
			case refused:
				waited++
				if waits == "" {
					waits = line
				}
				if res := <-r.Reply; res.Code != api.CodeUnavailable || res.Note != deskHeldDock {
					t.Errorf("%q while held: %+v", line, res)
				}
				if deskHeldDockLine(words, m.mode.global) {
					t.Errorf("%q waits, but the window's list has it", line)
				}
			default:
				taken++
				if _, ok := m.deskHeldLine(line); !ok {
					t.Errorf("%q runs from the dock while held, but not in Den's window", line)
				}
			}
		}
	}
	if taken == 0 || waited == 0 {
		t.Fatalf("taken %d, waited %d: the table proves nothing", taken, waited)
	}
	// End to end through applyAPIAct: a line that waits doesn't run.
	before := len(m.orch.chat)
	r := &api.CommandRun{Text: waits, Console: "app-host", Reply: make(chan api.CommandResult, 1)}
	m.applyAPIAct(api.Act{Command: r})
	if res := <-r.Reply; res.Code != api.CodeUnavailable || len(m.orch.chat) != before {
		t.Fatalf("%q through the gate while held: %+v, chat %d→%d", waits, res, before, len(m.orch.chat))
	}
	// /desktop from the dock stays pause and stop only, held or not.
	if _, _, why := dockLine("/desktop resume"); why == "" {
		t.Error("/desktop resume from the dock")
	}
}

// While held, a turn for the orchestrator (a console's or an app's chat)
// waits, as a message typed in Den's window does; once paused and frozen
// it is taken again.
func TestDockHeldSay(t *testing.T) {
	m, dm := dockHeldModel(t)
	old := deskSayFail
	t.Cleanup(func() { deskSayFail = old })
	var failed []string
	deskSayFail = func(y *api.Say, why string) { failed = append(failed, y.Turn+": "+why) }
	y := &api.Say{Turn: "t1", Text: "go on", Console: "app-host"}
	m.applyAPIAct(api.Act{Say: y})
	if m.ctl.turn != nil || len(failed) != 1 || failed[0] != "t1: "+deskHeldDock {
		t.Fatalf("a turn while held: started %v, failed %v", m.ctl.turn != nil, failed)
	}
	pauseNow(dm)
	// applyAPIAct asks deskHeldAct only while deskHolds().
	if m.deskHolds() {
		t.Fatal("a turn after a frozen pause is still held")
	}
}
