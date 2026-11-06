package ui

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/desktop"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/notify"
	"github.com/GlacierHubAB/den/internal/sysd"
)

// fakeSys is the systemd user manager as the ui tests see it: units in
// memory, each slice holding its services, and switches for failures.
// No test reaches a real manager (TestMain installs it).
type fakeSys struct {
	mu                        sync.Mutex
	units                     map[string]*sysd.UnitState
	started                   []sysd.Service
	failStart, failFreeze     bool
	failThaw, failStop, stuck bool // stuck: a stop never ends
	freezeSlow                bool // FreezerState stays "freezing"
	cgroups                   map[int]string
}

var deskSys *fakeSys

// deskSysFake installs a fresh fake manager.
func deskSysFake() *fakeSys {
	f := &fakeSys{units: map[string]*sysd.UnitState{}, cgroups: map[int]string{}}
	deskSys = f
	deskSysStart = func(s sysd.Service) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.failStart {
			return errors.New("start refused")
		}
		f.started = append(f.started, s)
		f.units[s.Name] = &sysd.UnitState{Loaded: true, Active: "active", Freeze: "running"}
		if _, ok := f.units[s.Slice]; !ok {
			f.units[s.Slice] = &sysd.UnitState{Loaded: true, Active: "active", Freeze: "running"}
		}
		return nil
	}
	deskSysFreeze = func(unit string) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		u, ok := f.units[unit]
		switch {
		case f.failFreeze:
			return errors.New("freeze refused")
		case !ok:
			return errors.New("org.freedesktop.systemd1.NoSuchUnit")
		case f.freezeSlow:
			u.Freeze = "freezing"
		default:
			u.Freeze = "frozen"
		}
		return nil
	}
	deskSysThaw = func(unit string) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.failThaw {
			return errors.New("thaw refused")
		}
		if u, ok := f.units[unit]; ok {
			u.Freeze = "running"
		}
		return nil
	}
	deskSysStop = func(unit string) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.failStop {
			return errors.New("stop refused")
		}
		if f.stuck {
			if u, ok := f.units[unit]; ok {
				u.Active = "deactivating"
			}
			return nil
		}
		prefix := strings.TrimSuffix(unit, ".slice") + "_" // its services
		for name := range f.units {
			if name == unit || strings.HasSuffix(unit, ".slice") && strings.HasPrefix(name, prefix) {
				delete(f.units, name)
			}
		}
		return nil
	}
	deskSysState = func(unit string) (sysd.UnitState, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if u, ok := f.units[unit]; ok {
			return *u, nil
		}
		return sysd.UnitState{}, nil
	}
	deskSysList = func(pattern string) ([]string, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		prefix := strings.TrimSuffix(pattern, "*.slice")
		var out []string
		for name := range f.units {
			if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ".slice") {
				out = append(out, name)
			}
		}
		return out, nil
	}
	deskCgroupOf = func(pid int) string {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.cgroups[pid]
	}
	deskDispatch = func(...string) error { return nil }
	deskStopRetry = 10 * time.Millisecond
	return f
}

// unit is a unit's state now.
func (f *fakeSys) unit(name string) sysd.UnitState {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u, ok := f.units[name]; ok {
		return *u
	}
	return sysd.UnitState{}
}

// runSlice runs what the driver asked for, as Bubble Tea would, until it
// asks for nothing more: commands run and their messages are fed back
// through Update. Ticks are run at once.
func runSlice(t *testing.T, m Model) Model {
	t.Helper()
	var cmd tea.Cmd
	for i := 0; i < 50; i++ {
		if cmd == nil {
			// Nothing pending: what the state asks for now (a mission
			// that just ended asks for its stop).
			// A look can change the state without a command (a mission
			// ended there): look once more before giving up.
			if cmd = tea.Batch(m.deskDrive(), m.deskFlush()); cmd == nil {
				if cmd = tea.Batch(m.deskDrive(), m.deskFlush()); cmd == nil {
					break
				}
			}
		}
		var next []tea.Cmd
		for _, msg := range runBatch(cmd) {
			mm, c := m.Update(msg)
			m = mm.(Model)
			if c != nil {
				next = append(next, c)
			}
		}
		cmd = tea.Batch(next...)
	}
	return m
}

// sliceModel is Den's window with a live mission m1 that has launched an
// app into its slice.
func sliceModel(t *testing.T) (Model, *deskMission, *fakeSys) {
	t.Helper()
	f := deskSysFake()
	m, dm, _ := holdModel(t)
	dm.ID = "m1"
	m.desk.missions = map[string]*deskMission{"m1": dm}
	deskSysStart(sysd.Service{Name: dm.service(1), Slice: dm.slice(), Argv: []string{"x"}})
	dm.launched = true
	return m, dm, f
}

// A pause freezes the slice, and only then does Den's window answer;
// going on brings the hold back at once and thaws it.
func TestDeskPauseFreezesTheSlice(t *testing.T) {
	m, dm, f := sliceModel(t)
	dm.humanPaused = true
	if !m.deskHolds() {
		t.Fatal("paused but not yet frozen: Den's window answered")
	}
	m = runSlice(t, m)
	if f.unit(dm.slice()).Freeze != "frozen" || !dm.frozen || m.deskHolds() {
		t.Fatalf("after the freeze: %+v frozen %v held %v", f.unit(dm.slice()), dm.frozen, m.deskHolds())
	}
	dm.humanPaused = false
	cmd := m.deskDrive()
	if dm.frozen || !m.deskHolds() {
		t.Fatal("going on: the hold did not come back before the thaw")
	}
	for _, msg := range runBatch(cmd) {
		mm, _ := m.Update(msg)
		m = mm.(Model)
	}
	if f.unit(dm.slice()).Freeze != "running" {
		t.Errorf("not thawed: %+v", f.unit(dm.slice()))
	}
}

// Each pause state freezes: the human's, a your-turn card, the overlay's,
// and an open card of the mission.
func TestDeskEveryPauseFreezes(t *testing.T) {
	for name, set := range map[string]func(*deskMission, bool){
		"human":   func(dm *deskMission, on bool) { dm.humanPaused = on },
		"turn":    func(dm *deskMission, on bool) { dm.Paused = on },
		"overlay": func(dm *deskMission, on bool) { dm.overlayPaused = on },
		"card": func(dm *deskMission, on bool) {
			dm.cards = map[string]bool{}
			if on {
				dm.cards["c1"] = true
			}
		},
	} {
		m, dm, f := sliceModel(t)
		set(dm, true)
		m = runSlice(t, m)
		if !dm.frozen || f.unit(dm.slice()).Freeze != "frozen" || m.deskHolds() {
			t.Errorf("%s: frozen %v, %+v, held %v", name, dm.frozen, f.unit(dm.slice()), m.deskHolds())
		}
		set(dm, false)
		m = runSlice(t, m)
		if dm.frozen || f.unit(dm.slice()).Freeze != "running" || !m.deskHolds() {
			t.Errorf("%s going on: frozen %v, %+v", name, dm.frozen, f.unit(dm.slice()))
		}
	}
}

// A freeze that fails or never confirms keeps the hold, with one note.
func TestDeskFailedFreezeKeepsTheHold(t *testing.T) {
	old := deskFreezeWait
	t.Cleanup(func() { deskFreezeWait = old })
	deskFreezeWait = 50 * time.Millisecond
	for _, slow := range []bool{false, true} {
		m, dm, f := sliceModel(t)
		f.failFreeze, f.freezeSlow = !slow, slow
		dm.humanPaused = true
		// One freeze, as the update loop takes it; its retry tick is not
		// run here.
		for round := 0; round < 2; round++ {
			for _, msg := range runBatch(m.deskDrive()) {
				m.deskSliceMsg(msg)
			}
		}
		if dm.frozen || !m.deskHolds() {
			t.Errorf("slow %v: frozen %v, held %v", slow, dm.frozen, m.deskHolds())
		}
		// No retry at once: the manager is not asked in a loop.
		if cmd := m.deskDrive(); cmd != nil {
			t.Errorf("slow %v: a freeze tried again at once", slow)
		}
		n := 0
		for _, e := range m.orch.chat {
			if strings.HasPrefix(e.text, "Den could not pause the apps") {
				n++
			}
		}
		if n != 1 {
			t.Errorf("slow %v: %d notes", slow, n)
		}
		// Once the wait is over, it tries again, and a freeze that works
		// then frees Den's window.
		dm.freezeAfter = time.Time{}
		f.failFreeze, f.freezeSlow = false, false
		m = runSlice(t, m)
		if !dm.frozen || m.deskHolds() {
			t.Errorf("slow %v: the retry: frozen %v, held %v", slow, dm.frozen, m.deskHolds())
		}
	}
}

// A call in flight delays the freeze; past the bound the mission ends
// (and its slice is stopped), never frozen with the call running.
func TestDeskInFlightDelaysTheFreeze(t *testing.T) {
	m, dm, f := sliceModel(t)
	dm.inflight = map[string]time.Time{"c1": time.Now()}
	dm.humanPaused = true
	m.deskDrive()
	if dm.frozen || dm.freezing || f.unit(dm.slice()).Freeze != "running" {
		t.Fatal("frozen with a call in flight")
	}
	delete(dm.inflight, "c1") // the proxy's done
	dm.draining = false
	m = runSlice(t, m)
	if !dm.frozen {
		t.Fatal("not frozen once the call ended")
	}

	m, dm, f = sliceModel(t)
	stopDeskForTest(t)
	dm.inflight = map[string]time.Time{"c1": time.Now()}
	dm.humanPaused = true
	dm.pausedAt = time.Now().Add(-deskDrainBound - time.Second)
	m = runSlice(t, m)
	if dm.Ended == "" || dm.frozen {
		t.Fatalf("past the bound: ended %q frozen %v", dm.Ended, dm.frozen)
	}
	if !f.unit(dm.slice()).Gone() || !dm.reaped || m.deskHolds() {
		t.Errorf("the ended mission's slice: %+v reaped %v held %v", f.unit(dm.slice()), dm.reaped, m.deskHolds())
	}
}

// stopDeskForTest makes a mission's stop succeed with nothing to signal.
func stopDeskForTest(t *testing.T) {
	t.Helper()
	old := deskStopFn
	t.Cleanup(func() { deskStopFn = old })
	deskStopFn = func(desktop.Targets, string) desktop.StopReport { return desktop.StopReport{} }
	oldNotify := deskNotify
	t.Cleanup(func() { deskNotify = oldNotify })
	deskNotify = func(notify.Note) {}
}

// shortStopWait makes a stop that never ends give up at once.
func shortStopWait(t *testing.T) {
	t.Helper()
	old := deskStopWait
	t.Cleanup(func() { deskStopWait = old })
	deskStopWait = 50 * time.Millisecond
}

// A failed thaw ends the mission.
func TestDeskFailedThawEndsTheMission(t *testing.T) {
	m, dm, f := sliceModel(t)
	stopDeskForTest(t)
	dm.humanPaused = true
	m = runSlice(t, m)
	f.failThaw = true
	dm.humanPaused = false
	m = runSlice(t, m)
	if dm.Ended == "" {
		t.Error("a failed thaw left the mission on")
	}
}

// The end stops the slice; Den's window holds until it is gone, and a
// stop that fails is tried again.
func TestDeskEndStopsTheSlice(t *testing.T) {
	m, dm, f := sliceModel(t)
	f.failStop = true
	dm.Ended = "test"
	// One round: the stop fails; its retry tick is not run here.
	for _, msg := range runBatch(m.deskDrive()) {
		m.deskSliceMsg(msg)
	}
	if dm.reaped || dm.reaping || !m.deskHolds() || f.unit(dm.slice()).Gone() {
		t.Fatalf("a failed stop: reaped %v held %v", dm.reaped, m.deskHolds())
	}
	if !strings.HasPrefix(m.orch.chat[len(m.orch.chat)-1].text, "Den could not stop the apps") {
		t.Errorf("no note for the failed stop")
	}
	f.failStop = false
	dm.reapAfter = time.Time{} // the retry is due
	m = runSlice(t, m)
	if !dm.reaped || m.deskHolds() || !f.unit(dm.slice()).Gone() || !f.unit(dm.service(1)).Gone() {
		t.Errorf("the retry: reaped %v held %v", dm.reaped, m.deskHolds())
	}
}

// The start stops only this Den's own slices; another Den's are left, and
// one that will not stop is held until it does.
func TestDeskStartSweepsOnlyItsOwn(t *testing.T) {
	f := deskSysFake()
	mine := deskSliceOf("old")
	other := "den_desk_000000000000_old.slice"
	deskSysStart(sysd.Service{Name: strings.TrimSuffix(mine, ".slice") + "-1.service", Slice: mine, Argv: []string{"x"}})
	deskSysStart(sysd.Service{Name: "den_desk_000000000000_old_1.service", Slice: other, Argv: []string{"x"}})
	if left := SweepDesktopSlices(); len(left) != 0 {
		t.Fatalf("left %v", left)
	}
	if !f.unit(mine).Gone() || f.unit(other).Gone() {
		t.Errorf("mine %+v, other %+v", f.unit(mine), f.unit(other))
	}

	f = deskSysFake()
	deskSysStart(sysd.Service{Name: strings.TrimSuffix(mine, ".slice") + "-1.service", Slice: mine, Argv: []string{"x"}})
	f.stuck = true
	shortStopWait(t)
	left := SweepDesktopSlices()
	if len(left) != 1 || left[0] != mine {
		t.Fatalf("a slice that will not stop: left %v", left)
	}
	m := testModel(140, 40)
	m.desk.leftover = append([]string(nil), deskSwept...) // as New takes it
	if !m.deskHolds() {
		t.Error("Den's window answered with a slice from before still running")
	}
	f.stuck = false
	m = runSlice(t, m)
	if m.deskHolds() || !f.unit(mine).Gone() {
		t.Errorf("after the retry: held %v, %+v", m.deskHolds(), f.unit(mine))
	}
	deskSwept = nil
}

// A start whose listing fails holds Den's window (the manager is there):
// it lists again from the retry, and only a listing read releases it
// (sec's S2-3). With no manager at all, nothing holds.
func TestDeskSweepFailsClosed(t *testing.T) {
	f := deskSysFake()
	oldList, oldHere := deskSysList, deskManagerHere
	t.Cleanup(func() { deskSysList, deskManagerHere = oldList, oldHere; deskSwept = nil })
	fail := true
	deskSysList = func(prefix string) ([]string, error) {
		if fail {
			return nil, errors.New("systemctl took over 5s")
		}
		return oldList(prefix)
	}
	deskManagerHere = func() bool { return true }
	left := SweepDesktopSlices()
	m := testModel(140, 40)
	m.desk.leftover = append([]string(nil), deskSwept...)
	if len(left) != 1 || !m.deskHolds() {
		t.Fatalf("a failed listing: left %v, held %v", left, m.deskHolds())
	}
	m = runSlice(t, m)
	if !m.deskHolds() {
		t.Error("released while the listing still fails")
	}
	fail = false
	mine := deskSliceOf("old")
	deskSysStart(sysd.Service{Name: strings.TrimSuffix(mine, ".slice") + "_1.service", Slice: mine, Argv: []string{"x"}})
	// runSlice gave up with a retry still out: as if it had come back.
	m.desk.leftoverRetry, m.desk.leftoverBusy = time.Time{}, false
	m = runSlice(t, m)
	if m.deskHolds() || !f.unit(mine).Gone() {
		t.Errorf("after a good listing: held %v, %+v", m.deskHolds(), f.unit(mine))
	}
	fail = true
	deskManagerHere = func() bool { return false }
	if left := SweepDesktopSlices(); len(left) != 0 {
		t.Errorf("no manager at all: left %v", left)
	}
}

// The slice's name: this Den's instance, the escaped id, systemd's limit.
func TestDeskSliceNames(t *testing.T) {
	dm := &deskMission{Mission: desktop.Mission{ID: "a-b"}}
	if s := dm.slice(); !strings.HasPrefix(s, "den_desk_"+deskInstance()+"_") || strings.Contains(strings.TrimSuffix(s, `a\x2db.slice`), "-") || !strings.HasSuffix(s, `a\x2db.slice`) {
		t.Errorf("slice %q", s)
	}
	if (&deskMission{Mission: desktop.Mission{ID: "a/b"}}).slice() == dm.slice() {
		t.Error("two ids make one slice")
	}
	if why := dm.sliceOK(); why != "" {
		t.Error(why)
	}
	// No '-' (a level of slices) and no '_' (Den's separator) in a part:
	// every name maps back to one id, one launch or one app (sec's S2-6).
	names := map[string]string{}
	add := func(what, name string) {
		if was, ok := names[name]; ok {
			t.Errorf("%s and %s make one name %q", was, what, name)
		}
		names[name] = what
	}
	for _, id := range []string{"x_app", "x", "a", "a_1", "a/b", "a-b", "a_b"} {
		m := &deskMission{Mission: desktop.Mission{ID: id}}
		if why := m.sliceOK(); why != "" {
			t.Errorf("%q: %s", id, why)
		}
		s := strings.TrimPrefix(m.slice(), deskSlicePrefix())
		if strings.ContainsAny(strings.TrimSuffix(s, ".slice"), "_-") {
			t.Errorf("%q's slice part %q holds a separator", id, s)
		}
		add("slice of "+id, m.slice())
		add("launch 1 of "+id, m.service(1))
		add("app 1 of "+id, strings.TrimSuffix(m.slice(), ".slice")+"_app_"+sysd.EscapePart("1")+".service")
	}
	long := &deskMission{Mission: desktop.Mission{ID: strings.Repeat("é", 100)}}
	if why := long.sliceOK(); why == "" {
		t.Errorf("a %d-character service name passed", len(long.service(999999)))
	}
}

// launch runs in the mission's slice through /bin/sh -c, with Den's
// environment minus Den's, tmux's and Hyprland's instance; the agent
// hears the window that came up in the slice, and the call counts as in
// flight until then.
func TestDeskLaunchRunsInTheSlice(t *testing.T) {
	m, d, _ := taskMission(t)
	f := deskSysFake()
	t.Setenv("DEN_SOCKET", "/x")
	t.Setenv("HYPRLAND_INSTANCE_SIGNATURE", "sig")
	t.Setenv("TMUX", "/tmp/t")
	dm := m.deskMission("m1")
	dm.limitActions = 100
	r := deskReq(600, bridge.DesktopInput{Kind: "call", Tool: "launch", Args: json.RawMessage(`{"command":"foot -e sh"}`)})
	var cmds []tea.Cmd
	for _, msg := range runBatch(m.handleRequest(r)) {
		if s, ok := msg.(deskSnapMsg); ok {
			cmds = append(cmds, m.decideDesktopCall(s))
		}
	}
	if len(dm.inflight) != 1 {
		t.Fatalf("a launch under way is not in flight: %v", dm.inflight)
	}
	// Its window comes up in the slice, after the launch was decided.
	_ = d
	base := deskSnapshot
	deskSnapshot = func(ctx context.Context) (desktop.Snapshot, error) {
		s, err := base(ctx)
		s.Windows = append(s.Windows, desktop.Window{Address: "0xnew", Class: "foot", PID: 4242, Workspace: 3, Mapped: true, Size: [2]int{100, 100}})
		return s, err
	}
	f.mu.Lock()
	f.cgroups[4242] = "/user.slice/user@1000.service/den.slice/" + dm.slice() + "/" + dm.service(1)
	f.mu.Unlock()
	for _, c := range cmds {
		for _, msg := range runBatch(c) {
			if l, ok := msg.(deskLaunchedMsg); ok {
				m.deskLaunched(l)
			}
		}
	}
	dv, done := r.Decided()
	if !done || dv.Behavior != "allow" || !strings.Contains(dv.Message, "0xnew") || len(dm.inflight) != 0 {
		t.Fatalf("launch: %+v, in flight %v", dv, dm.inflight)
	}
	if len(f.started) != 1 {
		t.Fatalf("started %v", f.started)
	}
	s := f.started[0]
	if s.Slice != dm.slice() || strings.Join(s.Argv, " ") != "/bin/sh -c foot -e sh" {
		t.Errorf("service %+v", s)
	}
	env := strings.Join(s.Env, "\n")
	if strings.Contains(env, "DEN_SOCKET=") || strings.Contains(env, "HYPRLAND_INSTANCE_SIGNATURE=") || strings.Contains(env, "TMUX=") {
		t.Errorf("env keeps what it must not:\n%s", env)
	}
	if !dm.launched {
		t.Error("not marked launched")
	}
}

// The proxy's done is taken only from the mission's own proxy, only for
// an id Den named for that mission, and once.
func TestDeskDoneOnlyFromItsProxy(t *testing.T) {
	m, _, _ := taskMission(t)
	deskSysFake()
	dm := m.deskMission("m1")
	dm.inflight = map[string]time.Time{"c1": time.Now()}
	other := &deskMission{Mission: desktop.Mission{ID: "m2"}, inflight: map[string]time.Time{"c2": time.Now()}}
	m.desk.missions["m2"] = other
	done := func(pid int, mission, call string) bridge.Decision {
		r := deskReq(pid, bridge.DesktopInput{Kind: "done", Mission: mission, Call: call})
		deskDo(m, r)
		d, _ := r.Decided()
		return d
	}
	if d := done(9999, "m1", "c1"); d.Behavior != "deny" || len(dm.inflight) != 1 {
		t.Errorf("a done from another process: %+v, %v", d, dm.inflight)
	}
	if d := done(600, "m1", "c2"); d.Behavior != "allow" || len(other.inflight) != 1 || len(dm.inflight) != 1 {
		t.Errorf("another mission's id: %+v", d)
	}
	done(600, "m1", "c1")
	if len(dm.inflight) != 0 {
		t.Error("its own done did not clear")
	}
	if d := done(600, "m1", "c1"); d.Behavior != "allow" {
		t.Errorf("a repeated done: %+v", d)
	}
}

// A pause between two pieces of a typed text: the next piece is refused.
func TestDeskPauseBetweenPieces(t *testing.T) {
	m, _, _ := taskMission(t)
	deskSysFake()
	dm := m.deskMission("m1")
	dm.limitActions = 100
	dm.tokens["t1"] = deskToken{window: "0xa", left: 2, until: time.Now().Add(time.Minute)}
	dm.humanPaused = true
	r := deskReq(600, bridge.DesktopInput{Kind: "call", Tool: "keyboard", Token: "t1", Args: json.RawMessage(`{"action":"type","text":"more","window":"0xa"}`)})
	deskDo(m, r)
	if d, _ := r.Decided(); d.Behavior != "deny" || !strings.Contains(d.Message, deskHumanPaused) {
		t.Errorf("a piece after a pause: %+v", d)
	}
}

// A mission card is a pause: other calls wait, its slice freezes before
// its ^Y works, and the answer goes on.
func TestDeskCardIsAPause(t *testing.T) {
	m := deskRig(t)
	deskStarted(t, &m)
	f := deskSysFake()
	dm := m.deskLive()
	deskSysStart(sysd.Service{Name: dm.service(1), Slice: dm.slice(), Argv: []string{"x"}})
	dm.launched = true
	r := deskReq(600, bridge.DesktopInput{Kind: "call", Tool: "keyboard", Args: json.RawMessage(`{"action":"type","text":"hello\nthere","window":"0xb"}`)})
	deskDo(&m, r)
	it := desktopCard(m, inbox.DesktopAct)
	if it == nil || len(dm.cards) != 1 {
		t.Fatalf("no card, or not a pause: %v", dm.cards)
	}
	if d, _ := deskCall(&m, "pointer", `{"action":"click","x":1200,"y":700}`); d.Behavior != "deny" {
		t.Errorf("a call while the card is open: %+v", d)
	}
	if !m.deskHolds() {
		t.Error("the card's ^Y works before the slice froze")
	}
	m = runSlice(t, m)
	if !dm.frozen || m.deskHolds() || f.unit(dm.slice()).Freeze != "frozen" {
		t.Fatalf("frozen %v held %v", dm.frozen, m.deskHolds())
	}
	m = answerDeskCard(t, m, it, "allow")
	if len(dm.cards) != 0 {
		t.Error("the card's pause outlived its answer")
	}
	m = runSlice(t, m)
	if f.unit(dm.slice()).Freeze != "running" {
		t.Errorf("not thawed after the answer: %+v", f.unit(dm.slice()))
	}
	if d, done := r.Decided(); !done || d.Behavior != "allow" {
		t.Errorf("the approved call after the answer: %+v", d)
	}
}
