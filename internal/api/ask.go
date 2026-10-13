package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/GlacierHubAB/den/internal/orch"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// The status table: Den's own sentences for what the lane is doing, never
// model text, so no host string can reach a status line.
const (
	StatusQueued  = "Waiting for an earlier question…"
	StatusReading = "Reading the fleet in Den…"
	StatusWriting = "Writing a reply…"
)

// Limits on asks.
const (
	MaxAskText   = 16 << 10        // a question's text, in bytes
	MaxReply     = 64 << 10        // a reply's text, in bytes
	askDeadline  = 3 * time.Minute // one turn
	queueMax     = 4               // asks waiting, all clients together
	laneIdle     = 10 * time.Minute
	initDeadline = time.Minute
)

// askJob is one ask, from its request to its done or error chunk.
type askJob struct {
	id      string
	session string
	text    string
	exe     string
	send    func(Chunk) bool // false: the client is gone
	seq     int
	reply   strings.Builder
}

func (j *askJob) status(text string) {
	j.seq++
	j.send(Chunk{Type: "status", Ask: j.id, Seq: j.seq, Text: text})
}

// LaneConfig configures the lane.
type LaneConfig struct {
	Command  []string // claude, by default
	Dir      string   // the lane's own empty folder
	Model    func() string
	BudgetUS func() float64 // today's ask budget in dollars
	Start    func(context.Context, orch.LaneOptions) (*orch.Orchestrator, error)
}

// lane runs client asks one at a time through the read-only lane process.
type lane struct {
	cfg  LaneConfig
	chat *chatStore
	view func() *View

	mu       sync.Mutex
	queue    []*askJob
	statuses map[string]askState // running and queued asks, by ask id
	spent    float64
	day      string
	wake     chan struct{}
	stop     chan struct{}
	note     func(string)
	pid      atomic.Int64 // the running lane's pid, 0 for none
}

type askState struct {
	AskStatus
	exe string
}

func newLane(cfg LaneConfig, chat *chatStore, view func() *View, note func(string)) *lane {
	if cfg.Start == nil {
		cfg.Start = orch.StartLane
	}
	l := &lane{cfg: cfg, chat: chat, view: view, statuses: map[string]askState{}, wake: make(chan struct{}, 1), stop: make(chan struct{}), note: note}
	go l.run()
	return l
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

// errAsk is a refused ask with its wire code.
type errAsk struct{ code, msg string }

func (e errAsk) Error() string { return e.msg }

// enqueue checks and queues an ask. It returns the ask id.
func (l *lane) enqueue(j *askJob) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.leftLocked() <= 0 {
		return errAsk{CodeUnavailable, "today's ask budget is spent"}
	}
	if len(l.queue) >= queueMax {
		return errAsk{CodeUnavailable, "too many questions are waiting"}
	}
	for _, q := range l.queue {
		if q.exe == j.exe {
			return errAsk{CodeUnavailable, "this app already has a question waiting"}
		}
	}
	l.queue = append(l.queue, j)
	l.statuses[j.id] = askState{AskStatus: AskStatus{Ask: j.id, Session: j.session, Seq: j.seq, Text: StatusQueued}, exe: j.exe}
	select {
	case l.wake <- struct{}{}:
	default:
	}
	return nil
}

// Asks is the latest status of every running or queued ask; the session
// id only for asks the viewer's exe made.
func (l *lane) Asks(viewerExe string) []AskStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []AskStatus
	for _, s := range l.statuses {
		a := s.AskStatus
		if s.exe != viewerExe {
			a.Session = ""
		}
		out = append(out, a)
	}
	return out
}

func (l *lane) setStatus(j *askJob, text string) {
	j.status(text)
	l.mu.Lock()
	if s, ok := l.statuses[j.id]; ok {
		s.Seq, s.Text = j.seq, text
		l.statuses[j.id] = s
	}
	l.mu.Unlock()
}

func (l *lane) close() {
	select {
	case <-l.stop:
	default:
		close(l.stop)
	}
}

// run is the lane's one worker: it takes asks in order and owns the
// process, which it stops after laneIdle without asks.
func (l *lane) run() {
	var p *laneProc
	defer func() {
		if p != nil {
			p.kill()
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
		}
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
				}
				idle.Reset(laneIdle)
				continue
			case e, ok := <-procEvents(p):
				// Output with no ask in flight: not Den's turn. The lane
				// is stopped; nothing reaches a client.
				if p != nil && (!ok || e.Kind != orch.Exited) {
					l.note("the read-only lane spoke with no question in flight; it was stopped")
				}
				if p != nil {
					p.kill()
					p = nil
				}
				continue
			}
		}
		p = l.serve(p, j)
		l.mu.Lock()
		delete(l.statuses, j.id)
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

// serve runs one ask to its done or error chunk and returns the process to
// keep for the next one (nil when it was stopped).
func (l *lane) serve(p *laneProc, j *askJob) *laneProc {
	fail := func(msg string) {
		j.send(Chunk{Type: "error", Message: msg})
	}
	if l.left() <= 0 {
		fail("today's ask budget is spent")
		return p
	}
	if p != nil && p.session != j.session {
		p.kill()
		p = nil
	}
	if p == nil {
		var err error
		p, err = l.start(j.session)
		if err != nil {
			fail(err.Error())
			return nil
		}
	}
	l.setStatus(j, StatusReading)
	v := l.view()
	var roster []orch.RosterAgent
	if v != nil {
		roster = v.Roster
	}
	l.chat.Add(j.session, "user", j.text)
	if err := p.o.Send(orch.ComposeWith(roster, nil, fence(j.text))); err != nil {
		p.kill()
		fail("the read-only lane stopped")
		return nil
	}
	deadline := time.NewTimer(askDeadline)
	defer deadline.Stop()
	gotDelta := false
	for {
		select {
		case <-deadline.C:
			p.kill()
			fail("the reply took too long")
			return nil
		case e, ok := <-p.o.Events():
			if !ok {
				fail("the read-only lane stopped")
				return nil
			}
			switch e.Kind {
			case orch.ToolUse:
				// It has no tools: a tool call means it is not the lane
				// Den asked for.
				p.kill()
				l.note("the read-only lane tried to use a tool; it was stopped")
				fail("the read-only lane misbehaved and was stopped")
				return nil
			case orch.Delta, orch.Text:
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
					fail("the reply was too long and was cut")
					return nil
				}
				j.reply.WriteString(text)
				if !j.send(Chunk{Type: "text", Delta: text}) {
					// The client left; the turn still has to finish.
					continue
				}
			case orch.Result:
				l.mu.Lock()
				l.spent += e.CostUSD
				l.mu.Unlock()
				if e.IsError {
					fail(sanitize.Label("the lane could not answer: "+e.Text, 200))
					return p
				}
				l.chat.Add(j.session, "orchestrator", j.reply.String())
				l.setStatus(j, "")
				j.send(Chunk{Type: "done", Session: j.session})
				return p
			case orch.Error:
				p.kill()
				fail(sanitize.Label("the lane could not answer: "+e.Text, 200))
				return nil
			case orch.Exited:
				fail("the read-only lane stopped")
				return nil
			}
		}
	}
}

// start launches the lane for a session and checks its init event: no
// tools and no MCP servers, or it is killed.
func (l *lane) start(session string) (*laneProc, error) {
	o := orch.LaneOptions{Command: l.cfg.Command, Dir: l.cfg.Dir, BudgetUSD: l.left()}
	if l.cfg.Model != nil {
		o.Model = l.cfg.Model()
	}
	if v := l.view(); v != nil {
		o.Guidance = v.Guidance
	}
	resume := l.chat.IsStarted(session)
	if resume {
		o.Resume = session
	} else {
		o.SessionID = session
	}
	proc, err := l.cfg.Start(context.Background(), o)
	if err != nil {
		if errors.Is(err, orch.ErrNoBudget) {
			return nil, errors.New("today's ask budget is spent")
		}
		return nil, errors.New("Den could not start a read-only orchestrator")
	}
	timer := time.NewTimer(initDeadline)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			proc.Kill()
			return nil, errors.New("Den could not start a read-only orchestrator")
		case e, ok := <-proc.Events():
			if !ok || e.Kind == orch.Exited {
				proc.Kill()
				if resume {
					// No replay: a fresh session, and the client is told.
					l.chat.Fresh()
					return nil, errors.New("the earlier conversation could not be resumed; ask again to start a new one")
				}
				return nil, errors.New("Den could not start a read-only orchestrator")
			}
			if e.Kind != orch.Started {
				continue
			}
			if err := orch.LaneInitOK(e); err != nil {
				proc.Kill()
				l.note("the read-only lane did not start: " + err.Error())
				return nil, errors.New("Den could not start a read-only orchestrator")
			}
			if e.SessionID != "" && e.SessionID != session {
				proc.Kill()
				return nil, fmt.Errorf("the read-only lane started another session")
			}
			l.chat.Started(session)
			l.pid.Store(int64(proc.PID))
			return &laneProc{o: proc, session: session, l: l}, nil
		}
	}
}

// fence wraps a client's question as data: sanitised, < and > made
// harmless, inside a block Den writes.
func fence(text string) string {
	t := strings.Map(func(r rune) rune {
		switch r {
		case '<':
			return '‹'
		case '>':
			return '›'
		}
		return r
	}, sanitize.Text(text))
	return `<client_request client="drey">` + "\n" + strings.TrimSpace(t) + "\n</client_request>"
}
