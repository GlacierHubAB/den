package view

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/hypr"
	"github.com/GlacierHubAB/den/internal/procs"
)

// handAgent adds an agent Den has no marker for: a session named peer whose
// process 200+n runs under terminal 2000+n, in a window with the given title.
func (r *rig) handAgent(n int, peer, title string) string {
	r.t.Helper()
	addr := "0xh" + string(rune('0'+n))
	c := win(addr, "com.mitchellh.ghostty", 2000+n, 1)
	c.Title = title
	r.wm.cs = append(r.wm.cs, c)
	sid := "hand-" + peer
	r.sess = append(r.sess, Session{ID: sid, PID: 200 + n, Peer: peer})
	chain := r.sw.Ancestors
	r.sw.Ancestors = func(pid int) []procs.Proc {
		if pid == 200+n {
			return []procs.Proc{{PID: 200 + n}, {PID: 2000 + n}}
		}
		return chain(pid)
	}
	return addr
}

// TestAnUnmarkedAgentIsHiddenByItsTitle: a hide finds the window of an
// agent Den has no marker for by the exact title Den gave it, hides it, and
// a show puts it back where it was. The record says it was found by title.
func TestAnUnmarkedAgentIsHiddenByItsTitle(t *testing.T) {
	r := newRig(t, 1)
	addr := r.handAgent(1, "old-agent", hypr.AgentTitle("old-agent"))
	res := r.must(User)
	if res != (Result{View: User, Shown: 0, Total: 3}) {
		t.Fatalf("hide: %+v", res)
	}
	if w := r.wm.where(addr); w != Workspace {
		t.Fatalf("the titled window is on %q", w)
	}
	st, _ := loadState(r.sw.path())
	var titled int
	for _, m := range st.Moved {
		if m.Titled {
			titled++
			if m.Address != addr || m.From != "1" {
				t.Errorf("record %+v", m)
			}
		}
	}
	if titled != 1 {
		t.Errorf("%d titled records in %+v", titled, st.Moved)
	}
	res = r.must(Agent)
	if res != (Result{View: Agent, Shown: 3, Total: 3}) {
		t.Fatalf("show: %+v", res)
	}
	if w := r.wm.where(addr); w != "1" {
		t.Errorf("the titled window came back to %q, want 1", w)
	}
}

// TestATitleAloneProvesNothing: a window with the right title that the
// agent does not run under, two windows with it, a title with a different
// peer, and a session with no peer name are not the agent's, and are not
// moved.
func TestATitleAloneProvesNothing(t *testing.T) {
	for name, set := range map[string]func(r *rig){
		"the agent does not run under that terminal": func(r *rig) {
			r.handAgent(1, "old-agent", hypr.AgentTitle("old-agent"))
			chain := r.sw.Ancestors
			r.sw.Ancestors = func(pid int) []procs.Proc {
				if pid == 201 {
					return []procs.Proc{{PID: 201}, {PID: 7777}}
				}
				return chain(pid)
			}
		},
		"two windows carry the title": func(r *rig) {
			r.handAgent(1, "old-agent", hypr.AgentTitle("old-agent"))
			c := win("0xtwin", "com.mitchellh.ghostty", 3001, 1)
			c.Title = hypr.AgentTitle("old-agent")
			r.wm.cs = append(r.wm.cs, c)
		},
		"its terminal has another window": func(r *rig) {
			r.handAgent(1, "old-agent", hypr.AgentTitle("old-agent"))
			r.wm.cs = append(r.wm.cs, win("0xsib", "com.mitchellh.ghostty", 2001, 1))
		},
		"the title is another peer's": func(r *rig) {
			r.handAgent(1, "old-agent", hypr.AgentTitle("someone-else"))
		},
		"the session has no peer name": func(r *rig) {
			r.handAgent(1, "old-agent", hypr.AgentTitle("old-agent"))
			r.sess[len(r.sess)-1].Peer = ""
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, 1)
			set(r)
			res := r.must(User)
			if res.Total != 2 || res.Shown != 0 {
				t.Fatalf("hide: %+v", res)
			}
			if res.Reason != "1 not Den's" {
				t.Errorf("reason %q", res.Reason)
			}
			for _, m := range r.wm.moves() {
				if strings.Contains(m, "0xh") || strings.Contains(m, "0xtwin") || strings.Contains(m, "0xsib") {
					t.Errorf("moved a window that is not the agent's: %s", m)
				}
			}
		})
	}
}

// TestAShowNeverFindsAWindowByTitle: a window with an agent's title that sits
// on special:den, with no record that Den put it there, is not moved by a
// show.
func TestAShowNeverFindsAWindowByTitle(t *testing.T) {
	r := newRig(t, 1)
	addr := r.handAgent(1, "old-agent", hypr.AgentTitle("old-agent"))
	r.must(User)
	// The record is lost; the window stays hidden.
	st, _ := loadState(r.sw.path())
	var kept []Moved
	for _, m := range st.Moved {
		if !m.Titled {
			kept = append(kept, m)
		}
	}
	st.Moved = kept
	if err := saveState(r.sw.path(), st); err != nil {
		t.Fatal(err)
	}
	r.must(Agent)
	if w := r.wm.where(addr); w != Workspace {
		t.Errorf("a show moved the titled window with no record: now on %q", w)
	}
}

// TestAPromptingAgentFoundByTitleStays: the hold on a window whose
// terminal waits on a prompt applies to one found by title too.
func TestAPromptingAgentFoundByTitleStays(t *testing.T) {
	r := newRig(t, 1)
	addr := r.handAgent(1, "old-agent", hypr.AgentTitle("old-agent"))
	rq := r.req(User)
	rq.Hold = []string{"hand-old-agent"}
	res, err := r.sw.Switch(context.Background(), rq)
	if err != nil {
		t.Fatal(err)
	}
	if w := r.wm.where(addr); w == Workspace {
		t.Errorf("a prompting agent's window was hidden")
	}
	if res.Shown != 1 || !strings.Contains(res.Reason, "1 waits on a prompt") {
		t.Errorf("%+v", res)
	}
}

// TestAHideThatCanHideNoAgentIsRefused: agents run but none has a window Den
// can prove is its own. Den does not enter User view: nothing moves, Den's
// own window included, and the view stays.
func TestAHideThatCanHideNoAgentIsRefused(t *testing.T) {
	r := newRig(t, 0)
	r.handAgent(1, "old-agent", "~")
	r.handAgent(2, "other-old", "~")
	_, err := r.sw.Switch(context.Background(), r.req(User))
	var refused *NoAgentWindows
	if !errors.As(err, &refused) || refused.N != 2 {
		t.Fatalf("hide: %v", err)
	}
	if !strings.Contains(err.Error(), "2 agents") {
		t.Errorf("message %q", err)
	}
	if len(r.wm.moves()) != 0 {
		t.Errorf("moved %v", r.wm.moves())
	}
	if got := r.sw.Current(); got != Agent {
		t.Errorf("the view after a refused hide is %q", got)
	}
	if w := r.wm.where("0xden"); w == Workspace {
		t.Error("Den's own window was hidden by a refused hide")
	}
	// A show is still fine.
	if res := r.must(Agent); res.View != Agent {
		t.Errorf("show: %+v", res)
	}
}

// TestAHideThatHidesSomeSaysHowManyAreNotDens: one recognised agent among
// others Den cannot tie: the hide goes through and the reason counts them.
func TestAHideThatHidesSomeSaysHowManyAreNotDens(t *testing.T) {
	r := newRig(t, 1)
	r.handAgent(1, "old-agent", "~")
	res := r.must(User)
	if res.Total != 2 || res.Shown != 0 || res.Reason != "1 not Den's" {
		t.Fatalf("hide: %+v", res)
	}
	if got := r.sw.Current(); got != User {
		t.Errorf("view %q", got)
	}
}

// TestNoAgentsAtAllStillHidesDensWindow: nothing running is not a refusal.
func TestNoAgentsAtAllStillHidesDensWindow(t *testing.T) {
	r := newRig(t, 0)
	res := r.must(User)
	if res.Total != 1 || res.Shown != 0 {
		t.Fatalf("hide: %+v", res)
	}
}
