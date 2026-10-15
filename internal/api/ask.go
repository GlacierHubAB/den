package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/text/unicode/norm"

	"github.com/GlacierHubAB/den/internal/orch"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// The status table: Den's own sentences for what the lane is doing, never
// model text, so no host string can reach a status line.
const (
	StatusStarting = "Starting Den's read-only orchestrator…"
	StatusReading  = "Reading the fleet in Den…"
	StatusWriting  = "Writing a reply…"
)

// statusQueued says how many asks are ahead.
func statusQueued(ahead int) string {
	if ahead == 1 {
		return "Waiting for 1 earlier question…"
	}
	return "Waiting for " + strconv.Itoa(ahead) + " earlier questions…"
}

// Limits on asks.
const (
	MaxAskText      = 16 << 10         // a question's text, in bytes
	MaxReply        = 64 << 10         // a reply's text, in bytes
	askDeadline     = 3 * time.Minute  // one turn
	silenceDeadline = 90 * time.Second // a turn with no output at all
	queueMax        = 4                // asks waiting, all clients together
	laneIdle        = 10 * time.Minute
	versionDeadline = 10 * time.Second
)

// checkDeadline bounds the lane's answers to Den's checks before any turn;
// tests shorten it.
var checkDeadline = 20 * time.Second

// askJob is one ask, from its request to its done or error chunk.
type askJob struct {
	id      string
	session string
	text    string
	exe     string
	// scope is who the ask belongs to: its connection, or the group the
	// client named in hello. A newer ask supersedes only one in its scope.
	scope string
	send  func(Chunk) bool // false: the client is gone
	seq   int
	reply strings.Builder
	// cancelled is closed by ask.cancel, by a newer ask in the same scope,
	// or when the asking connection goes: the turn stops, its slot frees.
	cancelled chan struct{}
	once      sync.Once
	mu        sync.Mutex // seq and ended: statuses come from two goroutines
	ended     bool       // a done or error chunk was sent
	reason    string     // why it was cancelled
}

func (j *askJob) cancel() { j.cancelWith("cancelled") }

// superseded is why an ask ends when its scope asks again.
const superseded = "superseded by a newer question"

// cancelWith stops the ask; the first reason given is the one its stream
// ends with.
func (j *askJob) cancelWith(reason string) {
	j.once.Do(func() {
		j.mu.Lock()
		j.reason = reason
		j.mu.Unlock()
		close(j.cancelled)
	})
}

// why is the reason the ask was stopped.
func (j *askJob) why() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.reason == "" {
		return "cancelled"
	}
	return j.reason
}

func (j *askJob) isCancelled() bool {
	select {
	case <-j.cancelled:
		return true
	default:
		return false
	}
}

func (j *askJob) status(text string) int {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.ended {
		return j.seq
	}
	j.seq++
	j.send(Chunk{Type: "status", Ask: j.id, Seq: j.seq, Text: text})
	return j.seq
}

// end sends the stream's last chunk, once: done, or error with why.
func (j *askJob) end(errMsg string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.ended {
		return
	}
	j.ended = true
	if errMsg == "" {
		j.send(Chunk{Type: "done", Ask: j.id, Session: j.session})
		return
	}
	j.send(Chunk{Type: "error", Ask: j.id, Message: errMsg})
}

// LaneConfig configures the lane.
type LaneConfig struct {
	Command  []string // claude, by default
	Dir      string   // the lane's own empty folder
	Model    func() string
	BudgetUS func() float64 // today's ask budget in dollars
	Start    func(context.Context, orch.LaneOptions) (*orch.Orchestrator, error)
	Version  func(context.Context, []string) (string, error)
	// SpendPath keeps today's spend across restarts ("" keeps it in memory).
	SpendPath string
}

// lane runs client asks one at a time through the read-only lane process.
type lane struct {
	cfg  LaneConfig
	chat *chatStore
	view func() *View

	mu       sync.Mutex
	queue    []*askJob
	cur      *askJob
	statuses map[string]askState // running and queued asks, by ask id
	jobs     map[string]*askJob  // the same asks, to cancel them
	spent    float64
	day      string
	state    string // off, starting, ready, failed: <why>
	wake     chan struct{}
	stop     chan struct{}
	note     func(string)
	changed  func() // asks or lane state moved: the fleet's asks are stale
	pid      atomic.Int64

	failures []time.Time // failed starts, for the backoff and the lockout
	nextTry  time.Time   // no start before this
	locked   bool        // 3 failed starts in 5 minutes: until /api resume
}

// Restart limits.
const (
	backoffMin  = time.Second
	backoffMax  = time.Minute
	lockoutRuns = 3
	lockoutSpan = 5 * time.Minute
	rotateTurns = 50 // a lane session starts afresh after this many turns
)

// startFailed counts a failed start: the next waits longer, and three in
// five minutes stop the lane until the human resumes the API.
func (l *lane) startFailed(why string) {
	l.mu.Lock()
	now := time.Now()
	kept := l.failures[:0]
	for _, t := range l.failures {
		if now.Sub(t) < lockoutSpan {
			kept = append(kept, t)
		}
	}
	l.failures = append(kept, now)
	wait := backoffMin << (len(l.failures) - 1)
	if wait > backoffMax {
		wait = backoffMax
	}
	l.nextTry = now.Add(wait)
	locked := len(l.failures) >= lockoutRuns && !l.locked
	if locked {
		l.locked = true
	}
	l.mu.Unlock()
	if locked {
		l.note("the read-only lane failed to start 3 times in 5 minutes; asks are refused until /api resume. Last: " + why)
	}
}

// startAllowed says why the lane may not start now, "" when it may.
func (l *lane) startAllowed() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.locked {
		return "the read-only lane failed to start 3 times; /api resume in Den lets it try again"
	}
	if wait := time.Until(l.nextTry); wait > 0 {
		return fmt.Sprintf("the read-only lane failed to start; it tries again in %d s", int(wait.Seconds())+1)
	}
	return ""
}

// resume lifts the lockout: /api resume.
func (l *lane) resume() {
	l.mu.Lock()
	l.locked, l.failures, l.nextTry = false, nil, time.Time{}
	l.mu.Unlock()
}

type askState struct {
	AskStatus
	scope string
}

func newLane(cfg LaneConfig, chat *chatStore, view func() *View, note func(string)) *lane {
	if cfg.Start == nil {
		cfg.Start = orch.StartLane
	}
	if cfg.Version == nil {
		cfg.Version = orch.LaneVersion
	}
	l := &lane{cfg: cfg, chat: chat, view: view, statuses: map[string]askState{}, jobs: map[string]*askJob{},
		state: "off", wake: make(chan struct{}, 1), stop: make(chan struct{}), note: note, changed: func() {}}
	l.day, l.spent = loadSpend(cfg.SpendPath)
	go l.run()
	return l
}

// addSpend counts a turn's cost against today's budget and keeps it, so a
// restart of Den does not reset it.
func (l *lane) addSpend(usd float64) {
	l.mu.Lock()
	l.leftLocked()
	l.spent += usd
	day, spent := l.day, l.spent
	l.mu.Unlock()
	saveSpend(l.cfg.SpendPath, day, spent)
}

// left is what remains of today's budget.
func (l *lane) left() float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.leftLocked()
}

func (l *lane) leftLocked() float64 {
	today := time.Now().Format("2006-01-02")
	if l.day != today {
		l.day, l.spent = today, 0
	}
	budget := 0.0
	if l.cfg.BudgetUS != nil {
		budget = l.cfg.BudgetUS()
	}
	return budget - l.spent
}

// LaneState is the lane's state and the asks waiting, for /api.
type LaneState struct {
	State    string // off, starting, ready, failed: <why>
	Queued   int
	Running  bool
	SpentUSD float64
}

func (l *lane) State() LaneState {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.leftLocked()
	return LaneState{State: l.state, Queued: len(l.queue), Running: l.cur != nil, SpentUSD: l.spent}
}

func (l *lane) setState(s string) {
	l.mu.Lock()
	l.state = s
	l.mu.Unlock()
	l.changed()
}

// errAsk is a refused ask with its wire code.
type errAsk struct{ code, msg string }

func (e errAsk) Error() string { return e.msg }

// enqueue queues an ask. A newer ask in the same scope (connection or
// hello group) supersedes its earlier one, which ends with "superseded";
// another process of the same program never does. The budget and the
// queue cap refuse at once.
func (l *lane) enqueue(j *askJob) error {
	l.mu.Lock()
	if l.leftLocked() <= 0 {
		l.mu.Unlock()
		return errAsk{CodeUnavailable, "today's ask budget is spent"}
	}
	var old []*askJob
	if l.cur != nil && l.cur.scope == j.scope {
		old = append(old, l.cur)
	}
	kept := l.queue[:0]
	for _, q := range l.queue {
		if q.scope == j.scope {
			old = append(old, q)
			continue
		}
		kept = append(kept, q)
	}
	l.queue = kept
	if len(l.queue) >= queueMax {
		l.mu.Unlock()
		return errAsk{CodeUnavailable, "too many questions are waiting"}
	}
	if j.cancelled == nil {
		j.cancelled = make(chan struct{})
	}
	l.queue = append(l.queue, j)
	l.jobs[j.id] = j
	ahead := len(l.queue) - 1
	if l.cur != nil && l.cur.scope != j.scope {
		ahead++
	}
	l.statuses[j.id] = askState{AskStatus: AskStatus{Ask: j.id, Session: j.session}, scope: j.scope}
	l.mu.Unlock()
	for _, o := range old {
		o.cancelWith(superseded)
	}
	// Queued, removed asks end here; the running one ends in serve.
	for _, o := range old {
		if o != l.current() {
			l.finish(o, superseded)
		}
	}
	if ahead > 0 {
		l.setStatus(j, statusQueued(ahead))
	}
	select {
	case l.wake <- struct{}{}:
	default:
	}
	return nil
}

func (l *lane) current() *askJob {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.cur
}

// finish ends an ask's stream and forgets it.
func (l *lane) finish(j *askJob, errMsg string) {
	j.end(errMsg)
	l.mu.Lock()
	delete(l.statuses, j.id)
	delete(l.jobs, j.id)
	l.mu.Unlock()
	l.changed()
}

// cancel stops an ask by its id, which only the asking connection was told,
// and only for the program that made it; false for any other ask.
func (l *lane) cancel(id, exe string) bool {
	l.mu.Lock()
	j := l.jobs[id]
	l.mu.Unlock()
	if j == nil || j.exe != exe {
		return false
	}
	j.cancel()
	l.mu.Lock()
	queued := false
	kept := l.queue[:0]
	for _, q := range l.queue {
		if q == j {
			queued = true
			continue
		}
		kept = append(kept, q)
	}
	l.queue = kept
	l.mu.Unlock()
	if queued {
		l.finish(j, "cancelled")
	}
	return true
}

// Asks is the latest status of every running or queued ask. The ask and
// session ids only for asks in the viewer's scope: an ask id is what cancels
// it.
func (l *lane) Asks(viewerScope string) []AskStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []AskStatus
	for _, s := range l.statuses {
		a := s.AskStatus
		if s.scope != viewerScope {
			a.Ask, a.Session = "", ""
		}
		out = append(out, a)
	}
	return out
}

func (l *lane) setStatus(j *askJob, text string) {
	seq := j.status(text)
	l.mu.Lock()
	if s, ok := l.statuses[j.id]; ok {
		s.Seq, s.Text = seq, text
		l.statuses[j.id] = s
	}
	l.mu.Unlock()
	l.changed()
}

func (l *lane) close() {
	select {
	case <-l.stop:
	default:
		close(l.stop)
	}
}

// run is the lane's one worker: it takes asks in order and owns the
// process, which it stops after laneIdle without asks. Every ask it takes
// ends with a done or error chunk.
func (l *lane) run() {
	var p *laneProc
	defer func() {
		if p != nil {
			p.kill()
		}
		l.mu.Lock()
		left := append([]*askJob(nil), l.queue...)
		l.queue = nil
		l.mu.Unlock()
		for _, j := range left {
			l.finish(j, "Den stopped the API")
		}
	}()
	idle := time.NewTimer(laneIdle)
	defer idle.Stop()
	for {
		l.mu.Lock()
		var j *askJob
		if len(l.queue) > 0 {
			j = l.queue[0]
			l.queue = l.queue[1:]
			l.cur = j
		}
		rest := append([]*askJob(nil), l.queue...)
		l.mu.Unlock()
		if j == nil {
			select {
			case <-l.stop:
				return
			case <-l.wake:
				continue
			case <-idle.C:
				if p != nil {
					p.kill()
					p = nil
					l.setState("off")
				}
				idle.Reset(laneIdle)
				continue
			case e, ok := <-procEvents(p):
				// Output with no ask in flight: not Den's turn. The lane
				// is stopped; nothing reaches a client.
				if !ok || e.Kind == orch.Exited {
					p = nil
					l.setState("off")
					continue
				}
				if e.Kind == orch.Control {
					continue
				}
				l.note("the read-only lane spoke with no question in flight; it was stopped")
				p.kill()
				p = nil
				l.setState("off")
				continue
			}
		}
		for i, q := range rest {
			l.setStatus(q, statusQueued(i+1))
		}
		if j.isCancelled() {
			l.finish(j, j.why())
		} else {
			var errMsg string
			p, errMsg = l.serve(p, j)
			l.finish(j, errMsg)
		}
		l.mu.Lock()
		l.cur = nil
		l.mu.Unlock()
		if !idle.Stop() {
			select {
			case <-idle.C:
			default:
			}
		}
		idle.Reset(laneIdle)
	}
}

// laneProc is a running lane process and the session it runs.
type laneProc struct {
	o       *orch.Orchestrator
	session string
	l       *lane
	inited  bool // its init event came and passed LaneInitOK
}

func (p *laneProc) kill() {
	if p != nil && p.o != nil {
		p.o.Kill()
		p.l.pid.CompareAndSwap(int64(p.o.PID), 0)
	}
}

func procEvents(p *laneProc) <-chan orch.Event {
	if p == nil || p.o == nil {
		return nil
	}
	return p.o.Events()
}

// serve runs one ask and returns the process to keep (nil when it was
// stopped) and "" for done or why it failed.
func (l *lane) serve(p *laneProc, j *askJob) (*laneProc, string) {
	if l.left() <= 0 {
		return p, "today's ask budget is spent"
	}
	if p != nil && p.session != j.session {
		p.kill()
		p = nil
	}
	if p == nil {
		if why := l.startAllowed(); why != "" {
			return nil, why
		}
		if l.chat.Turns(j.session) >= rotateTurns {
			// A long session makes every turn dearer: start a new one.
			j.session = l.chat.Fresh()
		}
		l.setStatus(j, StatusStarting)
		var err error
		p, err = l.start(j)
		if err != nil {
			if !j.isCancelled() {
				l.startFailed(err.Error())
			}
			l.setState("failed: " + err.Error())
			return nil, err.Error()
		}
	}
	l.setState("ready")
	l.setStatus(j, StatusReading)
	v := l.view()
	var roster []orch.RosterAgent
	if v != nil {
		roster = v.Roster
	}
	l.chat.Add(j.session, "user", j.text)
	if err := p.o.Send(orch.ComposeWith(roster, nil, fence(j.text))); err != nil {
		p.kill()
		return nil, "the read-only lane stopped"
	}
	deadline := time.NewTimer(askDeadline)
	defer deadline.Stop()
	silence := time.NewTimer(silenceDeadline)
	defer silence.Stop()
	gotDelta := false
	for {
		select {
		case <-deadline.C:
			p.kill()
			return nil, "the reply took too long"
		case <-silence.C:
			p.kill()
			return nil, "the read-only lane did not answer"
		case <-j.cancelled:
			// The app cancelled, asked anew, or its connection went: the
			// turn stops and the next ask resumes the session.
			p.kill()
			return nil, j.why()
		case e, ok := <-p.o.Events():
			if !ok {
				return nil, "the read-only lane stopped"
			}
			if !silence.Stop() {
				select {
				case <-silence.C:
				default:
				}
			}
			silence.Reset(silenceDeadline)
			switch e.Kind {
			case orch.Started:
				// The init event comes with the first turn: the last check
				// that the lane is what Den asked for.
				if err := orch.LaneInitOK(e); err != nil {
					p.kill()
					l.note("the read-only lane did not start: " + err.Error())
					l.setState("failed: " + err.Error())
					return nil, "Den could not start a read-only orchestrator: " + err.Error()
				}
				if e.SessionID != "" && e.SessionID != j.session {
					p.kill()
					return nil, "the read-only lane started another session"
				}
				p.inited = true
				l.chat.Started(j.session)
			case orch.ToolUse:
				// It has no tools: a tool call means it is not the lane
				// Den asked for.
				p.kill()
				l.note("the read-only lane tried to use a tool; it was stopped")
				return nil, "the read-only lane misbehaved and was stopped"
			case orch.Delta, orch.Text:
				if !p.inited {
					p.kill()
					return nil, "the read-only lane answered before it said what it is"
				}
				if e.Kind == orch.Text && gotDelta {
					continue
				}
				if e.Kind == orch.Delta && !gotDelta {
					gotDelta = true
					l.setStatus(j, StatusWriting)
				}
				text := sanitize.Text(e.Text)
				if j.reply.Len()+len(text) > MaxReply {
					p.kill()
					return nil, "the reply was too long and was cut"
				}
				j.reply.WriteString(text)
				if !j.send(Chunk{Type: "text", Ask: j.id, Delta: text}) {
					j.cancel() // the client left or stopped reading
				}
			case orch.Result:
				l.addSpend(e.CostUSD)
				if e.IsError {
					if !p.inited {
						p.kill()
						return nil, sanitize.Label("the read-only lane could not start: "+e.Text, 200)
					}
					return p, sanitize.Label("the lane could not answer: "+e.Text, 200)
				}
				l.chat.Add(j.session, "orchestrator", j.reply.String())
				l.setStatus(j, "")
				return p, ""
			case orch.Error:
				p.kill()
				return nil, sanitize.Label("the lane could not answer: "+e.Text, 200)
			case orch.Exited:
				if !l.chat.IsStarted(j.session) && strings.Contains(e.Stderr, "No conversation found") {
					l.chat.Fresh()
				}
				msg := "the read-only lane stopped"
				if e.Stderr != "" {
					msg += ": " + e.Stderr
				}
				return nil, sanitize.Label(msg, 200)
			}
		}
	}
}

// start launches the lane for a session and checks it before any turn:
// a verified Claude Code release, then its answers to the initialize and
// mcp_status control requests (no slash commands, dontAsk, no MCP
// servers). Its init event, which claude prints only once a turn arrives,
// is checked in serve. Every failure comes back within seconds.
func (l *lane) start(j *askJob) (*laneProc, error) {
	l.setState("starting")
	ctx, cancel := context.WithTimeout(context.Background(), versionDeadline)
	version, err := l.cfg.Version(ctx, l.cfg.Command)
	cancel()
	if err != nil {
		return nil, errors.New("Den could not run claude: " + sanitize.Label(err.Error(), 120))
	}
	if !orch.LaneVerifiedVersion(version) {
		err := fmt.Errorf("Claude Code %s is not verified for the read-only lane (scripts/lane-spike.sh)", sanitize.Label(version, 40))
		l.note("the read-only lane did not start: " + err.Error())
		return nil, err
	}
	o := orch.LaneOptions{Command: l.cfg.Command, Dir: l.cfg.Dir, BudgetUSD: l.left()}
	if l.cfg.Model != nil {
		o.Model = l.cfg.Model()
	}
	if v := l.view(); v != nil {
		o.Guidance = v.Guidance
	}
	if l.chat.IsStarted(j.session) {
		o.Resume = j.session
	} else {
		o.SessionID = j.session
	}
	proc, err := l.cfg.Start(context.Background(), o)
	if err != nil {
		if errors.Is(err, orch.ErrNoBudget) {
			return nil, errors.New("today's ask budget is spent")
		}
		return nil, errors.New("Den could not start a read-only orchestrator")
	}
	p := &laneProc{o: proc, session: j.session, l: l}
	l.pid.Store(int64(proc.PID))
	if proc.ControlRequest("den-initialize", "initialize") != nil || proc.ControlRequest("den-mcp-status", "mcp_status") != nil {
		p.kill()
		return nil, errors.New("Den could not talk to the read-only orchestrator")
	}
	var initialize, mcp json.RawMessage
	timer := time.NewTimer(checkDeadline)
	defer timer.Stop()
	for initialize == nil || mcp == nil {
		select {
		case <-timer.C:
			p.kill()
			return nil, errors.New("the read-only orchestrator did not answer Den's checks")
		case <-j.cancelled:
			p.kill()
			return nil, errors.New(j.why())
		case e, ok := <-proc.Events():
			if !ok || e.Kind == orch.Exited {
				p.kill()
				if o.Resume != "" {
					// No replay: a fresh session, and the client is told.
					l.chat.Fresh()
					return nil, errors.New("the earlier conversation could not be resumed; ask again to start a new one")
				}
				msg := "the read-only orchestrator stopped at start"
				if e.Stderr != "" {
					msg += ": " + sanitize.Label(e.Stderr, 160)
				}
				return nil, errors.New(msg)
			}
			if e.Kind != orch.Control {
				continue
			}
			if e.Response == nil {
				p.kill()
				return nil, errors.New("the read-only orchestrator refused Den's checks: " + sanitize.Label(e.Text, 120))
			}
			switch e.RequestID {
			case "den-initialize":
				initialize = e.Response
			case "den-mcp-status":
				mcp = e.Response
			}
		}
	}
	if err := orch.LaneControlOK(initialize, mcp); err != nil {
		p.kill()
		l.note("the read-only lane did not start: " + err.Error())
		return nil, errors.New("Den could not start a read-only orchestrator: " + err.Error())
	}
	return p, nil
}

// fence wraps a client's question as data: sanitised, < and > made
// harmless (after NFKC, so a fullwidth ＜ is caught too), inside a block
// Den writes.
func fence(text string) string {
	t := strings.Map(func(r rune) rune {
		switch r {
		case '<':
			return '‹'
		case '>':
			return '›'
		}
		return r
	}, norm.NFKC.String(sanitize.Text(text)))
	return `<client_request client="drey">` + "\n" + strings.TrimSpace(t) + "\n</client_request>"
}
