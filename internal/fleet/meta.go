package fleet

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/GlacierHubAB/den/internal/badge"
	"github.com/GlacierHubAB/den/internal/cost"
	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/gitinfo"
	"github.com/GlacierHubAB/den/internal/names"
	"github.com/GlacierHubAB/den/internal/sanitize"
	"github.com/GlacierHubAB/den/internal/transcript"
)

// LabelMax caps a badge role or task, and a peer name, in Meta.
const LabelMax = 80

// Meta is the metadata-only projection of an Agent: what may leave the machine
// under a remote host's observe policy (docs/REMOTE.md). Outside Full it holds
// no prompt, reply, command, tool input, file path, cwd, transcript or socket
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
	Given   string `json:"given,omitempty"`  // the name its Den gave it from its role
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

	// Full is what only a host under the full policy sends: content, not
	// metadata. serve sets it only then, and Den drops it unless both the
	// host's configured and applied policies are full. It is never saved to
	// disk on either side.
	Full *MetaFull `json:"full,omitempty"`
}

// MetaFull is an agent's content for the details pane of a Den whose host
// policy is full: its title, its folder on the host, and its recent
// activity. Every string is sanitised and capped on the host and again on
// arrival.
type MetaFull struct {
	Title   string      `json:"title,omitempty"`
	CWD     string      `json:"cwd,omitempty"` // a path on the host, never used as a local one
	Tail    []MetaEntry `json:"tail,omitempty"`
	TailCut bool        `json:"tail_cut,omitempty"` // older entries were left out to fit
}

// MetaEntry is a transcript.Entry with its time as an age.
type MetaEntry struct {
	Age     int64        `json:"age_ms"` // -1 unknown
	Kind    string       `json:"kind"`
	Text    string       `json:"text,omitempty"`
	Tool    string       `json:"tool,omitempty"`
	Code    string       `json:"code,omitempty"`
	Output  string       `json:"output,omitempty"`
	Changes []MetaChange `json:"changes,omitempty"`
}

// MetaChange is one file a tool call changed.
type MetaChange struct {
	Path    string     `json:"path"`
	Created bool       `json:"created,omitempty"`
	Deleted bool       `json:"deleted,omitempty"`
	Unsafe  bool       `json:"unsafe,omitempty"` // the real name has control characters: no hunks
	Hunks   []MetaHunk `json:"hunks,omitempty"`
}

// MetaHunk is a transcript.Hunk.
type MetaHunk struct {
	OldStart int      `json:"old_start"`
	NewStart int      `json:"new_start"`
	Lines    []string `json:"lines"`
}

// Caps of MetaFull, per agent. A snapshot is one message line, so Full
// takes a budget its sender divides among the agents.
const (
	FullTitleMax   = 200
	FullCWDMax     = 1024
	FullTextMax    = 4 << 10 // a prompt, reply, command or output
	FullChanges    = 4       // per entry
	FullHunkLines  = 40      // per change
	FullTailMax    = 20      // entries
	FullTailBudget = 24 << 10
)

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
		Name:       sanitize.Label(critterOf(a), LabelMax),
		Given:      givenOf(a),
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

// FullMeta is the agent's MetaFull, measured at now, with its Tail kept
// newest first within budget bytes of text.
func (a Agent) FullMeta(now time.Time, budget int) *MetaFull {
	f := &MetaFull{
		Title: sanitize.Label(a.Title, FullTitleMax),
		CWD:   capBytes(sanitize.Line(a.Session.CWD), FullCWDMax),
	}
	var rev []MetaEntry
	used := 0
	for i := len(a.Tail) - 1; i >= 0; i-- {
		if len(rev) == FullTailMax {
			f.TailCut = true
			break
		}
		e := metaEntry(a.Tail[i], now)
		n := e.size()
		if used+n > budget {
			f.TailCut = true
			break
		}
		used += n
		rev = append(rev, e)
	}
	for i := len(rev) - 1; i >= 0; i-- {
		f.Tail = append(f.Tail, rev[i])
	}
	return f
}

func metaEntry(e transcript.Entry, now time.Time) MetaEntry {
	m := MetaEntry{
		Age:    -1,
		Kind:   sanitize.Label(e.Kind, 16),
		Text:   capBytes(sanitize.Text(e.Text), FullTextMax),
		Tool:   sanitize.Label(e.Tool, LabelMax),
		Code:   capBytes(sanitize.Text(e.Code), FullTextMax),
		Output: capBytes(sanitize.Text(e.Output), FullTextMax),
	}
	if !e.At.IsZero() {
		m.Age = max(0, now.Sub(e.At).Milliseconds())
	}
	for _, c := range e.Changes[:min(len(e.Changes), FullChanges)] {
		mc := MetaChange{Path: capBytes(sanitize.Line(c.Path), FullCWDMax), Created: c.Created, Deleted: c.Deleted, Unsafe: c.Hidden()}
		left := FullHunkLines
		if mc.Unsafe {
			left = 0 // the name only, as Review sends it
		}
		for _, h := range c.Hunks {
			if left <= 0 {
				break
			}
			lines := h.Lines[:min(len(h.Lines), left)]
			left -= len(lines)
			mh := MetaHunk{OldStart: h.OldStart, NewStart: h.NewStart, Lines: make([]string, len(lines))}
			for i, l := range lines {
				mh.Lines[i] = capBytes(sanitize.Code(l), 1000)
			}
			mc.Hunks = append(mc.Hunks, mh)
		}
		m.Changes = append(m.Changes, mc)
	}
	return m
}

// size is how many bytes e takes on the wire, about.
func (e MetaEntry) size() int {
	n := 64 + WireLen(e.Text) + WireLen(e.Tool) + WireLen(e.Code) + WireLen(e.Output)
	for _, c := range e.Changes {
		n += 48 + WireLen(c.Path)
		for _, h := range c.Hunks {
			n += 48
			for _, l := range h.Lines {
				n += WireLen(l) + 1
			}
		}
	}
	return n
}

// WireLen is how many bytes s takes as a JSON string, as encoding/json
// writes it: <, > and & escaped, and U+2028 and U+2029 too.
func WireLen(s string) int {
	n := 2
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"' || c == '\\' || c == '\n' || c == '\t' || c == '\r':
			n += 2
		case c < 0x20 || c == '<' || c == '>' || c == '&':
			n += 6
		case c == 0xE2 && i+2 < len(s) && s[i+1] == 0x80 && (s[i+2] == 0xA8 || s[i+2] == 0xA9):
			n += 6
			i += 2
		default:
			n++
		}
	}
	return n
}

// capBytes cuts s to at most max bytes on a rune boundary, ending in "…"
// when cut.
func capBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// fromFull applies a received MetaFull to a, sanitising it again: Title,
// RemoteCWD (never Session.CWD, which Den reads as a local folder) and Tail
// with times counted back from received.
func (a *Agent) fromFull(f *MetaFull, received time.Time) {
	a.Title = sanitize.Label(f.Title, FullTitleMax)
	a.RemoteCWD = capBytes(sanitize.Line(f.CWD), FullCWDMax)
	for _, e := range f.Tail[:min(len(f.Tail), FullTailMax)] {
		te := transcript.Entry{
			Kind:   sanitize.Label(e.Kind, 16),
			Text:   capBytes(sanitize.Text(e.Text), FullTextMax),
			Tool:   sanitize.Label(e.Tool, LabelMax),
			Code:   capBytes(sanitize.Text(e.Code), FullTextMax),
			Output: capBytes(sanitize.Text(e.Output), FullTextMax),
		}
		if e.Age >= 0 {
			te.At = received.Add(-time.Duration(e.Age) * time.Millisecond)
		}
		for _, c := range e.Changes[:min(len(e.Changes), FullChanges)] {
			fc := transcript.FileChange{Path: capBytes(sanitize.Line(c.Path), FullCWDMax), Created: c.Created, Deleted: c.Deleted, Unsafe: c.Unsafe}
			left := FullHunkLines
			if fc.Unsafe {
				left = 0 // whatever a host sends for it
			}
			for _, h := range c.Hunks {
				if left <= 0 {
					break
				}
				lines := h.Lines[:min(len(h.Lines), left)]
				left -= len(lines)
				th := transcript.Hunk{OldStart: h.OldStart, NewStart: h.NewStart}
				for _, l := range lines {
					if l = sanitize.Code(l); l != "" && strings.ContainsRune(" +-", rune(l[0])) {
						th.Lines = append(th.Lines, capBytes(l, 1000))
					}
				}
				fc.Hunks = append(fc.Hunks, th)
			}
			te.Changes = append(te.Changes, fc)
		}
		a.Tail = append(a.Tail, te)
	}
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

// critterOf is an agent's critter name: its alias when it has a given name.
func critterOf(a Agent) string {
	if a.Alias != "" {
		return a.Alias
	}
	return a.Name
}

// givenOf is the name Den gave an agent, "" when it shows its critter name.
func givenOf(a Agent) string {
	if a.Alias != "" {
		return a.Name
	}
	return ""
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
	// The critter name is computed here from the session id, as the badge on
	// the host does, never taken from the host. The name the host's Den gave
	// is shown only when it is one this Den could have given (names.Valid),
	// and always with its host: a name with no host means this machine, so
	// a host's name never captures a mention or a command meant for a local
	// agent (sec's N7).
	a.Name = badge.Name(a.Session.SessionID)
	if g := m.Given; g != "" && names.Valid(g) {
		a.Name, a.Alias = g, a.Name
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
	if m.Full != nil {
		a.fromFull(m.Full, received)
	}
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
