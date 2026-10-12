package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/GlacierHubAB/den/internal/closer"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/peer"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// actions is serve's half of the two commands Den may give a host, send and
// close. They are the whole allowlist. Each is resolved against the
// host's live sessions when it arrives, by exact session id; neither runs a
// shell or any other program, and no text from the wire is ever an argument.
type actions struct {
	allowed  bool // the applied policy is approve or full
	sessions func() ([]fleet.Agent, error)
	post     func(socket, text string) error // peer.Send
	ops      closer.Ops
	self     closer.Self
	up       func(v any) error
	log      func(string)

	sem chan struct{} // MaxActions at once
	wg  sync.WaitGroup
}

func newActions(allowed bool, o ServeOptions, up func(any) error) *actions {
	return &actions{
		allowed: allowed, sessions: o.Snapshot, post: peer.Send, ops: o.CloseOps, self: o.CloseSelf,
		up: up, log: o.Log, sem: make(chan struct{}, MaxActions),
	}
}

// wait lets the actions in flight finish; their context ends them.
func (a *actions) wait() { a.wg.Wait() }

// handle takes a send or a close line already checked against the allowlist.
// It answers every one with exactly one result, except a line that is not a
// well formed command, which ends the session like any other protocol
// breach.
func (a *actions) handle(ctx context.Context, typ string, line []byte) error {
	var id, sid, text string
	var force bool
	switch typ {
	case TypeSend:
		var c SendCmd
		if json.Unmarshal(line, &c) != nil {
			return fmt.Errorf("a malformed send")
		}
		id, sid, text = c.ID, c.Session, c.Text
	default:
		var c CloseCmd
		if json.Unmarshal(line, &c) != nil {
			return fmt.Errorf("a malformed close")
		}
		id, sid, force = c.ID, c.Session, c.Force
	}
	if id == "" || len(id) > 64 || sanitize.Line(id) != id {
		return fmt.Errorf("a %s without a usable id", typ)
	}
	fail := func(why string) error {
		a.log(fmt.Sprintf("%s %s refused: %s", typ, shortID(id), why))
		return a.up(Result{Type: TypeResult, ID: id, Error: sanitize.Label(why, 160)})
	}
	switch {
	case !a.allowed:
		return fail("this host's policy is observe; it takes no actions")
	case sid == "" || len(sid) > 128 || sanitize.Line(sid) != sid:
		return fail("no such session")
	case typ == TypeSend && strings.TrimSpace(text) == "":
		return fail("nothing to send")
	case typ == TypeSend && len(text) > MaxSendText:
		return fail(fmt.Sprintf("the text is over %d KB", MaxSendText>>10))
	}
	select {
	case a.sem <- struct{}{}:
	default:
		return fail("too many actions at once")
	}
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer func() { <-a.sem }()
		var r Result
		if typ == TypeSend {
			r = a.send(sid, text)
		} else {
			r = a.close(ctx, sid, force)
		}
		r.Type, r.ID = TypeResult, id
		r.Error = sanitize.Label(r.Error, 160)
		if r.OK {
			a.log(fmt.Sprintf("%s %s done for %s", typ, shortID(id), sid[:min(8, len(sid))]))
		} else {
			a.log(fmt.Sprintf("%s %s failed: %s", typ, shortID(id), r.Error))
		}
		a.up(r)
	}()
	return nil
}

// resolve finds the live session with exactly this id. The agent is read
// from the host's own session files now, whatever Den saw before.
func (a *actions) resolve(sid string) (fleet.Agent, string) {
	agents, err := a.sessions()
	if err != nil {
		return fleet.Agent{}, "could not read the host's sessions"
	}
	var hit []fleet.Agent
	for _, ag := range agents {
		if ag.Session.SessionID == sid && ag.Session.Alive && ag.Status != fleet.Gone && ag.Session.PID > 1 {
			hit = append(hit, ag)
		}
	}
	switch len(hit) {
	case 0:
		return fleet.Agent{}, "no live session with that id"
	case 1:
		return hit[0], ""
	}
	return fleet.Agent{}, "more than one live session has that id"
}

// send posts text to the session's own messaging socket, as `den send` does.
// The socket path never leaves the host: the errors say what failed, not
// where.
func (a *actions) send(sid, text string) Result {
	ag, why := a.resolve(sid)
	if why != "" {
		return Result{Error: why}
	}
	if ag.Session.Socket == "" {
		return Result{Error: "this session has no inbox socket (Claude Code 2.1.224+ binds one)"}
	}
	if err := a.post(ag.Session.Socket, text); err != nil {
		msg := err.Error()
		switch {
		case strings.HasPrefix(msg, "inbox socket is gone"):
			return Result{Error: "its inbox socket is gone"}
		case strings.HasPrefix(msg, "inbox socket is not"), strings.HasPrefix(msg, "inbox socket is served"):
			return Result{Error: msg} // path-free by construction
		case strings.HasPrefix(msg, "connect"):
			return Result{Error: "could not connect to its inbox socket"}
		}
		return Result{Error: "could not write to its inbox socket"}
	}
	return Result{OK: true}
}

// close ends the session's process under the closer's rules: not Den's own
// process or an ancestor of it, not a working agent without force, SIGTERM
// and a wait, SIGKILL only with force. The closer first checks the pid is
// still this session's Claude Code and holds it by a pidfd (closer.System).
// A host has no desktop window to close.
func (a *actions) close(ctx context.Context, sid string, force bool) Result {
	ag, why := a.resolve(sid)
	if why != "" {
		return Result{Error: why}
	}
	t := closer.Target{SessionID: ag.Session.SessionID, Name: ag.Name, Peer: ag.Session.Name, PID: ag.Session.PID}
	if ag.Session.StartedAt > 0 {
		t.Started = ag.Session.Started() // the closer checks the pid is still this session's Claude Code
	}
	switch ag.Status {
	case fleet.Working:
		t.Status = "working"
	case fleet.NeedsYou:
		t.Status = "needs you"
	case fleet.Idle:
		t.Status = "idle"
	default:
		t.Status = "unknown"
	}
	plan := closer.Build([]closer.Target{t}, force, a.self)
	if len(plan.Refused) > 0 {
		return Result{Error: plan.Refused[0].Why}
	}
	res := closer.Run(ctx, plan, a.self, a.ops)
	r := res[0]
	switch {
	case r.Err != nil:
		// The closer says what it refused or could not do: not Claude
		// Code, a newer process on that pid, no pidfd, a wait cut short.
		return Result{Error: r.Err.Error()}
	case !r.Ended && force:
		return Result{Error: "still running after SIGKILL"}
	case !r.Ended:
		return Result{Error: "still running after the graceful stop; close it with force to kill it"}
	}
	return Result{OK: true, Killed: r.Killed}
}

// dirtyCache remembers, per directory, whether the checkout has uncommitted
// changes, so a snapshot never waits for git: a stale or unknown entry is
// refreshed in the background and nudges serve to send a snapshot once it
// knows. Only a yes or no is kept, never a path.
type dirtyCache struct {
	ttl   time.Duration
	check func(ctx context.Context, dir string) (dirty, ok bool)
	nudge func()

	mu sync.Mutex
	m  map[string]dirtyEntry
}

type dirtyEntry struct {
	at      time.Time
	dirty   bool
	ok      bool
	running bool
}

func newDirtyCache(ttl time.Duration, nudge func()) *dirtyCache {
	return &dirtyCache{ttl: ttl, nudge: nudge, m: map[string]dirtyEntry{}, check: func(ctx context.Context, dir string) (bool, bool) {
		paths, ok := closer.Checkout(ctx, dir)
		return len(paths) > 0, ok
	}}
}

// get is the last known state of dir, nil when unknown.
func (c *dirtyCache) get(dir string) *bool {
	if dir == "" || !filepath.IsAbs(dir) {
		return nil // a relative folder would be read relative to serve's own
	}
	c.mu.Lock()
	e := c.m[dir]
	if !e.running && time.Since(e.at) > c.ttl {
		e.running = true
		c.m[dir] = e
		go c.refresh(dir, e)
	}
	c.mu.Unlock()
	if !e.ok {
		return nil
	}
	d := e.dirty
	return &d
}

func (c *dirtyCache) refresh(dir string, before dirtyEntry) {
	d, ok := c.check(context.Background(), dir)
	c.mu.Lock()
	c.m[dir] = dirtyEntry{at: time.Now(), dirty: d, ok: ok}
	c.mu.Unlock()
	if ok != before.ok || d != before.dirty {
		c.nudge()
	}
}
