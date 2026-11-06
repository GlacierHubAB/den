package api

import (
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/GlacierHubAB/den/internal/names"
)

// Focus limits.
const (
	focusPerApp = 3 * time.Second // one focus per app this often
	focusPerMin = 10              // all apps together, per minute
)

// focusTimeout is how long a focus waits for the UI's answer; a var for
// tests.
var focusTimeout = 3 * time.Second

// FocusReq asks the UI to bring an agent's terminal to the front. The UI
// answers on Reply: a zero FocusAnswer when it did, else why not. It never
// changes Den's own tab, selection or command line.
type FocusReq struct {
	AgentID string
	Name    string
	Reply   chan FocusAnswer
}

// FocusAnswer is the UI's answer to a FocusReq: Reason, one of
// FocusReasons, and Message, a line for a human; both empty when the
// terminal came to the front.
type FocusAnswer struct {
	Reason  string
	Message string
}

// Why a focus failed: a refusal's error.data.reason. The set is closed;
// the reason is only the code, never the target or anything the
// compositor said.
const (
	FocusNoAgent         = "no_agent"          // no agent by that id or name in Den's list
	FocusAmbiguous       = "ambiguous"         // more than one agent has that name
	FocusInboxItem       = "inbox_item"        // the target is an inbox item
	FocusRemote          = "remote"            // the agent runs on another machine
	FocusNotRunning      = "not_running"       // the agent has ended
	FocusWaitsOnTerminal = "waits_on_terminal" // it waits on its own terminal
	FocusHumanTyping     = "human_typing"      // the human typed in Den just now
	FocusRateLimited     = "rate_limited"      // one per app every 3 s, ten a minute
	FocusBusy            = "busy"              // Den could not take the request now
	FocusNoAnswer        = "no_answer"         // Den's UI did not answer in time
	FocusNoWindow        = "no_window"         // Den knows no window, nor a herdr pane, for it
	FocusCompositorError = "compositor_error"  // the compositor did not focus it
)

// FocusReasons lists every reason, in the order the docs give them.
var FocusReasons = []string{
	FocusNoAgent, FocusAmbiguous, FocusInboxItem, FocusRemote, FocusNotRunning,
	FocusWaitsOnTerminal, FocusHumanTyping, FocusRateLimited, FocusBusy,
	FocusNoAnswer, FocusNoWindow, FocusCompositorError,
}

// focusFail refuses a focus with code, message and reason, and tells
// Options.FocusFailed: the reason, and the session and name of hit when it
// is an agent of this machine. The event is recorded before the app hears
// the refusal, so an app that has been refused can rely on it being there.
func (s *Server) focusFail(c *conn, id int64, code, msg, reason string, hit *Agent) {
	if s.opts.FocusFailed != nil {
		session, name := "", ""
		if hit != nil && !hit.Remote {
			session, name = hit.ID, hit.Name
		}
		s.opts.FocusFailed(reason, session, name)
	}
	c.failData(id, code, msg, map[string]string{"reason": reason})
}

type focusLimit struct {
	mu     sync.Mutex
	perApp map[string]time.Time
	recent []time.Time
}

// allow reports whether an app may focus now, and counts it if so.
func (f *focusLimit) allow(exe string, now time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.perApp == nil {
		f.perApp = map[string]time.Time{}
	}
	if t, ok := f.perApp[exe]; ok && now.Sub(t) < focusPerApp {
		return false
	}
	kept := f.recent[:0]
	for _, t := range f.recent {
		if now.Sub(t) < time.Minute {
			kept = append(kept, t)
		}
	}
	f.recent = kept
	if len(f.recent) >= focusPerMin {
		return false
	}
	f.perApp[exe] = now
	f.recent = append(f.recent, now)
	return true
}

// focus resolves a target only to an exact agent id or the name Den gave
// an agent; never a peer name, never a client string handed on. An inbox
// item is not focused from outside Den: the human goes there themselves.
func (s *Server) focus(c *conn, req Request) {
	var p struct {
		Target string `json:"target"`
	}
	if json.Unmarshal(req.Params, &p) != nil || p.Target == "" {
		c.fail(req.ID, CodeBadRequest, "target")
		return
	}
	v := s.view.Load()
	if v == nil {
		s.focusFail(c, req.ID, CodeNotFound, "no such agent", FocusNoAgent, nil)
		return
	}
	for _, it := range v.Items {
		if it.ID == p.Target {
			s.focusFail(c, req.ID, CodeUnavailable, "open the Inbox in Den to see it", FocusInboxItem, nil)
			return
		}
	}
	var hit *Agent
	for i := range v.Fleet.Agents {
		if a := &v.Fleet.Agents[i]; a.ID == p.Target {
			hit = a
		}
	}
	if hit == nil {
		// A name, as every place in Den resolves one (names.Resolve): a
		// name two agents share is refused, never guessed.
		var cands []names.Candidate
		for _, a := range v.Fleet.Agents {
			cands = append(cands, names.Candidate{ID: a.ID, Name: a.Name, Host: a.Host})
		}
		// The name as Den writes it, exactly.
		cd, err := names.Resolve(p.Target, cands, names.Opts{})
		var amb *names.Ambiguous
		var elsewhere *names.NeedsHost
		switch {
		case errors.As(err, &amb):
			s.focusFail(c, req.ID, CodeNotFound, "more than one agent has that name: name it by its id", FocusAmbiguous, nil)
			return
		case errors.As(err, &elsewhere):
			s.focusFail(c, req.ID, CodeUnavailable, "it runs on another machine: open it in Den", FocusRemote, nil)
			return
		}
		for i := range v.Fleet.Agents {
			if a := &v.Fleet.Agents[i]; err == nil && a.ID == cd.ID && a.Host == cd.Host && a.Name == p.Target {
				hit = a
			}
		}
	}
	switch {
	case hit == nil:
		s.focusFail(c, req.ID, CodeNotFound, "no such agent", FocusNoAgent, nil)
		return
	case hit.Remote:
		s.focusFail(c, req.ID, CodeUnavailable, "it runs on another machine: open it in Den", FocusRemote, nil)
		return
	case hit.Waiting == WaitingTTY:
		s.focusFail(c, req.ID, CodeUnavailable, "it waits on its terminal: open it in Den", FocusWaitsOnTerminal, hit)
		return
	}
	if !s.focusLimit.allow(c.peer.Exe, time.Now()) {
		s.focusFail(c, req.ID, CodeUnavailable, "too many focus requests", FocusRateLimited, hit)
		return
	}
	r := &FocusReq{AgentID: hit.ID, Name: hit.Name, Reply: make(chan FocusAnswer, 1)}
	select {
	case s.acts <- Act{Focus: r}:
	default:
		s.focusFail(c, req.ID, CodeUnavailable, "Den is busy", FocusBusy, hit)
		return
	}
	go func() {
		select {
		case a := <-r.Reply:
			if a.Reason != "" {
				if !slices.Contains(FocusReasons, a.Reason) {
					a.Reason = FocusCompositorError // the UI's own failure, never an unlisted code
				}
				s.focusFail(c, req.ID, CodeUnavailable, a.Message, a.Reason, hit)
				return
			}
			c.reply(req.ID, map[string]string{"focused": hit.Name})
		case <-time.After(focusTimeout):
			s.focusFail(c, req.ID, CodeUnavailable, "Den did not answer", FocusNoAnswer, hit)
		}
	}()
}
