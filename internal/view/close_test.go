package view

import (
	"context"
	"errors"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/closer"
	"github.com/GlacierHubAB/den/internal/hypr"
)

// closeRig closes through the same fake window manager the switch moves
// windows on: the agent's process ends at SIGTERM and its terminal stays
// open, as one held open after its command would.
type closeRig struct {
	*fakeWM
	mu    sync.Mutex
	alive map[int]bool
}

func (c *closeRig) Signal(pid int, sig syscall.Signal) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.alive, pid)
	return nil
}

func (c *closeRig) Alive(pid int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.alive[pid]
}

func (c *closeRig) Windows() ([]closer.Window, error) {
	cs, _ := c.fakeWM.Clients(context.Background())
	var out []closer.Window
	for _, w := range cs {
		out = append(out, closer.Window{Address: w.Address, Class: w.InitialClass, PID: w.PID})
	}
	return out, nil
}

func (c *closeRig) Focused() string { return "" }
func (c *closeRig) Busy(int) bool   { return false }
func (c *closeRig) CloseWindow(a string) error {
	c.fakeWM.mu.Lock()
	c.fakeWM.log = append(c.fakeWM.log, "close "+a)
	c.fakeWM.mu.Unlock()
	c.fakeWM.remove(a)
	return nil
}

func (r *rig) closeAgent1(t *testing.T) (*closeRig, closer.Plan) {
	t.Helper()
	cr := &closeRig{fakeWM: r.wm, alive: map[int]bool{101: true}}
	p := closer.Plan{Targets: []closer.Target{{Name: "A", Peer: "agent-1", PID: 101, Status: "idle", Marker: marker(1)}}}
	ws, _ := cr.Windows()
	closer.Locate(&p, ws, func(pid int) []int {
		for _, pr := range r.sw.Ancestors(pid) {
			if pr.PID == 1001 {
				return []int{pid, 1001, 1}
			}
		}
		return []int{pid, 1}
	})
	if p.Targets[0].Window != "0xa1" {
		t.Fatalf("the window was not tied: %+v", p.Targets[0])
	}
	return cr, p
}

func fastCloser(t *testing.T) {
	t.Helper()
	was := [4]time.Duration{closer.StopWait, closer.KillWait, closer.Between, closer.WindowWait}
	closer.StopWait, closer.KillWait, closer.Between, closer.WindowWait = 200*time.Millisecond, 200*time.Millisecond, time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() {
		closer.StopWait, closer.KillWait, closer.Between, closer.WindowWait = was[0], was[1], was[2], was[3]
	})
}

// TestCloseWorksOnAHiddenWindow: closing an agent whose window is hidden on
// special:den closes that window, by address, and touches no other.
func TestCloseWorksOnAHiddenWindow(t *testing.T) {
	fastCloser(t)
	r := newRig(t, 3)
	r.must(User)
	cr, p := r.closeAgent1(t)
	res := closer.Run(context.Background(), p, closer.Self{}, cr)
	if !res[0].Ended || !res[0].WindowClosed {
		t.Fatalf("result %+v", res[0])
	}
	if r.wm.where("0xa1") != "" {
		t.Error("the hidden window is still open")
	}
	for _, a := range []string{"0xa2", "0xa3"} {
		if r.wm.where(a) != Workspace {
			t.Errorf("%s is on %q", a, r.wm.where(a))
		}
	}
	// The show afterwards counts what is left: two agents and Den's own.
	if res := r.must(Agent); res.Total != 3 || res.Shown != 3 {
		t.Errorf("show after the close: %+v", res)
	}
}

// TestCloseDuringAHideNeverOverlaps: a close that reaches its window while
// a hide is moving windows waits for it, then closes the window; and a
// switch asked for while the close holds the lock answers busy.
func TestCloseDuringAHideNeverOverlaps(t *testing.T) {
	fastCloser(t)
	r := newRig(t, 3)
	cr, p := r.closeAgent1(t)
	r.wm.hang = make(chan struct{})
	r.wm.entered = make(chan struct{}, 1)

	hid := make(chan error, 1)
	go func() {
		_, err := r.sw.Switch(context.Background(), r.req(User))
		hid <- err
	}()
	select {
	case <-r.wm.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the hide never started")
	}
	closed := make(chan []closer.Result, 1)
	go func() { closed <- closer.Run(context.Background(), p, closer.Self{}, cr) }()
	select {
	case <-closed:
		t.Fatal("the close ran while the hide held the lock")
	case <-time.After(300 * time.Millisecond):
	}
	for _, l := range r.wm.logs() {
		if strings.HasPrefix(l, "close ") {
			t.Fatalf("closed a window during the hide: %v", l)
		}
	}
	close(r.wm.hang)
	if err := <-hid; err != nil {
		t.Fatal(err)
	}
	select {
	case res := <-closed:
		if !res[0].WindowClosed {
			t.Errorf("result %+v", res[0])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the close never finished")
	}
	if r.wm.where("0xa1") != "" {
		t.Error("the window is still open")
	}

	// And the other way round: while a close holds the lock, a switch is
	// answered busy at once.
	unlock := hypr.LockWindows()
	defer unlock()
	if _, err := r.sw.Switch(context.Background(), r.req(User)); !errors.Is(err, ErrBusy) {
		t.Errorf("switch during a close: %v", err)
	}
}
