package ui

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/mission"
	"github.com/GlacierHubAB/den/internal/process"
	"github.com/GlacierHubAB/den/internal/procs"
	"github.com/GlacierHubAB/den/internal/sanitize"
	"github.com/GlacierHubAB/den/internal/spawn"
)

// The small-team process, as Den keeps it: Den reads each local agent's
// branch from git itself, and two agents whose branches change the same
// files hear it from Den, once, so they can agree who goes first or how to
// split, with the orchestrator deciding. Den never stops anyone over it.

const (
	// teamEvery is how often Den reads the agents' branches.
	teamEvery = time.Minute
	// teamWarnEvery is the least time between two overlap messages to one
	// agent, so churn cannot make Den flood it.
	teamWarnEvery = 10 * time.Minute
	// overlapNames and overlapNameMax cap the file names a message quotes.
	overlapNames   = 20
	overlapNameMax = 120
	// teamScanTimeout bounds one read of every agent's branch.
	teamScanTimeout = 30 * time.Second
)

type teamState struct {
	scanning bool
	next     time.Time
	// overlaps are each agent's overlaps now, by session id; warned the
	// pairs told, until they stop overlapping; lastWarn when each agent
	// was last told.
	overlaps map[string][]process.Overlap
	warned   map[string]bool
	lastWarn map[string]time.Time
	// branches are each agent's branch as last read, by session id;
	// nudged when each agent's branch (session and branch) was last
	// nudged.
	branches map[string]process.Work
	nudged   map[string]time.Time
	// pending are the mission branches' tips to check until they land,
	// by repository and branch.
	pending map[string]pendingTip
}

// teamNudgeEvery is how often a branch that stays over a threshold is
// nudged again.
const teamNudgeEvery = 24 * time.Hour

// teamTarget is one agent's checkout to read.
type teamTarget struct{ key, dir string }

type teamScanMsg struct {
	works  []process.Work
	landed []string // pending tips that landed
}

// teamRead reads every target's branch; tests replace it.
var teamRead = func(targets []teamTarget) []process.Work {
	ctx, cancel := context.WithTimeout(context.Background(), teamScanTimeout)
	defer cancel()
	var works []process.Work
	for _, t := range targets {
		if w, err := process.Read(ctx, t.key, t.dir); err == nil {
			works = append(works, w)
		}
	}
	return works
}

// teamTick starts a read of the local agents' branches, at most one a
// teamEvery and one at a time: their overlaps, and their age.
func (m *Model) teamTick(now time.Time) tea.Cmd {
	m.memStartup() // once a run: each project's rules.md against Den's record
	if m.team.scanning || now.Before(m.team.next) {
		return nil
	}
	var targets []teamTarget
	for _, a := range m.agents {
		if a.Status == fleet.Gone || !a.Session.Alive || a.Host != "" || !a.HasGit || a.Session.CWD == "" || m.isOrchestrator(a) {
			continue
		}
		targets = append(targets, teamTarget{key: a.Session.SessionID, dir: a.Session.CWD})
	}
	m.team.next = now.Add(teamEvery)
	// A tip is checked after its agent has gone too: a worktree is often
	// closed right after its branch merged.
	checks := m.tipChecks(now)
	if len(targets) == 0 {
		m.team.overlaps, m.team.branches = nil, nil
		if len(checks) == 0 {
			return nil
		}
	}
	m.team.scanning = true
	return func() tea.Msg {
		var works []process.Work
		if len(targets) > 0 {
			works = teamRead(targets)
		}
		return teamScanMsg{works: works, landed: teamLanded(checks)}
	}
}

// applyTeamScan records the overlaps a read found and tells each new pair,
// both agents at once, unless one was told within teamWarnEvery: then the
// pair is told on a later read, while it still overlaps.
func (m *Model) applyTeamScan(msg teamScanMsg, now time.Time) tea.Cmd {
	m.team.scanning = false
	if m.team.warned == nil {
		m.team.warned, m.team.lastWarn = map[string]bool{}, map[string]time.Time{}
	}
	overlaps := process.Overlaps(msg.works)
	by := map[string][]process.Overlap{}
	current := map[string]bool{}
	var cmds []tea.Cmd
	for _, o := range overlaps {
		by[o.A] = append(by[o.A], o)
		by[o.B] = append(by[o.B], o)
		key := o.A + "\x00" + o.B
		current[key] = true
		if m.team.warned[key] {
			continue
		}
		a, okA := m.agentFor(o.A)
		b, okB := m.agentFor(o.B)
		if !okA || !okB || now.Sub(m.team.lastWarn[o.A]) < teamWarnEvery || now.Sub(m.team.lastWarn[o.B]) < teamWarnEvery {
			continue
		}
		m.team.warned[key] = true
		m.team.lastWarn[o.A], m.team.lastWarn[o.B] = now, now
		m.countProcess(func(s *mission.Stats) { s.Overlaps++ }, o.A, o.B)
		// A God-mode guarded agent takes messages only as Den mediates
		// them, and its hook would block this one: the orchestrator, which
		// sees the overlap in its roster, tells it instead.
		send := m.denSender() // framed as Den's, never the human's
		var to []fleet.Agent
		var guarded []string
		for _, x := range []fleet.Agent{a, b} {
			if m.med.guards[x.Session.SessionID] != nil {
				guarded = append(guarded, x.Name)
			} else {
				to = append(to, x)
			}
		}
		texts := map[string]string{a.Session.SessionID: overlapMessage(b, o.Files), b.Session.SessionID: overlapMessage(a, o.Files)}
		cmds = append(cmds, func() tea.Msg {
			for _, x := range to {
				send(x, texts[x.Session.SessionID])
			}
			return nil
		})
		note := "Den told " + a.Name + " and " + b.Name + " that their branches both change " + fileCount(len(o.Files)) + ": they agree who goes first or how to split, and the orchestrator decides."
		if len(guarded) > 0 {
			note += " " + strings.Join(guarded, " and ") + " runs guarded in God mode, so the orchestrator tells it instead."
		}
		m.logEntry(m.addUnthreaded(chatEntry{role: "note", at: now, text: note}))
	}
	for key := range m.team.warned {
		if !current[key] {
			delete(m.team.warned, key) // told again if they overlap again
		}
	}
	m.team.overlaps = by
	for _, w := range msg.works {
		if w.Repo != "" {
			m.tuneFile(w.Repo) // the project's tuning, read once
		}
	}
	m.applyLanded(msg.landed, now)
	m.notePending(msg.works, now)
	cmds = append(cmds, m.nudgeBranches(msg.works, now))
	return tea.Batch(cmds...)
}

// nudgeBranches tells each agent whose branch is older than BranchHours,
// or more than BehindMax commits behind its integration branch, to merge
// or rebase soon, once a teamNudgeEvery while it stays over; Den says so in
// its chat, which the dock shows too, and the roster carries it.
func (m *Model) nudgeBranches(works []process.Work, now time.Time) tea.Cmd {
	if m.team.nudged == nil {
		m.team.nudged = map[string]time.Time{}
	}
	m.team.branches = map[string]process.Work{}
	send := m.denSender()
	type nudge struct {
		agent fleet.Agent
		text  string
	}
	var out []nudge
	for _, w := range works {
		m.team.branches[w.Key] = w
		_, hours, behind, _ := m.teamFor(w.Repo)
		why := branchOver(w, now, hours, behind)
		key := w.Key + "\x00" + w.Branch
		if why == "" || now.Sub(m.team.nudged[key]) < teamNudgeEvery {
			continue
		}
		a, ok := m.agentFor(w.Key)
		if !ok {
			continue
		}
		m.team.nudged[key] = now
		m.countProcess(func(s *mission.Stats) { s.Nudges++ }, w.Key)
		branch := strconv.Quote(sanitize.Label(w.Branch, 80))
		// What the human can do about it from here, in Den or the dock.
		act := " You can tell it yourself (@" + a.Name + " merge or rebase now), or ask the orchestrator to split the work."
		note := "Den nudged " + a.Name + ": its branch " + branch + " " + why + "." + act
		if m.med.guards[w.Key] != nil {
			note = "Den did not message " + a.Name + ", which runs guarded in God mode: its branch " + branch + " " + why + ". The orchestrator tells it; or ask the orchestrator to split the work."
		} else {
			out = append(out, nudge{a, "Den, an automatic check (not the human): your branch " + branch + " " + why +
				". Short branches merge cleanly: merge it, or rebase it onto " + shortRef(w.Base) + ", soon, and tell the orchestrator if something holds it up."})
		}
		m.logEntry(m.addUnthreaded(chatEntry{role: "note", at: now, text: note}))
	}
	if len(out) == 0 {
		return nil
	}
	return func() tea.Msg {
		for _, n := range out {
			send(n.agent, n.text)
		}
		return nil
	}
}

// branchOver says how a branch is over the thresholds ("is 2 days old",
// "is 41 commits behind origin/dev", or both), "" when it is not.
func branchOver(w process.Work, now time.Time, hours, behind int) string {
	// Only a feature branch with work of its own not yet landed: never the
	// trunk, a checkout with no commits beyond its base, or one whose
	// commits are already merged.
	if w.Trunk || w.Landed || w.Since.IsZero() {
		return ""
	}
	var why []string
	if hours > 0 && !w.Since.IsZero() && now.Sub(w.Since) > time.Duration(hours)*time.Hour {
		why = append(why, "is "+age(now.Sub(w.Since))+" old")
	}
	if behind > 0 && w.Behind > behind {
		why = append(why, "is "+strconv.Itoa(w.Behind)+" commits behind "+shortRef(w.Base))
	}
	return strings.Join(why, " and ")
}

// age is a duration in whole days, or hours under two days.
func age(d time.Duration) string {
	if h := int(d.Hours()); h < 48 {
		return strconv.Itoa(h) + " hours"
	}
	return strconv.Itoa(int(d.Hours()/24)) + " days"
}

// shortRef is a ref as people say it: origin/dev, dev.
func shortRef(ref string) string {
	ref = strings.TrimPrefix(ref, "refs/remotes/")
	return strings.TrimPrefix(ref, "refs/heads/")
}

// branchLine is the roster's line for an agent's branch while it is over a
// threshold, "" otherwise.
func (m Model) branchLine(sessionID string, now time.Time) string {
	w, ok := m.team.branches[sessionID]
	if !ok {
		return ""
	}
	_, hours, behind, _ := m.teamFor(w.Repo)
	why := branchOver(w, now, hours, behind)
	if why == "" {
		return ""
	}
	return strconv.Quote(sanitize.Label(w.Branch, 80)) + " " + why
}

// overlapMessage is Den's message to an agent whose branch changes files
// that other's branch changes too. The file names are the branches' own,
// so they are quoted as data, never part of Den's sentence.
func overlapMessage(other fleet.Agent, files []string) string {
	return "Den, an automatic check (not the human): your branch and " + other.Name + "'s (peer for SendMessage: " +
		strconv.Quote(other.Session.Name) + ") both change these files, as git lists them: " + quotedFiles(files) +
		". Agree with " + other.Name + " who goes first or how to split the work, and tell the orchestrator, which decides."
}

// quotedFiles is files as quoted data: at most overlapNames, each cut to
// overlapNameMax, and how many more.
func quotedFiles(files []string) string {
	var q []string
	for i, f := range files {
		if i == overlapNames {
			q = append(q, "and "+strconv.Itoa(len(files)-overlapNames)+" more")
			break
		}
		q = append(q, strconv.Quote(sanitize.Label(f, overlapNameMax)))
	}
	return strings.Join(q, ", ")
}

func fileCount(n int) string {
	if n == 1 {
		return "1 file"
	}
	return strconv.Itoa(n) + " files"
}

// overlapLines are the roster's lines for an agent's overlaps: the other
// agent and the files, quoted as data.
func (m Model) overlapLines(sessionID string) []string {
	var out []string
	for _, o := range m.team.overlaps[sessionID] {
		other := o.B
		if other == sessionID {
			other = o.A
		}
		name := "another agent"
		if a, ok := m.agentFor(other); ok {
			name = a.Name
		}
		out = append(out, "with "+name+", files as git lists them: "+quotedFiles(o.Files))
	}
	return out
}

// teamReasonMax caps a reason for going over the team cap.
const teamReasonMax = 200

// teamCapMsg is a team query read off the loop: the repository the new
// agent goes into, and the live agents already in it.
type teamCapMsg struct {
	r       *bridge.Request
	in      bridge.TeamQueryInput
	repo    string // RepoID, "" when dir is not in a repository
	name    string // the repository's name, for the human
	members []string
}

// teamRepo reads a directory's repository id and name; tests replace it.
var teamRepo = func(ctx context.Context, dir string) (id, name string, err error) {
	id, err = process.RepoID(ctx, dir)
	if err != nil {
		return "", "", err
	}
	return id, id[:max(0, len(id)-13)], nil
}

// handleTeamQuery answers spawn_agent's question before it starts an
// agent: may the repository's team grow? It reads the repositories off the
// loop and answers there (applyTeamCap).
func (m *Model) handleTeamQuery(r *bridge.Request) tea.Cmd {
	var in bridge.TeamQueryInput
	if json.Unmarshal(r.Envelope.Payload, &in) != nil {
		r.Reply(bridge.Decision{Behavior: "allow"}) // spawn_agent refuses a bad input itself
		return nil
	}
	dir, err := spawn.ExpandDir(in.Dir)
	if err != nil {
		r.Reply(bridge.Decision{Behavior: "allow"}) // and a directory that is not there
		return nil
	}
	type member struct{ name, dir string }
	var live []member
	for _, a := range m.agents {
		if a.Status == fleet.Gone || !a.Session.Alive || a.Host != "" || a.Session.CWD == "" || m.isOrchestrator(a) {
			continue
		}
		live = append(live, member{a.Name, a.Session.CWD})
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), teamScanTimeout)
		defer cancel()
		msg := teamCapMsg{r: r, in: in}
		repo, name, err := teamRepo(ctx, dir)
		if err != nil {
			return msg // not a repository: no team to count
		}
		msg.repo, msg.name = repo, name
		for _, a := range live {
			if id, _, err := teamRepo(ctx, a.dir); err == nil && id == repo {
				msg.members = append(msg.members, a.name)
			}
		}
		return msg
	}
}

// applyTeamCap answers a team query: at the cap, only with a reason, which
// Den shows the human in its chat, quoted, with who gave it. Leads' spawns
// count as the orchestrator's do, and God mode does not lift the cap.
func (m *Model) applyTeamCap(msg teamCapMsg, now time.Time) {
	if msg.repo != "" {
		m.tuneFile(msg.repo)
	}
	limit, _, _, _ := m.teamFor(msg.repo)
	if limit <= 0 {
		limit = 3
	}
	if msg.repo == "" || len(msg.members) < limit {
		msg.r.Reply(bridge.Decision{Behavior: "allow"})
		return
	}
	repo := sanitize.Label(msg.name, 40)
	sort.Strings(msg.members)
	reason := sanitize.Label(strings.TrimSpace(msg.in.Reason), teamReasonMax)
	if reason == "" {
		msg.r.Reply(bridge.Decision{Behavior: "deny", Message: "the team in " + strconv.Quote(repo) + " has " + strconv.Itoa(len(msg.members)) +
			" agents, Den's cap of " + strconv.Itoa(limit) + " (" + strings.Join(msg.members, ", ") + "): give reason, one line on why one more agent is needed, or close one first"})
		return
	}
	// Who asks, Den reads from the asking process, never from the payload;
	// a caller that is neither the orchestrator's den mcp nor an agent's
	// gets its answer and no note in Den's chat.
	if who, agent, ok := m.teamCaller(msg.r.PeerPID); ok {
		line := who + " starts one more agent in " + strconv.Quote(repo) + ", over the team cap of " + strconv.Itoa(limit) + ", saying: " + strconv.Quote(reason)
		m.logEntry(m.addUnthreaded(chatEntry{role: "note", at: now, text: line}))
		if agent != "" && m.missions != nil {
			if ms := m.missions.Get(m.missionOf(agent)); ms != nil {
				ms.Note(now, line)
				m.missions.Save(ms)
			}
		}
	}
	msg.r.Reply(bridge.Decision{Behavior: "allow"})
}

// teamAncestors is a process's ancestors, itself first; tests replace it.
var teamAncestors = func(pid int) []int {
	var out []int
	for _, p := range procs.Ancestors(pid) {
		out = append(out, p.PID)
	}
	return out
}

// teamCaller names who asks, by its process: the orchestrator's den mcp,
// or a live local agent's (its process an ancestor of pid), with that
// agent's session id; ok is false for any other process.
func (m Model) teamCaller(pid int) (who, session string, ok bool) {
	if pid <= 1 {
		return "", "", false
	}
	if fromOrchestrator(&m, pid) {
		return "The orchestrator", "", true
	}
	by := map[int]fleet.Agent{}
	for _, a := range m.agents {
		if a.Status != fleet.Gone && a.Session.Alive && a.Host == "" && a.Session.PID > 1 && !m.isOrchestrator(a) {
			by[a.Session.PID] = a
		}
	}
	for _, p := range teamAncestors(pid) {
		if a, found := by[p]; found {
			return a.Name, a.Session.SessionID, true
		}
	}
	return "", "", false
}
