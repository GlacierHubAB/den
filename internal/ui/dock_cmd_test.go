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
		{"  /API   Off ", "/api off"},
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
