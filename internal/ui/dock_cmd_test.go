package ui

import (
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/api"
	"github.com/GlacierHubAB/den/internal/inbox"
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
		{"/API CONTROL OFF", "/api control off"},
		{"/remote RETRY Devbox", "/remote retry Devbox"},
		{"/close Otter", "/close Otter"},
		{"/flag Otter: Look", "/flag Otter: Look"},
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
		{"/usage set 10", "/usage set 10"},
		{"/usage clear", "/usage clear"},
		{"/usage nosuch", ""},
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
	// The names as pinned for consoles: "/" and lower-case letters.
	for _, c := range dockCommands() {
		if len(c.Name) < 2 || c.Name[0] != '/' || strings.TrimLeft(c.Name[1:], "abcdefghijklmnopqrstuvwxyz") != "" {
			t.Errorf("command name %q", c.Name)
		}
	}
	for name, want := range map[string]string{"/quit": api.DockRefused, "/allow": api.DockRefused, "/open": api.DockDenScreen, "/close": api.DockCard, "/fleet": api.DockOK, "/api": api.DockOK} {
		if flags[name] != want {
			t.Errorf("%s listed as %q, want %q", name, flags[name], want)
		}
	}
}

// No command that settles a card by typing runs from the dock, in any raw
// form: a card is answered only with card.answer, its hash and its nonce.
func TestDockRefusesCardSettlingCommands(t *testing.T) {
	forms := []string{"/allow", "/y", "/yes", "/deny", "/n", "/no", "/always", "/a", "/answer"}
	for _, f := range forms {
		for _, line := range []string{f, strings.ToUpper(f), " " + f + " ", "／" + f[1:], f + " Otter", f + " 1", "/answer yes"} {
			if words, _, why := dockLine(line); why == "" {
				t.Errorf("%q ran as %q", line, words)
			}
		}
	}
	listed := map[string]string{}
	for _, c := range dockCommands() {
		listed[c.Name] = c.Dock
	}
	for _, name := range []string{"/allow", "/deny", "/always", "/answer"} {
		if listed[name] != api.DockRefused {
			t.Errorf("%s listed as %q", name, listed[name])
		}
	}
}

// A judged word in upper case runs as the lower-case one, or is refused:
// what runs is what the verdict checked.
func TestDockLineCaseOfJudgedWords(t *testing.T) {
	for _, line := range []string{
		"/api off", "/api disconnect", "/api unpair", "/api disallow 2", "/api control off", "/remote retry h", "/fleet project",
		"/fleet flat", "/mission move other", "/template save mine", "/usage stop", "/usage resume", "/usage set 10",
		"/usage clear", "/orch new", "/orch restart",
	} {
		low, _, why := dockLine(line)
		if why != "" {
			t.Fatalf("%q refused: %s", line, why)
		}
		f := strings.Fields(line)
		f[1] = strings.ToUpper(f[1])
		if f[1] == "CONTROL" {
			f[2] = strings.ToUpper(f[2])
		}
		up, _, why := dockLine(strings.Join(f, " "))
		if why == "" && strings.Join(up, " ") != strings.Join(low, " ") {
			t.Errorf("%q upper-cased runs as %q, not %q", line, up, low)
		}
	}
}

// A console's /usage set or clear waits on a card; ^Y there takes it.
func TestDockUsageFigureOnACard(t *testing.T) {
	m := testModel(140, 40)
	r := &api.CommandRun{Text: "/usage set 10", Console: "app-host", Reply: make(chan api.CommandResult, 1)}
	m.runFromDock(r)
	if res := <-r.Reply; res.Code != "" {
		t.Fatalf("/usage set from the dock: %+v", res)
	}
	if m.use.st.SetPercent != nil {
		t.Fatal("the figure was taken without a card")
	}
	var it *inbox.Item
	for _, x := range m.store.Items() {
		if x.Kind == inbox.UsageSet && x.Answerable() {
			it = x
		}
	}
	if it == nil {
		t.Fatal("no card for the figure")
	}
	text := strings.Join(it.Body, "\n")
	for _, want := range []string{"your figure now: none", "the figure sent: 10%", "A lower figure can hold off your usage stop."} {
		if !strings.Contains(text, want) {
			t.Errorf("the card says %q, not %q", text, want)
		}
	}
	if m.dockFrom != "" {
		t.Error("dockFrom outlived the command")
	}
	mm, _ := m.answerItem(it, "allow")
	m = mm.(Model)
	if m.use.st.SetPercent == nil || *m.use.st.SetPercent != 10 || it.State != inbox.Resolved {
		t.Errorf("after ^Y: figure %v, state %v", m.use.st.SetPercent, it.State)
	}
	// Typed in Den, it is the human's own: no card.
	mm, _ = m.usageCommand([]string{"clear"})
	m = mm.(Model)
	if m.use.st.SetPercent != nil {
		t.Error("the human's /usage clear asked on a card")
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
		r := &api.CommandRun{Text: line, Console: "app-host", Reply: make(chan api.CommandResult, 1)}
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
		r := &api.CommandRun{Text: text, Console: "app-host", Reply: make(chan api.CommandResult, 1)}
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
		found = found || e.text == "from app-host: /fleet project"
	}
	if !found {
		t.Error("no chat note for the console's command")
	}
}
