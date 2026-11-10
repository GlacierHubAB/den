package ui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/api"
)

// actWith is an api.Act with only the named field set, to a value that is
// not the zero one.
func actWith(t *testing.T, name string) api.Act {
	t.Helper()
	var act api.Act
	f := reflect.ValueOf(&act).Elem().FieldByName(name)
	switch f.Kind() {
	case reflect.Ptr:
		f.Set(reflect.New(f.Type().Elem()))
	case reflect.Bool:
		f.SetBool(true)
	case reflect.String:
		f.SetString("x")
	case reflect.Slice:
		f.Set(reflect.MakeSlice(f.Type(), 1, 1))
	default:
		t.Fatalf("api.Act.%s is a %s: teach actWith to set one", name, f.Kind())
	}
	return act
}

// Every kind of act an app or a console can hand Den is placed: it runs
// while an agent has the desktop (and says why it cannot approve), or
// deskHeldAct decides it case by case. A field added to api.Act fails here
// until someone places it, so a new verb never runs by default.
func TestDeskActFieldsArePlaced(t *testing.T) {
	typ := reflect.TypeOf(api.Act{})
	fields := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		fields[name] = true
		_, runs := deskActRuns[name]
		held := deskActHeld[name]
		switch {
		case runs && held:
			t.Errorf("api.Act.%s is in both deskActRuns and deskActHeld", name)
		case !runs && !held:
			t.Errorf("api.Act.%s is in neither deskActRuns nor deskActHeld: it would wait while an agent has the desktop, and that is not decided", name)
		}
	}
	for name, why := range deskActRuns {
		if !fields[name] {
			t.Errorf("deskActRuns names %s, which api.Act does not have", name)
		}
		if strings.TrimSpace(why) == "" {
			t.Errorf("deskActRuns.%s says nothing of why it cannot approve", name)
		}
	}
	for name := range deskActHeld {
		if !fields[name] {
			t.Errorf("deskActHeld names %s, which api.Act does not have", name)
		}
	}
}

// Each kind listed as running does run while held, and takes nothing from
// the window: no chat note, nothing refused.
func TestDeskHeldActRunsPlacedKinds(t *testing.T) {
	m, _ := dockHeldModel(t)
	for name := range deskActRuns {
		before := len(m.orch.chat)
		if m.deskHeldAct(actWith(t, name)) {
			t.Errorf("%s was held, though it is listed as running", name)
		}
		if len(m.orch.chat) != before {
			t.Errorf("%s added a chat note on its way through", name)
		}
	}
}

// An act of a kind nobody placed waits while held, whatever it is: nothing
// of it runs and the human is told in Den's chat. Simulated by taking a
// kind off the list; once it is back, the same act runs.
func TestDeskHeldActFailsClosed(t *testing.T) {
	m, _ := dockHeldModel(t)
	for _, name := range []string{"Focus", "GodOff", "Grant"} {
		why := deskActRuns[name]
		delete(deskActRuns, name)
		before := len(m.orch.chat)
		held := m.deskHeldAct(actWith(t, name))
		note := lastNote(&m)
		deskActRuns[name] = why
		if !held {
			t.Errorf("%s, unplaced, ran while an agent had the desktop", name)
		}
		if len(m.orch.chat) != before+1 || !strings.Contains(note, name) || !strings.Contains(note, deskHeldDock) {
			t.Errorf("%s, unplaced: the note was %q", name, note)
		}
		if m.deskHeldAct(actWith(t, name)) {
			t.Errorf("%s, placed again, was held", name)
		}
	}
	// A kind taken off the list holds up only the acts that set it.
	why := deskActRuns["Focus"]
	delete(deskActRuns, "Focus")
	defer func() { deskActRuns["Focus"] = why }()
	if m.deskHeldAct(actWith(t, "View")) {
		t.Error("an act that does not set the unplaced kind was held")
	}
}

// Through applyAPIAct, the one gate: an unplaced kind is not carried out
// while held (a focus that would run answers nobody), and once the mission
// is paused and frozen the same act runs again.
func TestApplyAPIActHoldsAnUnplacedKind(t *testing.T) {
	m, dm := dockHeldModel(t)
	why := deskActRuns["Focus"]
	delete(deskActRuns, "Focus")
	t.Cleanup(func() { deskActRuns["Focus"] = why })
	f := &api.FocusReq{AgentID: "nobody", Reply: make(chan api.FocusAnswer, 1)}
	if cmd := m.applyAPIAct(api.Act{Focus: f}); cmd != nil {
		t.Fatalf("an unplaced focus returned a command while held")
	}
	select {
	case a := <-f.Reply:
		t.Fatalf("an unplaced focus was answered while held: %+v", a)
	default:
	}
	if note := lastNote(&m); !strings.Contains(note, "Focus") || !strings.Contains(note, deskHeldDock) {
		t.Fatalf("the human was told %q", note)
	}
	pauseNow(dm)
	if m.deskHolds() {
		t.Fatal("still held after a frozen pause")
	}
	m.applyAPIAct(api.Act{Focus: f})
	select {
	case <-f.Reply:
	default:
		t.Fatal("the focus was not answered once the mission was paused and frozen")
	}
}
