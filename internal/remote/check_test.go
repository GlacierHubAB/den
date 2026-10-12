package remote

import (
	"context"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/events"
	"github.com/GlacierHubAB/den/internal/fleet"
)

// hostileRun runs a Manager against a host under approve that says hello,
// sends msgs, then an event for sidA as a marker, and stays connected. It
// returns once the marker is in the local events log, so every message
// before it was handled, with the updates seen so far.
func hostileRun(t *testing.T, msgs ...any) (*Manager, []Update) {
	t.Helper()
	s := newScratch(t)
	marker := Event{Type: TypeEvent, Cursor: events.Cursor{Ino: 1, Off: 999}, Event: events.New(events.Done, sidA)}
	d := &pipeDialer{serve: func(ctx context.Context, in io.Reader, out io.Writer) error {
		newLineReader(in, MaxLine).next() // Den's request
		writeMsg(out, Hello{Type: TypeHello, Proto: 1, Policy: Approve, RemoteNow: time.Now()})
		for _, v := range msgs {
			writeMsg(out, v)
		}
		writeMsg(out, marker)
		<-ctx.Done()
		return nil
	}}
	h := hostB()
	h.Policy = Approve
	m := s.manager(d, h)
	var mu sync.Mutex
	var ups []Update
	done := make(chan struct{})
	go func() {
		defer close(done)
		for u := range m.Updates() {
			mu.Lock()
			ups = append(ups, u)
			mu.Unlock()
		}
	}()
	m.Start()
	t.Cleanup(func() { m.Close(); <-done })
	deadline := time.Now().Add(5 * time.Second)
	for !slices.ContainsFunc(localLines(t, s.localEvts), func(ev events.Event) bool { return ev.Session == sidA }) {
		if time.Now().After(deadline) {
			t.Fatal("the marker event never arrived")
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	return m, slices.Clone(ups)
}

func hasFlag(m *Manager, sub string) bool {
	return slices.ContainsFunc(state(m).Flags, func(f string) bool { return strings.Contains(f, sub) })
}

// Every session id a host sends must be a UUID: an agent, an event or a
// held request with anything else is dropped and the host flagged.
func TestHostSessionIDsMustBeUUIDs(t *testing.T) {
	const bad = "../../etc/passwd"
	m, ups := hostileRun(t,
		Snapshot{Type: TypeSnapshot, RemoteNow: time.Now(), Agents: []fleet.Meta{
			{Session: sidA, Status: "idle"},
			{Session: bad, Status: "idle"},
			{Session: "", Status: "idle"},
			{Session: strings.ToUpper(sidA[:35]) + "g", Status: "idle"},
		}},
		Event{Type: TypeEvent, Cursor: events.Cursor{Ino: 1, Off: 10}, Event: events.New(events.NeedsYou, bad)},
		Held{Type: TypeHeld, ID: strings.Repeat("a", 32), Session: bad, Tool: "Bash", Body: json.RawMessage(`{"command":"ls"}`)},
	)
	agents := m.Agents()
	if len(agents) != 1 || agents[0].Session.SessionID != sidA {
		t.Errorf("agents kept: %+v", agents)
	}
	for _, f := range []string{"an agent whose session id", "an event whose session id", "a held request whose session id"} {
		if !hasFlag(m, f) {
			t.Errorf("not flagged: %q; flags %q", f, state(m).Flags)
		}
	}
	for _, u := range ups {
		if u.Kind == HeldArrived || (u.Kind == EventArrived && u.Event.Session != sidA) {
			t.Errorf("an update for a bad session id: %+v", u)
		}
	}
}

func TestIsSessionID(t *testing.T) {
	for s, want := range map[string]bool{
		sidA:                                   true,
		strings.ToUpper(sidA):                  true,
		"":                                     false,
		sidA + "0":                             false,
		strings.ReplaceAll(sidA, "-", "0"):     false,
		"11111111-2222-4333-8444-55555555555g": false,
		"1111111-22222-4333-8444-555555555555": false,
		"11111111-2222-4333-8444-5555555555é":  false,
	} {
		if got := isSessionID(s); got != want {
			t.Errorf("isSessionID(%q) = %v, want %v", s, got, want)
		}
	}
}

// What an older Den saved is checked again when it is loaded.
func TestLoadKnownChecksAgain(t *testing.T) {
	dir := t.TempDir()
	saveKnown(dir, Known{Label: "b", Agents: []fleet.Meta{{Session: sidA}, {Session: "s1"}}})
	k, ok := LoadKnown(dir, "b")
	if !ok || len(k.Agents) != 1 || k.Agents[0].Session != sidA {
		t.Errorf("loaded %v %+v", ok, k.Agents)
	}
}
