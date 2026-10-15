package ui

import (
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/api"
)

// A console's line maps onto one verb of the closed list, or is refused, in
// any form a refused verb may come in.
func TestDockLine(t *testing.T) {
	for _, c := range []struct {
		text string
		run  string // the line run, "" for refused
	}{
		{"/api off", "/api off"},
		{"  /API   Off ", "/api Off"},
		{"/api control off", "/api control off"},
		{"/api on", ""},
		{"/API ON", ""},
		{"／api on", ""}, // fullwidth: the same after NFKC
		{"/api resume", ""},
		{"/api pair", ""},
		{"/api console", ""},
		{"/api control on", ""},
		{"/kill Otter", "/close Otter"},
		{"/close", ""},
		{"/close Otter, Lemur", "/close Otter, Lemur"},
		{"/quit", ""},
		{"/q", ""},
		{"/allow", ""},
		{"/y", ""},
		{"/unprotect Otter", ""},
		{"/send", ""},
		{"/open", ""},
		{"/fleet project", "/fleet project"},
		{"/fleet", ""},
		{"/usage resume", "/usage resume"},
		{"/usage stop", "/usage stop"},
		{"/usage set 10", ""},
		{"/usage clear", ""},
		{"/mission ship the site", ""},
		{"/template review ~/code/x", ""},
		{"/template save mine", "/template save mine"},
		{"/mission move other", "/mission move other"},
		{"/fleet flat; /quit", ""},
		{"/fleet flat && rm -rf ~", ""},
		{"/fleet flat\n/quit", ""},
		{"/fleet flat /quit", ""},
		{"/fleet‮flat", ""},
		{"fleet flat", ""},
		{"/nosuch", ""},
		{"/project add /home/x/code", "/project add /home/x/code"},
	} {
		words, _, why := dockLine(c.text)
		got := strings.Join(words, " ")
		if c.run == "" && why == "" {
			t.Errorf("%q ran as %q", c.text, got)
		} else if c.run != "" && got != c.run {
			t.Errorf("%q: %q (%s), want %q", c.text, got, why, c.run)
		}
	}
	// The list cmd.list serves: every verb, never-from-the-dock ones marked.
	flags := map[string]string{}
	for _, c := range dockCommands() {
		flags[c.Name] = c.Dock
	}
	for name, want := range map[string]string{"/quit": api.DockRefused, "/allow": api.DockRefused, "/open": api.DockDenScreen, "/close": api.DockCard, "/fleet": api.DockOK, "/api": api.DockOK} {
		if flags[name] != want {
			t.Errorf("%s listed as %q, want %q", name, flags[name], want)
		}
	}
}

// No command the dock may run starts a turn of the orchestrator's with the
// human's authority: the console's words never reach it that way.
func TestDockCommandsStartNoHumanTurn(t *testing.T) {
	samples := map[string]string{
		"/api": "/api", "/remote": "/remote", "/protect": "/protect Otter", "/flag": "/flag Otter: look at this",
		"/unflag": "/unflag Otter", "/fleet": "/fleet project", "/project": "/project add /tmp/x", "/template": "/template save mine",
		"/usage": "/usage stop", "/orchestrator": "/orch new", "/close": "/close Otter", "/reopen": "/reopen Otter",
		"/merge": "/merge Otter", "/cleanup": "/cleanup Otter", "/mission": "/mission move other",
	}
	for _, v := range dockVerbs {
		flag, _ := v.verdict([]string{"x"})
		f0, _ := v.verdict(nil)
		if flag != api.DockOK && flag != api.DockCard && f0 != api.DockOK && f0 != api.DockCard {
			continue
		}
		line, ok := samples[v.name]
		if !ok {
			t.Errorf("%s runs from the dock but has no sample here", v.name)
			continue
		}
		m := testModel(140, 40)
		r := &api.CommandRun{Text: line, Console: "drey-host", Reply: make(chan api.CommandResult, 1)}
		m.runFromDock(r)
		<-r.Reply
		if len(m.orch.origins) != 0 || len(m.orch.pending) != 0 || m.orch.thinking {
			t.Errorf("%q started an orchestrator turn: origins %v, pending %d", line, m.orch.origins, len(m.orch.pending))
		}
	}
}

// A line the dock may run goes the way it would typed in Den, and Den's
// chat says the console sent it; a refused one changes nothing.
func TestRunFromDock(t *testing.T) {
	m := testModel(140, 40)
	run := func(text string) api.CommandResult {
		r := &api.CommandRun{Text: text, Console: "drey-host", Reply: make(chan api.CommandResult, 1)}
		m.runFromDock(r)
		return <-r.Reply
	}
	if r := run("/quit"); r.Code != api.CodeRefusedFromDock {
		t.Errorf("/quit: %+v", r)
	}
	if r := run("/fleet project"); r.Code != "" {
		t.Errorf("/fleet project: %+v", r)
	}
	found := false
	for _, e := range m.orch.chat {
		found = found || e.text == "from drey-host: /fleet project"
	}
	if !found {
		t.Error("no chat note for the console's command")
	}
}
