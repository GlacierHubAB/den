package api

import (
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/api/convcases"
)

// Every refused focus carries the reason of its row of the shared table,
// in error.data and nothing else there, and Options.FocusFailed hears it
// with the session and name only of an agent of this machine. The table's
// reasons are exactly FocusReasons.
func TestFocusTable(t *testing.T) {
	var tableReasons []string
	for _, c := range convcases.FocusCases {
		tableReasons = append(tableReasons, c.Reason)
	}
	if !slices.Equal(tableReasons, FocusReasons) {
		t.Fatalf("the table's reasons %v, Den's %v", tableReasons, FocusReasons)
	}

	type heard struct{ reason, session, agent string }
	var mu sync.Mutex
	var got []heard
	h := newHarnessWith(t, 2, func(o *Options) {
		o.FocusFailed = func(reason, session, agent string) {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, heard{reason, session, agent})
		}
	})
	old := focusTimeout
	focusTimeout = 200 * time.Millisecond
	t.Cleanup(func() { focusTimeout = old })
	h.s.Publish(&View{
		Fleet: Fleet{Agents: []Agent{
			{ID: "s-1", Name: "Mossy Lemur", State: StateIdle},
			{ID: "s-2", Name: "Twin Owl", State: StateIdle},
			{ID: "s-3", Name: "Twin Owl", State: StateIdle},
			{ID: "s-4@devbox", Name: "Remote Ray", Host: "devbox", Remote: true, State: StateIdle},
			{ID: "s-5", Name: "Quiet Panda", State: StateWaiting, Waiting: WaitingTTY},
		}},
		Items: []Item{{ID: "i-1"}},
	})
	c := h.dial()
	c.hello()
	unlimit := func() {
		h.s.focusLimit.mu.Lock()
		h.s.focusLimit.perApp, h.s.focusLimit.recent = nil, nil
		h.s.focusLimit.mu.Unlock()
	}
	// answered sends a focus of Mossy Lemur, answers it on the UI's side
	// with a, and returns the reply.
	answered := func(a FocusAnswer) line {
		unlimit()
		id := c.send("focus", map[string]string{"target": "Mossy Lemur"})
		act := nextAct(t, h.s)
		if act.Focus == nil {
			t.Fatalf("act %+v", act)
		}
		act.Focus.Reply <- a
		for {
			if l := c.next(); l.ID != nil && *l.ID == id {
				return l
			}
		}
	}
	plain := func(target string) line { unlimit(); return c.call("focus", map[string]string{"target": target}) }
	// session is the session FocusFailed must hear for each row: Mossy
	// Lemur's for a refusal about it, Quiet Panda's for its own, none else.
	run := map[string]func() (line, string){
		"no agent by that id or name":          func() (line, string) { return plain("nobody"), "" },
		"a name two agents share":              func() (line, string) { return plain("Twin Owl"), "" },
		"an inbox item":                        func() (line, string) { return plain("i-1"), "" },
		"an agent on another machine":          func() (line, string) { return plain("Remote Ray"), "" },
		"an agent waiting on its own terminal": func() (line, string) { return plain("Quiet Panda"), "s-5" },
		"an agent that has ended": func() (line, string) {
			return answered(FocusAnswer{Reason: FocusNotRunning, Message: "that agent is gone"}), "s-1"
		},
		"the human typed in Den just now": func() (line, string) {
			return answered(FocusAnswer{Reason: FocusHumanTyping, Message: "the human is typing in Den"}), "s-1"
		},
		"no window for the agent": func() (line, string) {
			return answered(FocusAnswer{Reason: FocusNoWindow, Message: "no terminal window found for Mossy Lemur"}), "s-1"
		},
		"the compositor did not focus it": func() (line, string) {
			return answered(FocusAnswer{Reason: FocusCompositorError, Message: "hyprctl: no such window"}), "s-1"
		},
		"a second focus within 3 s": func() (line, string) {
			answered(FocusAnswer{}) // the first is focused
			return c.call("focus", map[string]string{"target": "Mossy Lemur"}), "s-1"
		},
		"Den could not take it now": func() (line, string) {
			unlimit()
			for full := false; !full; {
				select {
				case h.s.acts <- Act{}:
				default:
					full = true
				}
			}
			l := c.call("focus", map[string]string{"target": "Mossy Lemur"})
			for drained := false; !drained; {
				select {
				case <-h.s.acts:
				default:
					drained = true
				}
			}
			return l, "s-1"
		},
		"Den's UI did not answer in time": func() (line, string) {
			unlimit()
			id := c.send("focus", map[string]string{"target": "Mossy Lemur"})
			nextAct(t, h.s) // taken, never answered
			for {
				if l := c.next(); l.ID != nil && *l.ID == id {
					return l, "s-1"
				}
			}
		},
	}
	for _, fc := range convcases.FocusCases {
		f, ok := run[fc.Name]
		if !ok {
			t.Errorf("%s: no test for this row", fc.Name)
			continue
		}
		mu.Lock()
		got = nil
		mu.Unlock()
		l, session := f()
		if l.OK || l.Error == nil || l.Error.Code != fc.Code || l.Error.Data["reason"] != fc.Reason || len(l.Error.Data) != 1 {
			t.Errorf("%s: %+v, want code %s reason %s and nothing else in data", fc.Name, l.Error, fc.Code, fc.Reason)
			continue
		}
		mu.Lock()
		last := got
		mu.Unlock()
		if n := len(last); n == 0 || last[n-1].reason != fc.Reason || last[n-1].session != session || (session == "") != (last[n-1].agent == "") {
			t.Errorf("%s: FocusFailed heard %+v, want reason %s session %q", fc.Name, last, fc.Reason, session)
		}
	}
}
