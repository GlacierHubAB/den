package api

import (
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/GlacierHubAB/den/internal/names"
)

// Focus limits.
const (
	focusPerApp  = 3 * time.Second // one focus per app this often
	focusPerMin  = 10              // all apps together, per minute
	focusTimeout = 3 * time.Second // the UI's answer
)

// FocusReq asks the UI to bring an agent's terminal to the front. The UI
// answers on Reply: "" when it did, else why not. It never changes Den's
// own tab, selection or command line.
type FocusReq struct {
	AgentID string
	Name    string
	Reply   chan string
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
		c.fail(req.ID, CodeNotFound, "no such agent")
		return
	}
	for _, it := range v.Items {
		if it.ID == p.Target {
			c.fail(req.ID, CodeUnavailable, "open the Inbox in Den to see it")
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
			c.fail(req.ID, CodeNotFound, "more than one agent has that name: name it by its id")
			return
		case errors.As(err, &elsewhere):
			c.fail(req.ID, CodeUnavailable, "it runs on another machine: open it in Den")
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
		c.fail(req.ID, CodeNotFound, "no such agent")
		return
	case hit.Remote:
		c.fail(req.ID, CodeUnavailable, "it runs on another machine: open it in Den")
		return
	case hit.Waiting == WaitingTTY:
		c.fail(req.ID, CodeUnavailable, "it waits on its terminal: open it in Den")
		return
	}
	if !s.focusLimit.allow(c.peer.Exe, time.Now()) {
		c.fail(req.ID, CodeUnavailable, "too many focus requests")
		return
	}
	r := &FocusReq{AgentID: hit.ID, Name: hit.Name, Reply: make(chan string, 1)}
	select {
	case s.acts <- Act{Focus: r}:
	default:
		c.fail(req.ID, CodeUnavailable, "Den is busy")
		return
	}
	go func() {
		select {
		case why := <-r.Reply:
			if why != "" {
				c.fail(req.ID, CodeUnavailable, why)
				return
			}
			c.reply(req.ID, map[string]string{"focused": hit.Name})
		case <-time.After(focusTimeout):
			c.fail(req.ID, CodeUnavailable, "Den did not answer")
		}
	}()
}
