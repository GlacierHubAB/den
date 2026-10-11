package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/mission"
	"github.com/GlacierHubAB/den/internal/transcript"
	"github.com/GlacierHubAB/den/internal/tree"
)

// writeUsage writes a synthetic transcript: one assistant message per
// (time, output tokens) pair on the given model.
func writeUsage(t *testing.T, path, model string, base time.Time, outs ...int) {
	t.Helper()
	var b strings.Builder
	for i, out := range outs {
		at := base.Add(time.Duration(i) * time.Minute).UTC().Format(time.RFC3339Nano)
		fmt.Fprintf(&b, `{"type":"assistant","timestamp":%q,"message":{"id":"%s-%d","model":%q,"usage":{"input_tokens":0,"output_tokens":%d,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`+"\n", at, filepath.Base(path), i, model, out)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

// costModel is a mission with a lead and a worker under it, each with a
// synthetic transcript, and a run whose one card went to the lead.
func costModel(t *testing.T) (Model, *mission.Mission) {
	m := missionModel()
	dir := t.TempDir()
	t0 := time.Now().Add(-time.Hour)
	// The lead used 100k output tokens before the run (not the run's) and
	// then 200k during it; the worker, 300k during it (the card goes out
	// half a minute in).
	writeUsage(t, filepath.Join(dir, "lead.jsonl"), "claude-opus-5-5", t0, 100_000, 200_000)
	writeUsage(t, filepath.Join(dir, "worker.jsonl"), "claude-sonnet-5-5", t0.Add(2*time.Minute), 300_000)
	paths := map[string]string{"mossy-lemur": "lead.jsonl", "snug-capybara": "worker.jsonl"}
	for i := range m.base {
		if p, ok := paths[m.base[i].Session.SessionID]; ok {
			m.base[i].Path = filepath.Join(dir, p)
			m.base[i].Cost = fleet.Prices.Of(transcript.Shared.Account(m.base[i].Path))
		}
	}
	m.refresh()
	m.tree.edges.Add(tree.Edge{Parent: tree.Orchestrator, Child: "mossy-lemur", At: t0})
	m.tree.edges.Add(tree.Edge{Parent: "mossy-lemur", Child: "snug-capybara", At: t0})

	ms := &mission.Mission{ID: "m1", Project: m.proj.cur, Name: "Tests green", Mission: "keep green", Shape: mission.Once, State: mission.Running, Created: t0, Updated: t0}
	m.missions.Save(ms)
	r := ms.StartRun(t0.Add(time.Minute))
	d := &dispatch{ID: "c1", SessionID: "mossy-lemur", Agent: "Mossy Lemur", Brief: "go", SentAt: t0.Add(30 * time.Second), State: cardWorking, MissionID: ms.ID}
	r.Cards = []string{d.ID}
	m.orch.chat = append(m.orch.chat, chatEntry{role: "card", card: d, at: d.SentAt})
	return m, ms
}

func TestRollUpsOverATreeAndAMissionRun(t *testing.T) {
	m, ms := costModel(t)
	// Tree: the lead's own 300k opus tokens plus the worker's 300k sonnet.
	own := m.sessionCost("mossy-lemur")
	if own.Tokens.Output != 300_000 {
		t.Fatalf("lead own = %+v", own)
	}
	tr := m.treeCost("mossy-lemur")
	if tr.Tokens.Output != 600_000 || !nearUSD(tr.USD, 300_000*20/1e6+300_000*10/1e6) {
		t.Errorf("tree = %+v", tr)
	}
	if f := m.fleetCost(); f.Tokens.Output != 600_000 {
		t.Errorf("fleet = %+v", f)
	}
	// The run counts what the lead and its worker used from the card on:
	// the lead's second message (200k) and the worker's 300k, not the
	// lead's first 100k.
	m.settleCosts()
	got := m.runSpent(ms, &ms.Runs[0])
	if got.Tokens != 500_000 || !nearUSD(got.USD, 200_000*20/1e6+300_000*10/1e6) || got.Partial {
		t.Errorf("run = %+v", got)
	}
	if tot := m.missionSpent(ms); tot != got {
		t.Errorf("mission = %+v, run = %+v", tot, got)
	}
}

func nearUSD(a, b float64) bool { d := a - b; return d < 1e-9 && d > -1e-9 }

func TestEndedRunCostIsKeptAfterTheTranscriptsGo(t *testing.T) {
	m, ms := costModel(t)
	ms.Runs[0].EndedAt = time.Now()
	m.settleCosts()
	r := ms.Runs[0]
	if r.Cost == nil || r.Cost.Tokens != 500_000 {
		t.Fatalf("run cost not recorded: %+v", r.Cost)
	}
	// The agents vanish; the run still shows what it used.
	m.base, m.agents = nil, nil
	m.settleCosts()
	if got := m.runSpent(ms, &ms.Runs[0]); got.Tokens != 500_000 {
		t.Errorf("after the agents went: %+v", got)
	}
}

func TestBudgetCrossingPausesOnceAndAsksOnce(t *testing.T) {
	m, ms := costModel(t)
	ms.Budget = mission.Budget{Tokens: 1_000_000}
	m.settleCosts() // 500k of 1M
	if ms.State != mission.Running || m.budgetItem(ms.ID) != nil {
		t.Fatalf("under budget but state=%s item=%v", ms.State, m.budgetItem(ms.ID))
	}
	ms.Budget = mission.Budget{USD: 4}
	m.missions.Save(ms)
	m.settleCosts() // 200k opus ($4) + 300k sonnet ($3) = $7
	if ms.State != mission.Paused || !ms.Budget.Asked {
		t.Fatalf("not paused: state=%s budget=%+v", ms.State, ms.Budget)
	}
	for i := 0; i < 3; i++ {
		m.settleCosts()
	}
	n := 0
	for _, it := range m.store.Items() {
		if it.Kind == inbox.Budget {
			n++
			if !it.Answerable() || !strings.Contains(it.Title, "Tests green") {
				t.Errorf("item = %+v", it)
			}
		}
	}
	if n != 1 {
		t.Fatalf("budget questions = %d, want exactly 1", n)
	}
	// The agents were not touched: the card is still working.
	if m.cards()[0].State != cardWorking {
		t.Error("a budget stop must not touch agents")
	}
	// A restart loses the inbox but not the mission: the question returns.
	m.store = inbox.New()
	m.settleCosts()
	if m.budgetItem(ms.ID) == nil {
		t.Error("the pending question did not come back")
	}
}

func TestBudgetAnswerContinueAndStop(t *testing.T) {
	m, ms := costModel(t)
	ms.Budget = mission.Budget{Tokens: 100_000}
	m.settleCosts()
	it := m.budgetItem(ms.ID)
	if it == nil {
		t.Fatal("no question")
	}
	mm, _ := m.answer("allow")
	m = mm.(Model)
	if ms.State != mission.Running || !ms.Budget.Waived || m.budgetItem(ms.ID) != nil {
		t.Fatalf("continue: state=%s budget=%+v", ms.State, ms.Budget)
	}
	m.settleCosts()
	if m.budgetItem(ms.ID) != nil || ms.State != mission.Running {
		t.Error("a mission that was allowed on must not be asked again")
	}
	// A fresh budget asks afresh; this time stop.
	m.budgetCommand([]string{"50k"})
	m.settleCosts()
	if m.budgetItem(ms.ID) == nil || ms.State != mission.Paused {
		t.Fatalf("new budget not enforced: state=%s", ms.State)
	}
	mm, _ = m.answer("deny")
	m = mm.(Model)
	if ms.State != mission.Done || ms.Runs[0].EndedAt.IsZero() {
		t.Errorf("stop: state=%s run=%+v", ms.State, ms.Runs[0])
	}
	if m.cards()[0].State != cardWorking {
		t.Error("stopping the mission must not stop agents")
	}
}

func TestNewBudgetReplacesAnUnansweredQuestion(t *testing.T) {
	m, ms := costModel(t)
	ms.Budget = mission.Budget{Tokens: 100_000}
	m.settleCosts()
	if ms.State != mission.Paused || m.budgetItem(ms.ID) == nil {
		t.Fatal("setup: not paused and asked")
	}
	m.ov.open = ms.ID
	m.budgetCommand([]string{"5M"})
	if ms.State != mission.Running || m.budgetItem(ms.ID) != nil || ms.Budget.Asked {
		t.Fatalf("a new budget should release the pause: state=%s budget=%+v", ms.State, ms.Budget)
	}
	m.settleCosts()
	if ms.State != mission.Running {
		t.Errorf("under the new budget: state=%s", ms.State)
	}
	// A new budget that is also crossed pauses and asks again, once.
	m.budgetCommand([]string{"10k"})
	m.settleCosts()
	m.settleCosts()
	n := 0
	for _, it := range m.store.Items() {
		if it.Kind == inbox.Budget && it.State == inbox.Waiting {
			n++
		}
	}
	if ms.State != mission.Paused || n != 1 {
		t.Errorf("state=%s open questions=%d", ms.State, n)
	}
}

func TestBudgetCommand(t *testing.T) {
	m, ms := costModel(t)
	m.ov.open = ms.ID
	if note, isErr := m.budgetCommand([]string{"2M"}); isErr || ms.Budget.Tokens != 2_000_000 {
		t.Errorf("2M: %q %v %+v", note, isErr, ms.Budget)
	}
	if note, isErr := m.budgetCommand([]string{"$5"}); isErr || ms.Budget.USD != 5 || ms.Budget.Tokens != 0 {
		t.Errorf("$5: %q %v %+v", note, isErr, ms.Budget)
	}
	if note, isErr := m.budgetCommand([]string{"lots"}); !isErr {
		t.Errorf("bad value accepted: %q", note)
	}
	if note, _ := m.budgetCommand(nil); !strings.Contains(note, "of ≈ $5.00") {
		t.Errorf("show = %q", note)
	}
	m.budgetCommand([]string{"off"})
	if ms.Budget.Set() {
		t.Error("budget not removed")
	}
	// Off by default: a mission with no budget is never paused.
	m.settleCosts()
	if ms.State != mission.Running {
		t.Errorf("state = %s", ms.State)
	}
}

func TestCostShowsInViewsAndMarksEstimates(t *testing.T) {
	m, ms := costModel(t)
	m.settleCosts()
	ms.Budget = mission.Budget{Tokens: 5_000_000}
	m.ov.open = ms.ID
	out := ansi.Strip(m.View().Content)
	for _, want := range []string{"fleet 600k tok", "used", "500k tok", "≈ $", "estimate", "budget", "5M tok"} {
		if !strings.Contains(out, want) {
			t.Errorf("mission view lacks %q:\n%s", want, out)
		}
	}
	m.ov.open = ""
	m.tab = tabFleet
	m.cursor = 0
	for i, a := range m.agents {
		if a.Session.SessionID == "mossy-lemur" {
			m.cursor = i
		}
	}
	m.focus = paneDetail
	out = ansi.Strip(m.View().Content)
	for _, want := range []string{"tokens", "300k tok", "team", "600k tok", "≈ $"} {
		if !strings.Contains(out, want) {
			t.Errorf("details lack %q:\n%s", want, out)
		}
	}
}
