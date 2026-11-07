package ui

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/api"
	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/inbox"
)

// launchOnce asks for a launch on the task mission m1 and runs it to its
// answer; no window comes up, which is a launch all the same.
func launchOnce(t *testing.T, m *Model, command string) bridge.Decision {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"command": command, "wait_s": 1})
	r := deskReq(600, bridge.DesktopInput{Kind: "call", Tool: "launch", Args: raw})
	var cmds []tea.Cmd
	for _, msg := range runBatch(m.handleRequest(r)) {
		if s, ok := msg.(deskSnapMsg); ok {
			cmds = append(cmds, m.decideDesktopCall(s))
		}
	}
	for _, c := range cmds {
		for _, msg := range runBatch(c) {
			if l, ok := msg.(deskLaunchedMsg); ok {
				m.deskLaunched(l)
			}
		}
	}
	d, done := r.Decided()
	if !done {
		t.Fatal("the launch was never answered")
	}
	return d
}

// Without pasta (or bwrap, or with Den's binary changed) a launch is
// refused, saying why, and nothing starts: Den never falls back to the
// host's network or to no sandbox.
func TestDeskLaunchRefusedWithoutTheSandbox(t *testing.T) {
	m, _, _ := taskMission(t)
	f := deskSysFake()
	m.deskMission("m1").limitActions = 100
	f.sandErr = errors.New(deskNeedsPasta)
	d := launchOnce(t, m, "foot")
	if d.Behavior != "deny" || !strings.Contains(d.Message, "pasta (passt)") {
		t.Fatalf("launch without pasta: %+v", d)
	}
	if len(f.started) != 0 || f.walks != 0 {
		t.Fatalf("started %v, walks %d", f.started, f.walks)
	}
}

// The first launch walks the sandbox; a socket it should not see refuses
// the launch by name, and nothing starts. A clean walk is recorded, and
// later launches don't walk again.
func TestDeskWalkOnceAndRefuses(t *testing.T) {
	m, _, _ := taskMission(t)
	f := deskSysFake()
	dm := m.deskMission("m1")
	dm.limitActions = 100
	f.walkFinds = []string{`a socket at "/home/u/.tmux-s"`}
	d := launchOnce(t, m, "foot")
	if d.Behavior != "deny" || !strings.Contains(d.Message, `/home/u/.tmux-s`) || !strings.Contains(d.Message, "task_hide") {
		t.Fatalf("a socket found: %+v", d)
	}
	if len(f.started) != 0 || deskWalked(dm.Dir, dm.ID, false) {
		t.Fatalf("started %v after a failed walk", f.started)
	}
	f.walkFinds = nil
	for range 2 {
		if d := launchOnce(t, m, "foot"); d.Behavior != "allow" {
			t.Fatalf("clean: %+v", d)
		}
	}
	if f.walks != 2 || len(f.started) != 2 {
		t.Fatalf("walks %d (want the failed one and one clean), started %d", f.walks, len(f.started))
	}
}

// A writable folder is checked as bwrap binds it, links followed: a link
// into a hidden folder is refused, as are the mission's own folder, the
// folder of Den's binary and the whole home; a plain folder is bound by
// its resolved path.
func TestDeskWritableFollowsLinks(t *testing.T) {
	home := t.TempDir()
	state := filepath.Join(home, ".local/state/den")
	mission := filepath.Join(state, "desktop", "m1")
	bin := filepath.Join(home, "bin")
	out := filepath.Join(home, "Documents", "out")
	for _, d := range []string{mission, bin, out, filepath.Join(home, ".ssh")} {
		os.MkdirAll(d, 0o700)
	}
	hidden := []string{filepath.Join(home, ".ssh"), state}
	os.Symlink(state, filepath.Join(home, "notes"))
	os.Symlink(out, filepath.Join(home, "outlink"))
	for _, bad := range []string{filepath.Join(home, "notes"), mission, bin, home, filepath.Join(home, ".ssh", "x")} {
		os.MkdirAll(bad, 0o700)
		if got, err := deskWritable([]string{bad}, hidden, home, mission, bin); err == nil {
			t.Errorf("%s accepted as writable: %q", bad, got)
		}
	}
	got, err := deskWritable([]string{filepath.Join(home, "outlink")}, hidden, home, mission, bin)
	if want, _ := filepath.EvalSymlinks(out); err != nil || !slices.Equal(got, []string{want}) {
		t.Fatalf("a link to a plain folder: %q %v, want %s", got, err, want)
	}
}

// The X11 switch is the start card's: off by default, turned on only by
// /desktop x11 in Den's window, never from the dock, fixed for the
// mission once it starts, and never carried to the next one.
func TestDeskX11OnlyFromTheCard(t *testing.T) {
	m, d, _, _, _ := taskRig(t)
	runBatch(m.handleRequest(taskReq("open x.com")))
	it := desktopCard(*m, inbox.DesktopMission)
	if it == nil {
		t.Fatal("no task card")
	}
	if !slices.Contains(it.Body, deskX11Line(false)) || m.desk.offers[it.ID].x11 {
		t.Fatalf("not off by default: %q", it.Body)
	}
	for _, c := range dockVerbs {
		if c.name != "/desktop" {
			continue
		}
		for _, a := range [][]string{{"x11"}, {"x11", "on"}, {"X11"}, {"stop", "x11"}} {
			if v, _ := c.verdict(a); v == api.DockOK {
				t.Errorf("the dock may run /desktop %v", a)
			}
		}
		for _, a := range [][]string{nil, {"stop"}, {"pause"}, {"keep"}, {"STOP"}} {
			if v, _ := c.verdict(a); v != api.DockOK {
				t.Errorf("the dock may no longer run /desktop %v", a)
			}
		}
	}
	m.deskX11([]string{"on"})
	if !m.desk.offers[it.ID].x11 || !slices.Contains(it.Body, deskX11Line(true)) || slices.Contains(it.Body, deskX11Line(false)) {
		t.Fatalf("on: %v %q", m.desk.offers[it.ID].x11, it.Body)
	}
	m.deskX11([]string{"off"})
	if m.desk.offers[it.ID].x11 || !slices.Contains(it.Body, deskX11Line(false)) {
		t.Fatalf("off again: %q", it.Body)
	}
	m.deskX11(nil)
	if !m.desk.offers[it.ID].x11 {
		t.Fatal("/desktop x11 alone didn't turn it on")
	}
	if it.State != inbox.Waiting {
		t.Fatalf("the switch answered the card: %v", it.State)
	}
	// Started, the mission keeps it; nothing turns it off or on again.
	runTask(t, m, d, "allow")
	dm := m.deskLive()
	if dm == nil || !dm.x11 {
		t.Fatalf("the mission did not take the card's switch: %+v", dm)
	}
	if m.deskX11([]string{"off"}); !dm.x11 {
		t.Fatal("/desktop x11 off changed a running mission")
	}
}
