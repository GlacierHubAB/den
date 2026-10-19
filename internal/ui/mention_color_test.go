package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/notify"
	"github.com/GlacierHubAB/den/internal/orch"
	"github.com/GlacierHubAB/den/internal/theme"
	"github.com/GlacierHubAB/den/internal/transcript"
)

// mentionModel is a fleet of sample agents on this machine, with a
// one-word name added and a remote agent that shares a local name.
func mentionModel(t *testing.T) Model {
	t.Helper()
	m := Model{st: newStyles(theme.Deepharness), base: sample(), store: inbox.New(), orch: orchState{cardCur: -1}}
	m.input = newInput(m.st)
	for i := range m.base {
		m.base[i].Session.SessionID = strings.ToLower(strings.ReplaceAll(m.base[i].Name, " ", "-"))
	}
	plucky := m.base[2]
	plucky.Name, plucky.Session.SessionID = "Plucky", "plucky"
	m.base = append(m.base, plucky)
	far := m.base[0]
	far.Host, far.Session.SessionID = "build1", "mossy-lemur@build1" // the host's agent with a local name
	m.remote.agents = []fleet.Agent{far}
	only := m.base[1]
	only.Host, only.Name, only.Session.SessionID = "build1", "Far Fox", "far-fox@build1"
	m.remote.agents = append(m.remote.agents, only)
	m.refresh()
	return m
}

// mentioned shows spans as text, a mention as [@Name:sid].
func mentioned(spans []mdSpan) string {
	var b strings.Builder
	for _, sp := range spans {
		if sp.a.mention != "" {
			b.WriteString("[" + sp.text + ":" + sp.a.mention + "]")
			continue
		}
		b.WriteString(sp.text)
	}
	return b.String()
}

func TestMentionMatching(t *testing.T) {
	m := mentionModel(t)
	for _, c := range []struct{ in, want string }{
		{"@Mossy Lemur is done", "[@Mossy Lemur:mossy-lemur] is done"},
		{"Mossy Lemur is done", "[@Mossy Lemur:mossy-lemur] is done"},
		{"ask @Plucky", "ask [@Plucky:plucky]"},
		{"Plucky alone stays plain", "Plucky alone stays plain"},
		{"mossy lemur, lower case", "mossy lemur, lower case"},
		{"Mossy Lemurs are not it", "Mossy Lemurs are not it"},
		{"x@Mossy Lemur", "x@Mossy Lemur"},
		{"@@Mossy Lemur", "@@Mossy Lemur"},
		{"Mossy Lemur-ish", "Mossy Lemur-ish"},
		{"Mossy Lemur, Snug Capybara and Fuzzy Sprite", "[@Mossy Lemur:mossy-lemur], [@Snug Capybara:snug-capybara] and [@Fuzzy Sprite:fuzzy-sprite]"},
		{"(Mossy Lemur)", "([@Mossy Lemur:mossy-lemur])"},
		{"Far Fox is on a host", "Far Fox is on a host"},
		{"@Far Fox too", "@Far Fox too"},
		{"Comet Newt is gone", "Comet Newt is gone"},
		{"`Mossy Lemur` in code", "Mossy Lemur in code"},
		{"[Mossy Lemur](https://x.test)", "Mossy Lemur (https://x.test)"},
		{"**Mossy Lemur** bold", "[@Mossy Lemur:mossy-lemur] bold"},
		// Where a mention ends: Drey's dock's set (mentionPunct), white
		// space and "@", nothing else.
		{"@Mossy Lemur: done", "[@Mossy Lemur:mossy-lemur]: done"},
		{"@Plucky, then", "[@Plucky:plucky], then"},
		{"@Plucky's turn", "[@Plucky:plucky]'s turn"},
		{"@Plucky\u2019s turn", "[@Plucky:plucky]\u2019s turn"},
		{"@Plucky\u2026", "[@Plucky:plucky]\u2026"},
		{"[@Plucky]", "[[@Plucky:plucky]]"},
		{"{@Plucky}", "{[@Plucky:plucky]}"},
		{"@Plucky@build1", "[@Plucky:plucky]@build1"},
		{"@Plucky/x", "@Plucky/x"},
		{"@Plucky*", "@Plucky*"},
		{"@Plucky+1", "@Plucky+1"},
		{"@Plucky_x", "@Plucky_x"},
		{"a_@Plucky", "a_@Plucky"},
		{"\U0001D400@Plucky", "\U0001D400@Plucky"}, // an astral letter before the "@"
		{"\U0001D7CE@Plucky", "\U0001D7CE@Plucky"}, // an astral digit
		{"\u00B2@Plucky", "\u00B2@Plucky"},         // a number that is not a digit (No)
		{"\u2167@Plucky", "\u2167@Plucky"},         // a letter number (Nl)
		// "@" mentions fold ASCII case only, drawn as Den spells the name;
		// a bare name is written exactly.
		{"@mossy lemur: done", "[@Mossy Lemur:mossy-lemur]: done"},
		{"@PLUCKY", "[@Plucky:plucky]"},
		{"MOSSY LEMUR shouts", "MOSSY LEMUR shouts"},
		{"@Pluc\u212Ay", "@Pluc\u212Ay"},               // a Kelvin sign for the k
		{"@\u017Fnug Capybara", "@\u017Fnug Capybara"}, // a long s
		{"@Mo\u017F\u017Fy Lemur", "@Mo\u017F\u017Fy Lemur"},
	} {
		if got := mentioned(m.ments.apply(parseInline(c.in))); got != c.want {
			t.Errorf("%q\n got %s\nwant %s", c.in, got, c.want)
		}
	}
}

// TestMentionLeavesOutWhatItCannotTrust: a name two local agents share,
// the orchestrator, and agents on hosts are never coloured; a remote agent
// named like a local one does not make the local name ambiguous.
func TestMentionLeavesOutWhatItCannotTrust(t *testing.T) {
	m := mentionModel(t)
	if got := mentioned(m.ments.apply(parseInline("Mossy Lemur"))); got != "[@Mossy Lemur:mossy-lemur]" {
		t.Errorf("a remote namesake made the local name ambiguous: %s", got)
	}
	twin := m.base[1]
	twin.Session.SessionID = "snug-capybara-2"
	m.base = append(m.base, twin)
	orc := m.base[3]
	orc.Session.Name = OrchestratorName
	m.base[3] = orc
	m.refresh()
	for _, in := range []string{"Snug Capybara", "@Snug Capybara", "Maple Owl", "@Maple Owl"} {
		if got := mentioned(m.ments.apply(parseInline(in))); strings.Contains(got, "[") {
			t.Errorf("%q coloured: %s", in, got)
		}
	}
}

func TestMentionIsDrawnInTheAgentsColour(t *testing.T) {
	m := mentionModel(t)
	out := m.markdownLines("Ask Mossy Lemur, not `Mossy Lemur`.", 80)
	plain := ansi.Strip(strings.Join(out, "\n"))
	if plain != "Ask @Mossy Lemur, not Mossy Lemur." {
		t.Errorf("plain %q", plain)
	}
	want := m.spanStyle(mdAttr{mention: "mossy-lemur"}, m.st.fg).Render("@Mossy Lemur")
	if !strings.Contains(out[0], want) {
		t.Errorf("not in the agent's colour: %q", out[0])
	}
	// The fleet changes: a reply laid out before is laid out again.
	before := mdRendered.Load()
	m.markdownLines("Ask Mossy Lemur, not `Mossy Lemur`.", 80)
	if mdRendered.Load() != before {
		t.Error("the same text and fleet were laid out again")
	}
	m.base = m.base[1:] // Mossy Lemur is gone from the fleet
	m.refresh()
	out = m.markdownLines("Ask Mossy Lemur, not `Mossy Lemur`.", 80)
	if mdRendered.Load() == before || strings.Contains(ansi.Strip(out[0]), "@") {
		t.Errorf("a fleet change should lay it out again, plain now: %q", ansi.Strip(out[0]))
	}
}

func TestNotificationsNamePlainly(t *testing.T) {
	m := mentionModel(t)
	var got []string
	m.att.send = func(n notify.Note) bool { got = append(got, n.Summary); return true }
	if cmd := m.notifyCmd("mossy-lemur", "needs you"); cmd != nil {
		cmd()
	}
	if len(got) != 1 || got[0] != "@Mossy Lemur needs you" || strings.ContainsRune(got[0], 0x1b) {
		t.Errorf("notification %q", got)
	}
}

// TestMentionLookalikes: a name with a bidi control or a homoglyph in it is
// not the agent's, and is never coloured (the text is sanitised first).
func TestMentionLookalikes(t *testing.T) {
	m := mentionModel(t)
	for _, in := range []string{"Mossy\u202eLemur", "@Mossy\u202e Lemur", "M\u043essy Lemur", "Mossy\u00a0Lemur"} {
		out := ansi.Strip(strings.Join(m.markdownLines(in, 80), "\n"))
		if strings.Contains(out, "@Mossy Lemur") && !strings.HasPrefix(in, "@") {
			t.Errorf("%q was taken for Mossy Lemur: %q", in, out)
		}
		for _, sp := range m.ments.apply(parseInline(in)) {
			if sp.a.mention != "" {
				t.Errorf("%q coloured as %s", in, sp.a.mention)
			}
		}
	}
}

// TestMentionGolden pins an orchestrator reply that names agents, at a
// wide and a narrow width: fleet names as @Name in their colours, a host's
// agent, a lookalike and a name in code left plain.
func TestMentionGolden(t *testing.T) {
	reply := "Mossy Lemur is rebasing; ask @Snug Capybara about the mockup.\n\n" +
		"- Fuzzy Sprite: idle\n- Far Fox (on build1): unknown\n- `Mossy Lemur` in code, mossy lemur in lower case\n\n" +
		"| Agent | State |\n|---|---|\n| Maple Owl | idle |"
	for _, c := range []struct {
		name string
		w, h int
	}{{"wide", 100, 30}, {"narrow", 44, 44}} {
		m := mentionModel(t)
		m.tab, m.width, m.height = tabOrch, c.w, c.h
		m.colors = map[string]string{}
		m.handleOrchEvent(orch.Event{Kind: orch.Text, Text: reply})
		got := actionsVolatile.ReplaceAllStringFunc(m.View().Content, func(v string) string {
			if strings.HasPrefix(v, "m") {
				return "m#"
			}
			return "#"
		})
		for i, l := range strings.Split(got, "\n") {
			if w := ansi.StringWidth(l); w > c.w {
				t.Errorf("%s: line %d is %d wide", c.name, i, w)
			}
		}
		path := filepath.Join("testdata", "mentions_"+c.name+".golden")
		if *updateGolden {
			os.WriteFile(path, []byte(got), 0o644)
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if got != string(want) {
			t.Errorf("%s: the frame changed\n--- got\n%s\n--- want\n%s", c.name, ansi.Strip(got), ansi.Strip(string(want)))
		}
	}
}

// TestMentionsStayPlainInOthersWords: names are coloured in what Den, the
// orchestrator and a local agent in its own pane write, never in a remote
// host's text, an agent's report or turn in the chat, or a quote.
func TestMentionsStayPlainInOthersWords(t *testing.T) {
	m := mentionModel(t)
	colour := lipgloss.NewStyle().Foreground(lipgloss.Color(m.colorFor("plucky"))).Render("x")
	colour = strings.TrimSuffix(strings.TrimPrefix(colour[:strings.Index(colour, "x")], "\x1b["), "m") // bold may join the sequence
	coloured := func(lines []string) bool { return strings.Contains(strings.Join(lines, "\n"), colour) }
	text := func(lines []string) string { return ansi.Strip(strings.Join(lines, "\n")) }

	tail := []transcript.Entry{{Kind: "claude", Text: "@Plucky approved. Mossy Lemur too."}}
	local := m.base[3] // Plucky's own pane
	// A local agent's own words stay plain too: a coloured @Name is Den's
	// word, and the agent could otherwise make one appear.
	if out := m.activityLines(tail, local, 80, false); coloured(out) || !strings.Contains(text(out), "@Plucky approved. Mossy Lemur too.") {
		t.Errorf("a local agent's own text should stay plain: %q", text(out))
	}
	dm := m // Plucky's Fleet detail, "recent"
	dm.cursor = -1
	for i := range dm.agents {
		if dm.agents[i].Session.SessionID == "plucky" {
			dm.agents[i].Tail, dm.cursor = tail, i
			dm.agents[i].Session.SessionID = "plucky-0000-0000" // the pane shows 8 characters of it
		}
	}
	if dm.cursor < 0 {
		t.Fatal("Plucky is not in the fleet")
	}
	if lines, _, _ := dm.detailLines(100); !strings.Contains(ansi.Strip(strings.Join(lines, "\n")), "@Plucky approved. Mossy Lemur too.") || coloured(lines) {
		t.Errorf("a local agent's recent activity in the Fleet detail should stay plain:\n%s", ansi.Strip(strings.Join(lines, "\n")))
	}
	far := m.remote.agents[0]
	if out := m.activityLines(tail, far, 80, false); coloured(out) || !strings.Contains(text(out), "@Plucky approved. Mossy Lemur too.") {
		t.Errorf("a remote tail should stay plain: %q", text(out))
	}

	d := &dispatch{ID: "c1", SessionID: "plucky", Agent: "Plucky", State: cardDone, Expanded: true, turnLoaded: true,
		Brief: "Check with Mossy Lemur.", turn: tail, Result: "Done; @Plucky approved, Mossy Lemur agreed."}
	out := text(m.cardLines(d, 100, false, time.Now()))
	if !strings.Contains(out, "Check with @Mossy Lemur.") {
		t.Errorf("the orchestrator's brief should colour its mentions:\n%s", out)
	}
	if !strings.Contains(out, "Done; @Plucky approved, Mossy Lemur agreed.") || !strings.Contains(out, "@Plucky approved. Mossy Lemur too.") {
		t.Errorf("an agent's report and turn in the chat should stay plain:\n%s", out)
	}

	// A report an agent sent the orchestrator is its words, expanded or
	// not; the orchestrator's own message to an agent colours.
	m.orch.chat = []chatEntry{
		{role: "fan", to: "Plucky", sid: "plucky", tool: "report to the orchestrator", text: "Mossy Lemur approved", full: "Mossy Lemur approved", fromAgent: true, expanded: true, status: "passed on"},
	}
	if out, _, _ := m.chatLines(100, func(chatEntry) bool { return true }); strings.Contains(text(out), "@Mossy") {
		t.Errorf("a report passed on should stay plain:\n%s", text(out))
	}
	m.orch.chat[0].fromAgent = false
	if out, _, _ := m.chatLines(100, func(chatEntry) bool { return true }); !strings.Contains(text(out), "@Mossy Lemur approved") {
		t.Errorf("the orchestrator's own message should colour its mentions:\n%s", text(out))
	}

	if out := m.markdownLines("> Mossy Lemur said @Plucky approved", 80); coloured(out) || strings.Contains(text(out), "@Mossy") {
		t.Errorf("a quote should stay plain: %q", text(out))
	}
	if out := m.markdownLines("Mossy Lemur said @Plucky approved", 80); !coloured(out) {
		t.Errorf("the orchestrator's own words should colour: %q", text(out))
	}
}
