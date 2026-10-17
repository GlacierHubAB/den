package ui

import (
	"context"
	"os"
	"strconv"
	"time"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/mission"
	"github.com/GlacierHubAB/den/internal/process"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// What a mission costs per change it lands, and which of its agents sit
// idle: Den reads both itself, from git and the fleet, and gives them to the
// orchestrator in its missions block and to the human on the mission. The
// orchestrator acts on them with the tools it has (a dispatch, close_agent,
// which asks the human as before); Den closes no one.

// pendingTip is a mission branch's tip Den saw ahead of its integration
// branch: Den checks it on each read until it lands, or until
// pendingTipFor has passed since Den last saw it.
type pendingTip struct {
	Tip, Mission, Branch, Session, Repo, Dir string
	Seen, Since                              time.Time // Since: the branch's oldest commit of its own
}

const (
	pendingTipFor  = 14 * 24 * time.Hour
	maxPendingTips = 100
)

// tipCheck is one pending tip to check, with the checkouts to check it in:
// the one Den saw it in, then any live one of the same repository, for a
// worktree removed once its branch merged.
type tipCheck struct {
	tip  string
	dirs []string
}

// teamLanded checks each tip and returns those that landed; tests replace
// it.
var teamLanded = func(checks []tipCheck) []string {
	ctx, cancel := context.WithTimeout(context.Background(), teamScanTimeout)
	defer cancel()
	var out []string
	for _, c := range checks {
		for _, dir := range c.dirs {
			if _, err := os.Stat(dir); err != nil {
				continue
			}
			if ok, err := process.Landed(ctx, dir, c.tip); err == nil {
				if ok {
					out = append(out, c.tip)
				}
				break
			}
		}
	}
	return out
}

// tipChecks are the pending tips to check on this read, dropping the ones
// Den has not seen for pendingTipFor.
func (m *Model) tipChecks(now time.Time) []tipCheck {
	byRepo := map[string][]string{}
	for _, w := range m.team.branches {
		if w.Repo != "" && w.Tree != "" {
			byRepo[w.Repo] = append(byRepo[w.Repo], w.Tree)
		}
	}
	var out []tipCheck
	for key, p := range m.team.pending {
		if now.Sub(p.Seen) > pendingTipFor {
			delete(m.team.pending, key)
			continue
		}
		dirs := []string{p.Dir}
		for _, d := range byRepo[p.Repo] {
			if d != p.Dir {
				dirs = append(dirs, d)
			}
		}
		out = append(out, tipCheck{tip: p.Tip, dirs: dirs})
	}
	return out
}

// notePending records the tip of each branch with commits of its own whose
// agent works for a live mission, one per repository and branch: a later
// commit replaces the tip, so a branch merged whole counts once.
func (m *Model) notePending(works []process.Work, now time.Time) {
	if m.missions == nil {
		return
	}
	if m.team.pending == nil {
		m.team.pending = map[string]pendingTip{}
	}
	byCard := m.cardMissions()
	for _, w := range works {
		if w.Trunk || w.Since.IsZero() || w.Tip == "" || w.Repo == "" || w.Branch == "" || w.Branch == "HEAD" {
			continue
		}
		ms := m.missions.Get(m.missionVia(w.Key, byCard))
		if ms == nil || !ms.Live() || hasMerge(ms, w.Tip) {
			continue
		}
		m.team.pending[w.Repo+"\x00"+w.Branch] = pendingTip{Tip: w.Tip, Mission: ms.ID, Branch: w.Branch, Session: w.Key, Repo: w.Repo, Dir: w.Tree, Seen: now, Since: w.Since}
	}
	for len(m.team.pending) > maxPendingTips {
		oldest := ""
		for key, p := range m.team.pending {
			if oldest == "" || p.Seen.Before(m.team.pending[oldest].Seen) {
				oldest = key
			}
		}
		delete(m.team.pending, oldest)
	}
}

func hasMerge(ms *mission.Mission, tip string) bool {
	for _, g := range ms.Merges {
		if g.Tip == tip {
			return true
		}
	}
	return false
}

// applyLanded records each landed tip on its mission, once, and says so
// in Den's chat.
func (m *Model) applyLanded(tips []string, now time.Time) {
	for _, tip := range tips {
		for key, p := range m.team.pending {
			if p.Tip != tip {
				continue
			}
			delete(m.team.pending, key)
			ms := m.missions.Get(p.Mission)
			if ms == nil || !ms.AddMerge(mission.Merge{Tip: p.Tip, Branch: p.Branch, Session: p.Session, At: now, Since: p.Since}) {
				continue
			}
			branch := strconv.Quote(sanitize.Label(p.Branch, 80))
			ms.Note(now, "branch "+branch+" landed")
			m.missions.Save(ms)
			who := "an agent"
			if a, ok := m.agentFor(p.Session); ok {
				who = a.Name
			}
			m.logEntry(m.addUnthreaded(chatEntry{role: "note", at: now, text: "Den saw " + who + "'s branch " + branch +
				" land for mission " + strconv.Quote(ms.Name) + ": " + m.mergeLine(ms) + " so far."}))
		}
	}
}

// mergeLine is how many changes the mission landed and, once its cost is
// counted, what each cost: "2 merged changes · 600k tok · ≈ $1.70 each".
func (m Model) mergeLine(ms *mission.Mission) string {
	s := strconv.Itoa(len(ms.Merges)) + " merged changes"
	if len(ms.Merges) == 1 {
		s = "1 merged change"
	}
	if per := m.perMerge(ms); per != "" {
		s += " · " + per + " each"
	}
	return s
}

// perMerge is the mission's cost divided by the changes it landed, "" with
// none, or no cost counted yet.
func (m Model) perMerge(ms *mission.Mission) string {
	n := len(ms.Merges)
	s := m.missionSpent(ms)
	if n == 0 || s.Tokens == 0 {
		return ""
	}
	return spent{s.Tokens / int64(n), s.USD / float64(n), s.Partial}.line()
}

// idleText is an idle time as the mission view says it: minutes under two
// hours, then age's hours and days.
func idleText(d time.Duration) string {
	if d < 2*time.Hour {
		return strconv.Itoa(int(d.Minutes())) + " min"
	}
	return age(d)
}

// idleAgent is an agent on a mission with no turn for a while.
type idleAgent struct {
	agent fleet.Agent
	idle  time.Duration
}

// idleAgents are the local agents working for ms with no turn for
// [team] idle_minutes, no live card and no inbox item waiting: work the
// orchestrator could give the next sub-goal, or close.
func (m Model) idleAgents(ms *mission.Mission, byCard map[string]string, now time.Time) []idleAgent {
	limit := time.Duration(m.cfg.Team.IdleMinutes) * time.Minute
	if limit <= 0 || !ms.Live() {
		return nil
	}
	busy := map[string]bool{}
	for _, d := range m.cards() {
		if d.live() {
			busy[d.SessionID] = true
		}
	}
	if m.store != nil {
		for _, it := range m.store.Items() {
			if it.State == inbox.Waiting {
				busy[it.SessionID] = true
			}
		}
	}
	var out []idleAgent
	for _, a := range m.agents {
		id := a.Session.SessionID
		if a.Host != "" || !a.Session.Alive || a.Status != fleet.Idle || m.isOrchestrator(a) || busy[id] {
			continue
		}
		if d, ok := idleFor(a, now); ok && d >= limit && m.missionVia(id, byCard) == ms.ID {
			out = append(out, idleAgent{a, d})
		}
	}
	return out
}
