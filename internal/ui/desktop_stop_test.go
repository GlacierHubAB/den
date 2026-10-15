package ui

import (
	"encoding/json"
	"strings"
	"testing"

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
