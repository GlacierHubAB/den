package view

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/hypr"
	"github.com/GlacierHubAB/den/internal/procs"
	"github.com/GlacierHubAB/den/internal/winmark"
)

// fakeWM is the window manager: a list of windows that Move changes, and a
// log of every call. No real hyprctl.
type fakeWM struct {
	mu      sync.Mutex
	cs      []hypr.Client
	log     []string
	active  hypr.Workspace
	noMove  map[string]bool // addresses that will not move
	hang    chan struct{}   // Move blocks until it is closed or ctx ends
	entered chan struct{}   // told when a Move starts to block
	listErr error
}

func (f *fakeWM) Available() bool { return true }

func (f *fakeWM) Clients(ctx context.Context) ([]hypr.Client, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, "clients")
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]hypr.Client(nil), f.cs...), nil
}

func (f *fakeWM) Move(ctx context.Context, addr, ws string) error {
	if f.hang != nil {
		if f.entered != nil {
			select {
			case f.entered <- struct{}{}:
			default:
			}
		}
		select {
		case <-f.hang:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, "move "+addr+" "+ws)
	if f.noMove[addr] {
		return errors.New("will not move")
	}
	for i := range f.cs {
		if f.cs[i].Address == addr {
			if strings.HasPrefix(ws, "special:") {
				f.cs[i].Workspace.ID, f.cs[i].Workspace.Name = -99, ws
			} else {
				id, _ := strconv.Atoi(ws)
				f.cs[i].Workspace.ID, f.cs[i].Workspace.Name = id, ws
			}
			return nil
		}
	}
	return errors.New("hyprland: window not found")
}

func (f *fakeWM) Active(ctx context.Context) (hypr.Workspace, bool) {
	return f.active, f.active.ID != 0
}

func (f *fakeWM) moves() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, l := range f.log {
		if strings.HasPrefix(l, "move ") {
			out = append(out, l)
		}
	}
	return out
}

func (f *fakeWM) remove(addr string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.cs {
		if f.cs[i].Address == addr {
			f.cs = append(f.cs[:i], f.cs[i+1:]...)
			return
		}
	}
}

func (f *fakeWM) where(addr string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.cs {
		if c.Address == addr {
			return c.Workspace.Name
		}
	}
	return ""
}

func win(addr, class string, pid, ws int) hypr.Client {
	c := hypr.Client{Address: addr, PID: pid, Class: class, InitialClass: class}
	c.Workspace.ID, c.Workspace.Name = ws, strconv.Itoa(ws)
	return c
}

// rig is Den with agents a1..an: each Den-spawned agent has a recorded
// marker bound to its session, a terminal process (1000+i) and a window of
// its own on workspace 1 or 2. Den's own window (0xden) is known too, so
// every count is n + 1.
type rig struct {
	t    *testing.T
	wm   *fakeWM
	sw   *Switcher
	sess []Session
	own  *hypr.Client
}

func marker(i int) string { return fmt.Sprintf("io.glacierhub.den.agent-%d", i) }

func newRig(t *testing.T, n int) *rig {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	r := &rig{t: t, wm: &fakeWM{active: hypr.Workspace{ID: 1}, noMove: map[string]bool{}}}
	chains := map[int][]procs.Proc{}
	for i := 1; i <= n; i++ {
		peer := fmt.Sprintf("agent-%d", i)
		sid := fmt.Sprintf("sess-%d", i)
		if err := winmark.Record(peer, marker(i)); err != nil {
			t.Fatal(err)
		}
		if ok, err := winmark.Bind(peer, sid, func(string) bool { return true }); !ok || err != nil {
			t.Fatalf("bind %v %v", ok, err)
		}
		pid := 100 + i
		chains[pid] = []procs.Proc{{PID: pid, Comm: "claude"}, {PID: 1000 + i, Comm: "ghostty"}}
		ws := 1 + i%2
		r.wm.cs = append(r.wm.cs, win(fmt.Sprintf("0xa%d", i), marker(i), 1000+i, ws))
		r.sess = append(r.sess, Session{ID: sid, PID: pid})
	}
	own := win("0xden", "com.mitchellh.ghostty", 900, 1)
	r.wm.cs = append(r.wm.cs, own)
	r.own = &own
	r.sw = &Switcher{
		Ops:       r.wm,
		Marker:    winmark.For,
		Ancestors: func(pid int) []procs.Proc { return chains[pid] },
		Own: func(all []hypr.Client) (hypr.Client, bool) {
			if r.own == nil {
				return hypr.Client{}, false
			}
			for _, c := range all {
				if c.Address == r.own.Address {
					return c, true
				}
			}
			return hypr.Client{}, false
		},
		StatePath: filepath.Join(dir, "den", "view.json"),
		Now:       time.Now,
	}
	return r
}

func (r *rig) req(want string) Request { return Request{Want: want, Sessions: r.sess} }

func (r *rig) must(want string) Result {
	r.t.Helper()
	res, err := r.sw.Switch(context.Background(), r.req(want))
	if err != nil {
		r.t.Fatalf("%s: %v", want, err)
	}
	return res
}

// TestHideThenShowKeepsEveryWindow: with N sessions, hide then show leaves
// exactly N windows, each still carrying its own marker, one session each,
// back on the workspace it came from, and none of them was ever closed.
func TestHideThenShowKeepsEveryWindow(t *testing.T) {
	const n = 5
	r := newRig(t, n)
	before := map[string]int{}
	for _, c := range r.wm.cs {
		before[c.Address] = c.Workspace.ID
	}
	res := r.must(User)
	if res != (Result{View: User, Shown: 0, Total: n + 1}) {
		t.Fatalf("hide: %+v", res)
	}
	for _, c := range r.wm.cs {
		if c.Workspace.Name != Workspace {
			t.Errorf("%s is on %q after a hide", c.Address, c.Workspace.Name)
		}
	}
	if got := r.sw.Current(); got != User {
		t.Errorf("view after hide = %q", got)
	}
	res = r.must(Agent)
	if res != (Result{View: Agent, Shown: n + 1, Total: n + 1}) {
		t.Fatalf("show: %+v", res)
	}
	if len(r.wm.cs) != n+1 {
		t.Fatalf("%d windows after hide and show, want %d", len(r.wm.cs), n+1)
	}
	seen := map[string]bool{}
	for _, c := range r.wm.cs {
		if c.Workspace.ID != before[c.Address] {
			t.Errorf("%s came back to workspace %d, was on %d", c.Address, c.Workspace.ID, before[c.Address])
		}
		if seen[c.InitialClass] {
			t.Errorf("two windows carry %s", c.InitialClass)
		}
		seen[c.InitialClass] = true
	}
	for i := 1; i <= n; i++ {
		if !seen[marker(i)] {
			t.Errorf("no window carries %s", marker(i))
		}
	}
	for _, m := range r.wm.moves() {
		if !strings.HasPrefix(m, "move 0xa") && !strings.HasPrefix(m, "move 0xden") {
			t.Errorf("unexpected call %q", m)
		}
	}
	if r.sw.Current() != Agent {
		t.Errorf("view after show = %q", r.sw.Current())
	}
}

// TestSwitchIsIdempotent: asking for the view Den is in moves nothing,
// and says the same thing again.
func TestSwitchIsIdempotent(t *testing.T) {
	r := newRig(t, 3)
	first := r.must(User)
	n := len(r.wm.moves())
	if again := r.must(User); again != first {
		t.Errorf("second hide %+v, first %+v", again, first)
	}
	if len(r.wm.moves()) != n {
		t.Errorf("a second hide moved windows: %v", r.wm.moves()[n:])
	}
	r.must(Agent)
	n = len(r.wm.moves())
	if res := r.must(Agent); res.Shown != 4 || res.Total != 4 || res.Reason != "" {
		t.Errorf("second show %+v", res)
	}
	if len(r.wm.moves()) != n {
		t.Errorf("a second show moved windows: %v", r.wm.moves()[n:])
	}
	// Showing when nothing was ever hidden is a no-op too.
	r2 := newRig(t, 2)
	if res := r2.must(Agent); res != (Result{View: Agent, Shown: 3, Total: 3}) || len(r2.wm.moves()) != 0 {
		t.Errorf("show from the start: %+v, moves %v", res, r2.wm.moves())
	}
}

// TestHideCatchesAWindowThatCameUp: the same verb again hides an agent's
// window that appeared since, and touches no other.
func TestHideCatchesAWindowThatCameUp(t *testing.T) {
	r := newRig(t, 2)
	r.must(User)
	// A third agent, spawned in the meantime.
	if err := winmark.Record("agent-3", marker(3)); err != nil {
		t.Fatal(err)
	}
	winmark.Bind("agent-3", "sess-3", func(string) bool { return true })
	r.wm.cs = append(r.wm.cs, win("0xa3", marker(3), 1003, 3))
	chain := r.sw.Ancestors
	r.sw.Ancestors = func(pid int) []procs.Proc {
		if pid == 103 {
			return []procs.Proc{{PID: 103}, {PID: 1003}}
		}
		return chain(pid)
	}
	r.sess = append(r.sess, Session{ID: "sess-3", PID: 103})
	n := len(r.wm.moves())
	res := r.must(User)
	if res != (Result{View: User, Shown: 0, Total: 4}) {
		t.Fatalf("%+v", res)
	}
	if got := r.wm.moves()[n:]; len(got) != 1 || got[0] != "move 0xa3 "+Workspace {
		t.Errorf("moves after the new agent: %v", got)
	}
}

// TestHandStartedAgentsAreNotTouched: an agent Den did not spawn has no
// marker record, so its window, marked or not, is not counted and not
// moved. A window with a class that merely looks like Den's is the same.
func TestHandStartedAgentsAreNotTouched(t *testing.T) {
	r := newRig(t, 2)
	// A hand-started agent in a plain window, and one whose terminal
	// happens to carry a Den-looking class that Den never recorded.
	r.wm.cs = append(r.wm.cs, win("0xh1", "com.mitchellh.ghostty", 2001, 1), win("0xh2", "io.glacierhub.den.helper", 2002, 1))
	r.sess = append(r.sess, Session{ID: "hand-1", PID: 201}, Session{ID: "hand-2", PID: 202})
	chain := r.sw.Ancestors
	r.sw.Ancestors = func(pid int) []procs.Proc {
		switch pid {
		case 201:
			return []procs.Proc{{PID: 201}, {PID: 2001}}
		case 202:
			return []procs.Proc{{PID: 202}, {PID: 2002}}
		}
		return chain(pid)
	}
	res := r.must(User)
	if res.Total != 3 || res.Shown != 0 {
		t.Fatalf("hide: %+v", res)
	}
	for _, a := range []string{"0xh1", "0xh2"} {
		if w := r.wm.where(a); w != "1" {
			t.Errorf("%s moved to %q", a, w)
		}
	}
	for _, m := range r.wm.moves() {
		if strings.Contains(m, "0xh") {
			t.Errorf("touched a hand-started agent: %s", m)
		}
	}
	r.must(Agent)
	for _, m := range r.wm.moves() {
		if strings.Contains(m, "0xh") {
			t.Errorf("touched a hand-started agent: %s", m)
		}
	}
}

// TestWindowsDenCannotTieAreLeftAlone: two windows with one marker, or a
// marked window the agent does not run in, are counted as not Den's and
// moved never, whichever way the switch goes.
func TestWindowsDenCannotTieAreLeftAlone(t *testing.T) {
	r := newRig(t, 3)
	// agent-1 has a rival window with its class; agent-2's window is run by
	// someone else (its process is not under that terminal).
	r.wm.cs = append(r.wm.cs, win("0xr1", marker(1), 3001, 1))
	chain := r.sw.Ancestors
	r.sw.Ancestors = func(pid int) []procs.Proc {
		if pid == 102 {
			return []procs.Proc{{PID: 102}, {PID: 7777}}
		}
		return chain(pid)
	}
	res := r.must(User)
	if res.Total != 4 || res.Shown != 2 {
		t.Fatalf("hide: %+v", res)
	}
	if res.Reason != "2 of 4 not hidden; 2 not Den's" {
		t.Errorf("reason %q", res.Reason)
	}
	for _, a := range []string{"0xa1", "0xr1", "0xa2"} {
		if w := r.wm.where(a); w == Workspace {
			t.Errorf("%s was hidden", a)
		}
	}
	if w := r.wm.where("0xa3"); w != Workspace {
		t.Errorf("the one tied window is on %q", w)
	}
	res = r.must(Agent)
	if res.Total != 4 || res.Shown != 4 {
		t.Errorf("show: %+v", res)
	}
}

// TestPendingCardKeepsDensWindowUp: Den's own window is hidden with the
// rest, and never while a card is pending, and the reason says why.
func TestPendingCardKeepsDensWindowUp(t *testing.T) {
	r := newRig(t, 2)

	rq := r.req(User)
	rq.Keep = "a card waits"
	res, err := r.sw.Switch(context.Background(), rq)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 3 || res.Shown != 1 {
		t.Fatalf("hide with a card: %+v", res)
	}
	if res.Reason != "1 of 3 not hidden" {
		t.Errorf("reason %q", res.Reason)
	}
	if w := r.wm.where("0xden"); w == Workspace {
		t.Fatal("Den's own window was hidden with a card pending")
	}
	// The card is answered: the same verb hides it too.
	res = r.must(User)
	if res != (Result{View: User, Shown: 0, Total: 3}) {
		t.Fatalf("hide without a card: %+v", res)
	}
	if w := r.wm.where("0xden"); w != Workspace {
		t.Fatalf("Den's own window is on %q", w)
	}
	res = r.must(Agent)
	if res != (Result{View: Agent, Shown: 3, Total: 3}) || r.wm.where("0xden") != "1" {
		t.Fatalf("show: %+v, Den's window on %q", res, r.wm.where("0xden"))
	}
}

// TestOwnWindowOnlyWhenKnown: when Den cannot tell which window is its own,
// it stays where it is and the reply says so; and a recorded window is only
// brought back when its address, process and class all still match.
func TestOwnWindowOnlyWhenKnown(t *testing.T) {
	r := newRig(t, 1)
	r.own = nil
	res := r.must(User)
	if res.Total != 1 || res.Shown != 0 || res.Reason != "Den's own window not found" {
		t.Fatalf("hide without an own window: %+v", res)
	}
	if r.wm.where("0xden") == Workspace {
		t.Fatal("hid a window Den could not tell was its own")
	}
	r.must(Agent)

	own := win("0xden", "com.mitchellh.ghostty", 900, 4)
	for i := range r.wm.cs {
		if r.wm.cs[i].Address == "0xden" {
			r.wm.cs[i] = own
		}
	}
	r.own = &own
	r.must(User)
	if r.wm.where("0xden") != Workspace {
		t.Fatal("own window not hidden")
	}
	// Den restarts and cannot tell its window any more; the record still
	// finds it, by all three.
	r.own = nil
	res = r.must(Agent)
	if res.Total != 2 || res.Shown != 2 || r.wm.where("0xden") != "4" {
		t.Fatalf("show after a restart: %+v, own on %q", res, r.wm.where("0xden"))
	}

	// The address is reused by a window of another process: not Den's.
	r.own = &own
	r.must(User)
	for i := range r.wm.cs {
		if r.wm.cs[i].Address == "0xden" {
			r.wm.cs[i].PID = 12345
		}
	}
	r.own = nil
	n := len(r.wm.moves())
	r.must(Agent)
	for _, m := range r.wm.moves()[n:] {
		if strings.Contains(m, "0xden") {
			t.Errorf("moved a window that is not Den's own any more: %s", m)
		}
	}
}

// TestPartialSwitchSaysSo: a window that will not move leaves shown above
// zero with a reason; a window that closed meanwhile is not counted.
func TestPartialSwitchSaysSo(t *testing.T) {
	r := newRig(t, 4)
	r.wm.noMove["0xa2"] = true
	res := r.must(User)
	if res != (Result{View: User, Shown: 1, Total: 5, Reason: "1 of 5 not hidden"}) {
		t.Fatalf("hide: %+v", res)
	}
	if got := len(res.Reason); got > ReasonMax {
		t.Errorf("reason is %d characters", got)
	}
	// agent-4's window closes while hidden; a later show counts three.
	r.wm.remove("0xa4")
	delete(r.wm.noMove, "0xa2")
	res = r.must(Agent)
	if res != (Result{View: Agent, Shown: 4, Total: 4}) {
		t.Fatalf("show after a close: %+v", res)
	}
	if r.wm.where("0xa4") != "" {
		t.Error("a closed window came back")
	}
}

// TestBusyAndNeverWaits: one switch at a time, and a close in progress
// holds the same lock; a second call answers busy at once.
func TestBusyAndNeverWaits(t *testing.T) {
	r := newRig(t, 2)
	unlock := hypr.LockWindows() // a close is running
	start := time.Now()
	_, err := r.sw.Switch(context.Background(), r.req(User))
	unlock()
	if !errors.Is(err, ErrBusy) || time.Since(start) > time.Second {
		t.Fatalf("with a close running: %v after %v", err, time.Since(start))
	}

	// Two switches at once: the second finds the first running.
	r.wm.hang = make(chan struct{})
	r.wm.entered = make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		_, err := r.sw.Switch(context.Background(), r.req(User))
		done <- err
	}()
	select {
	case <-r.wm.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first switch never started")
	}
	if _, err := r.sw.Switch(context.Background(), r.req(User)); !errors.Is(err, ErrBusy) {
		t.Errorf("second switch: %v", err)
	}
	close(r.wm.hang)
	if err := <-done; err != nil {
		t.Errorf("first switch: %v", err)
	}
}

// TestAWholeSwitchStaysWithinTheBudget: a window manager that does not
// answer cannot hold a switch, or the lock closes share, past the budget;
// the reply says what was done and the next call finishes it.
func TestAWholeSwitchStaysWithinTheBudget(t *testing.T) {
	r := newRig(t, 3)
	r.sw.Budget = 150 * time.Millisecond
	r.wm.hang = make(chan struct{})
	start := time.Now()
	res, err := r.sw.Switch(context.Background(), r.req(User))
	took := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if took > 2*time.Second {
		t.Errorf("took %v", took)
	}
	if res.Shown != 4 || res.Total != 4 || !strings.Contains(res.Reason, "ran out of time") {
		t.Fatalf("%+v", res)
	}
	if unlock, ok := hypr.TryLockWindows(); !ok {
		t.Fatal("the lock is still held")
	} else {
		unlock()
	}
	r.wm.hang = nil
	r.sw.Budget = 0
	if res := r.must(User); res != (Result{View: User, Shown: 0, Total: 4}) {
		t.Errorf("the next call: %+v", res)
	}
}

// TestRecordSurvivesARestart: the record is in a file, so another Switcher
// (a restarted Den) knows the view and brings the windows back.
func TestRecordSurvivesARestart(t *testing.T) {
	r := newRig(t, 3)
	r.must(User)
	raw, err := os.ReadFile(r.sw.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(r.sw.StatePath); fi.Mode().Perm() != 0o600 {
		t.Errorf("record mode %v", fi.Mode().Perm())
	}
	if !strings.Contains(string(raw), `"view":"user"`) || strings.Count(string(raw), `"marker"`) != 3 || strings.Count(string(raw), `"own":true`) != 1 {
		t.Errorf("record: %s", raw)
	}
	again := *r.sw
	if again.Current() != User {
		t.Errorf("a restarted Den sees %q", again.Current())
	}
	// Den has no live sessions yet after a restart: the record alone
	// brings every window back.
	res, err := again.Switch(context.Background(), Request{Want: Agent})
	if err != nil {
		t.Fatal(err)
	}
	if res != (Result{View: Agent, Shown: 4, Total: 4}) {
		t.Errorf("%+v", res)
	}
	var addrs []string
	for _, c := range r.wm.cs {
		if c.Workspace.Name == Workspace {
			addrs = append(addrs, c.Address)
		}
	}
	sort.Strings(addrs)
	if len(addrs) != 0 {
		t.Errorf("still hidden: %v", addrs)
	}
}

// TestShowWithoutARecordUsesTheActiveWorkspace: a window found on
// special:den that Den holds no origin for is brought to where the human
// is, never left stranded.
func TestShowWithoutARecordUsesTheActiveWorkspace(t *testing.T) {
	r := newRig(t, 1)
	r.wm.cs[0].Workspace.ID, r.wm.cs[0].Workspace.Name = -99, Workspace
	r.wm.active = hypr.Workspace{ID: 7}
	res := r.must(Agent)
	if res.Shown != 2 || res.Total != 2 || r.wm.where("0xa1") != "7" {
		t.Errorf("%+v, on %q", res, r.wm.where("0xa1"))
	}
}

// TestUnavailableAndBadRequests: without a compositor nothing runs; the
// view must be one of the two.
func TestUnavailableAndBadRequests(t *testing.T) {
	r := newRig(t, 1)
	if _, err := r.sw.Switch(context.Background(), Request{Want: "both"}); !errors.Is(err, ErrWant) {
		t.Errorf("bad view: %v", err)
	}
	r.wm.listErr = errors.New("no hyprctl")
	if _, err := r.sw.Switch(context.Background(), r.req(User)); !errors.Is(err, ErrList) {
		t.Errorf("no list: %v", err)
	}
	if len(r.wm.moves()) != 0 {
		t.Errorf("moved with no list: %v", r.wm.moves())
	}
	sw := New()
	t.Setenv("HYPRLAND_INSTANCE_SIGNATURE", "")
	if got := sw.Current(); got != "" {
		t.Errorf("Current without Hyprland = %q", got)
	}
	if _, err := sw.Switch(context.Background(), Request{Want: User}); !errors.Is(err, ErrNoCompositor) {
		t.Errorf("no compositor: %v", err)
	}
}

// TestSettleHidesANewWindowOnlyInUserView: a spawn that just bound its
// window has it hidden when Den is in User view, and only when the
// session's process runs in it.
func TestSettleHidesANewWindowOnlyInUserView(t *testing.T) {
	r := newRig(t, 1)
	// Agent view: nothing happens.
	if ok, err := r.sw.Settle(context.Background(), marker(1), 101); ok || err != nil {
		t.Fatalf("in agent view: %v %v", ok, err)
	}
	r.must(User)
	r.must(Agent)
	r.must(User)

	// A new agent comes up on workspace 3.
	if err := winmark.Record("agent-2", marker(2)); err != nil {
		t.Fatal(err)
	}
	r.wm.cs = append(r.wm.cs, win("0xa2", marker(2), 1002, 3))
	chain := r.sw.Ancestors
	r.sw.Ancestors = func(pid int) []procs.Proc {
		if pid == 102 {
			return []procs.Proc{{PID: 102}, {PID: 1002}}
		}
		if pid == 103 {
			return []procs.Proc{{PID: 103}, {PID: 4444}}
		}
		return chain(pid)
	}
	// Run by someone else: not touched.
	if ok, _ := r.sw.Settle(context.Background(), marker(2), 103); ok || r.wm.where("0xa2") != "3" {
		t.Fatalf("settled a window the session does not run in: on %q", r.wm.where("0xa2"))
	}
	if ok, err := r.sw.Settle(context.Background(), marker(2), 102); !ok || err != nil {
		t.Fatalf("settle: %v %v", ok, err)
	}
	if r.wm.where("0xa2") != Workspace {
		t.Fatalf("on %q", r.wm.where("0xa2"))
	}
	// Its origin was recorded: the show sends it back to workspace 3.
	r.sess = append(r.sess, Session{ID: "sess-2", PID: 102})
	r.must(Agent)
	if r.wm.where("0xa2") != "3" {
		t.Errorf("came back to %q", r.wm.where("0xa2"))
	}
}

// TestARecordedOriginIsOnlyWhatDenWrites: a record that was edited to say
// something else is not handed to the window manager; the window goes to
// where the human is.
func TestARecordedOriginIsOnlyWhatDenWrites(t *testing.T) {
	for _, w := range []string{"", "0", "-1", "3; exec x", `2" }) os.exit(`, "special:", "special:A b", "name:foo", "1234567890"} {
		if validWorkspace(w) {
			t.Errorf("%q is taken for an origin", w)
		}
	}
	for _, w := range []string{"1", "12", "special:other", "special:den-2"} {
		if !validWorkspace(w) {
			t.Errorf("%q is refused", w)
		}
	}
	r := newRig(t, 1)
	r.must(User)
	raw, err := os.ReadFile(r.sw.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(raw), `"from":"2"`, `"from":"2; exec x"`, 1)
	if edited == string(raw) {
		t.Fatalf("record: %s", raw)
	}
	if err := os.WriteFile(r.sw.StatePath, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	r.wm.active = hypr.Workspace{ID: 6}
	r.must(Agent)
	for _, m := range r.wm.moves() {
		if strings.Contains(m, "exec") {
			t.Errorf("an edited origin reached the window manager: %s", m)
		}
	}
	if got := r.wm.where("0xa1"); got != "6" {
		t.Errorf("the window is on %q, want the active workspace 6", got)
	}
}
