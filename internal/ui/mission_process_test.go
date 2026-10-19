package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/process"
)

func tip(c byte) string { return strings.Repeat(string(c), 40) }

// A mission branch's tip, seen ahead of dev, counts once when it lands,
// even after its agent has gone: a later commit replaces the tip, a squash
// merge that leaves the branch ahead does not count it again, and a branch
// of an agent on no mission is never counted. The orchestrator's block and
// the mission view carry the count and the cost per merged change.
func TestMissionMergesCountOnce(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	m, ms := costModel(t)
	now := time.Now()
	landed := map[string]bool{}
	var asked []tipCheck
	oldL, oldR := teamLanded, teamRead
	teamLanded = func(checks []tipCheck) []string {
		asked = checks
		var out []string
		for _, c := range checks {
			if landed[c.tip] {
				out = append(out, c.tip)
			}
		}
		return out
	}
	var works []process.Work
	teamRead = func([]teamTarget) []process.Work { return works }
	t.Cleanup(func() { teamLanded, teamRead = oldL, oldR })
	scan := func(at time.Time) {
		t.Helper()
		m.team.next = time.Time{}
		if cmd := m.teamTick(at); cmd != nil {
			runCmds(m.applyTeamScan(cmd().(teamScanMsg), at))
		}
	}
	for i := range m.base {
		m.base[i].HasGit, m.base[i].Session.CWD, m.base[i].Session.Alive = true, "/w/"+m.base[i].Session.SessionID, true
	}
	m.refresh()
	m.settleCosts() // the tick's count of the live run
	ahead := func(key, branch string, t byte) process.Work {
		return process.Work{Key: key, Repo: "r", Tree: "/w/" + key, Branch: branch, Since: now.Add(-time.Hour), Tip: tip(t)}
	}
	works = []process.Work{ahead("mossy-lemur", "fix\x1b[31m", 'a'), ahead("lone-agent", "other", 'z')}
	scan(now)
	if len(m.team.pending) != 1 {
		t.Fatalf("pending %+v", m.team.pending)
	}
	// A later commit on the same branch replaces the tip.
	works = []process.Work{ahead("mossy-lemur", "fix\x1b[31m", 'b')}
	scan(now.Add(time.Minute))
	if len(asked) != 1 || asked[0].tip != tip('a') || len(m.team.pending) != 1 {
		t.Fatalf("asked %+v pending %+v", asked, m.team.pending)
	}
	landed[tip('a')], landed[tip('b')] = true, true
	scan(now.Add(2 * time.Minute))
	if got := m.missions.Get(ms.ID); len(got.Merges) != 1 || got.Merges[0].Tip != tip('b') || got.Merges[0].Branch != "fix" {
		t.Fatalf("merges %+v", got.Merges)
	}
	var notes []string
	for _, e := range m.orch.chat {
		notes = append(notes, e.text)
	}
	if all := strings.Join(notes, "\n"); !strings.Contains(all, `Den saw Mossy Lemur's branch "fix" land for mission "Tests green": 1 merged change · `) || !strings.Contains(all, " each so far.") {
		t.Errorf("notes:\n%s", all)
	}
	// Squashed, the branch is still ahead: it is not counted again.
	scan(now.Add(3 * time.Minute))
	scan(now.Add(4 * time.Minute))
	if got := m.missions.Get(ms.ID); len(got.Merges) != 1 || len(m.team.pending) != 0 {
		t.Errorf("counted again: %+v pending %+v", got.Merges, m.team.pending)
	}
	// A new tip, then the agent goes: the tip is still checked, in the
	// checkout Den saw it in, and counts when it lands.
	works = []process.Work{ahead("mossy-lemur", "fix-2", 'c')}
	scan(now.Add(5 * time.Minute))
	m.base, works = nil, nil
	m.refresh()
	landed[tip('c')] = true
	scan(now.Add(6 * time.Minute))
	if got := m.missions.Get(ms.ID); len(got.Merges) != 2 || len(asked) != 1 || asked[0].dirs[0] != "/w/mossy-lemur" {
		t.Errorf("after the agent went: %+v asked %+v", got.Merges, asked)
	}
	found := false
	for _, i := range m.missionInfos() {
		if i.ID == ms.ID {
			found = true
			if i.Merged != 2 || i.PerMerge == "" || i.Cost == "" {
				t.Errorf("block %+v", i)
			}
		}
	}
	if !found {
		t.Error("mission not in the block")
	}
	if out := strings.Join(m.costLines(m.missions.Get(ms.ID), 100), "\n"); !strings.Contains(out, "2 merged changes · ") || !strings.Contains(out, " each") {
		t.Errorf("view:\n%s", out)
	}
	// A checkout of the trunk is never tracked: its tip is on dev already,
	// and would count as a change the mission landed.
	m.team.pending = nil
	trunk := ahead("mossy-lemur", "main", 'e')
	trunk.Trunk = true
	m.notePending([]process.Work{trunk}, now)
	if len(m.team.pending) != 0 {
		t.Errorf("a trunk checkout was tracked: %+v", m.team.pending)
	}
	// A tip Den has not seen for two weeks is dropped unchecked.
	m.notePending([]process.Work{ahead("mossy-lemur", "fix-3", 'd')}, now)
	if checks := m.tipChecks(now.Add(pendingTipFor + time.Minute)); len(checks) != 0 || len(m.team.pending) != 0 {
		t.Errorf("stale tip kept: %+v", checks)
	}
}

// An agent on a mission with no turn for idle_minutes and nothing open is
// idle to the orchestrator and on the mission view; one with a live card or
// a waiting inbox item is not, and 0 turns it off.
func TestMissionIdleAgents(t *testing.T) {
	m, ms := costModel(t)
	m.cfg.Team.IdleMinutes = 30
	now := time.Now()
	for i := range m.base {
		m.base[i].Session.Alive, m.base[i].Status, m.base[i].LastActive = true, fleet.Idle, now.Add(-45*time.Minute)
	}
	m.refresh()
	idle := func() []string {
		var out []string
		for _, i := range m.missionInfos() {
			if i.ID == ms.ID {
				for _, d := range i.Idle {
					out = append(out, d.Owner)
				}
			}
		}
		return out
	}
	// The lead's card is live: only its worker is idle.
	if got := idle(); len(got) != 1 || got[0] != "Snug Capybara" {
		t.Fatalf("idle %v", got)
	}
	if out := strings.Join(m.costLines(ms, 100), "\n"); !strings.Contains(out, "Snug Capybara 45 min") {
		t.Errorf("view:\n%s", out)
	}
	it := m.store.AddClose("snug-capybara", "Close Snug Capybara?", []string{"x"}, nil)
	if got := idle(); len(got) != 0 {
		t.Errorf("idle with a waiting item: %v", got)
	}
	m.store.Release(it, "test")
	if got := idle(); len(got) != 1 {
		t.Errorf("not idle once the item went: %v", got)
	}
	m.cfg.Team.IdleMinutes = 0
	if got := idle(); len(got) != 0 {
		t.Errorf("idle with the check off: %v", got)
	}
}
