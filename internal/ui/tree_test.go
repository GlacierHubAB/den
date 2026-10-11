package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/theme"
	"github.com/GlacierHubAB/den/internal/tree"
)

func treeModel() Model {
	m := Model{st: newStyles(theme.Deepharness), store: inbox.New(), width: 120, height: 40, tree: newTreeState(), tab: tabFleet}
	m.base = sample()
	for i := range m.base {
		m.base[i].Session.SessionID = strings.ToLower(strings.ReplaceAll(m.base[i].Name, " ", "-"))
	}
	m.input = newInput(m.st)
	m.refresh()
	now := time.Now()
	m.learnEdge(tree.Edge{Parent: tree.Orchestrator, Child: "snug-capybara", Brief: "lead the release", At: now.Add(-time.Hour)})
	m.learnEdge(tree.Edge{Parent: "snug-capybara", Child: "mossy-lemur", Brief: "check the notes", At: now.Add(-30 * time.Minute)})
	m.learnEdge(tree.Edge{Parent: "snug-capybara", Child: "fuzzy-sprite", Brief: "check the installer", At: now.Add(-20 * time.Minute)})
	m.refresh()
	return m
}

func names(m Model) []string {
	var out []string
	for _, a := range m.agents {
		out = append(out, a.Name)
	}
	return out
}

func TestTreeOrdersChildrenUnderParents(t *testing.T) {
	m := treeModel()
	got := strings.Join(names(m), ",")
	want := "Snug Capybara,Mossy Lemur,Fuzzy Sprite,Maple Owl,Comet Newt"
	if got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
	r := m.rowFor("snug-capybara")
	if r.directs != 2 || r.depth != 1 || r.section != "orchestrator" || r.guide != "└─ " {
		t.Errorf("snug row = %+v", r)
	}
	if r := m.rowFor("fuzzy-sprite"); r.guide != "   └─ " || r.cont != "      " {
		t.Errorf("fuzzy row = %+v", r)
	}
	if r := m.rowFor("mossy-lemur"); r.guide != "   ├─ " || r.cont != "   │  " {
		t.Errorf("mossy row = %+v", r)
	}
	if d := strings.Join(m.descendants("snug-capybara"), ","); d != "mossy-lemur,fuzzy-sprite" {
		t.Errorf("descendants = %s", d)
	}
	if roll, via := m.rollupOf("snug-capybara"); roll.needsYou != 1 || via != "Mossy Lemur" {
		t.Errorf("rollup = %+v via %q", roll, via)
	}
	if ch := ansi.Strip(m.chainLine("fuzzy-sprite", true)); ch != "you › orchestrator › Snug Capybara › Fuzzy Sprite" {
		t.Errorf("chain = %q", ch)
	}
	out := ansi.Strip(m.agentsPane(60, 40))
	if !strings.Contains(out, "ORCHESTRATOR · 3") || !strings.Contains(out, "directs 2 · 1 needs you") || !strings.Contains(out, "├─ ◐ Mossy Lemur") {
		t.Errorf("agents pane:\n%s", out)
	}
}

func TestFoldHidesTheBranchAndTheCursorSkipsIt(t *testing.T) {
	m := treeModel()
	m.cursor = 0
	if !m.toggleFold(true) {
		t.Fatal("fold refused")
	}
	if !m.rowFor("mossy-lemur").hidden || !m.rowFor("fuzzy-sprite").hidden {
		t.Error("children not hidden")
	}
	m.move(1)
	if a, _ := m.selected(); a.Name != "Maple Owl" {
		t.Errorf("cursor landed on %s, want Maple Owl", a.Name)
	}
	out := ansi.Strip(m.agentsPane(60, 40))
	if strings.Contains(out, "Mossy Lemur") || !strings.Contains(out, "folded") {
		t.Errorf("folded pane still shows the branch:\n%s", out)
	}
	m.cursor = 0
	if !m.toggleFold(false) || m.rowFor("mossy-lemur").hidden {
		t.Error("unfold failed")
	}
}

func TestMapDrawsEveryNodeWithinWidth(t *testing.T) {
	m := treeModel()
	m.cursor = 2 // Fuzzy Sprite, the deepest node
	out := m.mapPane(116, 40)
	plain := ansi.Strip(out)
	for _, want := range []string{"you", "orchestrator", "Snug Capybara", "Mossy Lemur", "Fuzzy Sprite", "Maple Owl", "┌", "└"} {
		if !strings.Contains(plain, want) {
			t.Errorf("map lacks %q:\n%s", want, plain)
		}
	}
	for i, l := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(l); w > 116 {
			t.Errorf("line %d is %d wide", i, w)
		}
	}
	// A deep selection in a narrow pane scrolls sideways to it.
	narrow := ansi.Strip(m.mapPane(60, 40))
	if !strings.Contains(narrow, "Fuzzy Sprite") {
		t.Errorf("narrow map lost the selected node:\n%s", narrow)
	}
}

func TestPeerMessageMakesAnEdge(t *testing.T) {
	m := treeModel()
	m.base[1].Session.PID = 777 // Snug
	m.refresh()
	m.learnPeer("maple-owl", `<cross-session-message from="uds:/run/user/1000/cc-socks/777.sock" from-name="den-x" from-mode="prompting">
please run the tests</cross-session-message>`)
	m.refresh()
	if e := m.edgeFor("maple-owl"); e.Parent != "snug-capybara" || e.Brief != "please run the tests" {
		t.Errorf("edge = %+v", e)
	}
	// Maple replying to Snug does not put Snug under Maple.
	m.base[3].Session.PID = 778
	m.refresh()
	m.learnPeer("snug-capybara", `<cross-session-message from="uds:/run/user/1000/cc-socks/778.sock" from-name="den-y" from-mode="prompting">
done</cross-session-message>`)
	if e := m.edgeFor("snug-capybara"); e.Parent != tree.Orchestrator {
		t.Errorf("reply inverted the tree: %+v", e)
	}
}

func TestOpenRequestIsShownInFullWithIntent(t *testing.T) {
	m := treeModel()
	long := `S=/tmp/claude-1000/-home-grvlle/33503402-df6e-4023-8df1-fcd6e8e482ed/scratchpad/ser.py; python3 $S "exit\r" 2 >/dev/null; python3 $S "3\r" 4 | tail -6`
	m.store.Apply(req("PermissionRequest", "snug-capybara", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": long, "description": "Open the password reset option on the router menu"}}))
	m.refresh()
	for name, out := range map[string]string{
		"inbox":   ansi.Strip(m.inboxPane(70, 40)),
		"details": ansi.Strip(m.requestJoined("snug-capybara", 60)),
	} {
		if !strings.Contains(out, "password reset option") {
			t.Errorf("%s lacks the intent:\n%s", name, out)
		}
		if strings.Contains(out, "…") || !strings.Contains(out, "tail -6") {
			t.Errorf("%s truncated the command:\n%s", name, out)
		}
	}
}

// requestJoined renders the request block the details pane shows.
func (m Model) requestJoined(sid string, w int) string {
	it := m.openItem(sid)
	if it == nil {
		return ""
	}
	a, _ := m.agentFor(sid)
	return strings.Join(m.requestLines(it, a, w), "\n")
}

func TestSpawnsMakeEdgesUnderWhoeverStartedThem(t *testing.T) {
	m := treeModel()
	m.base[2].Session.Name = "designer" // Fuzzy
	m.base[3].Session.Name = "copy"     // Maple
	m.refresh()

	// A lead's own spawn, seen through its PostToolUse hook.
	m.learnSpawn(`Started Fuzzy Sprite (peer "designer") in ~/site on opus. It has the brief as its first prompt.`, "mossy-lemur", "", "Design the site.\nUse the design file.")
	if e := m.edgeFor("fuzzy-sprite"); e.Parent != "mossy-lemur" || e.Brief != "Design the site. …" || e.Source != "spawn" {
		t.Errorf("lead's spawn edge = %+v", e)
	}

	// The orchestrator's spawn: its note becomes the agent's card.
	now := time.Now()
	m.orch.chat = append(m.orch.chat, chatEntry{role: "note", text: "▶ starting an agent in ~/site", toolUseID: "t9", at: now})
	m.noteSpawn("t9", "Lead the website build.", "", now)
	m.learnSpawn(`Started Maple Owl (peer "copy") in ~/site on workspace 2 on opus.`, tree.Orchestrator, "t9", "")
	if e := m.edgeFor("maple-owl"); e.Parent != tree.Orchestrator || e.Brief != "Lead the website build." {
		t.Errorf("orchestrator's spawn edge = %+v", e)
	}
	last := m.orch.chat[len(m.orch.chat)-1]
	if last.role != "card" || last.card == nil || last.card.SessionID != "maple-owl" || last.card.State != cardWorking || last.card.Brief != "Lead the website build." {
		t.Fatalf("spawn note did not become a card: %+v", last)
	}

	// A session not in the fleet yet waits.
	m.learnSpawn(`Started Someone (peer "nobody") in ~/site.`, "mossy-lemur", "", "x")
	if len(m.tree.pending) != 1 || m.tree.pending[0].peer != "nobody" {
		t.Errorf("pending = %+v", m.tree.pending)
	}
}

func TestLeadCardWaitsForItsTeam(t *testing.T) {
	m := treeModel() // Snug leads Mossy (needs you) and Fuzzy (idle)
	now := time.Now()
	d := &dispatch{ID: "c1", SessionID: "snug-capybara", Agent: "Snug Capybara", Brief: "lead the release", SentAt: now, StartedAt: now, State: cardWorking}
	m.orch.chat = append(m.orch.chat, chatEntry{role: "card", card: d, at: now})
	stop := &bridge.Request{Envelope: bridge.Envelope{Event: "Stop", SessionID: "snug-capybara"}, Input: bridge.HookInput{LastAssistant: "Team started; waiting for their reports."}}
	m.trackHook(stop, nil)
	if d.State != cardWorking || d.Ticker != "waiting on 1 agent" || d.Stopped.IsZero() || d.Result == "" {
		t.Fatalf("lead's card after its turn: %+v", d)
	}
	m.settleCards()
	if d.State != cardWorking {
		t.Fatalf("settled while a teammate needs the human: %+v", d)
	}
	m.base[0].Status = fleet.Idle // Mossy done
	m.base[1].Status = fleet.Idle // Snug between turns
	m.refresh()
	m.settleCards()
	if d.State != cardDone || d.EndedAt.IsZero() || d.Result != "Team started; waiting for their reports." {
		t.Errorf("lead's card once the team is done: %+v", d)
	}
	// A plain agent's Stop still ends its card at once.
	e := &dispatch{ID: "c2", SessionID: "fuzzy-sprite", Agent: "Fuzzy Sprite", Brief: "x", SentAt: now, StartedAt: now, State: cardWorking}
	m.orch.chat = append(m.orch.chat, chatEntry{role: "card", card: e, at: now})
	m.trackHook(&bridge.Request{Envelope: bridge.Envelope{Event: "Stop", SessionID: "fuzzy-sprite"}, Input: bridge.HookInput{LastAssistant: "done"}}, nil)
	if e.State != cardDone {
		t.Errorf("plain card = %+v", e)
	}
}
