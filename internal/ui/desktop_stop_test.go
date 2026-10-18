package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/desktop"
	"github.com/GlacierHubAB/den/internal/notify"
)

// stopRig records what the stop was asked to do instead of signalling.
func stopRig(t *testing.T) (*Model, *[]desktop.Targets, *[]string) {
	t.Helper()
	m := deskRig(t)
	deskStarted(t, &m)
	oldStop, oldNotify, oldClose := deskStopFn, deskNotify, deskClose
	t.Cleanup(func() { deskStopFn, deskNotify, deskClose = oldStop, oldNotify, oldClose })
	var stops []desktop.Targets
	deskStopFn = func(tg desktop.Targets, _ string) desktop.StopReport {
		stops = append(stops, tg)
		return desktop.StopReport{Stopped: []string{"hypruse", "the agent"}}
	}
	deskNotify = func(notify.Note) {}
	var closed []string
	deskClose = func(addr string) error {
		closed = append(closed, addr)
		return nil
	}
	return &m, &stops, &closed
}

func TestDesktopStopCommand(t *testing.T) {
	m, stops, closed := stopRig(t)
	mm, _ := m.desktopCommand([]string{"stop"})
	*m = mm.(Model)
	if len(*stops) != 1 || (*stops)[0].Child.PID != 700 || (*stops)[0].Proxy.PID != 600 || (*stops)[0].Agent.PID != 500 {
		t.Fatalf("stop targets %+v", *stops)
	}
	if len(*closed) != 2 {
		t.Fatalf("closed %v", *closed)
	}
	if d, _ := deskCall(m, "desktop", `{}`); d.Behavior != "deny" {
		t.Fatalf("a call after the stop: %+v", d)
	}
	if m.deskLive() != nil {
		t.Fatal("still live")
	}
}

// A stop that could not signal the agent never reads as done: the mission
// stays live with its chip red, every call is refused, the human is told
// loudly once, and the tick's retry, once the process is gone, ends it and
// closes Den's windows.
func TestDesktopStopThatFailsKeepsTheMissionOn(t *testing.T) {
	m, _, closed := stopRig(t)
	failing := true
	deskStopFn = func(desktop.Targets, string) desktop.StopReport {
		if failing {
			return desktop.StopReport{Failed: []string{"the agent (pid 500: pid 500 is another process now)"}}
		}
		return desktop.StopReport{}
	}
	var notes []notify.Note
	deskNotify = func(n notify.Note) { notes = append(notes, n) }
	mm, _ := m.desktopCommand([]string{"stop"})
	*m = mm.(Model)
	dm := m.deskLive()
	if dm == nil {
		t.Fatal("a failed stop ended the mission")
	}
	if m.desk.chip != deskStopFailedChip {
		t.Errorf("chip %q", m.desk.chip)
	}
	if d, _ := deskCall(m, "desktop", `{}`); d.Behavior != "deny" {
		t.Errorf("a call after a failed stop: %+v", d)
	}
	if len(notes) != 1 || !notes[0].Urgent || !strings.Contains(notes[0].Summary, "NOT stopped") {
		t.Errorf("notes %+v", notes)
	}
	if len(*closed) != 0 {
		t.Errorf("closed windows before the agent stopped: %v", *closed)
	}
	// Still failing: the tick keeps it on, red, and says nothing more.
	m.deskOnTick()
	if m.deskLive() == nil || m.desk.chip != deskStopFailedChip || len(notes) != 1 {
		t.Fatalf("after a failing retry: live %v chip %q notes %d", m.deskLive() != nil, m.desk.chip, len(notes))
	}
	// The process is gone: the next retry ends it, as the human's stop.
	failing = false
	m.deskOnTick()
	if m.deskLive() != nil || dm.Ended != "you stopped it with /desktop stop" {
		t.Fatalf("not ended after the process went: %q", dm.Ended)
	}
	if len(*closed) != 2 {
		t.Errorf("Den's windows not closed after the retry: %v", *closed)
	}
	m.deskOnTick()
	if m.desk.chip != "" {
		t.Errorf("chip after the end: %q", m.desk.chip)
	}
}

// The task's limits end a mission through the same stop, and a failed one
// keeps it on too.
func TestDesktopFinishThatFailsKeepsTheMissionOn(t *testing.T) {
	m, _, _ := stopRig(t)
	deskStopFn = func(desktop.Targets, string) desktop.StopReport {
		return desktop.StopReport{Failed: []string{"the agent (pid 500: the signal failed)"}}
	}
	dm := m.deskLive()
	if cmd := m.finishDeskMission(dm, "the task's time ran out"); cmd != nil || dm.Ended != "" || dm.stopFailed == "" {
		t.Fatalf("ended %q, failed %q", dm.Ended, dm.stopFailed)
	}
	if d, _ := deskCall(m, "desktop", `{}`); d.Behavior != "deny" {
		t.Errorf("a call after a failed finish: %+v", d)
	}
}

// den desktop stop over den.sock is never refused, whoever asks.
func TestDesktopStopFromSocket(t *testing.T) {
	m, stops, _ := stopRig(t)
	r := bridge.TestRequest(bridge.Envelope{Event: bridge.DesktopStopEvent})
	r.PeerPID = 12345
	runBatch(m.handleRequest(r))
	if d, done := r.Decided(); !done || d.Behavior != "allow" || len(*stops) != 1 {
		t.Fatalf("got %+v, stops %d", d, len(*stops))
	}
}

// The agent's Stop hook ends the mission with its report; the windows stay.
func TestDesktopAgentFinishes(t *testing.T) {
	m, stops, closed := stopRig(t)
	payload, _ := json.Marshal(map[string]any{"session_id": "sess-desk", "hook_event_name": "Stop", "last_assistant_message": "Wrote the note."})
	r := bridge.TestRequest(bridge.Envelope{Event: "Stop", SessionID: "sess-desk", Payload: payload})
	runBatch(m.handleRequest(r))
	dm := m.deskMission("m1")
	if dm.Ended == "" || dm.report != "Wrote the note." || len(*stops) != 1 {
		t.Fatalf("mission %+v stops %d", dm, len(*stops))
	}
	if len(*closed) != 0 {
		t.Fatalf("a finished mission's windows were closed: %v", *closed)
	}
}

// The agent's process gone ends the mission on the next tick.
func TestDesktopTickNoticesAgentGone(t *testing.T) {
	m, stops, _ := stopRig(t)
	m.deskOnTick()
	if !strings.Contains(m.desk.chip, "DESKTOP") {
		t.Fatalf("chip %q", m.desk.chip)
	}
	old := deskStart
	deskStart = func(pid int) string {
		if pid == 500 {
			return "9999" // another process now
		}
		return old(pid)
	}
	m.deskOnTick()
	if m.deskLive() != nil || len(*stops) != 1 {
		t.Fatal("the mission outlived its agent")
	}
	m.deskOnTick()
	if m.desk.chip != "" {
		t.Fatalf("chip after the end %q", m.desk.chip)
	}
}

// An agent whose desktop server never calls in does not hold the mission
// slot: the mission ends after deskStartWait and its windows close.
func TestDesktopAgentThatNeverStarts(t *testing.T) {
	m, _, closed := stopRig(t)
	dm := m.deskMission("m1")
	dm.AgentPID, dm.AgentStart = 0, ""
	dm.launchedAt = time.Now()
	m.deskOnTick()
	if m.deskLive() == nil {
		t.Fatal("ended before its time")
	}
	dm.launchedAt = time.Now().Add(-deskStartWait - time.Second)
	m.deskOnTick()
	if m.deskLive() != nil || len(*closed) != 2 {
		t.Fatalf("live %v, closed %v", m.deskLive() != nil, *closed)
	}
}

func TestDesktopStopIsADockCommand(t *testing.T) {
	for _, v := range dockVerbs {
		if v.name == "/desktop" {
			if flag, _ := v.verdict([]string{"stop"}); flag != "ok" {
				t.Fatalf("dock flag %s", flag)
			}
			return
		}
	}
	t.Fatal("no /desktop dock command")
}

// An ended mission's journal, crops and log are deleted after deskKeepFor,
// unless the human keeps them.
func TestDesktopPrivateFilesPurged(t *testing.T) {
	m, _, _ := stopRig(t)
	dm := m.deskMission("m1")
	for _, f := range []string{"journal.ndjson", "desktop.log", "crop-i1.png", "settings.json"} {
		os.WriteFile(filepath.Join(dm.Dir, f), []byte("x"), 0o600)
	}
	mm, _ := m.desktopCommand([]string{"stop"})
	*m = mm.(Model)
	if !m.deskPurge(time.Now()) {
		t.Fatal("nothing waits after the stop")
	}
	if _, err := os.Stat(filepath.Join(dm.Dir, "journal.ndjson")); err != nil {
		t.Fatal("deleted before its time")
	}
	m.deskPurge(time.Now().Add(deskKeepFor + time.Second))
	for _, f := range []string{"journal.ndjson", "desktop.log", "crop-i1.png"} {
		if _, err := os.Stat(filepath.Join(dm.Dir, f)); err == nil {
			t.Errorf("%s survived", f)
		}
	}
	if _, err := os.Stat(filepath.Join(dm.Dir, "settings.json")); err != nil {
		t.Error("the settings file is not one of the private files")
	}

	m2, _, _ := stopRig(t)
	dm2 := m2.deskMission("m1")
	os.WriteFile(filepath.Join(dm2.Dir, "journal.ndjson"), []byte("x"), 0o600)
	mm, _ = m2.desktopCommand([]string{"stop"})
	*m2 = mm.(Model)
	mm, _ = m2.desktopCommand([]string{"keep"})
	*m2 = mm.(Model)
	m2.deskPurge(time.Now().Add(deskKeepFor + time.Second))
	if _, err := os.Stat(filepath.Join(dm2.Dir, "journal.ndjson")); err != nil {
		t.Fatal("a kept journal was deleted")
	}
}
