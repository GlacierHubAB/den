package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A console lists Den's commands, completes what it types (refused ones left
// out), and runs a line that Den decides; without a proof, none of it.
func TestConsoleCommands(t *testing.T) {
	h, c := provedConsole(t)
	cmds := []CommandInfo{
		{Name: "/fleet", Usage: "/fleet [project|flat]", Help: "group the fleet", Dock: DockOK},
		{Name: "/close", Usage: "/close <name>", Help: "close agents", Dock: DockCard},
		{Name: "/quit", Usage: "/quit", Help: "quit Den", Dock: DockRefused},
		{Name: "/bogus", Dock: "whatever"},
	}
	h.s.Publish(Project(Snapshot{Taken: time.Now(), Commands: cmds}))
	var list []CommandInfo
	if l := c.call("cmd.list", nil); !l.OK || json.Unmarshal(l.Result, &list) != nil || len(list) != 3 {
		t.Fatalf("cmd.list: %+v %s", l.Error, l.Result)
	}
	var got []CommandInfo
	json.Unmarshal(c.call("cmd.complete", map[string]string{"prefix": "/"}).Result, &got)
	for _, g := range got {
		if g.Dock == DockRefused {
			t.Errorf("a refused command offered: %+v", g)
		}
	}
	if len(got) != 2 {
		t.Errorf("completions %+v", got)
	}
	json.Unmarshal(c.call("cmd.complete", map[string]string{"prefix": "\uff0f\uff26\uff2c"}).Result, &got)
	if len(got) != 1 || got[0].Name != "/fleet" {
		t.Errorf("a fullwidth prefix: %+v", got)
	}
	id := c.send("cmd.run", map[string]string{"text": "/fleet flat"})
	r := ctlAct(t, h.s, func(a Act) bool { return a.Command != nil }).Command
	if r.Text != "/fleet flat" {
		t.Fatalf("run %+v", r)
	}
	r.Reply <- CommandResult{Note: "the fleet is flat"}
	if l := c.replyTo(id); !l.OK || !strings.Contains(string(l.Result), "the fleet is flat") {
		t.Errorf("cmd.run reply %+v %s", l.Error, l.Result)
	}
	if l := c.call("cmd.run", map[string]string{"text": strings.Repeat("x", cmdTextMax+1)}); code(l) != CodeBadRequest {
		t.Errorf("an over-long line: %+v", l)
	}
	other := h.dial()
	other.hello()
	for _, op := range []string{"cmd.list", "cmd.complete", "cmd.run"} {
		if l := other.call(op, map[string]string{"text": "/fleet"}); code(l) != CodeForbidden {
			t.Errorf("%s without a proof: %+v", op, l)
		}
	}
}
