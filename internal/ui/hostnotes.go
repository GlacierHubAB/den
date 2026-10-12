package ui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/notify"
	"github.com/GlacierHubAB/den/internal/remote"
)

// Limits on what a remote host's events may start on this machine. A host
// chooses its events, so each one may not cost a notify-send or an [on]
// process: a host's notifications are coalesced into one per window, and
// both are capped.
const (
	hostNoteWindow = 30 * time.Second // one notification per host this often
	hostNotesMax   = 6                // notify-send runs a minute for all hosts together
	hostOnMax      = 30               // [on] commands a minute for one host
)

// hostNotes coalesces remote notifications per host: the first goes out
// at once and opens a window; what comes during it is counted and goes out
// as one "<host>: N agents need you" on the first tick after it closes.
type hostNotes struct {
	window  time.Duration
	max     int
	onMax   int
	open    map[string]*noteWindow // host -> its window, while one is open
	sent    []time.Time            // notify-send runs for remote hosts in the last minute
	on      map[string][]time.Time // host -> its [on] runs in the last minute
	dropped int                    // notifications over the cap
}

type noteWindow struct {
	opened time.Time
	needs  map[string]bool // sessions waiting on the human
	other  int             // finished, failed
}

func newHostNotes() *hostNotes {
	return &hostNotes{window: hostNoteWindow, max: hostNotesMax, onMax: hostOnMax,
		open: map[string]*noteWindow{}, on: map[string][]time.Time{}}
}

// needsYou reports whether a notification's words ask something of the human.
func needsYou(what string) bool {
	return strings.HasPrefix(what, "needs") || strings.HasPrefix(what, "has a")
}

// note shows n now if no window is open for host, and counts it in the
// open one otherwise.
func (h *hostNotes) note(host, key string, needs bool, n notify.Note, send func(notify.Note) bool, now time.Time) tea.Cmd {
	if w, ok := h.open[host]; ok {
		if needs {
			w.needs[key] = true
		} else {
			w.other++
		}
		return nil
	}
	h.open[host] = &noteWindow{opened: now, needs: map[string]bool{}}
	return h.post(n, send, now)
}

// hostNotesDue closes the windows that are over, on the periodic tick.
func (m Model) hostNotesDue(now time.Time) tea.Cmd {
	if m.att.hosts == nil {
		return nil
	}
	var cmds []tea.Cmd
	for host, w := range m.att.hosts.open {
		if now.Sub(w.opened) >= m.att.hosts.window {
			cmds = append(cmds, m.att.hosts.flush(host, m.att.send, now))
		}
	}
	return tea.Batch(cmds...)
}

// flush closes host's window: one notification for what it collected, and
// a new window after it so a host that keeps on stays at one per window.
func (h *hostNotes) flush(host string, send func(notify.Note) bool, now time.Time) tea.Cmd {
	w := h.open[host]
	delete(h.open, host)
	if w == nil || len(w.needs)+w.other == 0 {
		return nil
	}
	var parts []string
	switch n := len(w.needs); {
	case n == 1:
		parts = append(parts, "1 agent needs you")
	case n > 1:
		parts = append(parts, fmt.Sprintf("%d agents need you", n))
	}
	switch {
	case w.other == 1:
		parts = append(parts, "1 more update")
	case w.other > 1:
		parts = append(parts, fmt.Sprintf("%d more updates", w.other))
	}
	h.open[host] = &noteWindow{opened: now, needs: map[string]bool{}}
	return h.post(notify.Note{Summary: host + ": " + strings.Join(parts, ", ")}, send, now)
}

// post runs notify-send for n unless the cap for the last minute is used.
func (h *hostNotes) post(n notify.Note, send func(notify.Note) bool, now time.Time) tea.Cmd {
	h.sent = recent(h.sent, now)
	if send == nil || len(h.sent) >= h.max {
		h.dropped++
		return nil
	}
	h.sent = append(h.sent, now)
	return func() tea.Msg {
		send(n)
		return nil
	}
}

// allowOn reports whether an [on] command may run for an event from host
// now, and counts it if so.
func (h *hostNotes) allowOn(host string, now time.Time) bool {
	runs := recent(h.on[host], now)
	if len(runs) >= h.onMax {
		h.on[host] = runs
		return false
	}
	h.on[host] = append(runs, now)
	return true
}

// recent keeps the times within the last minute.
func recent(ts []time.Time, now time.Time) []time.Time {
	i := 0
	for i < len(ts) && now.Sub(ts[i]) >= time.Minute {
		i++
	}
	return ts[i:]
}

// droppedText says what Den dropped from a host, for /remote: events the
// manager did not take, and inbox items over the caps.
func (m Model) droppedText(h remote.HostStatus) string {
	var parts []string
	if h.Dropped > 0 {
		parts = append(parts, fmt.Sprintf("%d events", h.Dropped))
	}
	if n := m.store.RemoteDropped(h.Label); n > 0 {
		parts = append(parts, fmt.Sprintf("%d inbox items (inbox full for this host)", n))
	}
	return strings.Join(parts, ", ")
}
