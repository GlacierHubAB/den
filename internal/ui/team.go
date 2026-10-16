package ui

import (
	"context"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/process"
	"github.com/GlacierHubAB/den/internal/sanitize"
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
}

// teamTarget is one agent's checkout to read.
type teamTarget struct{ key, dir string }

type teamScanMsg struct{ works []process.Work }

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
// teamEvery and one at a time.
func (m *Model) teamTick(now time.Time) tea.Cmd {
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
	if len(targets) < 2 {
		m.team.overlaps = nil
		return nil
	}
	m.team.scanning = true
	return func() tea.Msg { return teamScanMsg{works: teamRead(targets)} }
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
		send := m.sender()
		toA, toB := overlapMessage(b, o.Files), overlapMessage(a, o.Files)
		cmds = append(cmds, func() tea.Msg {
			send(a, toA)
			send(b, toB)
			return nil
		})
		m.logEntry(m.addUnthreaded(chatEntry{role: "note", at: now,
			text: "Den told " + a.Name + " and " + b.Name + " that their branches both change " + fileCount(len(o.Files)) + ": they agree who goes first or how to split, and the orchestrator decides."}))
	}
	for key := range m.team.warned {
		if !current[key] {
			delete(m.team.warned, key) // told again if they overlap again
		}
	}
	m.team.overlaps = by
	return tea.Batch(cmds...)
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
