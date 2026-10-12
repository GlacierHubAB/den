package fleet

import (
	"time"

	"github.com/GlacierHubAB/den/internal/badge"
	"github.com/GlacierHubAB/den/internal/cost"
	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/gitinfo"
	"github.com/GlacierHubAB/den/internal/sanitize"
	"github.com/GlacierHubAB/den/internal/transcript"
)

// LabelMax caps a badge role or task, and a peer name, in Meta.
const LabelMax = 80

// Meta is the metadata-only projection of an Agent: what may leave the
// machine under a remote host's observe policy (docs/REMOTE.md). It holds no
// prompt, reply, command, tool input, file path, cwd, transcript or socket
// path, title or Tail. A field added here crosses the wire to every remote
// Den, so TestMetaFieldsAreAllowlisted names each one; keep it that way.
//
// Times are ages measured on the host that made the projection, so the
// receiving side never compares clocks across machines. Dollars are not
// sent: tokens go by model and the receiver prices them from its own table.
type Meta struct {
	Index   int    `json:"index"`            // position in this host's fleet, from 0
	Session string `json:"session"`          // Claude Code session id
	Status  string `json:"status"`           // Status.Key()
	Name    string `json:"name"`             // critter name
	Peer    string `json:"peer,omitempty"`   // Claude Code's session name, --name
	Role    string `json:"role,omitempty"`   // badge role, capped; withheld without labels
	Task    string `json:"task,omitempty"`   // badge task, capped; withheld without labels
	Model   string `json:"model,omitempty"`  // raw model id
	Claude  string `json:"claude,omitempty"` // Claude Code version of the session
	Repo    string `json:"repo,omitempty"`   // repository name, never its path
	Branch  string `json:"branch,omitempty"` // checked-out branch
	// Dirty says the checkout has uncommitted changes. A yes or no, never a
	// path; absent when unknown. serve fills it only under approve or full,
	// for the close question.
	Dirty      *bool `json:"dirty,omitempty"`
	StatusAge  int64 `json:"status_age_ms"`  // since the last busy/idle change, -1 unknown
	StartedAge int64 `json:"started_age_ms"` // since the session started, -1 unknown
	ActiveAge  int64 `json:"last_active_ms"` // since LastActive, -1 unknown

	Context        int  `json:"context_tokens,omitempty"` // context used by the last turn
	Window         int  `json:"context_window,omitempty"` // the model's context window
	HasUsage       bool `json:"has_usage,omitempty"`
	UsageFromBadge bool `json:"usage_from_badge,omitempty"`

	Limits   *MetaLimits                  `json:"limits,omitempty"`          // rate limits as agent-badge recorded them
	Tokens   map[string]transcript.Tokens `json:"tokens,omitempty"`          // own tokens by model id
	Sub      map[string]transcript.Tokens `json:"subagent_tokens,omitempty"` // its subagents' tokens by model id
	Reported *float64                     `json:"reported_usd,omitempty"`    // Claude Code's own cost figure
	Unpriced bool                         `json:"reported_unpriced,omitempty"`
}

// MetaLimits is the numeric part of badge.Usage.
type MetaLimits struct {
	Context        *float64 `json:"context,omitempty"`
	Filled         *float64 `json:"filled,omitempty"`
	Window         *float64 `json:"window,omitempty"`
	FiveHour       *float64 `json:"five_hour,omitempty"`
	FiveHourResets string   `json:"five_hour_resets,omitempty"`
	SevenDay       *float64 `json:"seven_day,omitempty"`
	SevenDayResets string   `json:"seven_day_resets,omitempty"`
	USD            *float64 `json:"usd,omitempty"`
}

// Meta projects the agent as host-local agent number index, with ages
// measured at now. labels false withholds the badge role and task. Every
// string is sanitised and the free-form ones capped, so the projection is
// safe to send as it is.
func (a Agent) Meta(index int, now time.Time, labels bool) Meta {
	m := Meta{
		Index:      index,
		Session:    sanitize.Label(a.Session.SessionID, LabelMax),
		Status:     a.Status.Key(),
		Name:       sanitize.Label(a.Name, LabelMax),
		Peer:       sanitize.Label(a.Session.Name, LabelMax),
		Model:      sanitize.Label(a.Model, LabelMax),
		Claude:     sanitize.Label(a.Session.Version, 40),
		StatusAge:  age(now, a.Session.StatusAt),
		StartedAge: age(now, a.Session.StartedAt),
		ActiveAge:  -1,
		Context:    a.Usage.Tokens,
		Window:     a.Usage.Window,
		HasUsage:   a.HasUsage,
	}
	m.UsageFromBadge = a.UsageFromBadge
	if labels {
		m.Role = sanitize.Label(a.Labels.Role, LabelMax)
		m.Task = sanitize.Label(a.Labels.Task, LabelMax)
	}
	if a.HasGit {
		m.Repo = sanitize.Label(a.Git.Repo, LabelMax)
		m.Branch = sanitize.Label(a.Git.Branch, LabelMax)
	}
	if !a.LastActive.IsZero() {
		m.ActiveAge = max(0, now.Sub(a.LastActive).Milliseconds())
	}
	if a.HasLimit {
		l := a.Limits
		m.Limits = &MetaLimits{
			Context: l.Context, Filled: l.Filled, Window: l.Window,
			FiveHour: l.FiveHour, FiveHourResets: sanitize.Label(l.FiveHourResets, 40),
			SevenDay: l.SevenDay, SevenDayResets: sanitize.Label(l.SevenDayResets, 40),
			USD: l.USD,
		}
	}
	if a.Path != "" {
		acc := transcript.Shared.Account(a.Path)
		m.Tokens, m.Sub = cleanModels(acc.Main), cleanModels(acc.Sub)
		if acc.Reported != nil {
			r := acc.Reported.USD
			m.Reported, m.Unpriced = &r, acc.Reported.Unknown
		}
	}
	return m
}

func cleanModels(in map[string]transcript.Tokens) map[string]transcript.Tokens {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]transcript.Tokens, len(in))
	for k, v := range in {
		out[sanitize.Label(k, LabelMax)] = v
	}
	return out
}

// age is now minus a millisecond instant, -1 when the instant is unknown.
func age(now time.Time, ms int64) int64 {
	if ms <= 0 {
		return -1
	}
	return max(0, now.UnixMilli()-ms)
}

// FromMeta turns a projection received from host back into an Agent for
// Den's views. received is the local time the projection arrived: the
// remote's ages are counted back from it, so durations shown are the
// remote's own and no clock is compared across machines. Tokens are priced
// from prices, the local table. Strings are sanitised again here: the
// receiver never trusts the sender to have done it.
func FromMeta(m Meta, host string, received time.Time, prices cost.Table) Agent {
	back := func(ageMs int64) int64 {
		if ageMs < 0 {
			return 0
		}
		return received.UnixMilli() - ageMs
	}
	a := Agent{
		Session: discover.Session{
			SessionID: sanitize.Label(m.Session, LabelMax),
			Name:      sanitize.Label(m.Peer, LabelMax),
			Version:   sanitize.Label(m.Claude, 40),
			StartedAt: back(m.StartedAge),
			StatusAt:  back(m.StatusAge),
		},
		Name:           sanitize.Label(m.Name, LabelMax),
		Labels:         badge.Labels{Role: sanitize.Label(m.Role, LabelMax), Task: sanitize.Label(m.Task, LabelMax)},
		Model:          sanitize.Label(m.Model, LabelMax),
		Status:         statusFromKey(m.Status),
		Usage:          transcript.Usage{Tokens: m.Context, Window: m.Window, Model: sanitize.Label(m.Model, LabelMax)},
		HasUsage:       m.HasUsage,
		UsageFromBadge: m.UsageFromBadge,
		Host:           host,
	}
	if a.Name == "" {
		a.Name = badge.Name(a.Session.SessionID)
	}
	switch a.Status {
	case Working:
		a.Session.Status = "busy"
	case Idle:
		a.Session.Status = "idle"
	}
	a.Session.Alive = a.Status != Gone
	a.Dirty = m.Dirty
	if m.Repo != "" {
		a.Git, a.HasGit = gitinfo.Info{Repo: sanitize.Label(m.Repo, LabelMax), Branch: sanitize.Label(m.Branch, LabelMax)}, true
	}
	if m.ActiveAge >= 0 {
		a.LastActive = time.UnixMilli(back(m.ActiveAge))
	}
	if l := m.Limits; l != nil {
		a.Limits = badge.Usage{
			Context: l.Context, Filled: l.Filled, Window: l.Window,
			FiveHour: l.FiveHour, FiveHourResets: sanitize.Label(l.FiveHourResets, 40),
			SevenDay: l.SevenDay, SevenDayResets: sanitize.Label(l.SevenDayResets, 40),
			USD: l.USD,
		}
		a.HasLimit = true
	}
	acc := transcript.Account{Main: cleanModels(m.Tokens), Sub: cleanModels(m.Sub)}
	if m.Reported != nil {
		acc.Reported = &transcript.Reported{USD: *m.Reported, Unknown: m.Unpriced}
	}
	a.Cost = prices.Of(acc)
	return a
}

func statusFromKey(k string) Status {
	switch k {
	case "needs_you":
		return NeedsYou
	case "working":
		return Working
	case "idle":
		return Idle
	case "gone":
		return Gone
	}
	return Unknown
}
