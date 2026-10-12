package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/GlacierHubAB/den/internal/badge"
	"github.com/GlacierHubAB/den/internal/cost"
	"github.com/GlacierHubAB/den/internal/events"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// State is where a host's link stands (docs/REMOTE-DESIGN.md §4.4).
type State string

const (
	Connecting        State = "connecting"         // ssh is starting
	Live              State = "live"               // hello received, versions compatible
	Offline           State = "offline"            // the session ended or failed; retried with backoff
	NeedsAttention    State = "needs_attention"    // auth or host key failure: not retried until Reconnect
	VersionMismatch   State = "version_mismatch"   // den missing, too old, or another proto: not retried until Reconnect
	AttachedElsewhere State = "attached_elsewhere" // reserved for slice 2: another Den holds the host
)

// HostStatus is one host as the views show it.
type HostStatus struct {
	Label    string
	Target   string
	Policy   Policy    // configured in remotes.toml
	Applied  Policy    // what the host says it applies; "" before hello
	State    State     //
	Reason   string    // why it is in that state, sanitised
	Since    time.Time // local time the state began
	Hostname string    // the host's own name, from hello
	Den      string    // den version on the host
	Claude   string    // newest Claude Code version among its sessions
	Offset   time.Duration
	LastSeen time.Time // local time of the last message from the host
	Agents   int       // in the last snapshot
	Flags    []string  // what the host sent that Den dropped: unknown types or fields, flooding
	Hint     string    // what the human can do, for needs attention and version mismatch
}

// UpdateKind says what an Update carries.
type UpdateKind int

const (
	HostChanged  UpdateKind = iota // Status changed; Agents are the host's agents re-marked
	FleetChanged                   // a new snapshot: Agents
	EventArrived                   // Event, already appended to the local events.jsonl
)

// Update is one change, sent on Manager.Updates.
type Update struct {
	Kind   UpdateKind
	Host   string
	Status HostStatus
	Agents []fleet.Agent // HostChanged and FleetChanged: the host's agents, Host, HostState and Stale set
	Event  events.Event  // EventArrived: Host set, Time on the local clock
}

// Conn is one running session with `den remote serve`.
type Conn struct {
	In   io.WriteCloser // serve's stdin
	Out  io.Reader      // serve's stdout
	Wait func() error   // waits for the session to end; a *Failure says why
	Kill func()         // ends it now
}

// Dialer starts a session with a host.
type Dialer func(ctx context.Context, h Host) (*Conn, error)

// Options tune a Manager; the zero value is what Den uses.
type Options struct {
	Version    string     // this den's version, sent in the request
	Prices     cost.Table // prices remote tokens; default the built-in table, Den passes cost.NewTable(config.Prices())
	StateDir   string     // default StateDir()
	EventsPath string     // where remote events are appended; default events.Path()
	NoEventLog bool       // do not append remote events (config events.log = false)
	Dial       Dialer     // default ssh through DefaultSSH
	Backoff    time.Duration
	MaxBackoff time.Duration
	Healthy    time.Duration // a session live this long resets the backoff
	MaxBacklog int           // events waiting for the reader before a host counts as flooding; default 10000
	Now        func() time.Time
}

func (o *Options) defaults() {
	if reflect.ValueOf(o.Prices).IsZero() {
		o.Prices = cost.NewTable(nil)
	}
	if o.StateDir == "" {
		o.StateDir = StateDir()
	}
	if o.EventsPath == "" {
		o.EventsPath = events.Path()
	}
	if o.Dial == nil {
		o.Dial = SSHDialer(DefaultSSH())
	}
	if o.Backoff == 0 {
		o.Backoff = 5 * time.Second
	}
	if o.MaxBackoff == 0 {
		o.MaxBackoff = 2 * time.Minute
	}
	if o.Healthy == 0 {
		o.Healthy = time.Minute
	}
	if o.MaxBacklog == 0 {
		o.MaxBacklog = 10000
	}
	if o.Now == nil {
		o.Now = time.Now
	}
}

// SSHDialer starts `den remote serve` on the host with s. ssh's stderr is
// kept (capped) to classify the failure.
func SSHDialer(s SSH) Dialer {
	return func(ctx context.Context, h Host) (*Conn, error) {
		s.Hygiene(ctx, h.Target)
		cmd, err := s.Command(ctx, h.Target, ServeCommand(h))
		if err != nil {
			return nil, err
		}
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return nil, err
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		var stderr strings.Builder
		cmd.Stderr = &limitWriter{w: &stderr, n: 16 << 10}
		cmd.WaitDelay = 2 * time.Second
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		var once sync.Once
		var waitErr error
		return &Conn{
			In:  stdin,
			Out: stdout,
			Wait: func() error {
				once.Do(func() {
					err := cmd.Wait()
					var ee *exec.ExitError
					switch {
					case err == nil:
						waitErr = &Failure{Code: 0, Stderr: lastLines(stderr.String(), 4)}
					case errors.As(err, &ee) || stderr.Len() > 0:
						waitErr = failure(err, stderr.String())
					default:
						waitErr = err
					}
				})
				return waitErr
			},
			Kill: func() { cmd.Process.Kill() },
		}, nil
	}
}

// Manager keeps one session per enabled host while Den runs, and tells the
// views what changed. It never installs or changes anything on a host.
type Manager struct {
	o       Options
	hosts   []Host
	updates chan Update

	// The queue between the sessions and Updates: events in order and
	// never dropped, host and fleet changes coalesced to the newest.
	qmu     sync.Mutex
	queue   []Update
	backlog map[string]int // queued events per host
	qsig    chan struct{}
	qdone   chan struct{}

	mu    sync.Mutex
	state map[string]*hostState

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	closed sync.Once
}

type hostState struct {
	status    HostStatus
	known     Known
	reconnect chan struct{}
}

// NewManager prepares sessions for cfg's enabled hosts, starting from what
// was saved last time, so their last known agents show at once. Start
// connects.
func NewManager(cfg Config, o Options) *Manager {
	o.defaults()
	m := &Manager{o: o, hosts: cfg.Enabled(), updates: make(chan Update), state: map[string]*hostState{},
		backlog: map[string]int{}, qsig: make(chan struct{}, 1), qdone: make(chan struct{})}
	m.ctx, m.cancel = context.WithCancel(context.Background())
	go m.dispatch()
	for _, h := range m.hosts {
		k, _ := LoadKnown(o.StateDir, h.Label)
		k.Label = h.Label
		st := Offline
		if k.State != "" && k.State != Live && k.State != Connecting {
			st = k.State
		}
		m.state[h.Label] = &hostState{
			known:     k,
			reconnect: make(chan struct{}, 1),
			status: HostStatus{
				Label: h.Label, Target: h.Target, Policy: h.Policy, State: st, Reason: k.Reason, Since: k.Since,
				Hostname: k.Hostname, Den: k.Den, Claude: k.Claude, Applied: k.Policy, Offset: k.Offset, Agents: len(k.Agents),
			},
		}
	}
	return m
}

// Start connects to every enabled host, each in its own goroutine.
func (m *Manager) Start() {
	for _, h := range m.hosts {
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.loop(h)
		}()
	}
}

// Close ends every session and waits for them. Updates is closed after;
// events still queued then are in events.jsonl already. Calling it again
// does nothing.
func (m *Manager) Close() {
	m.closed.Do(func() {
		m.cancel()
		m.wg.Wait()
		<-m.qdone
		close(m.updates)
	})
}

// Updates delivers changes in order. EventArrived updates are never
// dropped: a host whose events wait unread past Options.MaxBacklog is
// taken offline as flooding instead, and read again once the reader has
// caught up. HostChanged and FleetChanged are coalesced: a reader that
// falls behind gets each host's newest one. Hosts and Agents always give
// the current state.
func (m *Manager) Updates() <-chan Update { return m.updates }

// Hosts is every enabled host's status, in remotes.toml order.
func (m *Manager) Hosts() []HostStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]HostStatus, 0, len(m.hosts))
	for _, h := range m.hosts {
		s := m.state[h.Label].status
		s.Flags = slices.Clone(s.Flags)
		out = append(out, s)
	}
	return out
}

// Agents is every remote agent, the last known ones of hosts that are not
// live marked Stale.
func (m *Manager) Agents() []fleet.Agent {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []fleet.Agent
	for _, h := range m.hosts {
		out = append(out, m.agentsLocked(h)...)
	}
	return out
}

func (m *Manager) agentsLocked(h Host) []fleet.Agent {
	st := m.state[h.Label]
	out := make([]fleet.Agent, 0, len(st.known.Agents))
	for _, meta := range st.known.Agents {
		a := fleet.FromMeta(recheck(meta, h), h.Label, st.known.Received, m.o.Prices)
		a.HostState, a.Stale = string(st.status.State), st.status.State != Live
		out = append(out, a)
	}
	return out
}

// Reconnect retries a host now, also one that needs attention or has a
// version mismatch (after the human fixed it).
func (m *Manager) Reconnect(label string) {
	m.mu.Lock()
	st, ok := m.state[label]
	m.mu.Unlock()
	if !ok {
		return
	}
	select {
	case st.reconnect <- struct{}{}:
	default:
	}
}

// send queues u for Updates. A host or fleet change replaces the host's
// earlier one of the same kind still queued, and goes to the end, after
// the events that came before it.
func (m *Manager) send(u Update) {
	m.qmu.Lock()
	if u.Kind == EventArrived {
		m.backlog[u.Host]++
	} else {
		q := m.queue[:0]
		for _, o := range m.queue {
			if o.Host != u.Host || o.Kind != u.Kind {
				q = append(q, o)
			}
		}
		clear(m.queue[len(q):])
		m.queue = q
	}
	m.queue = append(m.queue, u)
	m.qmu.Unlock()
	select {
	case m.qsig <- struct{}{}:
	default:
	}
}

// waiting is how many of a host's events are queued and not yet read.
func (m *Manager) waiting(host string) int {
	m.qmu.Lock()
	defer m.qmu.Unlock()
	return m.backlog[host]
}

// dispatch hands the queue to Updates, one at a time, until Close.
func (m *Manager) dispatch() {
	defer close(m.qdone)
	for {
		m.qmu.Lock()
		if len(m.queue) == 0 {
			m.qmu.Unlock()
			select {
			case <-m.qsig:
				continue
			case <-m.ctx.Done():
				return
			}
		}
		u := m.queue[0]
		m.queue[0] = Update{}
		m.queue = m.queue[1:]
		m.qmu.Unlock()
		select {
		case m.updates <- u:
		case <-m.ctx.Done():
			return
		}
		if u.Kind == EventArrived {
			m.qmu.Lock()
			m.backlog[u.Host]--
			m.qmu.Unlock()
		}
	}
}

// setState moves a host to a state and tells the views.
func (m *Manager) setState(h Host, s State, reason string) {
	m.mu.Lock()
	st := m.state[h.Label]
	reason = sanitize.Label(reason, 200)
	if st.status.State != s || st.status.Reason != reason {
		if st.status.State != s {
			st.status.Since = m.o.Now()
		}
		st.status.State, st.status.Reason = s, reason
		st.status.Hint = ""
		switch s {
		case NeedsAttention:
			st.status.Hint = AttentionHint(h.Target)
		case VersionMismatch:
			st.status.Hint = fmt.Sprintf("install a den that speaks proto %d on the host: den remote install %s", Proto, h.Label)
		}
	}
	st.known.State, st.known.Reason, st.known.Since = s, reason, st.status.Since
	if s == Live {
		st.known.Heartbeat = m.o.Now()
	}
	saveKnown(m.o.StateDir, st.known)
	u := Update{Kind: HostChanged, Host: h.Label, Status: st.status, Agents: m.agentsLocked(h)}
	m.mu.Unlock()
	m.send(u)
}

func (m *Manager) flag(h Host, what string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.state[h.Label]
	what = sanitize.Label(what, 120)
	if !slices.Contains(st.status.Flags, what) && len(st.status.Flags) < 20 {
		st.status.Flags = append(st.status.Flags, what)
	}
}

// loop runs a host's sessions until Close: connect, read until the
// session ends, then wait (backoff, or for Reconnect when the human must
// act) and connect again.
func (m *Manager) loop(h Host) {
	delay := m.o.Backoff
	st := m.state[h.Label]
	for {
		// A host taken offline for an unread backlog waits for the reader.
		for m.waiting(h.Label) > m.o.MaxBacklog/2 {
			select {
			case <-m.ctx.Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
		m.setState(h, Connecting, "")
		start := m.o.Now()
		s, reason, live := m.session(h)
		if m.ctx.Err() != nil {
			return
		}
		m.setState(h, s, reason)
		var wait <-chan time.Time
		if s == Offline {
			if live && m.o.Now().Sub(start) >= m.o.Healthy {
				delay = m.o.Backoff
			}
			wait = time.After(delay)
			delay = min(delay*2, m.o.MaxBackoff)
		}
		select {
		case <-m.ctx.Done():
			return
		case <-st.reconnect:
			delay = m.o.Backoff
		case <-wait:
		}
	}
}

// session runs one connection and says how it ended, and whether it got
// as far as live.
func (m *Manager) session(h Host) (State, string, bool) {
	ctx, cancel := context.WithCancel(m.ctx)
	defer cancel()
	conn, err := m.o.Dial(ctx, h)
	if err != nil {
		s, r := Classify(err)
		return s, r, false
	}
	defer func() {
		conn.Kill()
		conn.Wait() // reap it
	}()
	m.mu.Lock()
	cur := m.state[h.Label].known.Cursor
	m.mu.Unlock()
	req := Request{Type: TypeHello, Proto: Proto, Den: m.o.Version, Policy: h.Policy, Labels: h.Labels, Cursor: cur}
	if err := writeMsg(conn.In, req); err != nil {
		s, r := Classify(conn.Wait())
		return s, r, false
	}
	// stdin stays open: closing it ends serve.

	type msg struct {
		line []byte
		err  error
		at   time.Time
	}
	msgs := make(chan msg)
	go func() {
		lr := newLineReader(conn.Out, MaxLine)
		for {
			line, err := lr.next()
			select {
			case msgs <- msg{line, err, m.o.Now()}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	heartbeat := time.NewTicker(HeartbeatEvery)
	defer heartbeat.Stop()
	rc := rateCap{max: MaxRate}
	live, applied := false, Policy("")
	end := func(s State, r string) (State, string, bool) {
		conn.Kill()
		return s, r, live
	}
	for {
		var in msg
		select {
		case <-m.ctx.Done():
			return end(Offline, "Den stopped")
		case <-heartbeat.C:
			if live {
				m.mu.Lock()
				st := m.state[h.Label]
				st.known.Heartbeat = m.o.Now()
				saveKnown(m.o.StateDir, st.known)
				m.mu.Unlock()
			}
			continue
		case in = <-msgs:
		}
		if errors.Is(in.err, ErrLineTooLong) {
			m.flag(h, "flooding: a message over 256 KB")
			return end(Offline, "flooding: a message over 256 KB")
		}
		if in.err != nil {
			conn.In.Close()
			s, r := Classify(conn.Wait())
			if live && s == Offline && r == "exited 0" {
				r = "the session ended"
			}
			return s, r, live
		}
		if !rc.allow(in.at) {
			m.flag(h, "flooding: over 2000 messages a second")
			return end(Offline, "flooding: too many messages")
		}
		typ, unknown, err := peek(in.line)
		if err != nil {
			m.flag(h, "a message that is not JSON")
			continue
		}
		if len(unknown) > 0 {
			sort.Strings(unknown)
			m.flag(h, fmt.Sprintf("unknown fields in %s: %s", sanitize.Line(typ), sanitize.Line(strings.Join(unknown, ", "))))
		}
		if !live && typ != TypeHello && typ != TypeError {
			m.flag(h, "a "+sanitize.Label(typ, 40)+" before hello")
			continue
		}
		switch typ {
		case TypeHello:
			var hm Hello
			if json.Unmarshal(in.line, &hm) != nil || live {
				m.flag(h, "a second or malformed hello")
				continue
			}
			if hm.Proto != Proto {
				return end(VersionMismatch, fmt.Sprintf("the host speaks proto %d, Den %d", hm.Proto, Proto))
			}
			applied = Min(h.Policy, hm.Policy)
			if hm.Policy.rank() > h.Policy.rank() {
				m.flag(h, "the host claimed a policy above the one requested")
			}
			m.mu.Lock()
			st := m.state[h.Label]
			st.status.Applied = applied
			st.status.Hostname = sanitize.Label(hm.Hostname, 64)
			st.status.Den = sanitize.Label(hm.Den, 40)
			st.status.Claude = sanitize.Label(hm.Claude, 40)
			st.status.Offset = hm.RemoteNow.Sub(in.at)
			st.status.LastSeen = in.at
			st.known.Hostname, st.known.Den, st.known.Claude = st.status.Hostname, st.status.Den, st.status.Claude
			st.known.Policy, st.known.Offset = applied, st.status.Offset
			if st.known.Cursor.IsZero() {
				st.known.Cursor = hm.Cursor
			}
			m.mu.Unlock()
			live = true
			m.setState(h, Live, "")
		case TypeSnapshot:
			var sm Snapshot
			if json.Unmarshal(in.line, &sm) != nil {
				m.flag(h, "a malformed snapshot")
				continue
			}
			m.mu.Lock()
			st := m.state[h.Label]
			agents := make([]fleet.Meta, 0, len(sm.Agents))
			for _, a := range sm.Agents {
				agents = append(agents, cleanMeta(recheck(a, h)))
			}
			st.known.Agents, st.known.Received, st.known.Heartbeat = agents, in.at, in.at
			st.status.Agents, st.status.LastSeen = len(agents), in.at
			if !sm.RemoteNow.IsZero() {
				st.status.Offset = sm.RemoteNow.Sub(in.at)
				st.known.Offset = st.status.Offset
			}
			saveKnown(m.o.StateDir, st.known)
			u := Update{Kind: FleetChanged, Host: h.Label, Status: st.status, Agents: m.agentsLocked(h)}
			m.mu.Unlock()
			m.send(u)
		case TypeEvent:
			var em Event
			if json.Unmarshal(in.line, &em) != nil {
				m.flag(h, "a malformed event")
				continue
			}
			ev, ok := cleanEvent(em.Event, h.Label)
			if !ok {
				m.flag(h, "an event of an unknown kind or version")
				continue
			}
			m.mu.Lock()
			st := m.state[h.Label]
			if em.Cursor == st.known.Cursor {
				m.mu.Unlock()
				continue // already have it
			}
			ev.Time = ev.Time.Add(-st.status.Offset).UTC().Truncate(time.Millisecond)
			if !m.o.NoEventLog {
				events.Append(m.o.EventsPath, ev)
			}
			st.known.Cursor = em.Cursor
			st.status.LastSeen = in.at
			saveKnown(m.o.StateDir, st.known)
			m.mu.Unlock()
			m.send(Update{Kind: EventArrived, Host: h.Label, Event: ev})
			if m.waiting(h.Label) > m.o.MaxBacklog {
				// Stop reading rather than drop: the rest waits on the host,
				// after the saved cursor, until the reader catches up.
				why := fmt.Sprintf("flooding: over %d events wait unread", m.o.MaxBacklog)
				m.flag(h, why)
				return end(Offline, why)
			}
		case TypeError:
			var e Error
			json.Unmarshal(in.line, &e)
			reason := "the host reported: " + sanitize.Label(e.Message, 160)
			if e.Code == "proto" {
				return end(VersionMismatch, reason)
			}
			return end(Offline, reason)
		default:
			m.flag(h, "unknown message type "+sanitize.Label(typ, 40))
		}
	}
}

// cleanMeta sanitises every string a host sent in a projection.
func cleanMeta(a fleet.Meta) fleet.Meta {
	l := func(s string) string { return sanitize.Label(s, fleet.LabelMax) }
	a.Session, a.Status, a.Name, a.Peer = l(a.Session), l(a.Status), l(a.Name), l(a.Peer)
	a.Role, a.Task, a.Model, a.Claude = l(a.Role), l(a.Task), l(a.Model), sanitize.Label(a.Claude, 40)
	a.Repo, a.Branch = l(a.Repo), l(a.Branch)
	if a.Limits != nil {
		c := *a.Limits
		c.FiveHourResets, c.SevenDayResets = sanitize.Label(c.FiveHourResets, 40), sanitize.Label(c.SevenDayResets, 40)
		a.Limits = &c
	}
	return a
}

// cleanEvent keeps a known kind of version 1 only, sanitises its strings,
// takes the agent name from the session id as Den would, and sets the host
// from the connection.
func cleanEvent(ev events.Event, host string) (events.Event, bool) {
	if ev.V != events.Version || !slices.Contains(events.Kinds, ev.Kind) {
		return events.Event{}, false
	}
	l := func(s string) string { return sanitize.Label(s, fleet.LabelMax) }
	ev.Session = l(ev.Session)
	ev.Agent = badge.Name(ev.Session)
	ev.Request, ev.Tool, ev.Error, ev.Reason = l(ev.Request), l(ev.Tool), l(ev.Error), l(ev.Reason)
	ev.Host = host
	return ev, true
}
