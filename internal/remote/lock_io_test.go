package remote

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/events"
	"github.com/GlacierHubAB/den/internal/fleet"
)

// A slow disk never holds up Hosts and Agents, which the Fleet view and the
// dock call while a host floods snapshots and events (#175): the session
// writes known.json and events.jsonl after it lets go of the lock. What it
// saves last is still the newest.
func TestSlowWritesNeverHoldUpHostsOrAgents(t *testing.T) {
	const slow = 100 * time.Millisecond
	saveK, appendE := writeKnown, appendEvent
	t.Cleanup(func() { writeKnown, appendEvent = saveK, appendE })
	writeKnown = func(dir string, k Known) error { time.Sleep(slow); return saveK(dir, k) }
	appendEvent = func(path string, ev events.Event) error { time.Sleep(slow); return appendE(path, ev) }

	s := newScratch(t)
	flooded := make(chan struct{})
	d := &pipeDialer{serve: func(ctx context.Context, in io.Reader, out io.Writer) error {
		newLineReader(in, MaxLine).next() // Den's request
		writeMsg(out, Hello{Type: TypeHello, Proto: 1, Policy: Observe, RemoteNow: time.Now()})
		for i := range 20 {
			writeMsg(out, Snapshot{Type: TypeSnapshot, RemoteNow: time.Now(), Agents: []fleet.Meta{{Session: sidA, Status: "working"}}})
			if i%4 == 0 {
				writeMsg(out, Event{Type: TypeEvent, Cursor: events.Cursor{Ino: 1, Off: int64(i + 1)}, Event: events.New(events.Done, sidA)})
			}
		}
		writeMsg(out, Snapshot{Type: TypeSnapshot, RemoteNow: time.Now(), Agents: []fleet.Meta{{Session: sidA, Status: "idle"}}})
		close(flooded)
		<-ctx.Done()
		return nil
	}}
	m := s.manager(d, hostB())
	go func() {
		for range m.Updates() {
		}
	}()
	m.Start()
	t.Cleanup(m.Close)

	var worst time.Duration
	calls := 0
	for done := false; !done; {
		select {
		case <-flooded:
			done = true
		case <-time.After(5 * time.Millisecond):
		}
		t0 := time.Now()
		m.Hosts()
		m.Agents()
		worst = max(worst, time.Since(t0))
		calls++
	}
	t.Logf("%d calls, the slowest %s", calls, worst)
	if worst > slow/2 {
		t.Errorf("Hosts and Agents took %s behind a %s write", worst, slow)
	}
	waitFor(t, "the last snapshot saved", func() bool {
		k, ok := LoadKnown(s.stateDir, "b")
		return ok && len(k.Agents) == 1 && k.Agents[0].Status == "idle"
	})
}
