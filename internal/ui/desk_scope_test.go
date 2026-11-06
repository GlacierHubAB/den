package ui

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/api"
	"github.com/GlacierHubAB/den/internal/desktop"
	"github.com/GlacierHubAB/den/internal/inbox"
)

// listPicks runs /desktop pick as the event loop would.
func listPicks(t *testing.T, m *Model) []deskPick {
	t.Helper()
	mm, cmd := m.deskPickCmd(nil)
	*m = mm.(Model)
	for _, msg := range runBatch(cmd) {
		if p, ok := msg.(deskPicksMsg); ok {
			m.finishPicks(p)
		}
	}
	it := desktopCard(*m, inbox.DesktopMission)
	return m.desk.offers[it.ID].pickable
}

// pickAddr picks the listed window at an address with /desktop pick <n>.
func pickAddr(t *testing.T, m *Model, list []deskPick, addr string) {
	t.Helper()
	for i, p := range list {
		if p.Address == addr {
			mm, _ := m.deskPickCmd([]string{itoa(i + 1)})
			*m = mm.(Model)
			return
		}
	}
	t.Fatalf("%s is not on the list %+v", addr, list)
}

// A task is held by default; the card lists what it may pick, the pick
// carries the warning, and the mission takes the picks by address and pid.
func TestDeskHeldTaskTakesItsPicks(t *testing.T) {
	m, d, _, _, _ := taskRig(t)
	d.held = true
	runBatch(m.handleRequest(taskReq("tidy the inbox")))
	it := desktopCard(*m, inbox.DesktopMission)
	if it == nil || m.desk.offers[it.ID].wide {
		t.Fatal("no held card")
	}
	list := listPicks(t, m)
	for _, p := range list {
		if p.Address == "0xview" {
			t.Error("the agent view is offered for picking")
		}
	}
	pickAddr(t, m, list, "0xa")
	body := strings.Join(it.Body, "\n")
	if !strings.Contains(body, "Picked: zen — Inbox. "+deskPickWarning) {
		t.Errorf("the pick or its warning is missing:\n%s", body)
	}
	if it.State != inbox.Waiting {
		t.Fatal("picking answered the card")
	}
	runTask(t, m, d, "allow")
	dm := taskLive(t, *m)
	if !dm.Scoped || dm.Picked["0xa"].PID != 900 || len(dm.Picked) != 1 {
		t.Fatalf("scoped %v picked %+v", dm.Scoped, dm.Picked)
	}
}

// On a build Den hasn't checked, a held task doesn't start: the card says
// to use the whole desktop instead, or decline.
func TestDeskHeldTaskNeedsTheCheckedBuild(t *testing.T) {
	m, d, spawned, _, _ := taskRig(t)
	d.held = true
	deskScopedBuild = func(context.Context) string { return "this Hyprland build (v0.57.0) isn't the one checked" }
	runBatch(m.handleRequest(taskReq("tidy the inbox")))
	runTask(t, m, d, "allow")
	if m.deskLive() != nil || len(*spawned) != 0 {
		t.Fatal("a held task started on an unchecked build")
	}
	it := settledTask(*m)
	if it == nil || !strings.Contains(it.Resolved, "/desktop whole") {
		t.Fatalf("the card doesn't say what to do: %+v", it)
	}
	// The whole desktop on, the same build starts it, unheld.
	m2, d2, _, _, _ := taskRig(t)
	deskScopedBuild = func(context.Context) string { return "this Hyprland build (v0.57.0) isn't the one checked" }
	runBatch(m2.handleRequest(taskReq("tidy the inbox")))
	runTask(t, m2, d2, "allow")
	if dm := taskLive(t, *m2); dm.Scoped {
		t.Fatal("a whole-desktop task is held")
	}
}

// Den's own window, picked before Den found it, stops the start.
func TestDeskPickOfDensWindowIsRefused(t *testing.T) {
	m, d, spawned, _, _ := taskRig(t)
	d.held = true
	runBatch(m.handleRequest(taskReq("tidy the inbox")))
	pickAddr(t, m, listPicks(t, m), "0xden")
	runTask(t, m, d, "allow")
	if m.deskLive() != nil || len(*spawned) != 0 {
		t.Fatal("a task started with Den's own window picked")
	}
	if it := settledTask(*m); it == nil || !strings.Contains(it.Resolved, "Den's own") {
		t.Fatalf("%+v", it)
	}
}

// A picked window that closes is gone for good, even if its address
// comes back; a window of the slice is the task's.
func TestDeskHeldGate(t *testing.T) {
	m, d, _ := taskMission(t)
	dm := m.deskMission("m1")
	dm.Scoped, dm.Picked = true, map[string]desktop.Pick{"0xa": {PID: 900}}
	oldCg := deskCgroupOf
	t.Cleanup(func() { deskCgroupOf = oldCg })
	deskCgroupOf = func(pid int) string {
		if pid == 901 {
			return "/user.slice/" + dm.slice() + "/x.service"
		}
		return "/user.slice/app.slice"
	}
	if dv, _ := deskCall(m, "keyboard", `{"action":"type","text":"hi","window":"0xa"}`); dv.Behavior != "allow" {
		t.Fatalf("keys into the picked window: %+v", dv)
	}
	if dv, _ := deskCall(m, "keyboard", `{"action":"type","text":"hi","window":"0xh"}`); dv.Behavior != "deny" {
		t.Fatalf("keys into an outside window: %+v", dv)
	}
	d.appGone = true
	if dv, _ := deskCall(m, "pointer", `{"action":"move","x":1200,"y":800}`); dv.Behavior != "allow" {
		t.Fatalf("a move: %+v", dv)
	}
	if len(dm.Picked) != 0 {
		t.Fatalf("a closed pick stayed: %+v", dm.Picked)
	}
	d.appGone, d.appPID = false, 900
	if dv, _ := deskCall(m, "keyboard", `{"action":"type","text":"hi","window":"0xa"}`); dv.Behavior != "deny" {
		t.Fatalf("keys into a closed pick's address, back: %+v", dv)
	}
	// The same address on a slice process is the task's own.
	d.appPID = 901
	if dv, _ := deskCall(m, "keyboard", `{"action":"type","text":"hi","window":"0xa"}`); dv.Behavior != "allow" {
		t.Fatalf("keys into a launched window: %+v", dv)
	}
}

// The switch and the picks are the card's, in Den's window: never the
// dock's; the whole desktop drops the picks; nothing changes once started.
func TestDeskScopeOnlyFromTheCard(t *testing.T) {
	for _, c := range dockVerbs {
		if c.name != "/desktop" {
			continue
		}
		for _, a := range [][]string{{"whole"}, {"whole", "on"}, {"WHOLE"}, {"pick"}, {"pick", "1"}, {"stop", "whole"}} {
			if v, _ := c.verdict(a); v == api.DockOK {
				t.Errorf("the dock may run /desktop %v", a)
			}
		}
	}
	m, d, _, _, _ := taskRig(t)
	d.held = true
	runBatch(m.handleRequest(taskReq("tidy the inbox")))
	it := desktopCard(*m, inbox.DesktopMission)
	pickAddr(t, m, listPicks(t, m), "0xa")
	m.deskWhole(nil)
	o := m.desk.offers[it.ID]
	if !o.wide || len(o.picks) != 0 || !slices.Contains(it.Body, deskWideLine) {
		t.Fatalf("whole on: %+v %q", o, it.Body)
	}
	if mm, _ := m.deskPickCmd(nil); !mm.(Model).flashErr {
		t.Error("/desktop pick ran with the whole desktop on")
	}
	m.deskWhole([]string{"off"})
	if m.desk.offers[it.ID].wide || slices.Contains(it.Body, deskWideLine) {
		t.Fatal("whole off didn't hold it again")
	}
	runTask(t, m, d, "allow")
	dm := taskLive(t, *m)
	if m.deskWhole(nil); !dm.Scoped {
		t.Fatal("/desktop whole changed a running mission")
	}
}

// The windows a card offers: none of Den's own making, no X window, no
// console's.
func TestPickable(t *testing.T) {
	s := desktop.Snapshot{Windows: []desktop.Window{
		{Address: "0x1", Class: "zen", PID: 900, Mapped: true},
		{Address: "0x2", Class: "io.glacierhub.den.desktop", PID: 300, Mapped: true},
		{Address: "0x3", Class: "ghostty", InitialClass: "io.glacierhub.den.worker-1", PID: 300, Mapped: true},
		{Address: "0x4", Class: "ghostty", Title: "den · worker-2", PID: 300, Mapped: true},
		{Address: "0x5", Class: "xterm", PID: 901, Mapped: true, XWayland: true},
		{Address: "0x6", Class: "dock", PID: 77, Mapped: true},
		{Address: "0x7", Class: "zen", PID: 902},
	}}
	var got []string
	for _, w := range desktop.Pickable(s, map[int]bool{77: true}) {
		got = append(got, w.Address)
	}
	if strings.Join(got, ",") != "0x1" {
		t.Errorf("pickable %v, want 0x1", got)
	}
}

// settledTask is the task card once answered.
func settledTask(m Model) *inbox.Item {
	for _, it := range m.store.Items() {
		if it.Kind == inbox.DesktopMission && it.State != inbox.Waiting {
			return it
		}
	}
	return nil
}
