package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/desktop"
)

// pauseRig is a running task with the console's process (pid 1500,
// started at 15000) proved: backed, linked and glow say whether the
// console has linked its overlay to the task, whether the link holds, and
// whether its glow is on the desktop.
type pauseRig struct {
	m                    *Model
	stops                *[]desktop.Targets
	backed, linked, glow bool
}

func newPauseRig(t *testing.T) *pauseRig {
	t.Helper()
	m, d, stops := taskMission(t)
	p := &pauseRig{m: m, stops: stops, backed: true, linked: true, glow: true}
	oldConsole, oldStart, oldSnap, oldOverlay := deskConsole, deskStart, deskSnapshot, deskOverlay
	t.Cleanup(func() { deskConsole, deskStart, deskSnapshot, deskOverlay = oldConsole, oldStart, oldSnap, oldOverlay })
	deskConsole = func(*Model) map[int]uint64 { return map[int]uint64{1500: 15000} }
	deskStart = func(pid int) string {
		if pid == 1500 {
			return "15000"
		}
		return oldStart(pid)
	}
	deskSnapshot = func(context.Context) (desktop.Snapshot, error) {
		s := d.snap()
		if p.glow {
			s.Layers = append(s.Layers, desktop.Layer{Monitor: "DP-1", Level: desktop.OverlayLevel, W: 1920, H: 1080, Namespace: desktop.PointerNamespace, PID: 1500})
		}
		return s, nil
	}
	deskOverlay = func(_ *Model, mission string) (bool, bool) {
		return p.backed && mission == "m1", p.backed && p.linked && mission == "m1"
	}
	return p
}

// click is an input call on the app window.
func (p *pauseRig) click() (string, string) {
	dv, _ := deskCall(p.m, "pointer", `{"action":"click","x":1200,"y":700}`)
	return dv.Behavior, dv.Message
}

func (p *pauseRig) notes(t *testing.T, word string) int {
	t.Helper()
	n := 0
	for _, e := range p.m.orch.chat {
		if e.role == "note" && strings.HasPrefix(e.text, "Desktop agent "+word) {
			n++
		}
	}
	return n
}

// The link dropping mid-task pauses input (looking goes on) with one note,
// and the link back with the glow resumes it with another.
func TestDesktopTaskPausesWhileTheOverlayIsDown(t *testing.T) {
	p := newPauseRig(t)
	p.m.deskMission("m1").limitActions = 100
	if b, msg := p.click(); b != "allow" {
		t.Fatalf("linked: %s %s", b, msg)
	}
	p.linked = false
	for i := 0; i < 2; i++ {
		if b, msg := p.click(); b != "deny" || !strings.Contains(msg, deskOverlayPaused) {
			t.Fatalf("link down: %s %q", b, msg)
		}
	}
	for _, c := range []struct{ tool, args string }{
		{"keyboard", `{"action":"type","text":"hi","window":"0xa"}`},
		{"launch", `{"command":"foot"}`},
	} {
		if dv, _ := deskCall(p.m, c.tool, c.args); dv.Behavior != "deny" || !strings.Contains(dv.Message, deskOverlayPaused) {
			t.Errorf("%s while paused: %+v", c.tool, dv)
		}
	}
	if dv, _ := deskCall(p.m, "screenshot", `{}`); dv.Behavior != "allow" {
		t.Errorf("a screenshot while paused: %+v", dv)
	}
	if n := p.notes(t, "paused"); n != 1 {
		t.Errorf("%d pause notes, want 1", n)
	}
	// The link is back but Den does not see the glow: still paused.
	p.linked, p.glow = true, false
	if b, _ := p.click(); b != "deny" {
		t.Fatal("resumed without the glow")
	}
	p.glow = true
	if b, msg := p.click(); b != "allow" {
		t.Fatalf("resumed: %s %q", b, msg)
	}
	if n := p.notes(t, "resumed"); n != 1 {
		t.Errorf("%d resume notes, want 1", n)
	}
	if p.m.deskMission("m1").Ended != "" {
		t.Fatal("the task ended")
	}
}

// The periodic check pauses and resumes too, with no agent call, and a
// pause past deskOverlayWait ends the task.
func TestDesktopTaskOverlayDownEndsTheTask(t *testing.T) {
	p := newPauseRig(t)
	dm := p.m.deskMission("m1")
	p.linked = false
	runBatch(p.m.finishTaskCheck(deskTaskCheckMsg{mission: "m1", glow: true}))
	if !dm.overlayPaused || p.notes(t, "paused") != 1 {
		t.Fatalf("the check did not pause: %v", dm.overlayPaused)
	}
	dm.overlayPausedAt = time.Now().Add(-deskOverlayWait)
	runBatch(p.m.finishTaskCheck(deskTaskCheckMsg{mission: "m1", glow: true}))
	if !strings.Contains(dm.Ended, "overlay was down for 5 minutes") || len(*p.stops) == 0 {
		t.Fatalf("ended %q, stops %d", dm.Ended, len(*p.stops))
	}
}

// A task the console never linked runs as before, link or glow or not.
func TestDesktopTaskWithNoOverlayIsNeverPaused(t *testing.T) {
	p := newPauseRig(t)
	p.backed, p.linked, p.glow = false, false, false
	if b, msg := p.click(); b != "allow" {
		t.Fatalf("unbacked: %s %q", b, msg)
	}
	if p.m.deskMission("m1").overlayPaused || p.notes(t, "paused") != 0 {
		t.Fatal("an unbacked task paused")
	}
}

// The human's stop works while paused.
func TestDesktopStopWhilePaused(t *testing.T) {
	p := newPauseRig(t)
	p.linked = false
	if b, _ := p.click(); b != "deny" {
		t.Fatal("not paused")
	}
	before := len(*p.stops)
	mm, _ := p.m.desktopCommand([]string{"stop"})
	*p.m = mm.(Model)
	if len(*p.stops) != before+1 || p.m.deskLive() != nil {
		t.Fatalf("stop while paused: %d stops, live %v", len(*p.stops)-before, p.m.deskLive() != nil)
	}
}
