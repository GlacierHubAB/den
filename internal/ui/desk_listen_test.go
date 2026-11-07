package ui

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/desksand"
	"github.com/GlacierHubAB/den/internal/desktop"
	"github.com/GlacierHubAB/den/internal/sysd"
)

// fakeListener stands in a held task's wlsec listener.
type fakeListener struct {
	path   string
	closed int
}

func (l *fakeListener) Socket() string { return l.path }
func (l *fakeListener) Live() bool     { return l.closed == 0 }
func (l *fakeListener) Close() error   { l.closed++; return nil }

// heldLaunch runs one launch of a held mission m1 through Den and returns
// the decision and the service started, if any.
func heldLaunch(t *testing.T, m *Model, f *fakeSys) (bridge.Decision, *sysd.Service) {
	t.Helper()
	before := len(f.started)
	r := deskReq(600, bridge.DesktopInput{Kind: "call", Tool: "launch", Args: json.RawMessage(`{"command":"foot","wait_s":1}`)})
	for _, msg := range runBatch(m.handleRequest(r)) {
		if s, ok := msg.(deskSnapMsg); ok {
			for _, msg := range runBatch(m.decideDesktopCall(s)) {
				if l, ok := msg.(deskLaunchedMsg); ok {
					m.deskLaunched(l)
				}
			}
		}
	}
	dv, _ := r.Decided()
	if len(f.started) > before {
		return dv, &f.started[len(f.started)-1]
	}
	return dv, nil
}

// A held task's launch binds its listener at the Wayland path; with the
// listener gone, the launch is refused and nothing starts.
func TestDeskHeldLaunchUsesItsListener(t *testing.T) {
	m, _, _ := taskMission(t)
	f := deskSysFake()
	spec := deskSandSpec
	deskSandSpec = func(dir, app string, x11 bool) (desksand.Spec, error) {
		s, err := spec(dir, app, x11)
		s.RuntimeDir, s.Wayland = "/run/user/1000", "wayland-1"
		return s, err
	}
	dm := m.deskMission("m1")
	dm.limitActions = 100
	l := &fakeListener{path: "/run/user/1000/den-wl/m1/wayland"}
	dm.Scoped, dm.listener = true, l
	dv, svc := heldLaunch(t, m, f)
	if svc == nil {
		t.Fatalf("nothing started: %+v", dv)
	}
	argv := strings.Join(svc.Argv, " ")
	if !strings.Contains(argv, "--ro-bind "+l.path+" /run/user/1000/wayland-1") || strings.Contains(argv, "--ro-bind /run/user/1000/wayland-1") {
		t.Fatalf("the launch doesn't bind the listener in place of the socket:\n%s", argv)
	}
	l.closed = 1
	dv, svc = heldLaunch(t, m, f)
	if svc != nil || dv.Behavior != "deny" || !strings.Contains(dv.Message, "never opens apps on the compositor's own") {
		t.Fatalf("a launch with the listener gone: %+v %v", dv, svc)
	}
	// A whole-desktop task has no listener and the compositor's socket.
	dm.Scoped, dm.listener = false, nil
	dv, svc = heldLaunch(t, m, f)
	if svc == nil || !strings.Contains(strings.Join(svc.Argv, " "), "--ro-bind /run/user/1000/wayland-1 /run/user/1000/wayland-1") {
		t.Fatalf("a whole-desktop launch: %+v", dv)
	}
}

// The listener is closed only once the slice is stopped: a client already
// connected outlives the close fd.
func TestDeskListenerClosesAfterTheSlice(t *testing.T) {
	m, dm, f := sliceModel(t)
	l := &fakeListener{path: "/x"}
	dm.Scoped, dm.listener = true, l
	f.failStop = true
	dm.Ended = "test"
	for _, msg := range runBatch(m.deskDrive()) {
		m.deskSliceMsg(msg)
	}
	if l.closed != 0 {
		t.Fatal("the listener closed while the slice still ran")
	}
	f.failStop = false
	dm.reapAfter = dm.reapAfter.AddDate(-1, 0, 0)
	m = runSlice(t, m)
	if !dm.reaped || l.closed != 1 {
		t.Fatalf("reaped %v, listener closed %d times", dm.reaped, l.closed)
	}
}

// A held task opens its listener at the start, and doesn't start without
// one; a whole-desktop task opens none.
func TestDeskHeldStartOpensItsListener(t *testing.T) {
	m, d, _, _, _ := taskRig(t)
	d.held = true
	var opened []string
	deskOpenListener = func(id string) (deskListener, error) {
		opened = append(opened, id)
		return &fakeListener{path: "/x"}, nil
	}
	runBatch(m.handleRequest(taskReq("tidy the inbox")))
	runTask(t, m, d, "allow")
	dm := taskLive(t, *m)
	if len(opened) != 1 || opened[0] != dm.ID || dm.listener == nil || !dm.listener.Live() {
		t.Fatalf("opened %v, listener %v", opened, dm.listener)
	}

	m2, d2, spawned2, _, _ := taskRig(t)
	d2.held = true
	deskOpenListener = func(string) (deskListener, error) { return nil, errors.New("no manager") }
	runBatch(m2.handleRequest(taskReq("tidy the inbox")))
	runTask(t, m2, d2, "allow")
	if m2.deskLive() != nil || len(*spawned2) != 0 {
		t.Fatal("a held task started without its listener")
	}
	if it := settledTask(*m2); it == nil || !strings.Contains(it.Resolved, "sandboxed Wayland socket") {
		t.Fatalf("%+v", it)
	}

	m3, d3, _, _, _ := taskRig(t)
	opened = nil
	deskOpenListener = func(id string) (deskListener, error) { opened = append(opened, id); return &fakeListener{}, nil }
	runBatch(m3.handleRequest(taskReq("tidy the inbox")))
	runTask(t, m3, d3, "allow")
	if dm := taskLive(t, *m3); dm.listener != nil || len(opened) != 0 {
		t.Fatal("a whole-desktop task opened a listener")
	}
}

// [C1] A build change found at a held task's launch refuses the launch
// and pauses the task for the human: it stays held, never wider, and goes
// on only once the build is the checked one again.
func TestDeskBuildChangePausesAHeldTask(t *testing.T) {
	m, _, _ := taskMission(t)
	f := deskSysFake()
	dm := m.deskMission("m1")
	dm.limitActions = 100
	l := &fakeListener{path: "/x"}
	dm.Scoped, dm.listener = true, l
	dm.Picked = map[string]desktop.Pick{"0xa": {PID: 900}}
	deskScopedBuild = func(context.Context) string { return "this Hyprland build (v0.57.0) isn't the one checked" }
	dv, svc := heldLaunch(t, m, f)
	if svc != nil || dv.Behavior != "deny" {
		t.Fatalf("a launch on a changed build: %+v %v", dv, svc)
	}
	if !dm.humanPaused || !dm.Scoped || dm.listener == nil || !l.Live() || dm.buildChanged == "" {
		t.Fatalf("paused %v scoped %v listener %v changed %q", dm.humanPaused, dm.Scoped, dm.listener, dm.buildChanged)
	}
	if dv, done := deskCall(m, "keyboard", `{"action":"type","text":"hi","window":"0xa"}`); done && dv.Behavior == "allow" {
		t.Fatal("keys went on while paused for the build")
	}
	if mm := resumeChecked(t, m, dm); !mm.flashErr || !dm.humanPaused {
		t.Fatal("resumed on a build still not the checked one")
	}
	deskScopedBuild = func(context.Context) string { return "" }
	if resumeChecked(t, m, dm); dm.humanPaused || dm.buildChanged != "" {
		t.Fatal("didn't resume on the checked build")
	}
	if dv, _ := deskCall(m, "keyboard", `{"action":"type","text":"hi","window":"0xh"}`); dv.Behavior == "allow" {
		t.Fatal("after the change the task reached outside its windows")
	}
}

// resumeChecked types /desktop resume on a task paused for a build
// change, runs the build read it asks for, and hands its answer back.
func resumeChecked(t *testing.T, m *Model, dm *deskMission) Model {
	t.Helper()
	_, cmd := m.deskPause(dm, false)
	var mm tea.Model = *m
	for _, msg := range runBatch(cmd) {
		if c, ok := msg.(deskResumeCheckMsg); ok {
			mm, _ = m.finishResumeCheck(c)
		}
	}
	return mm.(Model)
}

// [L1] /desktop resume after a build change reads the build off the
// update loop: the task stays paused until the answer is in, a second
// resume asks for no second read, and an answer for a task ended or
// paused differently meanwhile resumes nothing.
func TestDeskResumeReadsTheBuildOffTheLoop(t *testing.T) {
	m, _, _ := taskMission(t)
	dm := m.deskMission("m1")
	dm.Scoped, dm.humanPaused, dm.buildChanged = true, true, "this Hyprland build (v0.57.0) isn't the one checked"
	reads := 0
	deskScopedBuild = func(context.Context) string { reads++; return "" }
	_, cmd := m.deskPause(dm, false)
	if reads != 0 || !dm.humanPaused || !dm.resumeChecking {
		t.Fatalf("resume read the build on the loop (%d reads) or went on before the answer (paused %v)", reads, dm.humanPaused)
	}
	_, again := m.deskPause(dm, false)
	for _, msg := range runBatch(again) {
		if _, ok := msg.(deskResumeCheckMsg); ok || reads != 0 {
			t.Fatal("a second resume asked for a second read")
		}
	}
	var answer deskResumeCheckMsg
	for _, msg := range runBatch(cmd) {
		if c, ok := msg.(deskResumeCheckMsg); ok {
			answer = c
		}
	}
	if reads != 1 || answer.mission != "m1" {
		t.Fatalf("reads %d, answer %+v", reads, answer)
	}
	// The mission ended while the read was out: nothing resumes.
	dm.Ended = "stopped"
	m.finishResumeCheck(answer)
	if !dm.humanPaused || dm.buildChanged == "" || dm.resumeChecking {
		t.Fatal("an answer resumed an ended mission")
	}
	dm.Ended = ""
	// An answer for a mission Den no longer has is dropped.
	if _, c := m.finishResumeCheck(deskResumeCheckMsg{mission: "gone"}); c != nil {
		t.Fatal("an answer for no mission did something")
	}
	// The build changed again after the read: its "" answer is stale.
	m.deskBuildChanged(dm, "this Hyprland build (v0.57.1) isn't the one checked")
	if mm, _ := m.finishResumeCheck(answer); !mm.(Model).flashErr || !dm.humanPaused {
		t.Fatal("an answer read before a later build change resumed the task")
	}
	if resumeChecked(t, m, dm); dm.humanPaused || dm.buildChanged != "" {
		t.Fatal("the checked build's answer didn't resume the task")
	}
}
