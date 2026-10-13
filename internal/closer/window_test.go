package closer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// winOps is fakeOps with windows: each window is run by a terminal process
// (pid) above the agent's. A window whose agent ends goes with it unless
// it is held open, as a terminal that waits after its command would.
// Every listing and every close is recorded in calls; no real hyprctl.
type winOps struct {
	*fakeOps
	ws      []winRec
	focused string
}

type winRec struct {
	w      Window
	agent  int  // the agent pid inside, 0 for none
	holdOn bool // stays open after the agent ends
}

func (o *winOps) Windows() ([]Window, error) {
	o.record("windows")
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []Window
	for _, r := range o.ws {
		if r.agent != 0 && !o.alive[r.agent] && !r.holdOn {
			continue // closed with its process
		}
		out = append(out, r.w)
	}
	return out, nil
}

func (o *winOps) Focused() string { return o.focused }

func (o *winOps) CloseWindow(a string) error {
	o.record("close " + a)
	o.mu.Lock()
	defer o.mu.Unlock()
	for i, r := range o.ws {
		if r.w.Address == a {
			o.ws = append(o.ws[:i], o.ws[i+1:]...)
			return nil
		}
	}
	return errors.New("hyprland: window not found")
}

func (o *winOps) closes() []string {
	var out []string
	for _, c := range o.calls {
		if strings.HasPrefix(c, "close ") {
			out = append(out, c)
		}
	}
	return out
}

// terminal is the pid of the terminal process above agent pid p.
func terminal(p int) int { return 500 + p }

func ancestorsOf(p int) []int { return []int{terminal(p), 1} }

const (
	mkA = "io.glacierhub.den.agent-a"
	mkB = "io.glacierhub.den.agent-b"
)

func targetsAB() []Target {
	return []Target{{Name: "A", Peer: "agent-a", PID: 1, Status: "idle"}, {Name: "B", Peer: "agent-b", PID: 2, Status: "idle"}}
}

// TestLocateByMarkerOnly: a window is tied to an agent only by the marker
// Den gave it, held by a process above the agent's. Lookalikes by title or
// working directory, two windows with one marker, or a marked window run
// by someone else are never tied.
func TestLocateByMarkerOnly(t *testing.T) {
	plain := "com.mitchellh.ghostty"
	for _, c := range []struct {
		name   string
		ws     []Window
		window string
		note   string
	}{
		{"marker match", []Window{{"0xa", mkA, terminal(1)}}, "0xa", ""},
		{"no marked window", []Window{{"0xa", plain, terminal(1)}}, "", "no window marked for it"},
		{"two marked windows", []Window{{"0xa", mkA, terminal(1)}, {"0xb", mkA, terminal(1)}}, "", "2 windows carry its marker"},
		{"marked but not its terminal", []Window{{"0xa", mkA, 777}}, "", "does not run its process"},
		{"another agent's marker", []Window{{"0xb", mkB, terminal(1)}}, "", "no window marked for it"},
	} {
		p := Plan{Targets: []Target{{Name: "A", Peer: "agent-a", PID: 1}}}
		Locate(&p, c.ws, ancestorsOf)
		got := p.Targets[0]
		if got.Window != c.window || !strings.Contains(got.WindowNote, c.note) {
			t.Errorf("%s: window %q note %q", c.name, got.Window, got.WindowNote)
		}
	}
}

// TestWindowClosesWithItsProcess: the normal way. Den signals the agent,
// its terminal closes by itself, and Den closes nothing.
func TestWindowClosesWithItsProcess(t *testing.T) {
	ops := &winOps{fakeOps: &fakeOps{alive: map[int]bool{1: true}}, ws: []winRec{{w: Window{"0xa", mkA, terminal(1)}, agent: 1}}}
	fastWaits(t, ops.fakeOps)
	p := Plan{Targets: targetsAB()[:1]}
	ws, _ := ops.Windows()
	Locate(&p, ws, ancestorsOf)
	res := Run(context.Background(), p, Self{}, ops)
	if !res[0].Ended || res[0].WindowClosed || res[0].WindowNote != "" || len(ops.closes()) != 0 {
		t.Errorf("result %+v, closes %v", res[0], ops.closes())
	}
}

// TestLeftOpenWindowClosesOnce: a window still open after its agent ended,
// and still exactly the one the plan showed, is closed by Den, once.
func TestLeftOpenWindowClosesOnce(t *testing.T) {
	ops := &winOps{fakeOps: &fakeOps{alive: map[int]bool{1: true}}, ws: []winRec{{w: Window{"0xa", mkA, terminal(1)}, agent: 1, holdOn: true}}}
	fastWaits(t, ops.fakeOps)
	p := Plan{Targets: targetsAB()[:1]}
	ws, _ := ops.Windows()
	Locate(&p, ws, ancestorsOf)
	res := Run(context.Background(), p, Self{}, ops)
	if !res[0].WindowClosed || strings.Join(ops.closes(), ",") != "close 0xa" {
		t.Errorf("result %+v, closes %v", res[0], ops.closes())
	}
	if s := Summary(res); s != "closed A · Den closed 1 window(s) left open" {
		t.Errorf("summary %q", s)
	}
}

// TestNeverClosesTheWrongWindow: a left-open window is not closed when it
// is in front, is Den's own, or has changed since the plan (moved, a
// second window took the marker, another process runs it); the human is
// told instead, and no close is tried.
func TestNeverClosesTheWrongWindow(t *testing.T) {
	for _, c := range []struct {
		name    string
		focused string
		self    string
		change  func(o *winOps)
		note    string
	}{
		{"in front", "0xa", "", nil, "does not close the window in front"},
		{"Den's own", "", "0xa", nil, "or its own"},
		{"another window took the marker", "", "", func(o *winOps) {
			o.ws = append(o.ws, winRec{w: Window{"0xz", mkA, 900}})
		}, "no longer the one the plan showed"},
		{"a new window at the marker", "", "", func(o *winOps) {
			o.ws[0].w.Address = "0xnew"
		}, "no longer the one the plan showed"},
		{"another process runs it", "", "", func(o *winOps) {
			o.ws[0].w.PID = 901
		}, "no longer the one the plan showed"},
	} {
		ops := &winOps{fakeOps: &fakeOps{alive: map[int]bool{1: true}}, ws: []winRec{{w: Window{"0xa", mkA, terminal(1)}, agent: 1, holdOn: true}}, focused: c.focused}
		fastWaits(t, ops.fakeOps)
		p := Plan{Targets: targetsAB()[:1]}
		ws, _ := ops.Windows()
		Locate(&p, ws, ancestorsOf)
		if c.change != nil {
			c.change(ops)
		}
		res := Run(context.Background(), p, Self{Window: c.self}, ops)
		if res[0].WindowClosed || len(ops.closes()) != 0 || !strings.Contains(res[0].WindowNote, c.note) {
			t.Errorf("%s: result %+v, closes %v", c.name, res[0], ops.closes())
		}
		if s := Summary(res); !strings.Contains(s, "A: its window") {
			t.Errorf("%s: summary should tell the human: %q", c.name, s)
		}
	}
}

// TestLookalikeWindowsAreNeverClosed: windows with the agent's title or in
// its directory but without its marker are never touched, whatever happens.
func TestLookalikeWindowsAreNeverClosed(t *testing.T) {
	ops := &winOps{fakeOps: &fakeOps{alive: map[int]bool{1: true, 2: true}}, ws: []winRec{
		{w: Window{"0xorch", "com.mitchellh.ghostty", terminal(1)}, holdOn: true}, // same title, same cwd
		{w: Window{"0xden", "com.mitchellh.ghostty", terminal(2)}, holdOn: true},
	}}
	fastWaits(t, ops.fakeOps)
	p := Plan{Force: true, Targets: targetsAB()}
	ws, _ := ops.Windows()
	Locate(&p, ws, ancestorsOf)
	Run(context.Background(), p, Self{}, ops)
	if len(ops.closes()) != 0 {
		t.Errorf("closed %v", ops.closes())
	}
}

// TestRunIsSerialisedAndReresolves: targets go one at a time with a pause
// between, and each left-open window is looked up afresh just before it
// is closed.
func TestRunIsSerialisedAndReresolves(t *testing.T) {
	ops := &winOps{fakeOps: &fakeOps{alive: map[int]bool{1: true, 2: true}}, ws: []winRec{
		{w: Window{"0xa", mkA, terminal(1)}, agent: 1, holdOn: true},
		{w: Window{"0xb", mkB, terminal(2)}, agent: 2, holdOn: true},
	}}
	fastWaits(t, ops.fakeOps)
	WindowWait = 0 // one listing per target, to keep the order exact
	p := Plan{Targets: targetsAB()}
	ws, _ := ops.Windows()
	Locate(&p, ws, ancestorsOf)
	ops.calls = nil
	Run(context.Background(), p, Self{}, ops)
	want := "terminated:1 | windows | close 0xa | pause 500ms | terminated:2 | windows | close 0xb"
	if got := strings.Join(ops.calls, " | "); got != want {
		t.Errorf("calls\n got %s\nwant %s", got, want)
	}
}

// TestRunsNeverOverlap: two plans approved together take turns.
func TestRunsNeverOverlap(t *testing.T) {
	var inFlight, most atomic.Int32
	ops := &countOps{fakeOps: &fakeOps{alive: map[int]bool{}}, in: &inFlight, most: &most}
	fastWaits(t, ops.fakeOps)
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pid := 10 + i
			ops.mu.Lock()
			ops.alive[pid] = true
			ops.mu.Unlock()
			Run(context.Background(), Plan{Targets: []Target{{Name: fmt.Sprint(pid), PID: pid}}}, Self{}, ops)
		}()
	}
	wg.Wait()
	if most.Load() != 1 {
		t.Errorf("%d closes ran at once", most.Load())
	}
}

type countOps struct {
	*fakeOps
	in, most *atomic.Int32
}

func (c *countOps) Signal(pid int, sig syscall.Signal) error {
	n := c.in.Add(1)
	for {
		m := c.most.Load()
		if n <= m || c.most.CompareAndSwap(m, n) {
			break
		}
	}
	time.Sleep(5 * time.Millisecond)
	defer c.in.Add(-1)
	return c.fakeOps.Signal(pid, sig)
}

// TestTheTerminalIsNeverSignalled: only the agent's own pid gets a signal,
// even forced and with its window held open; the terminal process above it
// never does.
func TestTheTerminalIsNeverSignalled(t *testing.T) {
	ops := &winOps{fakeOps: &fakeOps{alive: map[int]bool{1: true, 2: true, terminal(1): true, terminal(2): true}, stubborn: map[int]bool{2: true}}, ws: []winRec{
		{w: Window{"0xa", mkA, terminal(1)}, agent: 1, holdOn: true},
		{w: Window{"0xb", mkB, terminal(2)}, agent: 2},
	}}
	fastWaits(t, ops.fakeOps)
	p := Plan{Force: true, Targets: targetsAB()}
	ws, _ := ops.Windows()
	Locate(&p, ws, ancestorsOf)
	Run(context.Background(), p, Self{}, ops)
	for _, s := range ops.signals {
		if !strings.HasSuffix(s, ":1") && !strings.HasSuffix(s, ":2") {
			t.Errorf("signalled %s", s)
		}
	}
	if !ops.alive[terminal(1)] || !ops.alive[terminal(2)] {
		t.Error("a terminal process was ended")
	}
}

// fakeVerifier holds only the pids it knows to be the agents' own; any
// other target fails the check, as System's pidfd and identity check does.
type fakeVerifier struct {
	*winOps
	verified map[int]bool
}

type fakeHeld struct {
	ops *fakeOps
	pid int
}

func (h fakeHeld) Signal(sig syscall.Signal) error { return h.ops.Signal(h.pid, sig) }
func (h fakeHeld) Alive() bool                     { return h.ops.Alive(h.pid) }
func (h fakeHeld) Release()                        {}

func (v fakeVerifier) Hold(t Target) (Held, error) {
	if !v.verified[t.PID] {
		return nil, errors.New("not Claude Code")
	}
	return fakeHeld{v.fakeOps, t.PID}, nil
}

// TestUnverifiedTargetGetsNothing: a target whose process fails the check
// is neither signalled nor has its window closed, even forced.
func TestUnverifiedTargetGetsNothing(t *testing.T) {
	ops := &winOps{fakeOps: &fakeOps{alive: map[int]bool{1: true, 2: true}}, ws: []winRec{
		{w: Window{"0xa", mkA, terminal(1)}, agent: 1, holdOn: true},
		{w: Window{"0xb", mkB, terminal(2)}, agent: 2, holdOn: true},
	}}
	fastWaits(t, ops.fakeOps)
	v := fakeVerifier{ops, map[int]bool{1: true}}
	p := Plan{Force: true, Targets: targetsAB()}
	ws, _ := ops.Windows()
	Locate(&p, ws, ancestorsOf)
	res := Run(context.Background(), p, Self{}, v)
	if strings.Join(ops.signals, " ") != "terminated:1" || strings.Join(ops.closes(), ",") != "close 0xa" {
		t.Errorf("signals %v closes %v", ops.signals, ops.closes())
	}
	if res[1].Err == nil || res[1].Ended {
		t.Errorf("unverified B: %+v", res[1])
	}
}

// TestOwnWindow: Den's window is the single window of a process above it;
// a terminal process holding several windows cannot say which.
func TestOwnWindow(t *testing.T) {
	ws := []Window{{"0xden", "com.mitchellh.ghostty", 40}, {"0xother", "x", 41}}
	if got := OwnWindow(ws, []int{40, 1}); got != "0xden" {
		t.Errorf("own window %q", got)
	}
	ws = append(ws, Window{"0xsibling", "com.mitchellh.ghostty", 40})
	if got := OwnWindow(ws, []int{40, 1}); got != "" {
		t.Errorf("a shared terminal process gave %q", got)
	}
}
