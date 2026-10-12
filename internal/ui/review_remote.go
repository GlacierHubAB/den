package ui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/remote"
	"github.com/GlacierHubAB/den/internal/review"
	"github.com/GlacierHubAB/den/internal/transcript"
)

// Remote agents in Review (docs/REMOTE.md). A remote agent's
// work is read on its host, by `den remote serve`, and only when the host's
// policy is full in remotes.toml and as the host applies it. Its diff
// arrives sanitised and capped; Den shows it like a local one, with the
// host chip, and never treats its paths as folders here. Comments /send
// through the host's session. Merging and removing a worktree stay on this
// machine: they are refused for remote work.

// remoteReview is one remote agent's diff as Review last read it.
type remoteReview struct {
	host    string
	loading bool
	at      time.Time // when the last answer came
	err     string    // why the last read failed
	works   []work    // the diff as work, none or one
}

type reviewRemoteMsg struct {
	key  string
	diff *remote.Diff
	err  error
}

// remoteReviewable is the remote agents whose diff Review may ask for now:
// live hosts whose policy is full on both sides.
func (m Model) remoteReviewable() []fleet.Agent {
	var out []fleet.Agent
	for _, a := range m.agents {
		if a.Host == "" || a.Status == fleet.Gone || a.Stale || m.remote.feed == nil {
			continue
		}
		if h, ok := m.hostStatus(a.Host); !ok || h.State != remote.Live || h.Policy != remote.Full || h.Applied != remote.Full {
			continue
		}
		out = append(out, a)
	}
	return out
}

// loadRemoteReviews asks each reviewable remote agent's host for its diff,
// one request per agent and at most remote.MaxDiffs per host at a time, as
// serve reads no more at once; the rest are asked as answers come back.
// The Manager's wait bounds each one.
func (m *Model) loadRemoteReviews() tea.Cmd { return m.loadRemote(false) }

// loadRemote is loadRemoteReviews; onlyNew asks only for agents not read
// since their host last came up.
func (m *Model) loadRemote(onlyNew bool) tea.Cmd {
	var cmds []tea.Cmd
	feed := m.remote.feed
	busy := map[string]int{}
	for _, rr := range m.rev.remote {
		if rr.loading {
			busy[rr.host]++
		}
	}
	for _, a := range m.remoteReviewable() {
		key := a.Session.SessionID
		if m.rev.remote == nil {
			m.rev.remote = map[string]*remoteReview{}
		}
		rr := m.rev.remote[key]
		if rr == nil {
			rr = &remoteReview{host: a.Host}
			m.rev.remote[key] = rr
		}
		if rr.loading || busy[a.Host] >= remote.MaxDiffs || (onlyNew && !rr.at.IsZero()) {
			continue
		}
		busy[a.Host]++
		rr.loading = true
		sid, host := splitKey(key)
		cmds = append(cmds, func() tea.Msg {
			d, err := feed.Diff(host, sid)
			return reviewRemoteMsg{key: key, diff: d, err: err}
		})
	}
	return tea.Batch(cmds...)
}

// applyRemoteReview takes one host's answer, and asks for the next agent
// waiting on that host.
func (m *Model) applyRemoteReview(msg reviewRemoteMsg) tea.Cmd {
	rr := m.rev.remote[msg.key]
	if rr == nil {
		return nil
	}
	rr.loading, rr.at = false, time.Now()
	if h, ok := m.hostStatus(rr.host); !ok || h.State != remote.Live {
		// The link dropped while it was read: it is not current.
		rr.works, rr.err, rr.at = nil, "", time.Time{}
		m.mergeReview()
		return nil
	}
	switch {
	case msg.err != nil:
		rr.err, rr.works = msg.err.Error(), nil
	case msg.diff != nil && msg.diff.Unowned:
		rr.err, rr.works = "none of the changes in its checkout on "+rr.host+" match what it wrote, so they stay there", nil
	default:
		rr.err, rr.works = "", workFromDiff(msg.key, rr.host, msg.diff)
	}
	m.mergeReview()
	return m.loadRemote(true)
}

// remoteWorks is the remote agents' work to list: only from hosts that are
// live now, so a diff read before a link dropped never shows as current.
func (m Model) remoteWorks() []work {
	var out []work
	for key, rr := range m.rev.remote {
		if h, ok := m.hostStatus(rr.host); !ok || h.State != remote.Live || h.Policy != remote.Full || h.Applied != remote.Full {
			continue
		}
		if a, ok := m.agentFor(key); !ok || a.Stale || a.Status == fleet.Gone {
			continue
		}
		out = append(out, rr.works...)
	}
	return out
}

// workFromDiff turns a host's diff into the one work Review shows for the
// agent with key. Its Root is a key made of the host's folder and the host,
// never a folder on this machine.
func workFromDiff(key, host string, d *remote.Diff) []work {
	if d == nil || len(d.Files) == 0 {
		return nil
	}
	w := work{
		SessionID: key, Host: host, Root: d.Root + " @" + host, Main: remoteProject(host, d.Repo),
		Repo: d.Repo, Worktree: d.Worktree, Branch: d.Branch, Ahead: d.Ahead, Cut: d.Cut,
	}
	if d.Branch != "" {
		w.Repo += " · " + d.Branch
	}
	for _, df := range d.Files {
		f := review.File{Path: df.Path, OldPath: df.OldPath, Created: df.Created, Deleted: df.Deleted, Binary: df.Binary}
		for _, h := range df.Hunks {
			f.Hunks = append(f.Hunks, transcript.Hunk{OldStart: h.OldStart, NewStart: h.NewStart, Lines: h.Lines})
			own := make([]string, len(h.Lines))
			for _, i := range h.Mine {
				if i >= 0 && i < len(own) {
					own[i] = key
				}
			}
			f.Owner = append(f.Owner, own)
		}
		added, removed := ownedCounts(f, key)
		w.Added += added
		w.Removed += removed
		w.Files = append(w.Files, f)
	}
	return []work{w}
}

// remoteNote is what Review says of a remote agent with no diff to list:
// why it stays on its host, that it is loading, or why the last read failed.
func (m Model) remoteNote(a fleet.Agent) string {
	h, ok := m.hostStatus(a.Host)
	switch {
	case !ok || a.Stale || (h.State != remote.Live && h.State != remote.AttachedElsewhere):
		return a.Host + " is offline"
	case h.State == remote.AttachedElsewhere:
		return remote.ElsewhereNote(a.Host)
	case h.Policy != remote.Full || h.Applied != remote.Full:
		p := h.Policy
		if h.Applied != "" {
			p = remote.Min(p, h.Applied)
		}
		return remote.FullNote(a.Host, p)
	}
	rr := m.rev.remote[a.Session.SessionID]
	switch {
	case rr == nil || (rr.loading && rr.at.IsZero()):
		return "reading its diff on " + a.Host + "…"
	case rr.err != "":
		return rr.err
	}
	return "no changes on " + a.Host
}

// remoteReviewChanged follows a host's link in Review: a host that is not
// live loses the diffs read from it, so none shows as current when it comes
// back; a live full host is asked again while Review is open.
func (m *Model) remoteReviewChanged(host string) tea.Cmd {
	h, ok := m.hostStatus(host)
	if !ok || h.State != remote.Live || h.Policy != remote.Full || h.Applied != remote.Full {
		dropped := false
		for _, rr := range m.rev.remote {
			if rr.host == host && (len(rr.works) > 0 || !rr.at.IsZero()) {
				rr.works, rr.err, rr.at, dropped = nil, "", time.Time{}, true
			}
		}
		if dropped {
			m.mergeReview()
		}
		return nil
	}
	if m.tab != tabReview {
		return nil
	}
	return m.loadRemoteReviews()
}
