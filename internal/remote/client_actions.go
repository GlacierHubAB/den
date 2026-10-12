package remote

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/GlacierHubAB/den/internal/sanitize"
)

// Send and close are the two things Den may ask a host to do.
// Both go down the host's live session as one message each and come back as
// one result. Den refuses them itself under observe, for a host that is not
// live, and for text over MaxSendText, so a refusal never costs a round trip.

// Default waits for a result. serve's own close wait is 7 s at most.
const (
	defaultSendWait  = 10 * time.Second
	defaultCloseWait = 15 * time.Second
)

// link is a live session's way to carry actions: the pending ones wait on
// their result here, and all of them are failed when the session ends.
type link struct {
	write   func(v any) error
	applied Policy

	mu      sync.Mutex
	pending map[string]chan Result
	ended   bool
}

func newLink(write func(any) error, applied Policy) *link {
	return &link{write: write, applied: applied, pending: map[string]chan Result{}}
}

// open registers a request; false once the session has ended.
func (l *link) open(id string) (chan Result, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ended {
		return nil, false
	}
	c := make(chan Result, 1)
	l.pending[id] = c
	return c, true
}

func (l *link) forget(id string) {
	l.mu.Lock()
	delete(l.pending, id)
	l.mu.Unlock()
}

// resolve hands a result to the request waiting for it, once.
func (l *link) resolve(r Result) bool {
	l.mu.Lock()
	c, ok := l.pending[r.ID]
	delete(l.pending, r.ID)
	l.mu.Unlock()
	if ok {
		c <- r
	}
	return ok
}

// end fails every pending request: the session is gone, and whether the
// host acted is not known.
func (l *link) end(host string) {
	l.mu.Lock()
	l.ended = true
	pending := l.pending
	l.pending = map[string]chan Result{}
	l.mu.Unlock()
	for id, c := range pending {
		c <- Result{ID: id, Error: "the link to " + host + " ended before it answered; whether it acted is not known"}
	}
}

// Send posts text to the session with this id on host, as `den send` does
// there. It returns once the host's socket write succeeded or failed; Claude
// Code sends no acknowledgement, so success means delivered to its inbox.
func (m *Manager) Send(host, sessionID, text string) error {
	switch {
	case strings.TrimSpace(text) == "":
		return errors.New("nothing to send")
	case len(text) > MaxSendText:
		return fmt.Errorf("the message is %d KB, over the %d KB limit for a remote agent", (len(text)+1023)>>10, MaxSendText>>10)
	}
	// Escaping can make a text several times longer; check the line as it
	// will be written, so too large is not reported as offline.
	if raw, err := json.Marshal(SendCmd{Type: TypeSend, ID: newID(), Session: sessionID, Text: text}); err == nil && len(raw) > MaxLine {
		return fmt.Errorf("the message is too large once encoded (%d KB; the most is %d KB): shorten it", len(raw)>>10, MaxLine>>10)
	}
	_, err := m.act(host, "send", m.o.SendWait, func(id string) any {
		return SendCmd{Type: TypeSend, ID: id, Session: sessionID, Text: text}
	})
	return err
}

// CloseAgent ends the session with this id on host: SIGTERM there, and SIGKILL
// only with force. A working agent is refused without force. killed says it
// needed SIGKILL. The caller has asked the human first.
func (m *Manager) CloseAgent(host, sessionID string, force bool) (killed bool, err error) {
	r, err := m.act(host, "close", m.o.CloseWait, func(id string) any {
		return CloseCmd{Type: TypeClose, ID: id, Session: sessionID, Force: force}
	})
	return r.Killed, err
}

// Actionable says whether Send and Close would be tried now, and if not
// why, in words fit for the human. The views use it to say so before they
// ask anything.
func (m *Manager) Actionable(host string) error {
	_, err := m.linkFor(host, "act")
	return err
}

func nothingDone(verb string) string {
	switch verb {
	case "send":
		return "nothing was sent"
	case "close":
		return "nothing was closed"
	case "diff":
		return "no diff was read"
	case "spawn":
		return "nothing was started"
	case "cleanup":
		return "nothing was cleaned up"
	}
	return "nothing was done"
}

func (m *Manager) linkFor(host, verb string) (*link, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.state[host]
	if !ok {
		return nil, fmt.Errorf("no remote host called %s", sanitize.Label(host, 40))
	}
	s := st.status.State
	nothing := nothingDone(verb)
	switch {
	case st.link == nil || (s != Live && s != AttachedElsewhere):
		switch s {
		case Connecting:
			return nil, fmt.Errorf("%s is connecting; %s", host, nothing)
		case NeedsAttention, VersionMismatch, Disabled:
			return nil, fmt.Errorf("%s is %s; %s", host, strings.ReplaceAll(string(s), "_", " "), nothing)
		}
		return nil, fmt.Errorf("%s is offline; %s", host, nothing)
	case s == AttachedElsewhere:
		return nil, fmt.Errorf("%s; %s", ElsewhereNote(host), nothing)
	case st.link.applied.rank() < Approve.rank() || st.status.Policy.rank() < Approve.rank():
		return nil, fmt.Errorf("%s; %s", ObserveNote(host), nothing)
	}
	return st.link, nil
}

func (m *Manager) act(host, verb string, wait time.Duration, build func(id string) any) (Result, error) {
	l, err := m.linkFor(host, verb)
	if err != nil {
		return Result{}, err
	}
	id := newID()
	c, ok := l.open(id)
	if !ok {
		return Result{}, fmt.Errorf("%s is offline; %s", host, nothingDone(verb))
	}
	defer l.forget(id)
	if err := l.write(build(id)); err != nil {
		if errors.Is(err, ErrLineTooLong) {
			return Result{}, errors.New("the request is too large to send")
		}
		return Result{}, fmt.Errorf("%s is offline; %s", host, nothingDone(verb))
	}
	select {
	case r := <-c:
		if !r.OK {
			return r, errors.New(r.Error)
		}
		return r, nil
	case <-time.After(wait):
		return Result{}, fmt.Errorf("%s did not answer within %s; whether it acted is not known", host, wait.Round(time.Second))
	case <-m.ctx.Done():
		return Result{}, errors.New("Den is stopping; whether it acted is not known")
	}
}
