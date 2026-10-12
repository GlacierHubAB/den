package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/GlacierHubAB/den/internal/badge"
	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/closer"
	"github.com/GlacierHubAB/den/internal/config"
	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/events"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// ServeOptions tune Serve; the zero value is what `den remote serve` uses.
type ServeOptions struct {
	Version       string                        // this den's version, for hello
	EventsPath    string                        // default events.Path()
	Snapshot      func() ([]fleet.Agent, error) // default fleet.Snapshot, without Tail
	Watch         []string                      // default the sessions dir and the badge store
	Skew          time.Duration                 // fakes a host clock this far off; default clockOffset, 0 outside sshtest builds
	Debounce      time.Duration                 // a burst of file changes becomes one snapshot; default 250 ms
	SnapshotEvery time.Duration                 // a snapshot this often even with no change; default 15 s
	PollEvents    time.Duration                 // how often the event stream is read; default 200 ms
	EventPace     time.Duration                 // pause after each event sent, so a backfill stays under MaxRate; default 1 ms
	RequestWait   time.Duration                 // how long to wait for Den's hello; default 30 s
	Orphaned      func() bool                   // the watchdog: true once the ssh session is gone; default parent pid 1
	Socket        string                        // den-remote.sock; default bridge.RemoteSocketPath, its private fallback dir made 0700
	Questions     *bool                         // relay AskUserQuestion; default the host's hooks.answer_questions
	Silence       time.Duration                 // relaying, no word from Den this long lets every held request go; default 60 s
	Log           func(string)                  // what the relay did, metadata only; default remote-serve.log in the state dir
	CloseOps      closer.Ops                    // how close signals; default closer.System
	CloseSelf     closer.Self                   // what a close must leave alone; default this process and its ancestors
	DirtyTTL      time.Duration                 // how long a checkout's dirty flag is trusted; default 20 s
}

// noTail sets fleet.Recent to 0 once per process for serve.
var noTail sync.Once

func (o *ServeOptions) defaults() {
	if o.EventsPath == "" {
		o.EventsPath = events.Path()
	}
	if o.Snapshot == nil {
		// Never read what is never sent. Set once, before any snapshot runs:
		// actions read the fleet from goroutines of their own.
		noTail.Do(func() { fleet.Recent = 0 })
		o.Snapshot = fleet.Snapshot
	}
	if o.Watch == nil {
		o.Watch = []string{discover.Dir(), badge.StoreDir()}
	}
	if o.Skew == 0 {
		o.Skew = clockOffset
	}
	if o.Debounce == 0 {
		o.Debounce = 250 * time.Millisecond
	}
	if o.SnapshotEvery == 0 {
		o.SnapshotEvery = 15 * time.Second
	}
	if o.PollEvents == 0 {
		o.PollEvents = 200 * time.Millisecond
	}
	if o.EventPace == 0 {
		o.EventPace = time.Millisecond
	}
	if o.RequestWait == 0 {
		o.RequestWait = 30 * time.Second
	}
	if o.Orphaned == nil {
		o.Orphaned = func() bool { return os.Getppid() == 1 }
	}
	if o.Questions == nil {
		q := config.Load().Hooks.AnswerQuestions
		o.Questions = &q
	}
	if o.Silence == 0 {
		o.Silence = 4 * PingEvery
	}
	if o.CloseOps == nil {
		o.CloseOps = closer.System{}
	}
	if o.CloseSelf.PID == 0 {
		o.CloseSelf = closer.DenSelf("", "")
	}
	if o.DirtyTTL == 0 {
		o.DirtyTTL = 20 * time.Second
	}
	if o.Log == nil {
		o.Log = serveLog(filepath.Join(filepath.Dir(o.EventsPath), "remote-serve.log"))
	}
}

// remoteSocket is where serve listens for held requests. Without
// $XDG_RUNTIME_DIR it makes the private fallback directory (0700) and
// refuses one somebody else made or opened up.
func remoteSocket() (string, error) {
	if os.Getenv("DEN_REMOTE_SOCKET") == "" && os.Getenv("XDG_RUNTIME_DIR") == "" {
		os.Mkdir(bridge.FallbackDir(), 0o700)
	}
	return bridge.RemoteSocketPath()
}

// serveLog appends one line per relay happening to path (0600): ids cut to
// 8 characters, session prefixes, tool names, never a body.
func serveLog(path string) func(string) {
	return func(line string) {
		os.MkdirAll(filepath.Dir(path), 0o700)
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		defer f.Close()
		fmt.Fprintf(f, "%s pid %d %s\n", time.Now().Format("2006-01-02 15:04:05"), os.Getpid(), sanitize.Line(line))
	}
}

// syncWriter writes whole messages from several goroutines.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// Serve is `den remote serve --proto 1`, the remote side: it reads Den's
// request from in, then writes hello, snapshots and events to out until in
// reaches EOF, out fails, the watchdog sees the session gone, or ctx ends.
// Under approve it also owns den-remote.sock and relays held permission
// requests (relay.go), and takes send and close (actions.go); decisions,
// pings and those two are all it reads. It never executes anything it
// reads: send posts to a session's socket, close signals a process it found
// in the host's own session files. Errors are also sent as an error message when
// out still works.
func Serve(ctx context.Context, in io.Reader, out io.Writer, o ServeOptions) error {
	o.defaults()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	out = &syncWriter{w: out}

	lines := newLineReader(in, MaxLine)
	reqc := make(chan []byte, 1)
	errc := make(chan error, 1)
	fromDen := make(chan []byte)
	go func() {
		line, err := lines.next()
		if err != nil {
			errc <- err
			return
		}
		reqc <- line
		// Decisions and pings; EOF ends the session.
		for {
			line, err := lines.next()
			if err != nil {
				cancel()
				return
			}
			select {
			case fromDen <- line:
			case <-ctx.Done():
				return
			}
		}
	}()
	var line []byte
	select {
	case line = <-reqc:
	case err := <-errc:
		return err
	case <-time.After(o.RequestWait):
		return fmt.Errorf("no request within %s", o.RequestWait)
	case <-ctx.Done():
		// The reader cancels when in ends, which can be right after the
		// request it already queued: answer that request rather than lose it
		// to select's random choice.
		select {
		case line = <-reqc:
		default:
			return ctx.Err()
		}
	}
	var req Request
	if json.Unmarshal(line, &req) != nil || req.Type != TypeHello {
		writeMsg(out, Error{Type: TypeError, Code: "request", Message: "expected a hello request"})
		return fmt.Errorf("bad request")
	}
	if req.Proto != Proto {
		writeMsg(out, Error{Type: TypeError, Code: "proto", Message: fmt.Sprintf("this den speaks proto %d, not %d", Proto, req.Proto)})
		return fmt.Errorf("unsupported proto %d", req.Proto)
	}
	policy := Min(req.Policy, Supported)
	var rl *relay
	var attached *Attached
	note := ""
	if policy.rank() >= Approve.rank() {
		path := o.Socket
		var err error
		if path == "" {
			path, err = remoteSocket()
		}
		if err == nil {
			rl, attached, err = openRelay(path, req.Controller, *o.Questions, func(v any) error { return writeMsg(out, v) }, o.Log)
		}
		switch {
		case err != nil:
			policy, note = Observe, sanitize.Label("held requests are not relayed: "+err.Error(), 160)
			o.Log(note)
		case attached != nil:
			policy = Observe
		}
	}
	if rl != nil {
		defer rl.close()
	}
	acts := newActions(policy.rank() >= Approve.rank(), o, func(v any) error { return writeMsg(out, v) })
	defer func() {
		cancel() // an action in flight stops waiting
		acts.wait()
	}()
	var dirty *dirtyCache
	dirtyc := make(chan struct{}, 1)
	if acts.allowed {
		dirty = newDirtyCache(o.DirtyTTL, func() {
			select {
			case dirtyc <- struct{}{}:
			default:
			}
		})
	}

	tail := events.Follow(o.EventsPath, req.Cursor)
	defer tail.Close()
	host, _ := os.Hostname()
	agents, _ := o.Snapshot()
	hello := Hello{
		Type: TypeHello, Proto: Proto, Den: o.Version, Claude: claudeVersion(agents),
		Hostname: sanitize.Label(host, 64), RemoteNow: time.Now().Add(o.Skew).UTC(), Policy: policy, Labels: req.Labels,
		Cursor: tail.Cursor(), Attached: attached, Note: note,
	}
	if hello.Cursor.IsZero() {
		hello.Cursor = req.Cursor
	}
	if err := writeMsg(out, hello); err != nil {
		return err
	}
	snap := func(agents []fleet.Agent) error {
		// Ages are measured against the session files' own instants, which
		// the host's clock wrote: a skew shifts both and cancels out.
		now := time.Now()
		s := Snapshot{Type: TypeSnapshot, RemoteNow: now.Add(o.Skew).UTC(), Agents: make([]fleet.Meta, 0, len(agents))}
		for i, a := range agents {
			m := a.Meta(i, now, req.Labels)
			if dirty != nil {
				m.Dirty = dirty.get(a.Session.CWD)
			}
			s.Agents = append(s.Agents, m)
		}
		return writeMsg(out, s)
	}
	if err := snap(agents); err != nil {
		return err
	}
	var handed <-chan struct{}
	var silent <-chan time.Time
	heard := time.Now()
	inRate := rateCap{max: 100} // decisions are a human's keypresses, pings one per PingEvery
	if rl != nil {
		rl.start()
		handed = rl.Done()
		t := time.NewTicker(o.Silence / 4)
		defer t.Stop()
		silent = t.C
	}

	changed := watch(ctx, o.Watch, o.Debounce)
	every := time.NewTicker(o.SnapshotEvery)
	defer every.Stop()
	poll := time.NewTicker(o.PollEvents)
	defer poll.Stop()
	dog := time.NewTicker(2 * time.Second)
	defer dog.Stop()
	for {
		var err error
		select {
		case <-ctx.Done():
			return nil
		case <-handed:
			return fmt.Errorf("handed over to a new session of the same Den")
		case <-silent:
			if time.Since(heard) > o.Silence {
				return fmt.Errorf("no word from Den for %s; held requests went to their terminals", o.Silence)
			}
		case line := <-fromDen:
			heard = time.Now()
			if !inRate.allow(heard) {
				writeMsg(out, Error{Type: TypeError, Code: "request", Message: "more than 100 messages a second from Den"})
				return fmt.Errorf("Den sent more than 100 messages a second")
			}
			if err := fromDenMsg(ctx, line, rl, acts, o.Log); err != nil {
				writeMsg(out, Error{Type: TypeError, Code: "request", Message: err.Error()})
				return err
			}
		case <-dog.C:
			if o.Orphaned() {
				return fmt.Errorf("the ssh session is gone")
			}
		case <-changed:
			err = snapAgain(o, snap)
		case <-dirtyc:
			err = snapAgain(o, snap)
		case <-every.C:
			err = snapAgain(o, snap)
		case <-poll.C:
			tail.Poll(func(line []byte, at events.Cursor) {
				if err != nil || ctx.Err() != nil {
					return
				}
				var ev events.Event
				if json.Unmarshal(line, &ev) != nil || ev.Kind == "" || ev.Host != "" {
					return // not an event, or one from a third machine
				}
				if o.Skew != 0 {
					ev.Time = ev.Time.Add(o.Skew) // a skewed host would have stamped it so
				}
				err = writeMsg(out, Event{Type: TypeEvent, Cursor: at, Event: ev})
				time.Sleep(o.EventPace)
			})
		}
		if err != nil {
			return err
		}
	}
}

// fromDenMsg handles one message from Den after its request: a decision
// for the relay, a ping, or a send or close for the actions. Anything else
// is refused: the session ends with an error (code request). A decision
// with nothing relayed in this session is dropped and logged, as a decision
// for an id that is not open is. A send or close under observe is answered
// with a failed result and nothing happens.
func fromDenMsg(ctx context.Context, line []byte, rl *relay, acts *actions, log func(string)) error {
	typ, unknown, err := peekIn(line, allowedIn)
	switch {
	case err != nil:
		return fmt.Errorf("a message from Den that is not JSON")
	case len(unknown) > 0 || allowedIn[typ] == nil:
		return fmt.Errorf("proto 1 takes only decision, ping, send and close after hello, not %s", sanitize.Label(typ, 40))
	case typ == TypeSend || typ == TypeClose:
		return acts.handle(ctx, typ, line)
	case typ == TypeDecision:
		var d Decision
		if json.Unmarshal(line, &d) != nil {
			return fmt.Errorf("a malformed decision")
		}
		if rl == nil {
			log("dropped a decision: nothing is relayed in this session")
			return nil
		}
		rl.decide(d)
	}
	return nil
}

func snapAgain(o ServeOptions, snap func([]fleet.Agent) error) error {
	agents, err := o.Snapshot()
	if err != nil {
		return nil // a failed scan is skipped; the next one may work
	}
	return snap(agents)
}

// claudeVersion is the newest Claude Code version among the live sessions.
func claudeVersion(agents []fleet.Agent) string {
	var vs []string
	for _, a := range agents {
		if a.Session.Alive && a.Session.Version != "" {
			vs = append(vs, a.Session.Version)
		}
	}
	if len(vs) == 0 {
		return ""
	}
	sort.Slice(vs, func(i, j int) bool { return versionLess(vs[i], vs[j]) })
	return sanitize.Label(vs[len(vs)-1], 40)
}

// watch signals once per burst of changes in the given directories. A
// directory that does not exist yet is tried again every second, and its
// appearing counts as a change, so the first session file on a fresh host
// shows within a second or two.
func watch(ctx context.Context, dirs []string, debounce time.Duration) <-chan struct{} {
	out := make(chan struct{}, 1)
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return out
	}
	missing := map[string]bool{}
	for _, d := range dirs {
		if w.Add(d) != nil {
			missing[d] = true
		}
	}
	go func() {
		defer w.Close()
		retry := time.NewTicker(time.Second)
		defer retry.Stop()
		var timer *time.Timer
		fire := func() {
			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(debounce, func() {
				select {
				case out <- struct{}{}:
				default:
				}
			})
		}
		for {
			select {
			case <-ctx.Done():
				if timer != nil {
					timer.Stop()
				}
				return
			case <-retry.C:
				for d := range missing {
					if w.Add(d) == nil {
						delete(missing, d)
						fire()
					}
				}
			case _, ok := <-w.Events:
				if !ok {
					return
				}
				fire()
			case <-w.Errors:
			}
		}
	}()
	return out
}
