package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/api"
	"github.com/GlacierHubAB/den/internal/mission"
	"github.com/GlacierHubAB/den/internal/transcript"
)

// screenMission is a running mission with criteria in each state and
// sub-goals in each state, owned by the sample fleet.
func screenMission(m *Model) *mission.Mission {
	now := time.Now()
	ms := &mission.Mission{ID: "m-drey", Project: m.proj.cur, Name: "Drey not recognized in Den", Mission: "Den recognises Drey's console as soon as it pairs.", Shape: mission.Once, State: mission.Proposed, Created: now, Updated: now}
	ms.SetCriteria([]string{"The cause is explained", "A fix is merged on dev", "The human pairs Drey"})
	ms.AddGoal(mission.SubGoal{Text: "Find the cause", Owner: "snug-capybara", Name: "Snug Capybara", State: mission.GoalDone})
	ms.AddGoal(mission.SubGoal{Text: "Fix the pairing check", Owner: "mossy-lemur", Name: "Mossy Lemur", State: mission.GoalDoing})
	ms.AddGoal(mission.SubGoal{Text: "Release note", Owner: "fuzzy-sprite", Name: "Fuzzy Sprite", State: mission.GoalBlocked, Note: "waits for #97"})
	ms.AddGoal(mission.SubGoal{Text: "Docs for the console", State: mission.GoalTodo})
	ms.State = mission.Running
	ms.MeetAll(now)
	ms.SetMet("c2", false, now)
	ms.SetMet("c3", false, now)
	ms.Claim("c2", "PR #97 merged", now)
	ms.StartRun(now)
	m.missions.Save(ms)
	return ms
}

// boxed is every line of a missions pane of inner width w, plain.
func boxed(lines []string) []string {
	var out []string
	for _, l := range lines {
		out = append(out, ansi.Strip(l))
	}
	return out
}

// checkMargins: no glyph in a pane's first cell or its last two, on any
// line (the band's background is fine: it is drawn with spaces).
func checkMargins(t *testing.T, what string, lines []string, w int) {
	t.Helper()
	for i, l := range boxed(lines) {
		if l == "" {
			continue
		}
		r := []rune(l)
		if ansi.StringWidth(l) > w {
			t.Errorf("%s line %d is %d wide (pane %d): %q", what, i, ansi.StringWidth(l), w, l)
			continue
		}
		if r[0] != ' ' {
			t.Errorf("%s line %d starts at the border: %q", what, i, l)
		}
		padded := l + strings.Repeat(" ", w-ansi.StringWidth(l))
		if strings.TrimSpace(padded[len(padded)-2:]) != "" {
			t.Errorf("%s line %d reaches the right border: %q", what, i, l)
		}
	}
}

func TestMissionListSelectionFollowsTheMission(t *testing.T) {
	m := missionModel()
	now := time.Now()
	a := &mission.Mission{ID: "ma", Project: m.proj.cur, Name: "Alpha", Mission: "a", State: mission.Running, Created: now.Add(-2 * time.Hour), Updated: now}
	b := &mission.Mission{ID: "mb", Project: m.proj.cur, Name: "Bravo", Mission: "b", State: mission.Running, Created: now.Add(-time.Hour), Updated: now}
	m.missions.Save(a)
	m.missions.Save(b)
	m.moveMission(1) // Bravo is newest, so first; down selects Alpha
	if got := m.ovMission(); got == nil || got.ID != "ma" {
		t.Fatalf("selected %+v", got)
	}
	// Bravo finishes and the list re-sorts: Alpha stays selected.
	b.State = mission.Done
	if got := m.ovMission(); got == nil || got.ID != "ma" {
		t.Errorf("after the re-sort the selection is %+v", got)
	}
	// Alpha is dropped: its neighbour is selected, not nothing.
	a.State = mission.Dropped
	if got := m.ovMission(); got == nil || got.ID != "mb" {
		t.Errorf("after the selected mission went: %+v", got)
	}
}

func TestMissionListScrollsToTheSelection(t *testing.T) {
	m := missionModel()
	now := time.Now()
	for i := 0; i < 12; i++ {
		m.missions.Save(&mission.Mission{ID: fmt.Sprintf("m%02d", i), Project: m.proj.cur, Name: fmt.Sprintf("Mission %02d", i), Mission: "x", State: mission.Done, Created: now.Add(-time.Duration(i) * time.Minute), Updated: now})
	}
	m.width, m.height = 120, 30
	for i := 0; i < 11; i++ {
		m.moveMission(1)
	}
	h := m.listHeight()
	out := strings.Join(boxed(m.missionRows(60, h)), "\n")
	if !strings.Contains(out, "Mission 11") || !strings.Contains(out, "more") || strings.Contains(out, "Mission 00") {
		t.Errorf("the selection is not in view (h %d):\n%s", h, out)
	}
	if !strings.Contains(out, "↑ ") {
		t.Errorf("no ↑ more line:\n%s", out)
	}
	for i := 0; i < 11; i++ {
		m.moveMission(-1)
	}
	if out := strings.Join(boxed(m.missionRows(60, h)), "\n"); !strings.Contains(out, "Mission 00") || !strings.Contains(out, "↓ ") {
		t.Errorf("back at the top:\n%s", out)
	}
}

// The selected row's band has no hole: the gap between the name and the
// state is drawn in the band's style.
func TestSelectedRowBandHasNoHole(t *testing.T) {
	m := missionModel()
	screenMission(&m)
	lines := m.missionRows(70, 30)
	var first string
	for _, l := range lines {
		if strings.Contains(ansi.Strip(l), "Drey not recognized") {
			first = l
			break
		}
	}
	if first == "" {
		t.Fatalf("no row:\n%s", strings.Join(boxed(lines), "\n"))
	}
	if strings.Contains(first, "\x1b[m ") || strings.Contains(first, "\x1b[0m ") {
		t.Errorf("unstyled space inside the band: %q", first)
	}
	if plain := ansi.Strip(first); !strings.HasPrefix(plain, " ▌ ") {
		t.Errorf("the bar is not in the second column: %q", plain)
	}
}

func TestMissionPanesKeepTheirMargins(t *testing.T) {
	for _, w := range []int{48, 78, 118} {
		m := missionModel()
		ms := screenMission(&m)
		checkMargins(t, fmt.Sprintf("list %d", w), m.missionRows(w, 40), w)
		m.ov.open = ms.ID
		view, _ := m.missionView(w)
		checkMargins(t, fmt.Sprintf("mission %d", w), view, w)
		m.ov.transcript = "mossy-lemur"
		m.ov.trEntries = []transcript.Entry{{Kind: "you", Text: "Fix the pairing check"}, {Kind: "tool", Tool: "Bash", Text: "go test ./internal/api/", Output: "ok"}, {Kind: "text", Text: strings.Repeat("long words ", 30)}}
		checkMargins(t, fmt.Sprintf("transcript %d", w), m.transcriptView(w, 40), w)
		m.ov.open, m.ov.transcript, m.ov.proposal = "", "", ms.ID
		checkMargins(t, fmt.Sprintf("proposal %d", w), m.proposalView(w), w)
		checkMargins(t, fmt.Sprintf("strip %d", w), m.missionStrip(w, 40), w)
	}
}

func TestOpenMissionShowsWhoDoesWhat(t *testing.T) {
	m := missionModel()
	ms := screenMission(&m)
	m.ov.open = ms.ID
	view, _ := m.missionView(100)
	out := strings.Join(boxed(view), "\n")
	for _, want := range []string{
		"GOAL", "DONE WHEN", "WHO DOES WHAT", "1 of 3 met · 1 claimed",
		"☑ c1 The cause is explained", "☐ c2 A fix is merged on dev · claimed: PR #97 merged", "☐ c3 The human pairs Drey",
		"✓ done", "◐ needs you", "■ blocked · waits for #97", "unassigned", "○ todo",
		"type to tell the orchestrator",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("open mission lacks %q:\n%s", want, out)
		}
	}
	// Mossy Lemur needs the human, so the mission does too.
	if m.missionState(ms) != mission.Waiting {
		t.Errorf("state %s", m.missionState(ms))
	}
	if strings.Contains(out, "◐ c") {
		t.Error("◐ used for a criterion")
	}
}

func TestChipsCountLiveMissions(t *testing.T) {
	m := missionModel()
	m.width = 140
	screenMission(&m)
	strip := ansi.Strip(m.projectStrip(140))
	if !strings.Contains(strip, "tasklog 1 ◐1") || !strings.Contains(strip, "1 running · 0 done") {
		t.Errorf("strip: %q", strip)
	}
	m.base[0].Status = 0 // Mossy Lemur no longer needs the human
	m.refresh()
	if strip := ansi.Strip(m.projectStrip(140)); !strings.Contains(strip, "●1") {
		t.Errorf("strip: %q", strip)
	}
}

func TestAgentsPaneNarrowsUnder100Columns(t *testing.T) {
	if agentsWidth(80) != 30 || agentsWidth(99) != 30 || agentsWidth(120) != 50 {
		t.Errorf("widths %d %d %d", agentsWidth(80), agentsWidth(99), agentsWidth(120))
	}
	m := missionModel()
	if out := ansi.Strip(m.projectAgentsPane(28, 20)); strings.Contains(out, "CODING AGENT") {
		t.Errorf("role tags at 28 columns:\n%s", out)
	}
}

// Enter on an owner shows its transcript: only the transcript's text sits
// inside the fence, Den's status outside it, and none of it reaches a
// prompt, the chat log, an inbox item or the den-api view.
func TestTranscriptIsFencedAndGoesNowhere(t *testing.T) {
	m := missionModel()
	m.chatLog = filepath.Join(t.TempDir(), "orchestrator.jsonl")
	ms := screenMission(&m)
	m.ov.open = ms.ID
	m.ov.who = 1 // g2, Mossy Lemur
	if note := m.toggleMission(); note != "" || m.ov.transcript != "mossy-lemur" {
		t.Fatalf("Enter: %q, transcript %q", note, m.ov.transcript)
	}
	esc, osc := string(rune(0x1b)), string(rune(0x1b))+"]8;;http://evil"+string(rune(0x07))
	m.ov.trEntries = []transcript.Entry{
		{Kind: "you", Text: "Fix the pairing check MARKER-7f3a"},
		{Kind: "text", Text: "◐ Den: waiting for your answer in the Inbox " + osc + "link" + esc + "[31m"},
	}
	view := m.transcriptView(100, 40)
	plain := boxed(view)
	inside, status := false, false
	for _, l := range plain {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "│ ") && strings.Contains(t, "Den: waiting") {
			inside = true
		}
		if strings.HasPrefix(t, "◐ Den: waiting") {
			status = true
		}
	}
	if !inside {
		t.Errorf("the agent's own line is not inside the fence:\n%s", strings.Join(plain, "\n"))
	}
	if !status {
		t.Errorf("Den's status for a waiting agent is not outside the fence:\n%s", strings.Join(plain, "\n"))
	}
	for _, l := range view {
		if strings.Contains(l, "http://evil") || strings.Contains(l, esc+"[31m") {
			t.Errorf("escapes from the transcript reached the screen: %q", l)
		}
	}
	if !strings.Contains(strings.Join(plain, "\n"), "prompt › Fix the pairing check") {
		t.Errorf("the prompt's sender is not Den's word:\n%s", strings.Join(plain, "\n"))
	}
	// Nothing of it goes anywhere.
	m.orch.starting = true
	m.askOrchestrator("how is it going?")
	m.judgeMission(ms, ms.CurrentRun())
	for _, p := range m.orch.pending {
		if strings.Contains(p, "MARKER-7f3a") {
			t.Errorf("the transcript reached a prompt:\n%s", p)
		}
	}
	if raw, _ := os.ReadFile(m.chatLog); strings.Contains(string(raw), "MARKER-7f3a") {
		t.Error("the transcript reached the chat log")
	}
	for _, it := range m.store.Items() {
		if strings.Contains(it.Title+strings.Join(it.Body, "\n"), "MARKER-7f3a") {
			t.Error("the transcript reached an inbox item")
		}
	}
	var snap api.Snapshot
	publishedHook = func(s api.Snapshot) { snap = s }
	t.Cleanup(func() { publishedHook = nil })
	m.publishAPI()
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "MARKER-7f3a") {
		t.Error("the transcript reached the den-api view")
	}
	// Esc goes back to the mission, then to the list with it selected.
	m.tab = tabOverview
	m.input.SetValue("")
	mm, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = mm.(Model)
	if m.ov.transcript != "" || m.ov.open != ms.ID {
		t.Fatalf("Esc from the transcript: %+v", m.ov)
	}
	mm, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = mm.(Model)
	if m.ov.open != "" || m.ovMission() == nil || m.ovMission().ID != ms.ID {
		t.Errorf("Esc from the mission: %+v", m.ov)
	}
}

func TestTranscriptOnlyForOwnersOnThisMachine(t *testing.T) {
	m := missionModel()
	ms := screenMission(&m)
	m.ov.open = ms.ID
	m.ov.who = 3 // g4, unassigned
	if note := m.toggleMission(); !strings.Contains(note, "no owner") || m.ov.transcript != "" {
		t.Errorf("unassigned: %q %q", note, m.ov.transcript)
	}
	ms.Goals[3].Owner, ms.Goals[3].Host, ms.Goals[3].Name = "x", "build1", "Far Owl"
	if note := m.toggleMission(); !strings.Contains(note, "stays there") || m.ov.transcript != "" {
		t.Errorf("remote: %q %q", note, m.ov.transcript)
	}
}

func TestChatShowsMissionsOnTop(t *testing.T) {
	m := missionModel()
	ms := screenMission(&m)
	now := time.Now()
	for i := 0; i < 3; i++ {
		m.missions.Save(&mission.Mission{ID: fmt.Sprintf("x%d", i), Project: m.proj.cur, Name: fmt.Sprintf("Other %d", i), Mission: "x", State: mission.Running, Created: now.Add(-time.Hour), Updated: now})
	}
	prop := &mission.Mission{ID: "mp", Project: m.proj.cur, Name: "Docs for stage 2", Mission: "docs", Shape: mission.FanOut, State: mission.Proposed, Created: now, Updated: now}
	m.missions.Save(prop)
	for _, w := range []int{78, 118} {
		out := strings.Join(boxed(m.missionStrip(w, 40)), "\n")
		if !strings.Contains(out, "MISSIONS") || !strings.Contains(out, "⇶ Docs for stage 2") || !strings.Contains(out, "proposal") || !strings.Contains(out, "^Y start") || !strings.Contains(out, "more · /missions") {
			t.Errorf("strip at %d:\n%s", w, out)
		}
		if i, j := strings.Index(out, "Docs for stage 2"), strings.Index(out, ms.Name); j >= 0 && j < i {
			t.Errorf("the proposal is not first at %d:\n%s", w, out)
		}
	}
	if out := strings.Join(boxed(m.missionStrip(118, 40)), "\n"); !strings.Contains(out, "g2 · ◐ needs you") {
		t.Errorf("the needs-you row does not say what needs the human:\n%s", out)
	}
	if lines := m.missionStrip(118, 12); len(lines) != 2 {
		t.Errorf("not folded in a short pane: %q", boxed(lines))
	}
	// ^Y in the chat, with no card focused, starts nothing without criteria.
	m.tab = tabOrch
	m.orch.cardCur = -1
	mm, _ := m.handleKey(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	m = mm.(Model)
	if m.missions.Get("mp").State != mission.Proposed {
		t.Error("^Y started a proposal with no criteria")
	}
	prop.SetCriteria([]string{"docs reviewed"})
	prop.AddGoal(mission.SubGoal{Text: "write", Role: "writer"})
	m.orch.starting = true
	mm, _ = m.handleKey(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	m = mm.(Model)
	if m.missions.Get("mp").State != mission.Running {
		t.Errorf("^Y in the chat did not start the proposal: %s", m.missions.Get("mp").State)
	}
}
