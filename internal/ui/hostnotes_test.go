package ui

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/events"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/notify"
	"github.com/GlacierHubAB/den/internal/remote"
)

func floodSession(i int) string { return fmt.Sprintf("33333333-2222-4333-8444-%012d", i) }

// A host that sends a wait for many agents at once: the inbox keeps at
// most MaxRemoteOpen of them, one notification goes out, and the rest are
// one "<host>: N agents need you" when the window closes.
func TestRemoteFloodIsCappedAndCoalesced(t *testing.T) {
	m, feed, sent := remoteModel(t)
	for i := range 500 {
		ev := events.New(events.NeedsYou, floodSession(i))
		ev.Request, ev.Tool, ev.Host = "permission", "Bash", "devbox"
		runAll(m.remoteEvent(ev))
	}
	open := 0
	for _, it := range m.store.Items() {
		if it.Host == "devbox" && it.State != inbox.Resolved {
			open++
		}
	}
	if open != inbox.MaxRemoteOpen {
		t.Errorf("%d open items from devbox, want the cap %d", open, inbox.MaxRemoteOpen)
	}
	if n := m.store.RemoteDropped("devbox"); n != 500-inbox.MaxRemoteOpen {
		t.Errorf("dropped %d", n)
	}
	if len(*sent) != 1 {
		t.Fatalf("notifications during the window: %q", *sent)
	}
	runAll(m.hostNotesDue(time.Now().Add(10 * time.Second)))
	if len(*sent) != 1 {
		t.Errorf("a window closed early: %q", *sent)
	}
	runAll(m.hostNotesDue(time.Now().Add(hostNoteWindow)))
	if len(*sent) != 2 || (*sent)[1] != fmt.Sprintf("devbox: %d agents need you", inbox.MaxRemoteOpen-1) {
		t.Errorf("notifications = %q", *sent)
	}
	// Quiet now: no more notifications, and no window left open.
	runAll(m.hostNotesDue(time.Now().Add(3 * hostNoteWindow)))
	runAll(m.hostNotesDue(time.Now().Add(5 * hostNoteWindow)))
	if len(*sent) != 2 || len(m.att.hosts.open) != 0 {
		t.Errorf("after the flood: %q, %d windows open", *sent, len(m.att.hosts.open))
	}
	// The listing says what was dropped.
	m.remote.hosts[0].Dropped = 7
	if got := m.droppedText(m.remote.hosts[0]); !strings.Contains(got, "7 events") || !strings.Contains(got, "inbox full") {
		t.Errorf("dropped text %q", got)
	}

	// The link drops: everything from devbox is let go, nothing lingers.
	feed.setState("devbox", remote.Disabled)
	runAll(m.handleRemote(remoteMsg{u: remote.Update{Kind: remote.HostChanged, Host: "devbox"}, ok: true}))
	for _, it := range m.store.Items() {
		if it.Host == "devbox" && it.State != inbox.Resolved {
			t.Fatalf("still open after the host was disabled: %+v", it)
		}
	}
	for k := range m.att.noted {
		if hostOf(k) == "devbox" {
			t.Errorf("noted kept %s", k)
		}
	}
}

// Notifications for all hosts together stay under hostNotesMax a minute,
// however many hosts flood.
func TestRemoteNotificationsHardCap(t *testing.T) {
	h := newHostNotes()
	send := func(notify.Note) bool { return true }
	now := time.Now()
	posted := 0
	for i := range 50 {
		if cmd := h.note(fmt.Sprintf("h%d", i), "k", true, notify.Note{Summary: "x"}, send, now); cmd != nil {
			posted++
		}
	}
	if posted != hostNotesMax || h.dropped != 50-hostNotesMax {
		t.Errorf("posted %d dropped %d, want %d", posted, h.dropped, hostNotesMax)
	}
	if cmd := h.note("late", "k", true, notify.Note{Summary: "x"}, send, now.Add(time.Minute)); cmd == nil {
		t.Errorf("the cap did not reset after a minute")
	}
}

// [on] commands for one host's events stay under hostOnMax a minute.
func TestRemoteOnCommandsCapped(t *testing.T) {
	h := newHostNotes()
	now := time.Now()
	ok := 0
	for range 1000 {
		if h.allowOn("devbox", now) {
			ok++
		}
	}
	if ok != hostOnMax || !h.allowOn("gpu", now) || !h.allowOn("devbox", now.Add(time.Minute)) {
		t.Errorf("allowed %d", ok)
	}
}

// Finished turns from a flooding host: at most MaxRemoteNew a minute reach
// the inbox and history.jsonl.
func TestRemoteDoneFloodBoundsHistory(t *testing.T) {
	m, _, _ := remoteModel(t)
	for i := range 300 {
		ev := events.New(events.Done, floodSession(i%5))
		ev.Host = "devbox"
		runAll(m.remoteEvent(ev))
	}
	b, _ := os.ReadFile(inbox.HistoryPath())
	if n := strings.Count(string(b), "\n"); n != inbox.MaxRemoteNew {
		t.Errorf("history.jsonl has %d lines, want %d", n, inbox.MaxRemoteNew)
	}
}

// A remote agent has no window here: its notification, and the host's
// coalesced one, offer no click.
func TestRemoteNotificationsAreInert(t *testing.T) {
	m, _, _ := remoteModel(t)
	var notes []notify.Note
	m.att.send = func(n notify.Note) bool { notes = append(notes, n); return false }
	for i := range 3 {
		ev := events.New(events.NeedsYou, floodSession(i))
		ev.Request, ev.Tool, ev.Host = "permission", "Bash", "devbox"
		runAll(m.remoteEvent(ev))
	}
	runAll(m.hostNotesDue(time.Now().Add(hostNoteWindow)))
	if len(notes) != 2 {
		t.Fatalf("notes = %+v", notes)
	}
	for _, n := range notes {
		if !n.Inert {
			t.Errorf("not inert: %+v", n)
		}
	}
}
