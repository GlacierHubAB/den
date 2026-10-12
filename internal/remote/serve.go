package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/GlacierHubAB/den/internal/badge"
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
}

func (o *ServeOptions) defaults() {
	if o.EventsPath == "" {
		o.EventsPath = events.Path()
	}
	if o.Snapshot == nil {
		o.Snapshot = func() ([]fleet.Agent, error) {
			fleet.Recent = 0 // never read what is never sent
			return fleet.Snapshot()
		}
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
}

// Serve is `den remote serve --proto 1`, the remote side: it reads Den's
// request from in, then writes hello, snapshots and events to out until in
// reaches EOF, out fails, the watchdog sees the session gone, or ctx ends.
// It never executes anything it reads. Errors are also sent as an error
// message when out still works.
func Serve(ctx context.Context, in io.Reader, out io.Writer, o ServeOptions) error {
	o.defaults()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	lines := newLineReader(in, MaxLine)
	reqc := make(chan []byte, 1)
	errc := make(chan error, 1)
	extra := make(chan struct{}, 1)
	go func() {
		line, err := lines.next()
		if err != nil {
			errc <- err
			return
		}
		reqc <- line
		// Version 1 takes nothing after the hello: anything more is refused
		// and ends the session, and EOF ends it quietly.
		if _, err := lines.next(); err != nil {
			cancel()
			return
		}
		extra <- struct{}{}
	}()
	var req Request
	select {
	case line := <-reqc:
		if json.Unmarshal(line, &req) != nil || req.Type != TypeHello {
			writeMsg(out, Error{Type: TypeError, Code: "request", Message: "expected a hello request"})
			return fmt.Errorf("bad request")
		}
	case err := <-errc:
		return err
	case <-time.After(o.RequestWait):
		return fmt.Errorf("no request within %s", o.RequestWait)
	case <-ctx.Done():
		return ctx.Err()
	}
	if req.Proto != Proto {
		writeMsg(out, Error{Type: TypeError, Code: "proto", Message: fmt.Sprintf("this den speaks proto %d, not %d", Proto, req.Proto)})
		return fmt.Errorf("unsupported proto %d", req.Proto)
	}
	policy := Min(req.Policy, Supported)

	tail := events.Follow(o.EventsPath, req.Cursor)
	defer tail.Close()
	host, _ := os.Hostname()
	agents, _ := o.Snapshot()
	hello := Hello{
		Type: TypeHello, Proto: Proto, Den: o.Version, Claude: claudeVersion(agents),
		Hostname: sanitize.Label(host, 64), RemoteNow: time.Now().Add(o.Skew).UTC(), Policy: policy, Labels: req.Labels,
		Cursor: tail.Cursor(),
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
			s.Agents = append(s.Agents, a.Meta(i, now, req.Labels))
		}
		return writeMsg(out, s)
	}
	if err := snap(agents); err != nil {
		return err
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
		case <-extra:
			writeMsg(out, Error{Type: TypeError, Code: "request", Message: "proto 1 takes nothing after hello"})
			return fmt.Errorf("Den sent something after hello")
		case <-dog.C:
			if o.Orphaned() {
				return fmt.Errorf("the ssh session is gone")
			}
		case <-changed:
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
