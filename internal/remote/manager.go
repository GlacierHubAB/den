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
	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/cost"
	"github.com/GlacierHubAB/den/internal/events"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// State is where a host's link stands (docs/REMOTE.md).
type State string

const (
	Connecting        State = "connecting"         // ssh is starting
	Live              State = "live"               // hello received, versions compatible
	Offline           State = "offline"            // the session ended or failed; retried with backoff
	NeedsAttention    State = "needs_attention"    // auth or host key failure: not retried until Reconnect
	VersionMismatch   State = "version_mismatch"   // den missing, too old, or another proto: not retried until Reconnect
	AttachedElsewhere State = "attached_elsewhere" // live, but another Den answers the host's held requests: observe only
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
	Dropped  int       // events Den dropped from the host since it started (gate)
	Hint     string    // what the human can do, for needs attention and version mismatch
}

// UpdateKind says what an Update carries.
type UpdateKind int

const (
	HostChanged  UpdateKind = iota // Status changed; Agents are the host's agents re-marked
	FleetChanged                   // a new snapshot: Agents
	EventArrived                   // Event, already appended to the local events.jsonl
	HeldArrived                    // Request: a held permission, plan or question to put in the inbox
	HeldReleased                   // Request: no longer answerable (its Why says why)
)

// Update is one change, sent on Manager.Updates.
type Update struct {
	Kind   UpdateKind
	Host   string
	Status HostStatus
	Agents []fleet.Agent // HostChanged and FleetChanged: the host's agents, Host, HostState and Stale set
	Event  events.Event  // EventArrived: Host set, Time on the local clock
	// HeldArrived and HeldReleased: a PermissionRequest held on the host.
	// Its Host is the connection's label and its Reply goes back down that
	// connection, only while it is live. Never queued behind a coalesce.
	Request *bridge.Request
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
	Version     string     // this den's version, sent in the request
	Prices      cost.Table // prices remote tokens; default the built-in table, Den passes cost.NewTable(config.Prices())
	StateDir    string     // default StateDir()
	EventsPath  string     // where remote events are appended; default events.Path()
	NoEventLog  bool       // do not append remote events (config events.log = false)
	Dial        Dialer     // default ssh through DefaultSSH
	Backoff     time.Duration
	MaxBackoff  time.Duration
	Healthy     time.Duration // a session live this long resets the backoff
	MaxBacklog  int           // events waiting for the reader before a host counts as flooding; default 10000
	MaxRate     int           // messages a second a host may send before it counts as flooding; default MaxRate
	EventRate   int           // events a second Den logs and shows from one host after EventBurst; default EventRate
	EventBurst  int           // events Den takes from one host at once; default EventBurst
	FloodDrops  int           // events dropped within FloodWindow that disable a host until Reconnect; default FloodDrops
	FloodWindow time.Duration // default FloodWindow
	Controller  string        // this machine's controller id; default LoadController(StateDir)
	PingEvery   time.Duration // how often a live session pings serve; default PingEvery
	Retake      time.Duration // a host attached elsewhere is asked again this often; default 1 min
	SendWait    time.Duration // how long Send waits for the host's result; default 10 s
	CloseWait   time.Duration // how long Close waits for it; default 15 s
	DiffWait    time.Duration // how long Diff waits for it; default 30 s
	Now         func() time.Time

	// Reload reads remotes.toml again (Den passes Load). A host's policy,
	// labels, target and den_path then apply before each connection, on
	// Reconnect, and, with Watch, within Watch of the file changing: a
	// host whose settings changed is shown under them at once and its
	// session restarts, so a lowered policy never waits for a restart of
	// Den. Adding, removing or disabling a host still takes one. Nil
	// keeps the Config NewManager was given.
	Reload func() (Config, error)
	Watch  time.Duration // how often Reload is polled; 0 polls never
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
	if o.MaxRate == 0 {
		o.MaxRate = MaxRate
	}
	if o.EventRate == 0 {
		o.EventRate = EventRate
	}
	if o.EventBurst == 0 {
		o.EventBurst = EventBurst
	}
	if o.FloodDrops == 0 {
		o.FloodDrops = FloodDrops
	}
	if o.FloodWindow == 0 {
		o.FloodWindow = FloodWindow
	}
	if o.MaxBacklog == 0 {
		o.MaxBacklog = 10000
	}
	if o.Controller == "" {
		o.Controller = LoadController(o.StateDir)
	}
	if o.PingEvery == 0 {
		o.PingEvery = PingEvery
	}
	if o.Retake == 0 {
		o.Retake = time.Minute
	}
	if o.SendWait == 0 {
		o.SendWait = defaultSendWait
	}
	if o.CloseWait == 0 {
		o.CloseWait = defaultCloseWait
	}
	if o.DiffWait == 0 {
		o.DiffWait = defaultDiffWait
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
	host      Host // as remotes.toml says now (Options.Reload)
	restart   chan struct{}
	status    HostStatus
	known     Known
	reconnect chan struct{}
	link      *link        // the live session's way to carry actions, nil without one
	gate      *gate        // which of its events Den takes; used by its session only
	answered  *answeredIDs // held ids answered on any of the host's sessions
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
		if k.State != "" && k.State != Live && k.State != Connecting && k.State != AttachedElsewhere && k.State != Disabled {
			st = k.State
		}
		m.state[h.Label] = &hostState{
			host:      h,
			restart:   make(chan struct{}, 1),
			known:     k,
			reconnect: make(chan struct{}, 1),
			answered:  newAnsweredIDs(),
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
	if m.o.Reload != nil && m.o.Watch > 0 {
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			t := time.NewTicker(m.o.Watch)
			defer t.Stop()
			for {
				select {
				case <-m.ctx.Done():
					return
				case <-t.C:
					m.reload()
				}
			}
		}()
	}
}

// reload applies remotes.toml as it is now to the hosts Den runs (see
// Options.Reload). A host whose settings changed has its agents re-marked
// under them at once, which drops content the new policy does not allow,
// and its session restarts to ask the host for the new policy. A file
// that does not read keeps the settings in use.
func (m *Manager) reload() {
	if m.o.Reload == nil {
		return
	}
	cfg, err := m.o.Reload()
	if err != nil {
		return
	}
	now := map[string]Host{}
	for _, h := range cfg.Enabled() {
		now[h.Label] = h
	}
	var changed []Update
	m.mu.Lock()
	for _, h := range m.hosts {
		st, nh := m.state[h.Label], now[h.Label]
		if nh.Label == "" {
			continue // removed or disabled: that takes a restart
		}
		old := st.host
		if nh.Policy == old.Policy && nh.Labels == old.Labels && nh.Target == old.Target && nh.DenPath == old.DenPath {
			continue
		}
		st.host = nh
		st.status.Policy, st.status.Target = nh.Policy, nh.Target
		changed = append(changed, Update{Kind: HostChanged, Host: h.Label, Status: st.status, Agents: m.agentsLocked(nh)})
		select {
		case st.restart <- struct{}{}:
		default:
		}
		select {
		case st.reconnect <- struct{}{}:
		default:
		}
	}
	m.mu.Unlock()
	for _, u := range changed {
		m.send(u)
	}
}

// hostNow is h as remotes.toml says now.
func (m *Manager) hostNow(label string) Host {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state[label].host
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
	h = st.host // what remotes.toml allows now, whichever session asks
	out := make([]fleet.Agent, 0, len(st.known.Agents))
	for _, meta := range st.known.Agents {
		meta, _ = recheck(meta, h, st.known.Policy)
		a := fleet.FromMeta(meta, h.Label, st.known.Received, m.o.Prices)
		a.HostState, a.Stale = string(st.status.State), st.status.State != Live && st.status.State != AttachedElsewhere
		out = append(out, a)
	}
	return out
}

// Reconnect retries a host now, also one that needs attention or has a
// version mismatch (after the human fixed it). It reads remotes.toml
// again first: a live host whose settings changed reconnects under them.
func (m *Manager) Reconnect(label string) {
	m.mu.Lock()
	st, ok := m.state[label]
	m.mu.Unlock()
	if !ok {
		return
	}
	m.reload()
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
	switch u.Kind {
	case EventArrived:
		m.backlog[u.Host]++
	case HeldArrived, HeldReleased:
		// in order, never dropped
	default:
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
		case Disabled:
			st.status.Hint = "find out why it floods, then /remote connect " + h.Label
		}
	}
	st.known.State, st.known.Reason, st.known.Since = s, reason, st.status.Since
	if s == Live || s == AttachedElsewhere {
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
		// Each connection goes out under remotes.toml as it is now.
		m.reload()
		select {
		case <-st.restart: // the session this asked to end is over
		default:
		}
		h = m.hostNow(h.Label)
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
		if s == AttachedElsewhere {
			wait = time.After(0) // ask the host again at once
		}
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
	req := Request{Type: TypeHello, Proto: Proto, Den: m.o.Version, Policy: h.Policy, Labels: h.Labels, Cursor: cur, Controller: m.o.Controller}
	var wmu sync.Mutex
	write := func(v any) error {
		wmu.Lock()
		defer wmu.Unlock()
		return writeMsg(conn.In, v)
	}
	// Decisions are made in Den's update loop (a request's Reply); they
	// are queued here and written by their own goroutine, in order, so a
	// slow or stuck ssh pipe never holds up the TUI.
	held := newHeldSet(ctx, h.Label, m.state[h.Label].answered, pipeQueue(ctx, write, 2*MaxHeld))
	defer func() {
		cancel() // replies from now on go nowhere
		for _, r := range held.releaseAll() {
			m.send(Update{Kind: HeldReleased, Host: h.Label, Request: r})
		}
	}()
	if err := write(req); err != nil {
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
	ping := time.NewTicker(m.o.PingEvery)
	defer ping.Stop()
	var retake <-chan time.Time
	rc := rateCap{max: m.o.MaxRate}
	g := m.state[h.Label].gate
	if g == nil {
		g = newGate(m.o, m.o.Now())
		m.state[h.Label].gate = g
	}
	live, applied := false, Policy("")
	restart := m.state[h.Label].restart
	var lk *link
	defer func() {
		// The session is over: no action can start, and the ones waiting are
		// told the link ended.
		m.mu.Lock()
		if st := m.state[h.Label]; st.link == lk {
			st.link = nil
		}
		m.mu.Unlock()
		if lk != nil {
			lk.end(h.Label)
		}
	}()
	end := func(s State, r string) (State, string, bool) {
		conn.Kill()
		return s, r, live
	}
	for {
		var in msg
		select {
		case <-m.ctx.Done():
			return end(Offline, "Den stopped")
		case <-restart:
			return end(Offline, "remotes.toml changed; reconnecting under it")
		case <-heartbeat.C:
			if live {
				m.mu.Lock()
				st := m.state[h.Label]
				st.known.Heartbeat = m.o.Now()
				saveKnown(m.o.StateDir, st.known)
				m.mu.Unlock()
			}
			continue
		case <-ping.C:
			if live {
				write(Ping{Type: TypePing})
			}
			continue
		case <-retake:
			return end(AttachedElsewhere, m.statusQuiet(h).Reason)
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
			st.link = newLink(write, applied)
			lk = st.link
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
			g.begin(in.at)
			if a := hm.Attached; a != nil {
				// Another Den controls this host's held requests; ask again
				// now and then, so this one takes over once it leaves.
				m.setState(h, AttachedElsewhere, attachedReason(h.Label, a, m.o.Now()))
				retake = time.After(m.o.Retake)
			} else {
				m.setState(h, Live, sanitize.Label(hm.Note, 160))
			}
		case TypeSnapshot:
			var sm Snapshot
			if json.Unmarshal(in.line, &sm) != nil {
				m.flag(h, "a malformed snapshot")
				continue
			}
			agents, flags, overreach := checkAgents(sm.Agents, h, applied)
			for _, f := range flags {
				m.flag(h, f)
			}
			m.mu.Lock()
			st := m.state[h.Label]
			st.known.Agents, st.known.Received, st.known.Heartbeat = agents, in.at, in.at
			g.snapshot()
			st.status.Agents, st.status.LastSeen = len(agents), in.at
			if !sm.RemoteNow.IsZero() {
				st.status.Offset = sm.RemoteNow.Sub(in.at)
				st.known.Offset = st.status.Offset
			}
			saveKnown(m.o.StateDir, st.known)
			u := Update{Kind: FleetChanged, Host: h.Label, Status: st.status, Agents: m.agentsLocked(h)}
			m.mu.Unlock()
			m.send(u)
			if overreach {
				m.flag(h, "content in a snapshot its policy does not allow")
			}
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
			st.known.Cursor = em.Cursor
			st.status.LastSeen = in.at
			v, why := g.malformed(ev)
			if isSessionID(em.Event.Session) {
				v, why = g.check(ev, st.known.Agents, in.at)
			}
			if v == drop {
				// Past it, but not worth a write each: the cursor is saved
				// with the next event Den keeps, a heartbeat or the end.
				st.status.Dropped++
				m.mu.Unlock()
				m.flag(h, why)
				if g.dropped(in.at) {
					why := fmt.Sprintf("flooding: %d events dropped in %s; disabled until /remote connect %s", m.o.FloodDrops, strings.TrimSuffix(m.o.FloodWindow.String(), "0s"), h.Label)
					m.flag(h, "flooding: disabled")
					g.forgive() // for when the human connects it again
					return end(Disabled, why)
				}
				continue
			}
			if !m.o.NoEventLog {
				events.Append(m.o.EventsPath, ev)
			}
			saveKnown(m.o.StateDir, st.known)
			m.mu.Unlock()
			if v == logOnly {
				continue
			}
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
		case TypeHeld:
			var hm Held
			if json.Unmarshal(in.line, &hm) != nil {
				m.flag(h, "a malformed held request")
				continue
			}
			r, why := held.add(hm, h.Policy, applied)
			if r == nil {
				if why != "" {
					m.flag(h, why)
				}
				continue
			}
			m.send(Update{Kind: HeldArrived, Host: h.Label, Request: r})
		case TypeResult:
			var r Result
			if json.Unmarshal(in.line, &r) != nil || lk == nil {
				m.flag(h, "a malformed result")
				continue
			}
			r.Error = sanitize.Label(r.Error, 160)
			if r.Diff != nil && (h.Policy != Full || applied != Full) {
				r.Diff, r.OK, r.Error = nil, false, "a diff its policy does not allow"
				m.flag(h, "a diff its policy does not allow")
			} else if r.Diff != nil {
				r.Diff = cleanDiff(r.Diff)
			}
			if !lk.resolve(r) {
				m.flag(h, "a result for a request that is not open")
			}
		case TypeGone:
			var g Gone
			if json.Unmarshal(in.line, &g) != nil {
				m.flag(h, "a malformed gone")
				continue
			}
			if r := held.gone(g.ID); r != nil {
				m.send(Update{Kind: HeldReleased, Host: h.Label, Request: r})
			}
		default:
			m.flag(h, "unknown message type "+sanitize.Label(typ, 40))
		}
	}
}

// statusQuiet is a host's status.
func (m *Manager) statusQuiet(h Host) HostStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state[h.Label].status
}

// attachedReason says which Den's serve holds a host.
func attachedReason(label string, a *Attached, now time.Time) string {
	r := ElsewhereNote(label)
	if a.Pid > 0 {
		r += fmt.Sprintf(" (its serve is pid %d on the host", a.Pid)
		if !a.Since.IsZero() {
			r += fmt.Sprintf(", attached for %s", now.Sub(a.Since).Round(time.Second))
		}
		r += ")"
	}
	return r
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

// pipeQueue returns a send that queues messages for write and returns at
// once; one goroutine writes them in order until ctx ends. Past n queued
// messages it drops and says so: the request it answered then goes to its
// terminal when its hook or the link gives up, as with a lost link. One
// session never has more open requests than MaxHeld, each answered once,
// so 2*MaxHeld is never reached in practice.
func pipeQueue(ctx context.Context, write func(any) error, n int) func(any) error {
	q := make(chan any, n)
	go func() {
		for {
			select {
			case v := <-q:
				write(v) // a failed write ends the session elsewhere
			case <-ctx.Done():
				return
			}
		}
	}()
	return func(v any) error {
		select {
		case q <- v:
			return nil
		default:
			return errors.New("too many decisions waiting for the pipe")
		}
	}
}
