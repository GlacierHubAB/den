// Package view switches Den's own windows between Agent view and User view.
//
// Agent view is how Den always was: each session, every agent and Den's
// own, in its own terminal window that the window manager tiles. User
// view hides those windows, and the human works from the dock. Hiding
// moves each window to the special workspace special:den and records the
// workspace it came from; showing moves it back there. The windows stay
// alive. Never one terminal with panes: the point is that each session
// keeps a window of its own.
//
// Which windows are Den's? An agent's window is the one carrying the
// class Den chose at spawn and recorded for the agent's session
// (winmark), when the agent's own process runs in it (hypr.HeldWindow):
// never one found by title, working directory or any other guess. An
// agent Den did not spawn has no record, so it is not counted and never
// touched. Den's own window is the one window of a process above Den's,
// and only when Den can tell. Nothing here closes a window, signals a
// process or types into a terminal; the only change to a window is the
// move.
//
// A switch runs under hypr.LockWindows, the lock closes take too, so the
// two never overlap, and under a lock on the record, since an agent's
// spawn (another process) may hide its new window while a switch runs.
// The record is view.json in Den's state, written before the windows move,
// so a window is never stranded on special:den without one.
package view

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/GlacierHubAB/den/internal/hypr"
	"github.com/GlacierHubAB/den/internal/procs"
	"github.com/GlacierHubAB/den/internal/winmark"
)

// The two views, and the special workspace Den hides windows on: a fixed
// name, so a launcher may skip exactly it (hello's view_workspaces).
const (
	Agent     = "agent"
	User      = "user"
	Workspace = "special:den"
)

// Waits. A whole switch stays within Budget, since the dock gives up on a
// reply after 5 s; a call to hyprctl gets callTimeout.
const (
	Budget      = 4 * time.Second
	callTimeout = 1500 * time.Millisecond
	// ReasonMax is the longest reason, in characters.
	ReasonMax = 40
)

// Why a switch did not run.
var (
	ErrBusy         = errors.New("a view switch or a close is running")
	ErrNoCompositor = errors.New("Den cannot switch views here")
	ErrWant         = errors.New("a view is agent or user")
	ErrList         = errors.New("Den could not list the windows")
	ErrState        = errors.New("Den could not keep the view record")
	ErrMove         = errors.New("a window would not move")
)

// Ops is what a switch needs from the window manager; tests fake it.
type Ops interface {
	Available() bool
	Clients(ctx context.Context) ([]hypr.Client, error)
	Move(ctx context.Context, address, workspace string) error
	Active(ctx context.Context) (hypr.Workspace, bool)
}

type hyprOps struct{}

func (hyprOps) Available() bool { return hypr.Available() }

func (hyprOps) Clients(ctx context.Context) ([]hypr.Client, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	return hypr.ClientsContext(ctx)
}

func (hyprOps) Move(ctx context.Context, address, workspace string) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	return hypr.MoveToWorkspaceContext(ctx, address, workspace)
}

func (hyprOps) Active(ctx context.Context) (hypr.Workspace, bool) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	return hypr.ActiveWorkspaceContext(ctx)
}

// Session is a live agent session on this machine, as the fleet knows it.
type Session struct {
	ID  string
	PID int
	// Peer is the agent's peer name, which names its terminal (hypr.AgentTitle).
	// "" for none.
	Peer string
}

// Request is one switch.
type Request struct {
	Want     string // Agent or User
	Sessions []Session
	// Keep, when not empty, keeps Den's own window up, and says why
	// ("a card waits"). It matters only to a hide.
	Keep string
	// Hold lists sessions whose window a hide leaves visible, because the
	// agent waits on a prompt in its own terminal that nothing else can
	// answer. They count as total and shown.
	Hold []string
}

// Result is what a switch left. View is the view asked for. Shown counts
// Den's windows still visible once it ended, whichever way it went: a
// show succeeded when Shown == Total, a hide when Shown == 0, and Total 0
// means Den has no windows of its own. Reason is one plain line of at most
// ReasonMax characters, empty when nothing needs saying.
type Result struct {
	View   string
	Shown  int
	Total  int
	Reason string
}

// Moved is one hidden window and where it came from.
type Moved struct {
	// Marker is an agent's window class. An agent's window is found again
	// only by it, and only when it is the one window carrying it.
	Marker string `json:"marker,omitempty"`
	// Den's own window has no class of its own, so it is found again by
	// its address, its terminal process and its class, all three. So is
	// the window of an agent found by its title (Titled).
	Own     bool   `json:"own,omitempty"`
	Titled  bool   `json:"titled,omitempty"`
	Address string `json:"address,omitempty"`
	PID     int    `json:"pid,omitempty"`
	Class   string `json:"class,omitempty"`
	// From is the workspace it came from, "" when that was not known.
	From string    `json:"from,omitempty"`
	At   time.Time `json:"at"`
}

// State is view.json.
type State struct {
	View  string    `json:"view"`
	Since time.Time `json:"since,omitempty"`
	Moved []Moved   `json:"moved,omitempty"`
}

// NoAgentWindows is the refusal of a hide that could take no agent's window
// out of sight: N agents run, and none has a window Den can prove is the
// agent's.
type NoAgentWindows struct{ N int }

func (e *NoAgentWindows) Error() string {
	if e.N == 1 {
		return "no agent window is Den's to hide: 1 agent runs in a window Den cannot tell is its own"
	}
	return "no agent window is Den's to hide: " + strconv.Itoa(e.N) + " agents run in windows Den cannot tell are their own"
}

// Switcher switches views. The zero value is not ready: use New. Tests set
// the fields.
type Switcher struct {
	Ops       Ops
	Marker    func(session string) string
	Ancestors func(pid int) []procs.Proc
	Own       func(all []hypr.Client) (hypr.Client, bool)
	StatePath string // "" for view.json in Den's state
	Now       func() time.Time
	Budget    time.Duration
}

// New is the Switcher Den uses.
func New() *Switcher {
	return &Switcher{
		Ops:       hyprOps{},
		Marker:    winmark.For,
		Ancestors: procs.Ancestors,
		Own: func(all []hypr.Client) (hypr.Client, bool) {
			return hypr.OwnWindow(all, procs.Ancestors(os.Getpid()))
		},
		Now:    time.Now,
		Budget: Budget,
	}
}

// Path is view.json in Den's state folder, "" when there is none.
func Path() string {
	p := winmark.Path()
	if p == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(p), "view.json")
}

func (sw *Switcher) path() string {
	if sw.StatePath != "" {
		return sw.StatePath
	}
	return Path()
}

// Current is the view Den is in, and "" when this Den cannot switch (no
// Hyprland to talk to, or no place to keep the record).
func (sw *Switcher) Current() string {
	if !sw.Ops.Available() || sw.path() == "" {
		return ""
	}
	st, _ := loadState(sw.path())
	return st.View
}

func loadState(path string) (State, error) {
	var st State
	raw, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(raw, &st)
	}
	if err != nil || (st.View != Agent && st.View != User) {
		return State{View: Agent}, err
	}
	return st, nil
}

func saveState(path string, st State) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".view-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// withState holds the record's lock while f runs on the record as it is.
// It waits for the lock as long as ctx lasts, then gives ErrBusy.
func (sw *Switcher) withState(ctx context.Context, f func(st *State) error) error {
	p := sw.path()
	if p == "" {
		return ErrState
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return ErrState
	}
	lock, err := os.OpenFile(p+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return ErrState
	}
	defer lock.Close()
	for {
		err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EINTR {
			return ErrState
		}
		select {
		case <-ctx.Done():
			return ErrBusy
		case <-time.After(25 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	st, _ := loadState(p)
	return f(&st)
}

func (sw *Switcher) budget() time.Duration {
	if sw.Budget > 0 {
		return sw.Budget
	}
	return Budget
}

// Switch moves Den's windows to the view asked for. It never waits behind
// another switch or a close: it answers ErrBusy. Asking for the view Den
// is in again moves whatever is out of place (a window that came up since)
// and changes nothing else.
func (sw *Switcher) Switch(ctx context.Context, rq Request) (Result, error) {
	if rq.Want != Agent && rq.Want != User {
		return Result{}, ErrWant
	}
	if !sw.Ops.Available() {
		return Result{}, ErrNoCompositor
	}
	unlock, ok := hypr.TryLockWindows()
	if !ok {
		return Result{}, ErrBusy
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(ctx, sw.budget())
	defer cancel()
	var res Result
	err := sw.withState(ctx, func(st *State) error {
		var err error
		res, err = sw.run(ctx, st, rq)
		return err
	})
	return res, err
}

// Settle hides the window of a session that just came up, when Den is in
// User view: the one window the marker names, when the session's process
// runs in it, and only then. It reports whether the window was hidden. In
// Agent view, or when Den cannot tell the window is the session's, it does
// nothing.
func (sw *Switcher) Settle(ctx context.Context, marker string, pid int) (bool, error) {
	if marker == "" || !sw.Ops.Available() {
		return false, nil
	}
	for {
		if unlock, ok := hypr.TryLockWindows(); ok {
			defer unlock()
			break
		}
		select {
		case <-ctx.Done():
			return false, ErrBusy
		case <-time.After(25 * time.Millisecond):
		}
	}
	hidden := false
	err := sw.withState(ctx, func(st *State) error {
		if st.View != User {
			return nil
		}
		all, err := sw.Ops.Clients(ctx)
		if err != nil {
			return ErrList
		}
		c, ok := hypr.HeldWindow(all, marker, sw.Ancestors(pid))
		if !ok {
			return nil
		}
		if onSpecial(c) {
			hidden = true
			return nil
		}
		st.upsert(Moved{Marker: marker, From: origin(c), At: sw.now()})
		if err := saveState(sw.path(), *st); err != nil {
			return ErrState
		}
		_ = sw.Ops.Move(ctx, c.Address, Workspace)
		final, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		after, err := sw.Ops.Clients(final)
		if err != nil {
			return ErrList
		}
		hits := windowsOf(after, marker)
		hidden = len(hits) == 1 && onSpecial(hits[0])
		if !hidden {
			st.drop(Moved{Marker: marker})
			_ = saveState(sw.path(), *st)
		}
		return nil
	})
	return hidden, err
}

// Rescue brings back every window on special:den, whatever it is and
// whoever put it there: the human's way out when a window is stranded, for
// instance behind a decoy that carries an agent's class. A window with a
// recorded origin goes back there; any other goes to the workspace the
// human is on. It is for the human at Den's own screen only: no API verb
// reaches it. It reports how many windows moved and how many are still
// there.
func (sw *Switcher) Rescue(ctx context.Context) (moved, left int, err error) {
	if !sw.Ops.Available() {
		return 0, 0, ErrNoCompositor
	}
	unlock, ok := hypr.TryLockWindows()
	if !ok {
		return 0, 0, ErrBusy
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(ctx, sw.budget())
	defer cancel()
	err = sw.withState(ctx, func(st *State) error {
		all, err := sw.Ops.Clients(ctx)
		if err != nil {
			return ErrList
		}
		for _, c := range all {
			if !onSpecial(c) {
				continue
			}
			if ctx.Err() != nil {
				break
			}
			to := ""
			for _, r := range st.Moved {
				switch {
				case (r.Own || r.Titled) && r.Address == c.Address && r.PID == c.PID && r.Class == c.InitialClass:
					to = r.From
				case !r.Own && !r.Titled && r.Marker != "" && r.Marker == c.InitialClass && len(windowsOf(all, r.Marker)) == 1:
					to = r.From
				}
			}
			if !validWorkspace(to) {
				to = ""
				if ws, ok := sw.Ops.Active(ctx); ok {
					to = strconv.Itoa(ws.ID)
				}
			}
			if to != "" {
				_ = sw.Ops.Move(ctx, c.Address, to)
			}
		}
		final, cancelFinal := context.WithTimeout(context.Background(), callTimeout)
		defer cancelFinal()
		after, err := sw.Ops.Clients(final)
		if err != nil {
			return ErrList
		}
		before := map[string]bool{}
		for _, c := range all {
			if onSpecial(c) {
				before[c.Address] = true
			}
		}
		var keep []Moved
		for _, c := range after {
			if before[c.Address] && !onSpecial(c) {
				moved++
			}
			if onSpecial(c) {
				left++
			}
		}
		for _, r := range st.Moved {
			for _, c := range after {
				if onSpecial(c) && ((r.Own || r.Titled) && r.Address == c.Address || !r.Own && !r.Titled && r.Marker == c.InitialClass) {
					keep = append(keep, r)
					break
				}
			}
		}
		st.Moved, st.View = keep, Agent
		if err := saveState(sw.path(), *st); err != nil {
			return ErrState
		}
		return nil
	})
	return moved, left, err
}

// ShowOwn brings back Den's own window alone, from special:den, and leaves
// every other window, and the view Den is in, as they are. It is for a card
// that waits behind a hidden Den window when nobody is connected to call
// view.show. The window is found by Den's rule (the one window of a process
// above Den's) or by its record, never otherwise. It reports whether a
// window moved. A window that is on special:den and would not move is an
// error (ErrMove), so the caller tries again.
func (sw *Switcher) ShowOwn(ctx context.Context) (bool, error) {
	if !sw.Ops.Available() {
		return false, ErrNoCompositor
	}
	unlock, ok := hypr.TryLockWindows()
	if !ok {
		return false, ErrBusy
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(ctx, sw.budget())
	defer cancel()
	moved := false
	err := sw.withState(ctx, func(st *State) error {
		all, err := sw.Ops.Clients(ctx)
		if err != nil {
			return ErrList
		}
		var own hypr.Client
		found := false
		if c, ok := sw.Own(all); ok && onSpecial(c) {
			own, found = c, true
		}
		to := ""
		for _, r := range st.Moved {
			if !r.Own {
				continue
			}
			c, ok := byAddress(all, r.Address)
			if ok && c.PID == r.PID && c.InitialClass == r.Class && onSpecial(c) {
				own, found, to = c, true, r.From
			}
		}
		if !found {
			return nil
		}
		if !validWorkspace(to) {
			to = ""
			if ws, ok := sw.Ops.Active(ctx); ok {
				to = strconv.Itoa(ws.ID)
			}
		}
		if to == "" {
			return nil
		}
		_ = sw.Ops.Move(ctx, own.Address, to)
		final, cancelFinal := context.WithTimeout(context.Background(), callTimeout)
		defer cancelFinal()
		after, err := sw.Ops.Clients(final)
		if err != nil {
			return ErrList
		}
		c, ok := byAddress(after, own.Address)
		switch {
		case !ok:
			return nil // it closed meanwhile
		case onSpecial(c):
			return ErrMove
		}
		moved = true
		st.drop(Moved{Own: true, Address: own.Address})
		if err := saveState(sw.path(), *st); err != nil {
			return ErrState
		}
		return nil
	})
	return moved, err
}

func (sw *Switcher) now() time.Time {
	if sw.Now != nil {
		return sw.Now()
	}
	return time.Now()
}

// item is one window of Den's: tied to a live session or to Den itself, or
// only recorded (hidden earlier, its session gone since).
type item struct {
	addr   string
	marker string
	own    bool
	titled bool // an agent's window found by its title, hidden on that ground only
	tied   bool
}

func (sw *Switcher) run(ctx context.Context, st *State, rq Request) (Result, error) {
	all, err := sw.Ops.Clients(ctx)
	if err != nil {
		return Result{}, ErrList
	}
	var items []item
	have := map[string]int{}
	add := func(it item) {
		if i, ok := have[it.addr]; ok {
			items[i].tied = items[i].tied || it.tied
			return
		}
		have[it.addr] = len(items)
		items = append(items, it)
	}
	// A marker Den cannot tie to one window of its own: two windows carry
	// it, or the one that does is not run by the agent. Such windows are
	// counted and left where they are.
	notDens := map[string]bool{}
	seen := map[string]bool{}
	// unmarked counts the agents whose window is not Den's to move: it has
	// no marker, and no title window either. Only a hide counts them: a show
	// moves back what the record says Den hid, whoever else is running.
	unmarked := 0
	count := func() {
		if rq.Want == User {
			unmarked++
		}
	}
	titledAt := map[string]string{} // session id to its window, found by title
	for _, s := range rq.Sessions {
		m := sw.Marker(s.ID)
		if m == "" {
			// An agent Den did not spawn with a marker (or an older Den
			// did not): for a hide only, which a show undoes, the title Den
			// gave its terminal and the agent running under it find the
			// window. A show never relies on this: it moves what the record
			// says Den hid.
			if rq.Want == User && s.Peer != "" {
				if c, ok := hypr.TitledWindow(all, hypr.AgentTitle(s.Peer), sw.Ancestors(s.PID)); ok {
					add(item{addr: c.Address, titled: true, tied: true})
					titledAt[s.ID] = c.Address
					continue
				}
			}
			count()
			continue
		}
		if seen[m] {
			continue
		}
		seen[m] = true
		if len(windowsOf(all, m)) == 0 {
			count() // its window carries no such class: not Den's to move
			continue
		}
		if c, ok := hypr.HeldWindow(all, m, sw.Ancestors(s.PID)); ok {
			add(item{addr: c.Address, marker: m, tied: true})
		} else {
			notDens[m] = true
		}
	}
	hold := map[string]bool{}
	for _, id := range rq.Hold {
		hold[id] = true
	}
	held := map[string]bool{} // the windows of those, by address
	for _, s := range rq.Sessions {
		if !hold[s.ID] {
			continue
		}
		if a := titledAt[s.ID]; a != "" {
			held[a] = true
			continue
		}
		if m := sw.Marker(s.ID); m != "" {
			if c, ok := hypr.HeldWindow(all, m, sw.Ancestors(s.PID)); ok {
				held[c.Address] = true
			}
		}
	}
	own, ownKnown := sw.Own(all)
	if ownKnown {
		add(item{addr: own.Address, own: true, tied: true})
	}
	for _, r := range st.Moved {
		if r.Own || r.Titled {
			c, ok := byAddress(all, r.Address)
			if ok && c.PID == r.PID && c.InitialClass == r.Class && onSpecial(c) {
				add(item{addr: c.Address, own: r.Own, titled: r.Titled})
				ownKnown = ownKnown || r.Own
			}
			continue
		}
		hits := windowsOf(all, r.Marker)
		switch {
		case len(hits) == 1 && onSpecial(hits[0]):
			add(item{addr: hits[0].Address, marker: r.Marker})
		case len(hits) > 1:
			notDens[r.Marker] = true
		}
	}

	// A hide that could take no agent's window out of sight, though agents
	// are running, is refused whole: Den would be in User view with every
	// agent still on screen, and would say so. Nothing is moved, not even
	// Den's own window.
	if rq.Want == User && unmarked > 0 {
		agents := 0
		for _, it := range items {
			if !it.own {
				agents++
			}
		}
		if agents == 0 {
			return Result{}, &NoAgentWindows{N: unmarked}
		}
	}
	// The record before the move: a window is never out of sight without
	// one.
	type move struct {
		it item
		to string
	}
	var moves []move
	keptOwn := false
	heldN := 0
	for _, it := range items {
		c, _ := byAddress(all, it.addr)
		switch {
		case rq.Want == User && it.tied && !onSpecial(c):
			if it.own && rq.Keep != "" {
				keptOwn = true
				continue
			}
			if held[it.addr] {
				heldN++
				continue
			}
			r := Moved{Marker: it.marker, From: origin(c), At: sw.now()}
			if it.own || it.titled {
				r = Moved{Own: it.own, Titled: it.titled, Address: c.Address, PID: c.PID, Class: c.InitialClass, From: origin(c), At: sw.now()}
			}
			st.upsert(r)
			moves = append(moves, move{it, Workspace})
		case rq.Want == Agent && onSpecial(c):
			to := st.originOf(it)
			if !validWorkspace(to) {
				to = ""
			}
			if to == "" {
				if ws, ok := sw.Ops.Active(ctx); ok {
					to = strconv.Itoa(ws.ID)
				}
			}
			if to != "" {
				moves = append(moves, move{it, to})
			}
		}
	}
	if len(moves) > 0 {
		if err := saveState(sw.path(), *st); err != nil {
			return Result{}, ErrState
		}
	}
	timedOut := false
	for _, mv := range moves {
		if ctx.Err() != nil {
			timedOut = true
			break
		}
		_ = sw.Ops.Move(ctx, mv.it.addr, mv.to)
	}

	// What is true now, not what the dispatches said.
	final, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	after, err := sw.Ops.Clients(final)
	if err != nil {
		return Result{}, ErrList
	}
	// A marker Den cannot tie counts as shown unless one of its windows is
	// out of sight on special:den: then the hidden one is still hidden.
	res := Result{View: rq.Want, Total: len(notDens)}
	for m := range notDens {
		visible := true
		for _, c := range windowsOf(after, m) {
			if onSpecial(c) {
				visible = false
			}
		}
		if visible {
			res.Shown++
		}
	}
	var keep []Moved
	for _, r := range st.Moved {
		if !r.Own && !r.Titled && notDens[r.Marker] {
			keep = append(keep, r) // maybe the hidden window itself: not dropped
		}
	}
	for _, it := range items {
		c, ok := byAddress(after, it.addr)
		if !ok {
			continue // closed meanwhile
		}
		res.Total++
		if !onSpecial(c) {
			res.Shown++
			continue
		}
		r, found := st.find(it)
		if !found {
			r = Moved{Marker: it.marker, At: sw.now()}
			if it.own || it.titled {
				r = Moved{Own: it.own, Titled: it.titled, Address: c.Address, PID: c.PID, Class: c.InitialClass, At: sw.now()}
			}
		}
		keep = append(keep, r)
	}
	st.Moved = keep
	if st.View != rq.Want {
		st.Since = sw.now()
	}
	st.View = rq.Want
	if err := saveState(sw.path(), *st); err != nil {
		return Result{}, ErrState
	}
	res.Reason = reason(rq, res, len(notDens)+unmarked, heldN, timedOut, keptOwn, ownKnown)
	return res, nil
}

// reason says in one short line what did not go as asked.
func reason(rq Request, res Result, notDens, heldN int, timedOut, keptOwn, ownKnown bool) string {
	var parts []string
	if timedOut {
		parts = append(parts, "ran out of time")
	}
	if rq.Want == User && res.Shown > 0 {
		parts = append(parts, fmt.Sprintf("%d of %d not hidden", res.Shown, res.Total))
	}
	if rq.Want == Agent && res.Shown < res.Total {
		parts = append(parts, fmt.Sprintf("%d of %d shown", res.Shown, res.Total))
	}
	if notDens > 0 {
		parts = append(parts, fmt.Sprintf("%d not Den's", notDens))
	}
	switch {
	case heldN == 1:
		parts = append(parts, "1 waits on a prompt")
	case heldN > 1:
		parts = append(parts, fmt.Sprintf("%d wait on a prompt", heldN))
	}
	if keptOwn {
		parts = append(parts, "Den's window stays: "+rq.Keep)
	}
	if rq.Want == User && !ownKnown {
		parts = append(parts, "Den's own window not found")
	}
	out := ""
	for _, p := range parts {
		next := p
		if out != "" {
			next = out + "; " + p
		}
		if utf8.RuneCountInString(next) <= ReasonMax {
			out = next
		}
	}
	if out == "" && len(parts) > 0 {
		out = clip(parts[0], ReasonMax)
	}
	return out
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

func onSpecial(c hypr.Client) bool { return c.Workspace.Name == Workspace }

// origin is the workspace to send a window back to: its number, or the
// name of a special workspace. "" when Den cannot say.
func origin(c hypr.Client) string {
	switch {
	case c.Workspace.ID > 0:
		return strconv.Itoa(c.Workspace.ID)
	case strings.HasPrefix(c.Workspace.Name, "special:") && c.Workspace.Name != Workspace:
		return c.Workspace.Name
	}
	return ""
}

// validWorkspace says whether a recorded origin is what Den writes: a
// workspace number, or the name of a special workspace. The record is a
// file; what it says is not passed to the window manager otherwise.
func validWorkspace(w string) bool {
	if rest, ok := strings.CutPrefix(w, "special:"); ok {
		if rest == "" || len(rest) > 32 {
			return false
		}
		for _, r := range rest {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
				return false
			}
		}
		return true
	}
	if w == "" || len(w) > 9 {
		return false
	}
	for _, r := range w {
		if r < '0' || r > '9' {
			return false
		}
	}
	return w != "0"
}

func windowsOf(all []hypr.Client, marker string) []hypr.Client {
	var out []hypr.Client
	if marker == "" {
		return nil
	}
	for _, c := range all {
		if c.InitialClass == marker {
			out = append(out, c)
		}
	}
	return out
}

func byAddress(all []hypr.Client, addr string) (hypr.Client, bool) {
	for _, c := range all {
		if c.Address == addr && addr != "" {
			return c, true
		}
	}
	return hypr.Client{}, false
}

// upsert notes where a window came from, replacing an earlier note for
// the same window.
func (st *State) upsert(r Moved) {
	st.drop(r)
	st.Moved = append(st.Moved, r)
}

func (st *State) drop(r Moved) {
	kept := st.Moved[:0]
	for _, o := range st.Moved {
		if same(o, r) {
			continue
		}
		kept = append(kept, o)
	}
	st.Moved = kept
}

func same(a, b Moved) bool {
	if a.Own || b.Own || a.Titled || b.Titled {
		return a.Own == b.Own && a.Titled == b.Titled && a.Address == b.Address
	}
	return a.Marker == b.Marker
}

func (st *State) find(it item) (Moved, bool) {
	for _, r := range st.Moved {
		if (it.own || it.titled) && r.Own == it.own && r.Titled == it.titled && r.Address == it.addr {
			return r, true
		}
		if !it.own && !it.titled && !r.Own && !r.Titled && r.Marker != "" && r.Marker == it.marker {
			return r, true
		}
	}
	return Moved{}, false
}

func (st *State) originOf(it item) string {
	r, _ := st.find(it)
	return r.From
}
