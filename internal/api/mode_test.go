package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/fleet"
)

// The global mode reaches hello, the fleet (with each local agent's own
// reported mode, an unknown one as "unknown") and, on a change, the
// console's mode event.
func TestModeOnTheWire(t *testing.T) {
	h, c := provedConsole(t)
	agents := []fleet.Agent{
		{Session: discover.Session{SessionID: "s1", Alive: true}, Name: "ui"},
		{Session: discover.Session{SessionID: "s2", Alive: true}, Name: "api"},
		{Session: discover.Session{SessionID: "s3", Alive: true}, Name: "db"},
		{Session: discover.Session{SessionID: "s4", Alive: true}, Name: "far", Host: "box"},
	}
	modes := map[string]string{"s1": "plan", "s2": "yolo\x1b[31m", "s4": "auto"}
	h.s.Publish(Project(Snapshot{Taken: time.Now(), Agents: agents, Modes: modes, Mode: ModeInfo{Global: "default", Mixed: true}}))
	if e := c.next(); e.Event != EventMode || string(e.Data) != `{"global":"default","mixed":true}` {
		t.Fatalf("mode event %+v %s", e, e.Data)
	}
	other := h.dial()
	l := other.call("hello", map[string]any{"v": 1})
	var info Info
	if json.Unmarshal(l.Result, &info) != nil || info.Mode == nil || *info.Mode != (ModeInfo{Global: "default", Mixed: true}) {
		t.Errorf("hello %s", l.Result)
	}
	var f Fleet
	json.Unmarshal(other.call("fleet.snapshot", nil).Result, &f)
	if f.Mode == nil || f.Mode.Global != "default" || !f.Mode.Mixed {
		t.Errorf("fleet mode %+v", f.Mode)
	}
	want := map[string]string{"ui": "plan", "api": "unknown", "db": "", "far": ""}
	for _, a := range f.Agents {
		if a.PermissionMode != want[a.Name] {
			t.Errorf("%s: mode %q, want %q", a.Name, a.PermissionMode, want[a.Name])
		}
	}
	// No change, no event: the next line is the reply to a call.
	h.s.Publish(Project(Snapshot{Taken: time.Now(), Agents: agents, Modes: modes, Mode: ModeInfo{Global: "default", Mixed: true}}))
	if l := c.call("cmd.list", nil); !l.OK {
		t.Errorf("cmd.list %+v", l.Error)
	}
	h.s.Publish(Project(Snapshot{Taken: time.Now(), Mode: ModeInfo{Global: "plan"}}))
	if e := c.next(); e.Event != EventMode || string(e.Data) != `{"global":"plan","mixed":false}` {
		t.Errorf("mode event after a change %+v %s", e, e.Data)
	}
}

// mode.set takes a mode on the ranked line, spelled as Claude Code spells
// it, from a proved console only; Den decides, and the reply says whether
// it applied or waits on a card.
func TestModeSet(t *testing.T) {
	h, c := provedConsole(t)
	for _, bad := range []any{
		map[string]string{"mode": "bypassPermissions"},
		map[string]string{"mode": "Auto"},
		map[string]string{"mode": "accept-edits"},
		map[string]string{"mode": ""},
		map[string]int{"mode": 1},
	} {
		if l := c.call("mode.set", bad); code(l) != CodeBadRequest {
			t.Errorf("%v: %+v", bad, l)
		}
	}
	id := c.send("mode.set", map[string]string{"mode": "plan"})
	r := ctlAct(t, h.s, func(a Act) bool { return a.ModeSet != nil }).ModeSet
	if r.Mode != "plan" || r.Console == "" {
		t.Fatalf("mode.set reached Den as %+v", r)
	}
	r.Reply <- ModeSetResult{Applied: true, Note: "agents start in plan"}
	if l := c.replyTo(id); !l.OK || !strings.Contains(string(l.Result), `"applied":true`) {
		t.Errorf("a lowering's reply %+v %s", l.Error, l.Result)
	}
	id = c.send("mode.set", map[string]string{"mode": "auto"})
	r = ctlAct(t, h.s, func(a Act) bool { return a.ModeSet != nil }).ModeSet
	r.Reply <- ModeSetResult{Card: "i-9", Note: "it waits on a card"}
	if l := c.replyTo(id); !l.OK || !strings.Contains(string(l.Result), `"card":"i-9"`) || strings.Contains(string(l.Result), "applied") {
		t.Errorf("a raise's reply %+v %s", l.Error, l.Result)
	}
	id = c.send("mode.set", map[string]string{"mode": "auto"})
	r = ctlAct(t, h.s, func(a Act) bool { return a.ModeSet != nil }).ModeSet
	r.Reply <- ModeSetResult{Code: CodeUnavailable, Note: "no"}
	if l := c.replyTo(id); code(l) != CodeUnavailable {
		t.Errorf("a refusal %+v", l)
	}
	other := h.dial()
	other.hello()
	if l := other.call("mode.set", map[string]string{"mode": "plan"}); code(l) != CodeForbidden {
		t.Errorf("mode.set without a proof: %+v", l)
	}
}
