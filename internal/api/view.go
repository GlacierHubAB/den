package api

import (
	"bytes"
	"sync"
	"time"
)

// View switch limits. The dock gives up on a reply after 5 s and the
// switch itself stays within 4 (internal/view), so Den's wait for the UI
// ends before that.
const viewPerMin = 3

// viewWait is how long a call waits for the UI; tests shorten it.
var viewWait = 4500 * time.Millisecond

// ViewReq asks the UI to switch views: to ViewAgent, which shows Den's
// windows, or ViewUser, which hides them. The UI answers on Reply.
type ViewReq struct {
	Want  string
	Reply chan ViewReply
}

// ViewReply is the UI's answer: a result, or why not (Code and Msg are an
// error's).
type ViewReply struct {
	Result ViewResult
	Code   string
	Msg    string
}

// viewSwitch is one switch at a time, and at most viewPerMin a minute for
// each pairing.
type viewSwitch struct {
	mu      sync.Mutex
	running bool
	recent  map[string][]time.Time
}

// begin claims the one switch slot, and counts it for who. It reports
// whether the slot was free ("busy" is not counted) and, if so, whether who
// has switches left this minute.
func (v *viewSwitch) begin(who string, now time.Time) (free, within bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.running {
		return false, false
	}
	if v.recent == nil {
		v.recent = map[string][]time.Time{}
	}
	var kept []time.Time
	for _, t := range v.recent[who] {
		if now.Sub(t) < time.Minute {
			kept = append(kept, t)
		}
	}
	if len(kept) >= viewPerMin {
		v.recent[who] = kept
		return true, false
	}
	v.recent[who] = append(kept, now)
	v.running = true
	return true, true
}

func (v *viewSwitch) end() {
	v.mu.Lock()
	v.running = false
	v.mu.Unlock()
}

// viewLane is who may switch views: a confirmed pairing with control on.
// It returns the pairing's id, "" for anyone else.
func (s *Server) viewLane(c *conn) string {
	c.mu.Lock()
	paired := c.paired
	c.mu.Unlock()
	st := s.ctl
	st.mu.Lock()
	defer st.mu.Unlock()
	pr := st.pairings[paired]
	if paired == "" || pr == nil || !pr.Confirmed || pr.Off {
		return ""
	}
	return paired
}

// viewVerb is view.show and view.hide. They take no params, are the same
// when asked again, and move nothing but Den's own windows, which the UI
// does. A call is never an approval of anything.
func (s *Server) viewVerb(c *conn, req Request, want string) {
	who := s.viewLane(c)
	if who == "" {
		c.fail(req.ID, CodeForbidden, "prove first")
		return
	}
	if p := bytes.TrimSpace(req.Params); len(p) > 0 && string(p) != "null" && string(p) != "{}" {
		c.fail(req.ID, CodeBadRequest, "no params")
		return
	}
	if v := s.view.Load(); v == nil || v.Fleet.View == "" {
		c.fail(req.ID, CodeUnavailable, "this Den cannot switch views")
		return
	}
	free, within := s.viewSwitch.begin(who, time.Now())
	switch {
	case !free:
		c.fail(req.ID, CodeBusy, "a view switch is running")
		return
	case !within:
		c.fail(req.ID, CodeUnavailable, "too many view switches")
		return
	}
	r := &ViewReq{Want: want, Reply: make(chan ViewReply, 1)}
	if !s.act(Act{View: r}) {
		s.viewSwitch.end()
		c.fail(req.ID, CodeBusy, "Den is busy")
		return
	}
	go func() {
		defer s.viewSwitch.end()
		select {
		case rep := <-r.Reply:
			if rep.Code != "" {
				c.fail(req.ID, rep.Code, rep.Msg)
				return
			}
			c.reply(req.ID, rep.Result)
			s.pushView(ViewChange{View: rep.Result.View, Shown: rep.Result.Shown, Total: rep.Result.Total})
		case <-time.After(viewWait):
			c.fail(req.ID, CodeUnavailable, "Den did not answer")
		}
	}()
}

// pushView tells the connections that follow the fleet how a switch ended.
// A slow reader may miss it: hello and the fleet carry the view too.
func (s *Server) pushView(ch ViewChange) {
	s.mu.Lock()
	conns := make([]*conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		if c.subscribed(EventFleet) {
			c.push(Event{Event: EventView, Data: ch}, false)
		}
	}
}

// ConsoleUp reports whether a connection is open that may switch views: the
// dock that can call view.show. With none, a card that waits behind a hidden
// Den window has nobody to bring it back.
func (s *Server) ConsoleUp() bool {
	s.mu.Lock()
	conns := make([]*conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		if s.viewLane(c) != "" {
			return true
		}
	}
	return false
}
