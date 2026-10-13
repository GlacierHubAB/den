package ui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/mission"
	"github.com/GlacierHubAB/den/internal/orch"
	"github.com/GlacierHubAB/den/internal/theme"
)

func missionModel() Model {
	m := Model{st: newStyles(theme.Deepharness), store: inbox.New(), width: 120, height: 42, tree: newTreeState(), tab: tabOverview}
	m.base = sample()
	for i := range m.base {
		m.base[i].Session.SessionID = strings.ToLower(strings.ReplaceAll(m.base[i].Name, " ", "-"))
	}
	m.missions = mission.New("")
	m.input = newInput(m.st)
	m.refresh()
	m.proj.list = []project{{Root: "/home/x/tasklog", Name: "tasklog", Branch: "main", Agents: []string{"mossy-lemur"}}}
	m.proj.cur = "/home/x/tasklog"
	return m
}

func TestMissionProposeStartRunAndSettle(t *testing.T) {
	m := missionModel()
	m.orch.starting = true // no process: the prompt queues
	m.proposeMission("keep the tests green")
	if m.ov.proposal == "" || len(m.orch.pending) != 1 || !strings.Contains(m.orch.pending[0], "Propose a mission") || !strings.Contains(m.orch.pending[0], "fenced json block") {
		t.Fatalf("proposal not sent: %q", m.orch.pending)
	}
	out := ansi.Strip(m.View().Content)
	if !strings.Contains(out, "keep the tests green") || !strings.Contains(out, "Mission proposal") {
		t.Errorf("proposal view:\n%s", out)
	}
	// The orchestrator answers.
	reply := "A loop fits.\n```json\n{\"mission\": {\"name\": \"Tests green\", \"mission\": \"Keep tasklog's tests green.\", \"done_when\": \"standing\", \"constraints\": [\"no skipped tests\"], \"shape\": \"loop\", \"reason\": \"one agent keeps the context\", \"agents\": [\"Mossy Lemur\"]}}\n```"
	shown := m.missionReply(reply)
	if shown != "A loop fits." {
		t.Errorf("chat text = %q", shown)
	}
	ms := m.missions.Get(m.ov.proposal)
	if ms.Shape != mission.Loop || ms.Name != "Tests green" || len(ms.Constraints) != 1 {
		t.Fatalf("mission = %+v", ms)
	}
	out = ansi.Strip(m.View().Content)
	if !strings.Contains(out, "Tests green") || !strings.Contains(out, "no skipped tests") || !strings.Contains(out, "↻ loop") {
		t.Errorf("proposal card:\n%s", out)
	}
	// ^Y starts run 1 and the next dispatch belongs to it.
	m.startMission()
	if ms.State != mission.Running || len(ms.Runs) != 1 || m.turn.id != ms.ID || m.ov.open != ms.ID {
		t.Fatalf("after start: state=%s runs=%d turn=%q open=%q", ms.State, len(ms.Runs), m.turn.id, m.ov.open)
	}
	d := &dispatch{ID: "c1", SessionID: "mossy-lemur", Agent: "Mossy Lemur", Brief: "fix the flake", SentAt: time.Now(), State: cardWorking}
	m.attachCard(d)
	m.orch.chat = append(m.orch.chat, chatEntry{role: "card", card: d, at: time.Now()})
	if d.MissionID != ms.ID || len(ms.Runs[0].Cards) != 1 {
		t.Fatalf("card not attached: %+v", d)
	}
	m.turn = missionTurn{}
	if st := m.missionNow(ms); !strings.Contains(st, "run 1") || !strings.Contains(st, "Mossy Lemur working") {
		t.Errorf("now = %q", st)
	}
	// The card finishes: a loop asks the orchestrator whether the goal is met.
	d.State, d.Result = cardDone, "Pinned the flaky test to UTC. All green."
	m.settleMissions()
	if ms.Runs[0].EndedAt.IsZero() || !strings.Contains(ms.Runs[0].Summary, "Pinned") || m.turn.id != ms.ID {
		t.Fatalf("run not settled or not judged: %+v turn=%q", ms.Runs[0], m.turn.id)
	}
	if last := m.orch.pending[len(m.orch.pending)-1]; !strings.Contains(last, "Is the goal met?") {
		t.Errorf("judge prompt = %q", last)
	}
	// The orchestrator says done, through update_mission, in the turn Den
	// started to judge the run: the human confirms it.
	proc, _ := liveOrchestrator(t)
	m.orch.proc, m.orch.origins = proc, []string{originDen}
	raw, _ := json.Marshal(bridge.MissionInput{ID: ms.ID, State: "done", Summary: "suite green"})
	r := bridge.TestRequest(bridge.Envelope{Event: bridge.MissionEvent, Payload: raw})
	m.handleRequest(r)
	if ms.State == mission.Done {
		t.Fatal("done applied without the human")
	}
	it := missionItems(m)[0]
	m.tab, m.inboxSel = tabInbox, it.ID
	nm, _ := m.answer("allow")
	m = nm.(Model)
	m.tab = tabOverview
	if ms.State != mission.Done || ms.Runs[0].Summary != "suite green" {
		t.Errorf("after update: %+v", ms)
	}
	out = ansi.Strip(m.View().Content)
	if !strings.Contains(out, "✓ done") {
		t.Errorf("open mission view:\n%s", out)
	}
}

func TestMissionOnceEndsWithItsCardAndScheduleWaitsForItsTick(t *testing.T) {
	m := missionModel()
	now := time.Now()
	once := &mission.Mission{ID: "m-once", Project: m.proj.cur, Name: "Say hi", Mission: "Say hi", Shape: mission.Once, State: mission.Running, Created: now, Updated: now}
	once.StartRun(now)
	once.Runs[0].Cards = []string{"c9"}
	m.missions.Save(once)
	d := &dispatch{ID: "c9", SessionID: "mossy-lemur", Agent: "Mossy Lemur", MissionID: "m-once", State: cardDone, Result: "Hi."}
	m.orch.chat = append(m.orch.chat, chatEntry{role: "card", card: d, at: now})
	m.settleMissions()
	if once.State != mission.Done {
		t.Errorf("once mission = %s", once.State)
	}
	sched := &mission.Mission{ID: "m-sched", Project: m.proj.cur, Name: "Triage", Mission: "Triage issues", Shape: mission.Schedule, Cadence: "every 30m", State: mission.Running, Created: now, Updated: now, NextRun: now.Add(-time.Minute)}
	m.missions.Save(sched)
	m.orch.starting = true
	m.settleMissions()
	if len(sched.Runs) != 1 || sched.NextRun.Before(now.Add(29*time.Minute)) {
		t.Errorf("scheduled run not fired: runs=%d next=%v", len(sched.Runs), sched.NextRun)
	}
	if last := m.orch.pending[len(m.orch.pending)-1]; !strings.Contains(last, "Scheduled run of mission m-sched") {
		t.Errorf("prompt = %q", last)
	}
	// Paused: the tick does nothing.
	sched.Runs[0].EndedAt = now
	sched.State, sched.NextRun = mission.Paused, now.Add(-time.Minute)
	m.settleMissions()
	if len(sched.Runs) != 1 {
		t.Error("a paused schedule fired")
	}
	rows := ansi.Strip(m.missionsPane(70, 30))
	if !strings.Contains(rows, "‖ paused") || !strings.Contains(rows, "✓ done") {
		t.Errorf("rows:\n%s", rows)
	}
}

func TestTeamLinesDrawLeadsAboveTheirPeople(t *testing.T) {
	m := missionModel()
	team := []mission.Member{
		{Role: "product manager", Model: "opus"},
		{Role: "UI designer", Model: "opus", Under: "product manager"},
		{Role: "frontend developer", Model: "sonnet", Under: "product manager"},
		{Role: "SEO", Model: "haiku", Under: "Product Manager"},
	}
	var got []string
	for _, l := range m.teamLines(team) {
		got = append(got, ansi.Strip(l))
	}
	want := []string{"product manager · opus", "├─ UI designer · opus", "├─ frontend developer · sonnet", "└─ SEO · haiku"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("team lines = %q", got)
	}
	ms := &mission.Mission{Name: "Site", Mission: "Build it", Shape: mission.FanOut, Team: team}
	if b := m.missionBrief(ms); !strings.Contains(b, "team of 4") {
		t.Errorf("brief = %q", b)
	}
}

// TestOpenMissionShowsEveryMessage types to an open mission twice, the
// second time while the first answer is still streaming: both messages and
// the streamed text show in the mission pane as they arrive.
func TestOpenMissionShowsEveryMessage(t *testing.T) {
	m := missionModel()
	m.orch.starting = true
	now := time.Now()
	ms := &mission.Mission{ID: "m1", Project: m.proj.cur, Name: "Site", Mission: "Ship the site.", State: mission.Running, Created: now, Updated: now}
	m.missions.Save(ms)
	m.ov.open = ms.ID
	m.missionText("cover the Review section too")
	m.handleOrchEvent(orch.Event{Kind: orch.Delta, Text: "Sending that to "})
	m.missionText("and the avatar badge")
	out := ansi.Strip(m.missionsPane(80, 40))
	for _, want := range []string{"cover the Review section too", "Sending that to", "and the avatar badge"} {
		if !strings.Contains(out, want) {
			t.Errorf("mission pane lacks %q:\n%s", want, out)
		}
	}
	m.handleOrchEvent(orch.Event{Kind: orch.Delta, Text: "Plucky Otter."})
	m.handleOrchEvent(orch.Event{Kind: orch.Text, Text: "Sending that to Plucky Otter."})
	m.handleOrchEvent(orch.Event{Kind: orch.Text, Text: "The badge goes along too."})
	out = ansi.Strip(m.missionsPane(80, 40))
	for _, want := range []string{"cover the Review section too", "Sending that to Plucky Otter.", "and the avatar badge", "The badge goes along too."} {
		if !strings.Contains(out, want) {
			t.Errorf("mission pane lacks %q:\n%s", want, out)
		}
	}
	if i := m.orch.streaming(); i >= 0 || strings.Count(out, "Sending that to") != 1 {
		t.Errorf("streamed entry %d not finished in place:\n%s", i, out)
	}
	// The plain chat is its own thread.
	m.askOrchestrator("what is everyone doing?")
	if out := ansi.Strip(m.missionsPane(80, 40)); strings.Contains(out, "everyone doing") {
		t.Errorf("plain chat leaked into the mission:\n%s", out)
	}
}
