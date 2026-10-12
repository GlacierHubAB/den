package remote

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/GlacierHubAB/den/internal/cost"
	"github.com/GlacierHubAB/den/internal/events"
	"github.com/GlacierHubAB/den/internal/fleet"
)

// StateDir is where Den keeps what it last knew of each host:
// $XDG_STATE_HOME/den/remote, mode 0700.
func StateDir() string {
	p := events.Path()
	if p == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(p), "remote")
}

// Known is what Den last knew of one host, saved as <label>.json in
// StateDir (mode 0600). On disk it is metadata only (saveKnown drops Full): the fleet as fleet.Meta,
// already filtered by policy and sanitised, and where the host's event
// stream was read up to.
type Known struct {
	Label     string        `json:"label"`
	Cursor    events.Cursor `json:"cursor,omitzero"`
	State     State         `json:"state"`
	Reason    string        `json:"reason,omitempty"`
	Since     time.Time     `json:"since,omitzero"`
	Heartbeat time.Time     `json:"heartbeat,omitzero"` // the running Den last confirmed the state
	Hostname  string        `json:"hostname,omitempty"`
	Den       string        `json:"den,omitempty"`
	Claude    string        `json:"claude,omitempty"`
	Policy    Policy        `json:"policy,omitempty"` // as applied by the host
	Offset    time.Duration `json:"offset_ns,omitempty"`
	Received  time.Time     `json:"received,omitzero"` // local time the snapshot arrived
	Agents    []fleet.Meta  `json:"agents,omitempty"`
}

// HeartbeatEvery is how often a running Den re-saves a live host's state;
// a heartbeat older than three of these means no Den is attached.
const HeartbeatEvery = 15 * time.Second

func knownPath(dir, label string) string { return filepath.Join(dir, label+".json") }

// LoadKnown reads what was saved for label; ok is false when nothing was.
func LoadKnown(dir, label string) (Known, bool) {
	raw, err := os.ReadFile(knownPath(dir, label))
	if err != nil {
		return Known{}, false
	}
	var k Known
	if json.Unmarshal(raw, &k) != nil || k.Label != label {
		return Known{}, false
	}
	return k, true
}

// saveKnown writes k atomically, private to this user.
func saveKnown(dir string, k Known) error {
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	os.Chmod(dir, 0o700)
	// Content under the full policy is for the running Den's views only:
	// it is never written down.
	k.Agents = metaOnly(k.Agents)
	raw, err := json.Marshal(k)
	if err != nil {
		return err
	}
	return writeAtomic(knownPath(dir, k.Label), append(raw, '\n'), 0o600)
}

// Attached reports whether a running Den holds the host live now.
func (k Known) Attached(now time.Time) bool {
	return (k.State == Live || k.State == AttachedElsewhere) && now.Sub(k.Heartbeat) < 3*HeartbeatEvery
}

// Agents rebuilds the saved fleet with Host set, HostState and Stale
// saying how current it is. labels false drops role and task again.
func (k Known) agents(h Host, prices cost.Table, now time.Time) []fleet.Agent {
	state, stale := string(k.State), !k.Attached(now)
	if (k.State == Live || k.State == AttachedElsewhere) && stale {
		state = "detached" // saved as live, but no Den is attached now
	}
	out := make([]fleet.Agent, 0, len(k.Agents))
	for _, m := range k.Agents {
		m, _ = recheck(m, h, k.Policy)
		a := fleet.FromMeta(m, h.Label, k.Received, prices)
		a.HostState, a.Stale = state, stale
		out = append(out, a)
	}
	return out
}

// LastKnown is every enabled host's agents as last saved, for `den list`
// when it runs beside (or without) a Den: Host is set, and Stale is true
// unless a running Den holds the host live.
func LastKnown(cfg Config, prices cost.Table, now time.Time) []fleet.Agent {
	dir := StateDir()
	var out []fleet.Agent
	for _, h := range cfg.Enabled() {
		if k, ok := LoadKnown(dir, h.Label); ok {
			out = append(out, k.agents(h, prices, now)...)
		}
	}
	return out
}

// recheck is the local half of the policy filter: whatever the host sent,
// keep only what its policy (and labels setting) in remotes.toml and the
// policy the host said it applies both allow. The labels go when they are
// withheld; the content of Full unless both policies are full, and dropped
// then says the host sent what it should not have.
func recheck(m fleet.Meta, h Host, applied Policy) (_ fleet.Meta, dropped bool) {
	if !h.Labels {
		m.Role, m.Task = "", ""
	}
	if m.Full != nil && (h.Policy != Full || applied != Full) {
		m.Full, dropped = nil, true
	}
	return m, dropped
}

// metaOnly is the agents without their Full content, for what is saved.
func metaOnly(agents []fleet.Meta) []fleet.Meta {
	out := make([]fleet.Meta, len(agents))
	for i, a := range agents {
		a.Full = nil
		out[i] = a
	}
	return out
}
