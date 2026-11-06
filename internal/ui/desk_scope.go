package ui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/desktop"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/notify"
	"github.com/GlacierHubAB/den/internal/sanitize"
	"github.com/GlacierHubAB/den/internal/sysd"
	"github.com/GlacierHubAB/den/internal/wlsec"
)

// A task is held to its own windows unless the human turns on "whole
// desktop" on its start card: the windows of what it launches (its slice)
// and the ones the human picks there. Both are set in Den's window only,
// with /desktop whole and /desktop pick, like the X11 switch: not the
// agent's request, the config, the dock or den-api; fixed once it starts,
// and never inherited.

// deskPick is a window the human picked on a start card.
type deskPick struct {
	Address, Class, Title string
	PID                   int
}

// deskScopedBuild says whether Hyprland is the build a task can be held
// on ("" when it is); a seam for tests.
var deskScopedBuild = desktop.ScopedBuild

// deskWideLine is the card's line with "whole desktop" on.
const deskWideLine = "Whole desktop: on. While it runs, the agent can do anything you can at the keyboard: run commands as you in a terminal, type into other agents' terminals and their permission prompts, use the clipboard, read the screen, and act in apps you are signed in to. /desktop whole off holds it to its own windows."

// deskPickWarning is said of every picked window (sec's S4-2).
const deskPickWarning = "Keys into this window run as you, outside the task's sandbox. Pausing doesn't stop what they start."

// deskScopeLines are the card's lines for its scope switch and picks.
func (o deskOffer) deskScopeLines() []string {
	if o.wide {
		return []string{deskWideLine}
	}
	out := []string{"Keys and clicks: only into the windows it opens, and ones you pick with /desktop pick. It can still read the whole screen. /desktop whole lets it use the whole desktop instead. On a Hyprland build Den hasn't checked, a task can't be held this way and doesn't start: use /desktop whole then, or decline."}
	for _, p := range o.picks {
		out = append(out, "Picked: "+deskPickName(p)+". "+deskPickWarning)
	}
	if len(o.pickable) > 0 {
		out = append(out, "Windows you can pick (/desktop pick <n> picks or unpicks one):")
		for i, p := range o.pickable {
			out = append(out, "  "+strconv.Itoa(i+1)+"  "+deskPickName(p))
		}
	}
	return out
}

func deskPickName(p deskPick) string {
	return sanitize.Label(p.Class, 40) + " — " + sanitize.Label(p.Title, 60)
}

// deskWaitingTask is the task card waiting now and its offer, or why
// there is none to change.
func (m Model) deskWaitingTask() (*inbox.Item, deskOffer, string) {
	for _, it := range m.store.Items() {
		if it.Kind != inbox.DesktopMission || it.State != inbox.Waiting {
			continue
		}
		o, ok := m.desk.offers[it.ID]
		if !ok || it.Resolved != "" || m.desk.installing == it.ID {
			return nil, o, "That card is already starting; it stays as it was."
		}
		if o.task == "" {
			return nil, o, "That is for a task; a mission with configured apps opens only those."
		}
		return it, o, ""
	}
	return nil, deskOffer{}, "No desktop task card is waiting. This is set on its card, before it starts."
}

// deskWhole is /desktop whole [on|off], typed in Den's window.
func (m Model) deskWhole(args []string) (tea.Model, tea.Cmd) {
	on := true
	switch {
	case len(args) == 0:
	case len(args) == 1 && strings.EqualFold(args[0], "on"):
	case len(args) == 1 && strings.EqualFold(args[0], "off"):
		on = false
	default:
		return m.say("/desktop whole takes on or off.", true)
	}
	it, o, why := m.deskWaitingTask()
	if it == nil {
		return m.say(why, true)
	}
	o.wide = on
	if on {
		o.picks, o.pickable = nil, nil
	}
	m.desk.offers[it.ID] = o
	it.Body = o.cardBody()
	m.refresh()
	if on {
		return m.say("The task on the card may use the whole desktop.", false)
	}
	return m.say("The task on the card is held to its own windows and the ones you pick.", false)
}

// deskPicksMsg is the windows a card may offer to pick, as read now.
type deskPicksMsg struct {
	itemID string
	n      int // the number typed, 0 to list
	wins   []deskPick
	err    error
}

// deskPick is /desktop pick [n]: with no number it lists the windows the
// card can offer; with one it picks or unpicks that window of the list.
func (m Model) deskPickCmd(args []string) (tea.Model, tea.Cmd) {
	n := 0
	if len(args) == 1 {
		v, err := strconv.Atoi(args[0])
		if err != nil || v < 1 {
			return m.say("/desktop pick takes a window's number from the card's list.", true)
		}
		n = v
	} else if len(args) > 1 {
		return m.say("/desktop pick takes one number.", true)
	}
	it, o, why := m.deskWaitingTask()
	if it == nil {
		return m.say(why, true)
	}
	if o.wide {
		return m.say("The whole desktop is on for that card; /desktop whole off first.", true)
	}
	if n > 0 {
		if n > len(o.pickable) {
			return m.say("No window "+strconv.Itoa(n)+" on the card's list; /desktop pick lists them.", true)
		}
		p := o.pickable[n-1]
		kept := o.picks[:0:0]
		for _, q := range o.picks {
			if q.Address != p.Address {
				kept = append(kept, q)
			}
		}
		picked := len(kept) == len(o.picks)
		if picked {
			kept = append(kept, p)
		}
		o.picks = kept
		m.desk.offers[it.ID] = o
		it.Body = o.cardBody()
		m.refresh()
		if picked {
			return m.say("Picked "+deskPickName(p)+". "+deskPickWarning, false)
		}
		return m.say("Unpicked "+deskPickName(p)+".", false)
	}
	id := it.ID
	exclude := m.deskNeverPickable()
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		snap, err := deskSnapshot(ctx)
		if err != nil {
			return deskPicksMsg{itemID: id, err: err}
		}
		var wins []deskPick
		for _, w := range desktop.Pickable(snap, exclude) {
			wins = append(wins, deskPick{Address: w.Address, Class: w.Class, Title: w.Title, PID: w.PID})
		}
		return deskPicksMsg{itemID: id, wins: wins}
	}
}

// deskNeverPickable are processes whose windows a card never offers: the
// console's (Drey's dock), and the terminal of every agent Den knows a
// window of (its spawn and window records). One terminal process can hold
// the human's other windows too; none of them is offered either.
func (m Model) deskNeverPickable() map[int]bool {
	out := map[int]bool{}
	for pid := range m.consoleProcs() {
		out[pid] = true
	}
	for _, a := range append(append([]fleet.Agent(nil), m.base...), m.agents...) {
		if a.Window != nil && a.Window.Client.PID > 1 {
			out[a.Window.Client.PID] = true
		}
	}
	return out
}

// finishPicks puts the list on the card.
func (m *Model) finishPicks(msg deskPicksMsg) tea.Cmd {
	o, ok := m.desk.offers[msg.itemID]
	var it *inbox.Item
	for _, x := range m.store.Items() {
		if x.ID == msg.itemID && x.State == inbox.Waiting {
			it = x
		}
	}
	if !ok || it == nil || o.wide {
		return nil
	}
	if msg.err != nil {
		m.flash, m.flashErr = "Den could not read the desktop to list windows: "+sanitize.Line(msg.err.Error()), true
		return nil
	}
	o.pickable = msg.wins
	m.desk.offers[msg.itemID] = o
	it.Body = o.cardBody()
	m.flash, m.flashErr = strconv.Itoa(len(msg.wins))+" windows you can pick are on the card.", false
	m.refresh()
	return nil
}

// scopeTask checks a held task at its start, against the desktop as Den
// read it then: the build is the checked one, and every pick is still
// the window that was picked and still pickable. It returns the picks
// for the mission, or why it can't start.
func scopeTask(ctx context.Context, o deskOffer, snap desktop.Snapshot, own string, exclude map[int]bool) (map[string]desktop.Pick, string) {
	if why := deskScopedBuild(ctx); why != "" {
		return nil, why + ", so the task can't be held to its own windows; ask again and use /desktop whole on its card, or decline"
	}
	ok := map[string]bool{}
	for _, w := range desktop.Pickable(snap, exclude) {
		ok[w.Address+"\x00"+strconv.Itoa(w.PID)] = true
	}
	picks := map[string]desktop.Pick{}
	for _, p := range o.picks {
		if p.Address == own {
			return nil, "a window you picked is Den's own; ask again and pick anew"
		}
		if !ok[p.Address+"\x00"+strconv.Itoa(p.PID)] {
			return nil, "a window you picked (" + deskPickName(p) + ") changed or closed; ask again and pick anew"
		}
		picks[p.Address] = desktop.Pick{PID: p.PID}
	}
	return picks, ""
}

// prunePicks drops, for good, a picked window that is gone or now on
// another process: an address that comes back is another window.
func prunePicks(picks map[string]desktop.Pick, snap desktop.Snapshot) {
	for addr, p := range picks {
		if w, ok := snap.Window(addr); !ok || w.PID != p.PID {
			delete(picks, addr)
		}
	}
}

// deskOpenListener makes a held task's Wayland listener (wlsec): a socket
// the compositor serves as sandboxed, in a 0700 folder of Den's runtime
// folder, for as long as Den holds its close fd. A seam for tests.
var deskOpenListener = func(id string) (deskListener, error) {
	run, way := os.Getenv("XDG_RUNTIME_DIR"), os.Getenv("WAYLAND_DISPLAY")
	if run == "" || way == "" {
		return nil, errors.New("no Wayland session")
	}
	if !filepath.IsAbs(way) {
		way = filepath.Join(run, way)
	}
	parent := filepath.Join(run, "den-wl")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		return nil, err
	}
	l, err := wlsec.Open(way, filepath.Join(parent, sysd.EscapePart(id)), "io.glacierhub.den.desktop", id, 3*time.Second)
	if err != nil {
		return nil, err // never a nil *Listener in the interface
	}
	return l, nil
}

// deskListener is a held task's listener as Den uses it.
type deskListener interface {
	Socket() string
	Live() bool
	Close() error
}

// closeListener ends a held task's listener, once nothing it launched
// runs: a context outlives its close fd for clients already connected, so
// the slice is stopped first (sec).
func (dm *deskMission) closeListener() {
	if dm.listener != nil {
		dm.listener.Close()
		dm.listener = nil
	}
}

// deskBuildChanged pauses a held task whose launch found Hyprland changed
// (C1): it is held for the human in Den's window, and goes on only on the
// checked build again. It never falls back to the whole desktop or the
// compositor's own socket.
func (m *Model) deskBuildChanged(dm *deskMission, why string) {
	first := dm.buildChanged == ""
	dm.buildChanged = why
	dm.humanPaused = true
	if first {
		text := "Desktop agent paused: " + sanitize.Line(why) + " any more, so Den can't hold the task to its own windows. /desktop resume checks again; /desktop stop ends it."
		m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: text, at: time.Now()}))
		m.flash, m.flashErr = text, true
		deskNotify(notify.Note{Summary: "Desktop agent paused", Body: text})
	}
	m.refresh()
}
