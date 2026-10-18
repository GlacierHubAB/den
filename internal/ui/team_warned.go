package ui

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/process"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// Den warns a pair of agents whose branches both change files once per
// file: the pairs and the files each was told about, and the pairs that
// agreed (overlap_ack), are kept in Den's state dir, so neither a restart
// nor a branch that stops overlapping for a while (a checkout switched,
// say) warns them again. A pair is forgotten when either agent is gone or
// either branch has landed.

// warnedPair is one pair Den warned, by its two sessions (A sorts first).
type warnedPair struct {
	A     string    `json:"a"`
	B     string    `json:"b"`
	Files []string  `json:"files"`
	Acked string    `json:"acked,omitempty"` // who said they agreed, "" for none
	At    time.Time `json:"at"`
}

// warnedRecord is the state file.
type warnedRecord struct {
	V     int                    `json:"v"`
	Pairs map[string]*warnedPair `json:"pairs"`
}

// The warned record's caps.
const (
	warnedFileMax  = 1 << 20 // bytes read
	warnedPairsMax = 500
	warnedFilesMax = process.MaxFiles
	warnedStale    = 7 * 24 * time.Hour // an unknown agent's pair
)

// teamWarnedPath is where the record lives.
func teamWarnedPath() string { return filepath.Join(memStateDir(), "team-warned.json") }

func pairKey(a, b string) string {
	if b < a {
		a, b = b, a
	}
	return a + "\x00" + b
}

// warnedPairs is the record, read from Den's state dir the first time.
func (m *Model) warnedPairs() map[string]*warnedPair {
	if m.team.pairs != nil {
		return m.team.pairs
	}
	m.team.pairs = map[string]*warnedPair{}
	// Never a symlink followed, and a FIFO cannot block the open.
	f, err := os.OpenFile(teamWarnedPath(), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return m.team.pairs
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return m.team.pairs
	}
	raw, err := io.ReadAll(io.LimitReader(f, warnedFileMax+1))
	if err != nil || len(raw) > warnedFileMax {
		return m.team.pairs
	}
	var rec warnedRecord
	if json.Unmarshal(raw, &rec) != nil || rec.V != 1 {
		return m.team.pairs
	}
	for _, p := range rec.Pairs {
		if p == nil || p.A == "" || p.B == "" || p.A == p.B {
			continue
		}
		key := pairKey(p.A, p.B)
		if len(p.Files) > warnedFilesMax {
			p.Files = p.Files[:warnedFilesMax]
		}
		m.team.pairs[key] = p
	}
	return m.team.pairs
}

// saveWarned writes the record, private and whole, by an atomic rename.
func (m *Model) saveWarned() {
	pairs := m.warnedPairs()
	if len(pairs) > warnedPairsMax {
		// The oldest go first.
		keys := make([]string, 0, len(pairs))
		for k := range pairs {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return pairs[keys[i]].At.Before(pairs[keys[j]].At) })
		for _, k := range keys[:len(pairs)-warnedPairsMax] {
			delete(pairs, k)
		}
	}
	raw, err := json.Marshal(warnedRecord{V: 1, Pairs: pairs})
	if err != nil {
		return
	}
	path := teamWarnedPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	// A fresh .tmp of its own: a stale one, or a symlink planted there,
	// is removed, never written through.
	tmp := path + ".tmp"
	os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return
	}
	_, werr := f.Write(raw)
	if cerr := f.Close(); werr != nil || cerr != nil {
		os.Remove(tmp)
		return
	}
	os.Rename(tmp, path)
}

// freshFiles are the files of an overlap the pair was not yet told about.
func freshFiles(p *warnedPair, files []string) []string {
	if p == nil {
		return files
	}
	told := make(map[string]bool, len(p.Files))
	for _, f := range p.Files {
		told[f] = true
	}
	var out []string
	for _, f := range files {
		if !told[f] {
			out = append(out, f)
		}
	}
	return out
}

// noteWarned records that the pair was told about files (or agreed on
// them, with acked who said so).
func (m *Model) noteWarned(a, b string, files []string, acked string, now time.Time) {
	pairs := m.warnedPairs()
	key := pairKey(a, b)
	p := pairs[key]
	if p == nil {
		lo, hi := a, b
		if hi < lo {
			lo, hi = hi, lo
		}
		p = &warnedPair{A: lo, B: hi}
		pairs[key] = p
	}
	seen := make(map[string]bool, len(p.Files))
	for _, f := range p.Files {
		seen[f] = true
	}
	for _, f := range files {
		if !seen[f] && len(p.Files) < warnedFilesMax {
			p.Files = append(p.Files, f)
			seen[f] = true
		}
	}
	sort.Strings(p.Files)
	if acked != "" {
		p.Acked = acked
	}
	p.At = now
}

// pruneWarned forgets the pairs one of whose agents is gone, or whose
// branch landed, as works read them; it reports whether any went. A
// checkout on the trunk keeps its pairs (its agent may switch back), and
// an agent Den does not know yet (its fleet is still being read) keeps
// them until warnedStale.
func (m *Model) pruneWarned(works []process.Work, now time.Time) bool {
	landed := map[string]bool{}
	for _, w := range works {
		if w.Landed {
			landed[w.Key] = true
		}
	}
	gone := func(sid string, at time.Time) bool {
		a, ok := m.agentFor(sid)
		if !ok {
			return now.Sub(at) > warnedStale
		}
		return a.Status == fleet.Gone || !a.Session.Alive
	}
	changed := false
	for key, p := range m.warnedPairs() {
		if landed[p.A] || landed[p.B] || gone(p.A, p.At) || gone(p.B, p.At) {
			delete(m.team.pairs, key)
			changed = true
		}
	}
	return changed
}

// handleOverlapAck is overlap_ack: an agent says it and the agent named
// agreed who goes first (or the orchestrator says so of two), and Den then
// warns neither about the files they overlap on now.
func (m *Model) handleOverlapAck(r *bridge.Request) {
	deny := func(why string) { r.Reply(bridge.Decision{Behavior: "deny", Message: why}) }
	var in bridge.OverlapAckInput
	if json.Unmarshal(r.Payload, &in) != nil {
		deny("give with, or agents")
		return
	}
	who, caller, ok := m.teamCaller(r.PeerPID)
	if !ok {
		deny("only an agent Den started, or the orchestrator, says so")
		return
	}
	var a, b string
	switch {
	case caller == "" && len(in.Agents) == 2: // the orchestrator
		var okA, okB bool
		a, okA = m.ackAgent(in.Agents[0])
		b, okB = m.ackAgent(in.Agents[1])
		if !okA || !okB {
			deny("name two agents of this machine")
			return
		}
	case caller != "" && in.With != "":
		var okB bool
		a = caller
		b, okB = m.ackAgent(in.With)
		if !okB {
			deny("no agent of this machine is named " + sanitize.Label(in.With, 60))
			return
		}
	default:
		deny("an agent gives with (the other agent), the orchestrator agents (both)")
		return
	}
	if a == b {
		deny("name another agent")
		return
	}
	var files []string
	for _, o := range m.team.overlaps[a] {
		if pairKey(o.A, o.B) == pairKey(a, b) {
			files = o.Files
		}
	}
	if len(files) == 0 {
		deny("Den sees no files your two branches both change now")
		return
	}
	m.noteWarned(a, b, files, sanitize.Label(who, 60), time.Now())
	m.saveWarned()
	an, bn := m.nameFor(a), m.nameFor(b)
	m.logEntry(m.addUnthreaded(chatEntry{role: "note", at: time.Now(), text: sanitize.Label(who, 60) + " says " + an + " and " + bn + " agreed on " + fileCount(len(files)) + " both their branches change: Den warns them again only for another file."}))
	r.Reply(bridge.Decision{Behavior: "allow", Message: "Den records that " + an + " and " + bn + " agreed on " + fileCount(len(files)) + ": it warns them again only for a file new to them."})
}

// ackAgent is the session of the live agent of this machine with that
// critter or peer name, only when one has it.
func (m *Model) ackAgent(name string) (string, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	found := ""
	for _, a := range m.agents {
		if a.Host != "" || a.Status == fleet.Gone || !a.Session.Alive {
			continue
		}
		if strings.ToLower(a.Name) == n || strings.ToLower(a.Session.Name) == n {
			if found != "" {
				return "", false // two share it: neither
			}
			found = a.Session.SessionID
		}
	}
	return found, found != ""
}
