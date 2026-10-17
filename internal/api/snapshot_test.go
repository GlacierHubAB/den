package api

import (
	"encoding/json"
	"testing"
	"time"
)

// On console.events, Den first says where every stateful kind stands, in
// a fixed order, before any change: the console, each waiting card as
// new, its powers, God mode, the permission mode, the view, the desktop;
// only then what changes. No client has to infer a state from the order of changes.
func TestConsoleEventsSnapshotFirst(t *testing.T) {
	for _, locked := range []bool{false, true} {
		h, _ := consoleHarness(t)
		h.s.Publish(Project(Snapshot{Taken: time.Now(), View: ViewAgent, Mode: ModeInfo{Global: "plan"},
			Cards: []Card{card("w-1", 1, true)}}))
		h.s.SetGod(true, time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC))
		h.s.pushView(ViewChange{View: ViewAgent, Shown: 3, Total: 4})
		d := newDock(t, h)
		p := d.pair(h.s.StartConsoleCode())
		if locked {
			h.s.LockConsolePowers("a test")
		}
		c := h.dial()
		c.hello()
		if l, _, _, _ := d.auth(c, p); !l.OK {
			t.Fatalf("auth %+v", l.Error)
		}
		c.call("console.events", nil)
		var con, pw ConsoleEvent
		var god God
		var mode ModeInfo
		var view ViewChange
		var cd CardEvent
		var desk Desktop
		var mir MirrorState
		for i, want := range []struct {
			event string
			into  any
		}{
			{EventConsole, &con}, {EventCard, &cd}, {EventConsole, &pw}, {EventGod, &god}, {EventMode, &mode}, {EventView, &view}, {EventDesktop, &desk}, {EventMirror, &mir},
		} {
			l := c.next()
			if l.Event != want.event || json.Unmarshal(l.Data, want.into) != nil {
				t.Fatalf("locked %v: event %d is %q %s, want %q", locked, i, l.Event, l.Data, want.event)
			}
		}
		if con.Kind != "connected" || pw.Kind != "powers" || pw.On == nil || *pw.On == locked {
			t.Errorf("locked %v: console %+v, powers %+v", locked, con, pw)
		}
		if !god.On || god.Since != "2026-10-16T09:00:00Z" || mode.Global != "plan" || view != (ViewChange{View: ViewAgent, Shown: 3, Total: 4}) || cd.ID != "w-1" || cd.Change != "new" || desk != (Desktop{State: DesktopIdle}) || mir.State != MirrorOff || mir.Since != nil {
			t.Errorf("locked %v: god %+v, mode %+v, view %+v, card %+v, desktop %+v", locked, god, mode, view, cd, desk)
		}
		// Then changes only.
		h.s.SetGod(false, time.Time{})
		if l := c.next(); l.Event != EventGod || json.Unmarshal(l.Data, &god) != nil || god.On {
			t.Errorf("locked %v: the change %+v %s", locked, l, l.Data)
		}
	}
}
